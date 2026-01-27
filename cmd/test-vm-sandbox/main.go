//go:build darwin

// test-vm-sandbox boots a Linux VM and runs sandbox tests inside it.
// Usage: timeout 30 _build/test-vm-sandbox
package main

import (
	"context"
	"fmt"
	"io"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/Code-Hex/vz/v3"
)

func main() {
	if err := run(); err != nil {
		fmt.Fprintf(os.Stderr, "Error: %v\n", err)
		os.Exit(1)
	}
}

func run() error {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	// Handle signals
	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, syscall.SIGINT, syscall.SIGTERM)
	go func() {
		<-sigCh
		fmt.Println("\nReceived signal, shutting down...")
		cancel()
	}()

	// Find VM images
	libexecDir := os.Getenv("TEMPEST_LIBEXECDIR")
	if libexecDir == "" {
		libexecDir = "/tmp/tempest/libexec"
	}
	kernelPath := libexecDir + "/tempest/vm/kernel"
	initrdPath := libexecDir + "/tempest/vm/initrd"

	// Check files exist
	if _, err := os.Stat(kernelPath); err != nil {
		return fmt.Errorf("kernel not found at %s: %w", kernelPath, err)
	}
	if _, err := os.Stat(initrdPath); err != nil {
		return fmt.Errorf("initrd not found at %s: %w", initrdPath, err)
	}

	// Find data directories
	localstateDir := os.Getenv("TEMPEST_LOCALSTATEDIR")
	if localstateDir == "" {
		localstateDir = "/tmp/tempest/var"
	}
	packagesDir := localstateDir + "/sandstorm/apps"
	grainsDir := localstateDir + "/sandstorm/grains"

	fmt.Printf("Kernel: %s\n", kernelPath)
	fmt.Printf("Initrd: %s\n", initrdPath)
	fmt.Printf("Packages: %s\n", packagesDir)
	fmt.Printf("Grains: %s\n", grainsDir)
	fmt.Println()

	// Create boot loader with test command in cmdline
	// Use test-mount for C code testing, test-sandbox for Go/shell testing
	testMode := os.Getenv("TEST_MODE")
	if testMode == "" {
		testMode = "test-sandbox" // default to shell-based test
	}
	fmt.Printf("Test mode: %s\n", testMode)

	// Check if we should test without the F flag in binfmt_misc
	// This helps isolate whether the F flag is causing issues
	cmdlineExtra := ""
	if os.Getenv("TEST_NO_F_FLAG") != "" {
		cmdlineExtra = " no-f-flag"
		fmt.Println("Testing WITHOUT binfmt_misc F flag")
	}

	bootLoader, err := vz.NewLinuxBootLoader(
		kernelPath,
		vz.WithInitrd(initrdPath),
		vz.WithCommandLine("console=hvc0 rdinit=/init panic=-1 loglevel=3 "+testMode+cmdlineExtra),
	)
	if err != nil {
		return fmt.Errorf("failed to create boot loader: %w", err)
	}

	// Create VM configuration
	vmConfig, err := vz.NewVirtualMachineConfiguration(
		bootLoader,
		2,             // CPUs
		1024*1024*1024, // 1GB memory
	)
	if err != nil {
		return fmt.Errorf("failed to create VM config: %w", err)
	}

	// Platform config
	platformConfig, err := vz.NewGenericPlatformConfiguration()
	if err != nil {
		return fmt.Errorf("failed to create platform config: %w", err)
	}
	vmConfig.SetPlatformVirtualMachineConfiguration(platformConfig)

	// Entropy device
	entropyConfig, err := vz.NewVirtioEntropyDeviceConfiguration()
	if err != nil {
		return fmt.Errorf("failed to create entropy config: %w", err)
	}
	vmConfig.SetEntropyDevicesVirtualMachineConfiguration([]*vz.VirtioEntropyDeviceConfiguration{entropyConfig})

	// Serial console - output to stdout
	readPipe, writePipe, err := os.Pipe()
	if err != nil {
		return fmt.Errorf("failed to create pipe: %w", err)
	}
	defer readPipe.Close()
	defer writePipe.Close()

	// Copy console output to stdout in background
	go func() {
		io.Copy(os.Stdout, readPipe)
	}()

	devNull, err := os.Open("/dev/null")
	if err != nil {
		return fmt.Errorf("failed to open /dev/null: %w", err)
	}
	defer devNull.Close()

	serialAttachment, err := vz.NewFileHandleSerialPortAttachment(devNull, writePipe)
	if err != nil {
		return fmt.Errorf("failed to create serial attachment: %w", err)
	}
	serialConfig, err := vz.NewVirtioConsoleDeviceSerialPortConfiguration(serialAttachment)
	if err != nil {
		return fmt.Errorf("failed to create serial config: %w", err)
	}
	vmConfig.SetSerialPortsVirtualMachineConfiguration([]*vz.VirtioConsoleDeviceSerialPortConfiguration{serialConfig})

	// Set up virtio-fs shares
	var shares []vz.DirectorySharingDeviceConfiguration

	// Packages share
	if _, err := os.Stat(packagesDir); err == nil {
		packagesShare, err := vz.NewSharedDirectory(packagesDir, true)
		if err != nil {
			return fmt.Errorf("failed to create packages share: %w", err)
		}
		packagesMount, err := vz.NewVirtioFileSystemDeviceConfiguration("packages")
		if err != nil {
			return fmt.Errorf("failed to create packages mount: %w", err)
		}
		packagesSingleShare, err := vz.NewSingleDirectoryShare(packagesShare)
		if err != nil {
			return fmt.Errorf("failed to create packages single share: %w", err)
		}
		packagesMount.SetDirectoryShare(packagesSingleShare)
		shares = append(shares, packagesMount)
	} else {
		fmt.Printf("Warning: packages dir not found: %s\n", packagesDir)
	}

	// Grains share
	if _, err := os.Stat(grainsDir); err == nil {
		grainsShare, err := vz.NewSharedDirectory(grainsDir, false)
		if err != nil {
			return fmt.Errorf("failed to create grains share: %w", err)
		}
		grainsMount, err := vz.NewVirtioFileSystemDeviceConfiguration("grains")
		if err != nil {
			return fmt.Errorf("failed to create grains mount: %w", err)
		}
		grainsSingleShare, err := vz.NewSingleDirectoryShare(grainsShare)
		if err != nil {
			return fmt.Errorf("failed to create grains single share: %w", err)
		}
		grainsMount.SetDirectoryShare(grainsSingleShare)
		shares = append(shares, grainsMount)
	} else {
		fmt.Printf("Warning: grains dir not found: %s\n", grainsDir)
	}

	// Rosetta share
	availability := vz.LinuxRosettaDirectoryShareAvailability()
	if availability == vz.LinuxRosettaAvailabilityInstalled {
		rosettaShare, err := vz.NewLinuxRosettaDirectoryShare()
		if err != nil {
			fmt.Printf("Warning: failed to create Rosetta share: %v\n", err)
		} else {
			rosettaMount, err := vz.NewVirtioFileSystemDeviceConfiguration("rosetta")
			if err != nil {
				fmt.Printf("Warning: failed to create Rosetta mount: %v\n", err)
			} else {
				rosettaMount.SetDirectoryShare(rosettaShare)
				shares = append(shares, rosettaMount)
				fmt.Println("Rosetta share enabled")
			}
		}
	} else {
		fmt.Printf("Warning: Rosetta not available (status=%d)\n", availability)
	}

	vmConfig.SetDirectorySharingDevicesVirtualMachineConfiguration(shares)

	// Validate config
	valid, err := vmConfig.Validate()
	if err != nil {
		return fmt.Errorf("VM config validation failed: %w", err)
	}
	if !valid {
		return fmt.Errorf("VM config is invalid")
	}

	// Create VM
	vm, err := vz.NewVirtualMachine(vmConfig)
	if err != nil {
		return fmt.Errorf("failed to create VM: %w", err)
	}

	fmt.Println("Starting VM...")
	fmt.Println("---")

	// Start VM
	if err := vm.Start(); err != nil {
		return fmt.Errorf("failed to start VM: %w", err)
	}

	// Wait for VM to stop or context cancellation
	stateCh := vm.StateChangedNotify()
	for {
		select {
		case <-ctx.Done():
			fmt.Println("---")
			fmt.Println("Context cancelled, stopping VM...")
			if vm.CanStop() {
				vm.Stop()
			}
			return nil
		case state := <-stateCh:
			if state == vz.VirtualMachineStateStopped {
				fmt.Println("---")
				fmt.Println("VM stopped")
				return nil
			}
			if state == vz.VirtualMachineStateError {
				return fmt.Errorf("VM entered error state")
			}
		case <-time.After(60 * time.Second):
			fmt.Println("---")
			fmt.Println("Timeout waiting for VM, stopping...")
			if vm.CanStop() {
				vm.Stop()
			}
			return fmt.Errorf("VM timed out")
		}
	}
}
