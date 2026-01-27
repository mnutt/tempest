//go:build darwin

package vm

import (
	"context"
	"fmt"
	"net"
	"os"
	"sync"
	"time"

	"github.com/Code-Hex/vz/v3"
	"golang.org/x/exp/slog"
)

// DarwinManager manages a Linux VM using Apple Virtualization Framework.
type DarwinManager struct {
	log    *slog.Logger
	config Config

	mu      sync.RWMutex
	vm      *vz.VirtualMachine
	vsock   *vz.VirtioSocketDevice
	running bool
}

// NewDarwinManager creates a new VM manager for macOS.
func NewDarwinManager(log *slog.Logger, cfg Config) *DarwinManager {
	return &DarwinManager{
		log:    log,
		config: cfg,
	}
}

// Start boots the Linux VM.
func (m *DarwinManager) Start(ctx context.Context) error {
	m.mu.Lock()
	defer m.mu.Unlock()

	if m.running {
		return fmt.Errorf("VM is already running")
	}

	m.log.Info("Starting Linux VM",
		"kernel", m.config.KernelPath,
		"initrd", m.config.InitrdPath,
		"memory", m.config.MemoryMB,
		"cpus", m.config.CPUCount,
	)

	// Create boot loader
	// Kernel command line: try multiple console devices for output
	// ttyAMA0 is ARM PL011 UART, hvc0 is virtio-console
	bootLoader, err := vz.NewLinuxBootLoader(
		m.config.KernelPath,
		vz.WithInitrd(m.config.InitrdPath),
		vz.WithCommandLine("console=ttyAMA0 console=hvc0 rdinit=/init panic=-1 loglevel=7"),
	)
	if err != nil {
		return fmt.Errorf("failed to create boot loader: %w", err)
	}

	// Create VM configuration
	vmConfig, err := vz.NewVirtualMachineConfiguration(
		bootLoader,
		uint(m.config.CPUCount),
		m.config.MemoryMB*1024*1024, // Convert MB to bytes
	)
	if err != nil {
		return fmt.Errorf("failed to create VM config: %w", err)
	}

	// Set platform configuration for Linux guest on Apple Silicon
	platformConfig, err := vz.NewGenericPlatformConfiguration()
	if err != nil {
		return fmt.Errorf("failed to create platform config: %w", err)
	}
	vmConfig.SetPlatformVirtualMachineConfiguration(platformConfig)

	// Add virtio entropy device (provides /dev/random to guest)
	entropyConfig, err := vz.NewVirtioEntropyDeviceConfiguration()
	if err != nil {
		return fmt.Errorf("failed to create entropy config: %w", err)
	}
	vmConfig.SetEntropyDevicesVirtualMachineConfiguration([]*vz.VirtioEntropyDeviceConfiguration{entropyConfig})

	// Add virtio-vsock device for communication
	vsockConfig, err := vz.NewVirtioSocketDeviceConfiguration()
	if err != nil {
		return fmt.Errorf("failed to create vsock config: %w", err)
	}
	vmConfig.SetSocketDevicesVirtualMachineConfiguration([]vz.SocketDeviceConfiguration{vsockConfig})

	// Add virtio-console for serial output
	// Create a file to capture console output for debugging
	consoleLogPath := "/tmp/tempest-vm-console.log"
	consoleFile, err := os.OpenFile(consoleLogPath, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0644)
	if err != nil {
		return fmt.Errorf("failed to create console log file: %w", err)
	}
	m.log.Info("VM console output will be written to", "path", consoleLogPath)

	// Use /dev/null for input (no interactive console), file for output
	devNull, err := os.Open("/dev/null")
	if err != nil {
		consoleFile.Close()
		return fmt.Errorf("failed to open /dev/null: %w", err)
	}

	serialAttachment, err := vz.NewFileHandleSerialPortAttachment(devNull, consoleFile)
	if err != nil {
		devNull.Close()
		consoleFile.Close()
		return fmt.Errorf("failed to create serial attachment: %w", err)
	}
	serialConfig, err := vz.NewVirtioConsoleDeviceSerialPortConfiguration(serialAttachment)
	if err != nil {
		return fmt.Errorf("failed to create serial config: %w", err)
	}
	vmConfig.SetSerialPortsVirtualMachineConfiguration([]*vz.VirtioConsoleDeviceSerialPortConfiguration{serialConfig})

	// Add virtio-fs shares for packages and grains directories
	if err := m.configureFilesystemShares(vmConfig); err != nil {
		return fmt.Errorf("failed to configure filesystem shares: %w", err)
	}

	// Validate configuration
	valid, err := vmConfig.Validate()
	if err != nil {
		return fmt.Errorf("VM config validation failed: %w", err)
	}
	if !valid {
		return fmt.Errorf("VM config is invalid")
	}

	// Create and start the VM
	vm, err := vz.NewVirtualMachine(vmConfig)
	if err != nil {
		return fmt.Errorf("failed to create VM: %w", err)
	}

	// Log initial VM state
	m.log.Debug("VM created", "state", vm.State(), "canStart", vm.CanStart())

	// Get the vsock device
	socketDevices := vm.SocketDevices()
	if len(socketDevices) == 0 {
		return fmt.Errorf("no vsock device found")
	}
	m.vsock = socketDevices[0]

	// Check if VM can start
	if !vm.CanStart() {
		return fmt.Errorf("VM cannot start, current state: %v", vm.State())
	}

	// Start the VM
	if err := vm.Start(); err != nil {
		return fmt.Errorf("failed to start VM: %w", err)
	}

	m.vm = vm
	m.running = true

	m.log.Info("Linux VM started successfully")

	// Monitor VM state
	go m.monitorVM(ctx)

	return nil
}

