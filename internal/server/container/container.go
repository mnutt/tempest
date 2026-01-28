// Package container manages spawning containers/sandboxes
package container

import (
	"context"

	"capnproto.org/go/capnp/v3"
	"capnproto.org/go/capnp/v3/rpc"
	"capnproto.org/go/capnp/v3/rpc/transport"
	"golang.org/x/exp/slog"

	"sandstorm.org/go/tempest/capnp/grain"
	"sandstorm.org/go/tempest/internal/common/types"
	"sandstorm.org/go/tempest/internal/server/database"
	"zenhack.net/go/util/exn"
)

// A Container is a reference to a running container/sandboxed grain.
type Container struct {
	Bootstrap capnp.Client       // Bootstrap interface for the Container.
	cancel    context.CancelFunc // cancel causes the container to shut down.
	exited    <-chan struct{}    // closed when the container has exited.
}

// Kill forcably shuts down the container. (Note: we do not provide a way
// to ask nicely via SIGTERM or such; apps are expected to be crash-only
// software).
//
// Does not wait for shutdown to complete; see Wait().
func (c Container) Kill() {
	c.Bootstrap.Release()
	c.cancel()
}

// Wait blocks until the container has shut down and then returns.
func (c Container) Wait() {
	<-c.exited
}

// A Command specifies a task to start in a container.
type Command struct {
	Log     *slog.Logger
	DB      database.DB
	Backend Backend

	// GrainID is the ID of the grain to start
	GrainID types.GrainID

	// Api will be provided to the grain as our bootstrap interface.
	Api grain.SandstormApi

	// Args will be passed to the grain agent as extra arguments.
	Args []string
}

// Start starts the container. It will shut down when ctx is canceled or
// Kill() is called.
func (cmd Command) Start(ctx context.Context) (Container, error) {
	cmd.Log.Info("Starting grain",
		"grainID", cmd.GrainID,
	)
	return exn.Try(func(throw exn.Thrower) Container {
		tx, err := cmd.DB.Begin()
		throw(err)
		defer tx.Rollback()
		pkgID, err := tx.GrainPackageID(cmd.GrainID)
		throw(err)
		throw(tx.Commit())
		ret, err := pkgCommand{
			Command: cmd,
			PkgID:   pkgID,
		}.Start(ctx)
		throw(err)
		return ret
	})
}

// pkgCommand is like Command, but it also includes the package ID, so looking that
// up in the database is unnecessary.
type pkgCommand struct {
	Command
	PkgID string
}

// Start is like Command.Start
func (cmd pkgCommand) Start(ctx context.Context) (Container, error) {
	ctx, cancel := context.WithCancel(ctx)

	// Use the backend to spawn the grain
	handle, err := cmd.Backend.Spawn(ctx, SpawnRequest{
		PackageID: cmd.PkgID,
		GrainID:   cmd.GrainID,
		Args:      cmd.Args,
	})
	if err != nil {
		cmd.Api.Release()
		cancel()
		return Container{}, err
	}

	// Set up RPC connection over the grain handle's connection
	trans := transport.NewStream(handle.Conn())
	options := &rpc.Options{
		BootstrapClient: capnp.Client(cmd.Api),
	}
	conn := rpc.NewConn(trans, options)
	grainBootstrap := conn.Bootstrap(ctx)

	exited := make(chan struct{})
	go func() {
		<-ctx.Done()
		// Kill and wait for the grain to exit.
		// Ignore errors here - during shutdown the connection may already
		// be closed (e.g., if the VM was stopped first), which is fine.
		if err := handle.Kill(); err != nil {
			cmd.Log.Debug("Kill grain returned error (may be expected during shutdown)",
				"error", err,
				"grainID", cmd.GrainID,
			)
		} else {
			cmd.Log.Debug("Killed grain",
				"grainID", cmd.GrainID,
			)
		}
		if err := handle.Wait(); err != nil {
			cmd.Log.Debug("Wait() on grain returned error",
				"error", err,
				"grainID", cmd.GrainID,
			)
		}
		<-conn.Done()
		close(exited)
	}()

	return Container{
		Bootstrap: grainBootstrap,
		cancel:    cancel,
		exited:    exited,
	}, nil
}
