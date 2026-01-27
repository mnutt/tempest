//go:build linux

package container

import (
	"context"
	"io"
	"net"
	"os"
	"os/exec"
	"strconv"
	"sync"
	"time"

	"golang.org/x/exp/slog"
	"golang.org/x/sys/unix"

	"sandstorm.org/go/tempest/internal/common/types"
	"sandstorm.org/go/tempest/internal/config"
	"zenhack.net/go/util"
)

// LocalBackend spawns grain containers directly on Linux using unix socketpairs
// and the sandbox-launcher binary.
type LocalBackend struct {
	log *slog.Logger
}

// NewLocalBackend creates a new local backend for spawning grain containers.
func NewLocalBackend(log *slog.Logger) *LocalBackend {
	return &LocalBackend{log: log}
}

// Spawn starts a new grain container and returns a handle to it.
func (b *LocalBackend) Spawn(ctx context.Context, req SpawnRequest) (GrainHandle, error) {
	b.log.Debug("LocalBackend: spawning grain",
		"packageID", req.PackageID,
		"grainID", req.GrainID,
	)

	// Create RPC socket pair
	fds, err := unix.Socketpair(unix.AF_UNIX, unix.SOCK_STREAM, 0)
	if err != nil {
		return nil, err
	}

	grainSock := os.NewFile(uintptr(fds[0]), "grain api socket")
	defer grainSock.Close()
	supervisorSock := os.NewFile(uintptr(fds[1]), "supervisor api socket")

	// Pipe to communicate the grain's PID
	pidR, pidW, err := os.Pipe()
	if err != nil {
		supervisorSock.Close()
		return nil, err
	}
	defer pidR.Close()

	// Build command arguments
	args := append([]string{
		req.PackageID,
		string(req.GrainID),
	}, req.Args...)

	osCmd := exec.Command(
		config.Libexecdir+"/tempest/tempest-sandbox-launcher",
		args...,
	)

	// TODO(soon) capture/log stdout/stderr
	osCmd.Stdout = os.Stdout
	osCmd.Stderr = os.Stderr

	osCmd.ExtraFiles = []*os.File{grainSock, pidW}
	err = osCmd.Start()
	pidW.Close() // Close this now, so when the child closes it we hit EOF.
	if err != nil {
		b.log.Error("Starting sandbox launcher failed",
			"error", err,
			"grainID", req.GrainID,
		)
		supervisorSock.Close()
		return nil, err
	}

	launcherPid := osCmd.Process.Pid
	b.log.Debug("Started launcher process",
		"launcher-pid", launcherPid,
		"grainID", req.GrainID,
	)

	// Read grain PID from launcher
	pidBuf, err := io.ReadAll(pidR)
	if err != nil {
		b.log.Error("Failed to read grain pid",
			"error", err,
			"read", string(pidBuf),
			"grainID", req.GrainID,
			"launcher-pid", launcherPid,
		)
		return nil, err
	}

	grainPid, err := strconv.Atoi(string(pidBuf))
	if err != nil {
		b.log.Error("bug: sandbox-launcher returned invalid pid",
			"error", err,
			"grainID", req.GrainID,
			"launcher-pid", launcherPid,
			"bad-pid", strconv.Quote(string(pidBuf)),
		)
		supervisorSock.Close()
		util.Chkfatal(osCmd.Process.Kill())
		util.Must(osCmd.Process.Wait())
		return nil, err
	}

	grainProc, err := os.FindProcess(grainPid)
	util.Chkfatal(err) // Can't fail on unix

	b.log.Debug("Started grain process",
		"grainID", req.GrainID,
		"packageId", req.PackageID,
		"launcher-pid", launcherPid,
		"grain-pid", grainPid,
	)

	handle := &localGrainHandle{
		log:            b.log,
		conn:           &netFileConn{supervisorSock},
		grainProc:      grainProc,
		launcherCmd:    osCmd,
		grainID:        req.GrainID,
		done:           make(chan struct{}),
	}

	return handle, nil
}

// Close releases any resources held by the backend.
func (b *LocalBackend) Close() error {
	return nil
}

// localGrainHandle implements GrainHandle for local process-based grains.
type localGrainHandle struct {
	log         *slog.Logger
	conn        net.Conn
	grainProc   *os.Process
	launcherCmd *exec.Cmd
	grainID     types.GrainID
	done        chan struct{}
	closeOnce   sync.Once
}

func (h *localGrainHandle) Conn() net.Conn {
	return h.conn
}

func (h *localGrainHandle) Kill() error {
	if err := h.grainProc.Kill(); err != nil {
		return err
	}
	return nil
}

func (h *localGrainHandle) Wait() error {
	_, err := h.launcherCmd.Process.Wait()
	h.closeOnce.Do(func() {
		close(h.done)
	})
	return err
}

func (h *localGrainHandle) Done() <-chan struct{} {
	return h.done
}

// netFileConn wraps an *os.File to implement net.Conn.
// This is needed because unix.Socketpair returns file descriptors,
// but we need a net.Conn for the RPC transport.
type netFileConn struct {
	file *os.File
}

func (c *netFileConn) Read(b []byte) (n int, err error) {
	return c.file.Read(b)
}

func (c *netFileConn) Write(b []byte) (n int, err error) {
	return c.file.Write(b)
}

func (c *netFileConn) Close() error {
	return c.file.Close()
}

func (c *netFileConn) LocalAddr() net.Addr {
	return &net.UnixAddr{Name: c.file.Name(), Net: "unix"}
}

func (c *netFileConn) RemoteAddr() net.Addr {
	return &net.UnixAddr{Name: "grain", Net: "unix"}
}

func (c *netFileConn) SetDeadline(t time.Time) error {
	return c.file.SetDeadline(t)
}

func (c *netFileConn) SetReadDeadline(t time.Time) error {
	return c.file.SetReadDeadline(t)
}

func (c *netFileConn) SetWriteDeadline(t time.Time) error {
	return c.file.SetWriteDeadline(t)
}