// addDirectoryShare creates a virtio-fs share for a host directory.
func addDirectoryShare(shares *[]vz.DirectorySharingDeviceConfiguration, tag, path string, readOnly bool) error {
	dir, err := vz.NewSharedDirectory(path, readOnly)
	if err != nil {
		return fmt.Errorf("failed to create %s share: %w", tag, err)
	}
	mount, err := vz.NewVirtioFileSystemDeviceConfiguration(tag)
	if err != nil {
		return fmt.Errorf("failed to create %s mount config: %w", tag, err)
	}
	share, err := vz.NewSingleDirectoryShare(dir)
	if err != nil {
		return fmt.Errorf("failed to create %s single share: %w", tag, err)
	}
	mount.SetDirectoryShare(share)
	*shares = append(*shares, mount)
	return nil
}

// configureFilesystemShares sets up virtio-fs shares for packages and grains.
func (m *DarwinManager) configureFilesystemShares(vmConfig *vz.VirtualMachineConfiguration) error {
	var shares []vz.DirectorySharingDeviceConfiguration

	if err := addDirectoryShare(&shares, "packages", m.config.PackagesDir, true); err != nil {
		return err
	}
	if err := addDirectoryShare(&shares, "grains", m.config.GrainsDir, false); err != nil {
		return err
	}

	// Rosetta share for x86_64 binary translation (ARM64 only)
	if err := m.configureRosetta(&shares); err != nil {
		m.log.Warn("Rosetta not available, x86_64 binaries will not work", "error", err)
	}

	vmConfig.SetDirectorySharingDevicesVirtualMachineConfiguration(shares)
	return nil
}

