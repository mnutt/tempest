// Package vm provides virtual machine management for running grain containers.
// On macOS, this uses Apple Virtualization Framework to run a Linux VM.
package vm

import (
	"context"
	"net"

	"sandstorm.org/go/tempest/internal/config"
)

// Manager manages a Linux VM for running grain containers.
type Manager interface {
	// Start boots the VM. This blocks until the VM is ready to accept connections.
	Start(ctx context.Context) error

	// Stop shuts down the VM gracefully.
	Stop(ctx context.Context) error

	// DialDaemon connects to the VM daemon via vsock.
	DialDaemon(ctx context.Context) (net.Conn, error)

	// DialPort connects to a specific vsock port in the VM.
	DialPort(ctx context.Context, port uint32) (net.Conn, error)

	// IsRunning returns true if the VM is currently running.
	IsRunning() bool
}

// Config holds configuration for the VM manager.
type Config struct {
	// KernelPath is the path to the Linux kernel.
	KernelPath string

	// InitrdPath is the path to the initrd/initramfs.
	InitrdPath string

	// MemoryMB is the amount of memory to allocate to the VM in megabytes.
	MemoryMB uint64

	// CPUCount is the number of CPUs to allocate to the VM.
	CPUCount uint

	// PackagesDir is the host path to the packages directory (mounted read-only).
	PackagesDir string

	// GrainsDir is the host path to the grains directory (mounted read-write).
	GrainsDir string
}

// DefaultConfig returns the default VM configuration.
func DefaultConfig() Config {
	cfg := config.DefaultContainerConfig()
	return Config{
		KernelPath:  cfg.VMKernelPath,
		InitrdPath:  cfg.VMInitrdPath,
		MemoryMB:    cfg.VMMemoryMB,
		CPUCount:    cfg.VMCPUCount,
		PackagesDir: config.Localstatedir + "/sandstorm/apps",
		GrainsDir:   config.Localstatedir + "/sandstorm/grains",
	}
}

// DaemonVsockPort is the vsock port used by the VM daemon.
const DaemonVsockPort uint32 = 5000
