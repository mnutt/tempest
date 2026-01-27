package config

// ContainerBackendType identifies the type of container backend to use.
type ContainerBackendType string

const (
	// BackendLocal uses direct process spawning on Linux (sandbox-launcher).
	BackendLocal ContainerBackendType = "local"

	// BackendRemote uses a VM-based backend via Apple Virtualization Framework.
	BackendRemote ContainerBackendType = "remote"

	// BackendAuto automatically selects the best backend for the platform.
	BackendAuto ContainerBackendType = "auto"
)

// ContainerConfig holds configuration for the container backend.
type ContainerConfig struct {
	// Backend specifies which container backend to use.
	// Default: "auto" (selects based on platform)
	Backend ContainerBackendType

	// VMKernelPath is the path to the Linux kernel for the VM (remote backend).
	// Default: {Libexecdir}/tempest/vm/kernel
	VMKernelPath string

	// VMInitrdPath is the path to the initrd for the VM (remote backend).
	// Default: {Libexecdir}/tempest/vm/initrd
	VMInitrdPath string

	// VMMemoryMB is the amount of memory to allocate to the VM in megabytes.
	// Default: 512
	VMMemoryMB uint64

	// VMCPUCount is the number of CPUs to allocate to the VM.
	// Default: 2
	VMCPUCount uint
}

// DefaultContainerConfig returns the default container configuration.
func DefaultContainerConfig() ContainerConfig {
	return ContainerConfig{
		Backend:      BackendAuto,
		VMKernelPath: Libexecdir + "/tempest/vm/kernel",
		VMInitrdPath: Libexecdir + "/tempest/vm/initrd",
		VMMemoryMB:   1024,
		VMCPUCount:   2,
	}
}
