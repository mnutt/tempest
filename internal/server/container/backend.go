// Package container manages spawning containers/sandboxes
package container

import (
	"context"
	"net"

	"sandstorm.org/go/tempest/internal/common/types"
)

// Backend defines the interface for spawning and managing grain containers.
// Different implementations exist for different platforms:
// - LocalBackend: Direct process spawning on Linux using unix socketpairs
// - RemoteBackend: VM-based spawning via Apple Virtualization Framework on macOS
type Backend interface {
	// Spawn starts a new grain container and returns a handle to it.
	Spawn(ctx context.Context, req SpawnRequest) (GrainHandle, error)

	// Close releases any resources held by the backend.
	Close() error
}

// SpawnRequest contains the parameters needed to spawn a grain container.
type SpawnRequest struct {
	// PackageID identifies the application package to run
	PackageID string

	// GrainID identifies the specific grain instance
	GrainID types.GrainID

	// Args are additional arguments passed to the grain agent
	Args []string
}

// GrainHandle provides control over a running grain container.
type GrainHandle interface {
	// Conn returns the RPC connection to the grain.
	// The caller should not close this connection directly.
	Conn() net.Conn

	// Kill terminates the grain container immediately.
	Kill() error

	// Wait blocks until the grain container has exited.
	Wait() error

	// Done returns a channel that is closed when the grain has exited.
	Done() <-chan struct{}
}