// configureRosetta sets up Rosetta for x86_64 binary translation.
func (m *DarwinManager) configureRosetta(shares *[]vz.DirectorySharingDeviceConfiguration) error {
	// Check Rosetta availability
	availability := vz.LinuxRosettaDirectoryShareAvailability()
	switch availability {
	case vz.LinuxRosettaAvailabilityNotSupported:
		return fmt.Errorf("Rosetta is not supported on this system")
	case vz.LinuxRosettaAvailabilityNotInstalled:
		m.log.Info("Installing Rosetta for Linux...")
		if err := vz.LinuxRosettaDirectoryShareInstallRosetta(); err != nil {
			return fmt.Errorf("failed to install Rosetta: %w", err)
		}
		m.log.Info("Rosetta installed successfully")
	case vz.LinuxRosettaAvailabilityInstalled:
		m.log.Debug("Rosetta is available")
	}

	// Create Rosetta directory share
	rosettaShare, err := vz.NewLinuxRosettaDirectoryShare()
	if err != nil {
		return fmt.Errorf("failed to create Rosetta share: %w", err)
	}

	// Create virtio-fs device for Rosetta with tag "rosetta"
	rosettaMount, err := vz.NewVirtioFileSystemDeviceConfiguration("rosetta")
	if err != nil {
		return fmt.Errorf("failed to create Rosetta mount config: %w", err)
	}
	rosettaMount.SetDirectoryShare(rosettaShare)
	*shares = append(*shares, rosettaMount)

	m.log.Info("Rosetta share configured for x86_64 binary translation")
	return nil
}

// monitorVM watches the VM state and logs changes.
func (m *DarwinManager) monitorVM(ctx context.Context) {
	stateCh := m.vm.StateChangedNotify()
	for {
		select {
		case <-ctx.Done():
			return
		case newState := <-stateCh:
			m.log.Debug("VM state changed", "state", newState)
			if newState == vz.VirtualMachineStateStopped ||
				newState == vz.VirtualMachineStateError {
				m.mu.Lock()
				m.running = false
				m.mu.Unlock()
				return
			}
		}
	}
}

// Stop shuts down the VM gracefully.
func (m *DarwinManager) Stop(ctx context.Context) error {
	m.mu.Lock()
	defer m.mu.Unlock()

	if !m.running {
		return nil
	}

	m.log.Info("Stopping Linux VM")

	// Request stop (sends ACPI shutdown signal)
	if m.vm.CanRequestStop() {
		ok, err := m.vm.RequestStop()
		if err != nil {
			m.log.Warn("RequestStop failed, will force stop", "error", err)
		} else if ok {
			// Wait for graceful shutdown with timeout
			stateCh := m.vm.StateChangedNotify()
			timeout := time.After(10 * time.Second)
		waitLoop:
			for {
				select {
				case <-ctx.Done():
					m.log.Warn("Context cancelled during VM shutdown")
					break waitLoop
				case <-timeout:
					m.log.Warn("VM graceful shutdown timed out, forcing stop")
					break waitLoop
				case state := <-stateCh:
					if state == vz.VirtualMachineStateStopped {
						m.running = false
						m.log.Info("Linux VM stopped gracefully")
						return nil
					}
				}
			}
		}
	}

	// Force stop if graceful shutdown failed
	if m.vm.CanStop() {
		if err := m.vm.Stop(); err != nil {
			m.log.Error("Force stop failed", "error", err)
			return fmt.Errorf("failed to stop VM: %w", err)
		}
	}

	m.running = false
	m.log.Info("Linux VM stopped")
	return nil
}

// DialDaemon connects to the VM daemon via vsock.
func (m *DarwinManager) DialDaemon(ctx context.Context) (net.Conn, error) {
	return m.DialPort(ctx, DaemonVsockPort)
}

// DialPort connects to a specific vsock port in the VM.
func (m *DarwinManager) DialPort(ctx context.Context, port uint32) (net.Conn, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()

	if !m.running {
		return nil, fmt.Errorf("VM is not running")
	}

	if m.vsock == nil {
		return nil, fmt.Errorf("vsock device not available")
	}

	conn, err := m.vsock.Connect(port)
	if err != nil {
		return nil, fmt.Errorf("failed to connect to vsock port %d: %w", port, err)
	}

	return conn, nil
}

// IsRunning returns true if the VM is currently running.
func (m *DarwinManager) IsRunning() bool {
	m.mu.RLock()
	defer m.mu.RUnlock()
	return m.running
}

// Ensure DarwinManager implements Manager
var _ Manager = (*DarwinManager)(nil)
