package container

import (
	"context"
	"fmt"
	"net"
	"sync"

	"capnproto.org/go/capnp/v3"
	"capnproto.org/go/capnp/v3/rpc"
	"golang.org/x/exp/slog"

	"sandstorm.org/go/tempest/internal/capnp/vmdaemon"
)

// RemoteBackend spawns grain containers in a Linux VM via virtio-vsock.
// The VM runs a daemon (tempest-vm-daemon) that manages containers and
// exposes them via vsock ports.
type RemoteBackend struct {
	log    *slog.Logger
	conn   *rpc.Conn
	daemon vmdaemon.VmDaemon
	dialer VsockDialer

	mu     sync.Mutex
	grains map[string]*remoteGrainHandle
}

// VsockDialer is an interface for dialing vsock connections.
// This allows for platform-specific implementations and testing.
type VsockDialer interface {
	// DialDaemon connects to the VM daemon's control socket.
	DialDaemon(ctx context.Context) (net.Conn, error)

	// DialGrain connects to a specific grain's vsock port.
	DialGrain(ctx context.Context, port uint32) (net.Conn, error)
}

// NewRemoteBackend creates a new remote backend that connects to a VM daemon.
func NewRemoteBackend(log *slog.Logger, dialer VsockDialer) (*RemoteBackend, error) {
	ctx := context.Background()

	// Connect to the VM daemon
	conn, err := dialer.DialDaemon(ctx)
	if err != nil {
		return nil, fmt.Errorf("failed to connect to VM daemon: %w", err)
	}

	// Set up RPC connection
	rpcConn := rpc.NewConn(rpc.NewStreamTransport(conn), nil)
	daemon := vmdaemon.VmDaemon(rpcConn.Bootstrap(ctx))

	return &RemoteBackend{
		log:    log,
		conn:   rpcConn,
		daemon: daemon,
		dialer: dialer,
		grains: make(map[string]*remoteGrainHandle),
	}, nil
}

// Spawn starts a new grain container in the VM and returns a handle to it.
func (b *RemoteBackend) Spawn(ctx context.Context, req SpawnRequest) (GrainHandle, error) {
	b.log.Debug("RemoteBackend: spawning grain",
		"packageID", req.PackageID,
		"grainID", req.GrainID,
	)

	// Convert args to capnp list
	_, seg := capnp.NewSingleSegmentMessage(nil)
	argsList, err := capnp.NewTextList(seg, int32(len(req.Args)))
	if err != nil {
		return nil, fmt.Errorf("failed to create args list: %w", err)
	}
	for i, arg := range req.Args {
		if err := argsList.Set(i, arg); err != nil {
			return nil, fmt.Errorf("failed to set arg %d: %w", i, err)
		}
	}

	// Ask the daemon to spawn the grain
	result, rel := b.daemon.SpawnGrain(ctx, func(p vmdaemon.VmDaemon_spawnGrain_Params) error {
		p.SetPackageId(req.PackageID)
		p.SetGrainId(string(req.GrainID))
		p.SetArgs(argsList)
		return nil
	})
	defer rel()

	res, err := result.Struct()
	if err != nil {
		return nil, fmt.Errorf("failed to spawn grain in VM: %w", err)
	}

	vsockPort := res.VsockPort()
	b.log.Debug("Grain spawned in VM",
		"grainID", req.GrainID,
		"vsockPort", vsockPort,
	)

	// Connect to the grain's vsock port
	grainConn, err := b.dialer.DialGrain(ctx, vsockPort)
	if err != nil {
		// Try to kill the grain since we couldn't connect
		b.daemon.KillGrain(ctx, func(p vmdaemon.VmDaemon_killGrain_Params) error {
			p.SetGrainId(string(req.GrainID))
			return nil
		})
		return nil, fmt.Errorf("failed to connect to grain vsock port %d: %w", vsockPort, err)
	}

	handle := &remoteGrainHandle{
		backend:   b,
		grainID:   string(req.GrainID),
		vsockPort: vsockPort,
		conn:      grainConn,
		done:      make(chan struct{}),
	}

	b.mu.Lock()
	b.grains[string(req.GrainID)] = handle
	b.mu.Unlock()

	return handle, nil
}

// Close releases resources held by the backend.
func (b *RemoteBackend) Close() error {
	b.daemon.Release()
	return b.conn.Close()
}

// remoteGrainHandle implements GrainHandle for VM-based grains.
type remoteGrainHandle struct {
	backend   *RemoteBackend
	grainID   string
	vsockPort uint32
	conn      net.Conn
	done      chan struct{}
	closeOnce sync.Once
}

func (h *remoteGrainHandle) Conn() net.Conn {
	return h.conn
}

func (h *remoteGrainHandle) Kill() error {
	ctx := context.Background()

	// Ask the daemon to kill the grain (ignore errors - daemon may be gone)
	_, rel := h.backend.daemon.KillGrain(ctx, func(p vmdaemon.VmDaemon_killGrain_Params) error {
		p.SetGrainId(h.grainID)
		return nil
	})
	rel()

	// Close the connection (ignore errors - may already be closed)
	h.conn.Close()

	// Remove from backend's grain map
	h.backend.mu.Lock()
	delete(h.backend.grains, h.grainID)
	h.backend.mu.Unlock()

	// Signal that the grain has exited
	h.closeOnce.Do(func() {
		close(h.done)
	})

	return nil
}

func (h *remoteGrainHandle) Wait() error {
	<-h.done
	return nil
}

func (h *remoteGrainHandle) Done() <-chan struct{} {
	return h.done
}

// Ensure RemoteBackend implements Backend
var _ Backend = (*RemoteBackend)(nil)
