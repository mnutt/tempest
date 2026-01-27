package config

import (
	"os"
	"strconv"
)

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
// VM memory and CPU can be overridden via environment variables:
//   - TEMPEST_VM_MEMORY_MB (default: 1024)
//   - TEMPEST_VM_CPU_COUNT (default: 2)
func DefaultContainerConfig() ContainerConfig {
	memoryMB := uint64(1024)
	if v := os.Getenv("TEMPEST_VM_MEMORY_MB"); v != "" {
		if n, err := strconv.ParseUint(v, 10, 64); err == nil {
			memoryMB = n
		}
	}

	cpuCount := uint(2)
	if v := os.Getenv("TEMPEST_VM_CPU_COUNT"); v != "" {
		if n, err := strconv.ParseUint(v, 10, 32); err == nil {
			cpuCount = uint(n)
		}
	}

	return ContainerConfig{
		Backend:      BackendAuto,
		VMKernelPath: Libexecdir + "/tempest/vm/kernel",
		VMInitrdPath: Libexecdir + "/tempest/vm/initrd",
		VMMemoryMB:   memoryMB,
		VMCPUCount:   cpuCount,
	}
}
