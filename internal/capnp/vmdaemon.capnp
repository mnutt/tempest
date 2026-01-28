@0xd72118c15ecda7a8;
# The VM daemon runs inside a Linux VM (on macOS via Apple Virtualization
# Framework) and manages grain containers. The host connects to it via
# virtio-vsock to spawn and manage grains.

using Go = import "/go.capnp";
$Go.package("vmdaemon");
$Go.import("sandstorm.org/go/tempest/internal/capnp/vmdaemon");

interface VmDaemon {
  # VmDaemon is the main interface exposed by the VM daemon to the host.
  # The host connects via virtio-vsock and uses this interface to manage
  # grain containers running inside the VM.

  spawnGrain @0 (packageId :Text, grainId :Text, args :List(Text)) -> (vsockPort :UInt32);
  # Spawn a new grain container. Returns a vsock port number that the host
  # can connect to for Cap'n Proto RPC with the grain.
  #
  # The daemon will:
  # 1. Allocate a vsock port for this grain
  # 2. Run sandbox-launcher with the given package/grain IDs
  # 3. Bridge the grain's unix socket to the vsock port
  # 4. Return the port number to the host

  killGrain @1 (grainId :Text);
  # Terminate a running grain. This sends SIGKILL to the grain process.

  listGrains @2 () -> (grains :List(GrainInfo));
  # List all currently running grains managed by this daemon.
}

struct GrainInfo {
  # Information about a running grain.

  grainId @0 :Text;
  # The grain's unique identifier.

  packageId @1 :Text;
  # The package ID of the grain's application.

  vsockPort @2 :UInt32;
  # The vsock port allocated for this grain's RPC connection.

  pid @3 :Int32;
  # The process ID of the grain inside the VM.
}
