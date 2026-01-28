//go:build darwin

package container

import (
	"context"
	"fmt"
	"net"
	"os"
	"time"

	"golang.org/x/exp/slog"

	"sandstorm.org/go/tempest/internal/server/vm"
)

// NewDefaultBackend creates the default container backend for the current platform.
// On macOS, this starts a Linux VM using Apple Virtualization Framework and
// returns a RemoteBackend that communicates with the VM daemon.
//
// If the VM kernel/initrd files are not found, it falls back to a stub backend
// that allows the server to start but returns errors when spawning grains.
func NewDefaultBackend(log *slog.Logger) (Backend, error) {
	cfg := vm.DefaultConfig()

	// Check if VM files exist
	if _, err := os.Stat(cfg.KernelPath); os.IsNotExist(err) {
		log.Warn("VM kernel not found, falling back to stub backend",
			"kernelPath", cfg.KernelPath,
		)
		return newStubBackend(log), nil
	}
	if _, err := os.Stat(cfg.InitrdPath); os.IsNotExist(err) {
		log.Warn("VM initrd not found, falling back to stub backend",
			"initrdPath", cfg.InitrdPath,
		)
		return newStubBackend(log), nil
	}

	// Create and start the VM manager
	vmManager := vm.NewDarwinManager(log, cfg)

	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	log.Info("Starting Linux VM for grain containers...")
	if err := vmManager.Start(ctx); err != nil {
		log.Error("Failed to start VM, falling back to stub backend",
			"error", err,
		)
		return newStubBackend(log), nil
	}

	// Create a vsock dialer that uses the VM manager
	dialer := &vmManagerDialer{
		manager: vmManager,
	}

	// Wait for the VM daemon to become ready with retries
	log.Info("Waiting for VM daemon to become ready...")

	// Give the kernel time to boot before attempting connections.
	// The VM typically needs ~500ms to boot, so this avoids noisy retries.
	time.Sleep(400 * time.Millisecond)

	var backend *RemoteBackend
	var err error
	maxRetries := 30
	retryDelay := 200 * time.Millisecond

	for i := 0; i < maxRetries; i++ {
		select {
		case <-ctx.Done():
			log.Error("Timeout waiting for VM daemon")
			vmManager.Stop(context.Background())
			return newStubBackend(log), nil
		default:
		}

		backend, err = NewRemoteBackend(log, dialer)
		if err == nil {
			break
		}

		if i < maxRetries-1 {
			log.Debug("VM daemon not ready, retrying...",
				"attempt", i+1,
				"error", err,
			)
			time.Sleep(retryDelay)
			// Exponential backoff up to 2 seconds
			if retryDelay < 2*time.Second {
				retryDelay = retryDelay * 3 / 2
			}
		}
	}

	if err != nil {
		log.Error("Failed to connect to VM daemon after retries, falling back to stub backend",
			"error", err,
		)
		vmManager.Stop(context.Background())
		return newStubBackend(log), nil
	}

	log.Info("Remote backend connected to VM daemon")

	// Wrap the backend to also stop the VM on close
	return &darwinBackend{
		RemoteBackend: backend,
		vmManager:     vmManager,
		log:           log,
	}, nil
}

// darwinBackend wraps RemoteBackend and manages the VM lifecycle.
type darwinBackend struct {
	*RemoteBackend
	vmManager *vm.DarwinManager
	log       *slog.Logger
}

// Close stops the VM and releases all resources.
func (b *darwinBackend) Close() error {
	b.log.Info("Shutting down VM backend...")

	// Close the remote backend first
	if err := b.RemoteBackend.Close(); err != nil {
		b.log.Warn("Error closing remote backend", "error", err)
	}

	// Stop the VM
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	if err := b.vmManager.Stop(ctx); err != nil {
		b.log.Warn("Error stopping VM", "error", err)
		return err
	}

	b.log.Info("VM backend shut down")
	return nil
}

// vmManagerDialer implements VsockDialer using the VM manager.
type vmManagerDialer struct {
	manager *vm.DarwinManager
}

func (d *vmManagerDialer) DialDaemon(ctx context.Context) (net.Conn, error) {
	return d.manager.DialDaemon(ctx)
}

func (d *vmManagerDialer) DialGrain(ctx context.Context, port uint32) (net.Conn, error) {
	return d.manager.DialPort(ctx, port)
}

// Ensure vmManagerDialer implements VsockDialer
var _ VsockDialer = (*vmManagerDialer)(nil)

// stubBackend is a fallback backend for when the VM cannot be started.
type stubBackend struct {
	log *slog.Logger
}

func newStubBackend(log *slog.Logger) *stubBackend {
	log.Warn("Running on macOS without VM backend - grain spawning will not work")
	return &stubBackend{log: log}
}

func (b *stubBackend) Spawn(ctx context.Context, req SpawnRequest) (GrainHandle, error) {
	return nil, fmt.Errorf("grain spawning not available: VM backend not running (check that kernel and initrd exist)")
}

func (b *stubBackend) Close() error {
	return nil
}

// Ensure stubBackend implements Backend
var _ Backend = (*stubBackend)(nil)
