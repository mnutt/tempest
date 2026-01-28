//go:build mage

package main

import (
	"archive/tar"
	"compress/gzip"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"

	"github.com/magefile/mage/mg"
	"github.com/magefile/mage/sh"
)

const (
	vmReleaseVersion = "v0.0.1"
	vmReleaseBaseURL = "https://github.com/mnutt/tempest/releases/download"
)

// VM contains targets for building the Linux VM image
type VM mg.Namespace

// Build builds the VM image (kernel + initramfs)
// Note: Kernel build requires Linux. On macOS, only initramfs is built.
func (VM) Build() error {
	mg.Deps(Build) // Ensure tempest binaries are built first
	fmt.Println("Building VM image...")
	return sh.RunV("./vm/build.sh", "all")
}

// Initramfs builds only the initramfs (works on macOS)
func (VM) Initramfs() error {
	mg.Deps(Build) // Ensure tempest binaries are built first
	fmt.Println("Building VM initramfs...")
	return sh.RunV("./vm/build.sh", "initramfs")
}

// Kernel builds only the kernel (requires Linux)
func (VM) Kernel() error {
	if runtime.GOOS != "linux" {
		return fmt.Errorf("kernel build requires Linux (current OS: %s)", runtime.GOOS)
	}
	fmt.Println("Building VM kernel...")
	return sh.RunV("./vm/build.sh", "kernel")
}

// Install installs the VM images to libexec directory
func (VM) Install() error {
	fmt.Println("Installing VM images...")
	return sh.RunV("./vm/build.sh", "install")
}

// Clean removes VM build artifacts
func (VM) Clean() error {
	fmt.Println("Cleaning VM build artifacts...")
	return sh.RunV("./vm/build.sh", "clean")
}

// Docker builds the VM image using Docker (for building on macOS)
func (VM) Docker() error {
	mg.Deps(Build) // Ensure tempest binaries are built first

	fmt.Println("Building VM image in Docker...")

	// Check if Docker is available
	if _, err := exec.LookPath("docker"); err != nil {
		return fmt.Errorf("docker not found in PATH")
	}

	cwd, err := os.Getwd()
	if err != nil {
		return err
	}

	// Build or reuse the cached Docker image
	imageName := "tempest-vm-builder"
	platform := "linux/" + runtime.GOARCH

	// Check if image exists
	checkCmd := exec.Command("docker", "image", "inspect", imageName)
	if err := checkCmd.Run(); err != nil {
		fmt.Println("Building Docker image (this is cached for future runs)...")
		buildCmd := exec.Command("docker", "build",
			"--platform", platform,
			"-t", imageName,
			"-f", "vm/Dockerfile.build",
			".",
		)
		buildCmd.Stdout = os.Stdout
		buildCmd.Stderr = os.Stderr
		if err := buildCmd.Run(); err != nil {
			return fmt.Errorf("failed to build Docker image: %w", err)
		}
	} else {
		fmt.Println("Using cached Docker image...")
	}

	// Run build in the cached container
	cmd := exec.Command("docker", "run", "--rm",
		"-v", cwd+":/src",
		"-w", "/src",
		"--platform", platform,
		imageName,
		"./vm/docker-build.sh",
	)
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr

	if err := cmd.Run(); err != nil {
		return fmt.Errorf("docker build failed: %w", err)
	}

	// Install the built images
	return sh.RunV("./vm/build.sh", "install")
}

// Download downloads pre-built VM images from GitHub releases
// This is the fastest way to get started on macOS
func (VM) Download() error {
	arch := runtime.GOARCH
	if arch == "amd64" {
		arch = "x86_64"
	}

	url := fmt.Sprintf("%s/%s/tempest-vm-%s.tar.gz", vmReleaseBaseURL, vmReleaseVersion, arch)
	outputDir := filepath.Join(buildDir, "vm", "output")

	// Check if already downloaded
	kernelPath := filepath.Join(outputDir, "kernel")
	initrdPath := filepath.Join(outputDir, "initrd")
	if _, err := os.Stat(kernelPath); err == nil {
		if _, err := os.Stat(initrdPath); err == nil {
			fmt.Println("VM images already downloaded. Run 'mage vm:install' to install.")
			return nil
		}
	}

	fmt.Printf("Downloading pre-built VM from %s...\n", url)

	if err := os.MkdirAll(outputDir, 0755); err != nil {
		return fmt.Errorf("failed to create output directory: %w", err)
	}

	resp, err := http.Get(url)
	if err != nil {
		return fmt.Errorf("failed to download VM: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("failed to download VM: HTTP %d", resp.StatusCode)
	}

	gzr, err := gzip.NewReader(resp.Body)
	if err != nil {
		return fmt.Errorf("failed to decompress: %w", err)
	}
	defer gzr.Close()

	tr := tar.NewReader(gzr)
	for {
		header, err := tr.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			return fmt.Errorf("failed to read tar: %w", err)
		}

		// Only extract kernel and initrd
		name := filepath.Base(header.Name)
		if name != "kernel" && name != "initrd" {
			continue
		}

		target := filepath.Join(outputDir, name)
		f, err := os.Create(target)
		if err != nil {
			return fmt.Errorf("failed to create %s: %w", name, err)
		}

		if _, err := io.Copy(f, tr); err != nil {
			f.Close()
			return fmt.Errorf("failed to write %s: %w", name, err)
		}
		f.Close()
		fmt.Printf("Extracted %s\n", name)
	}

	fmt.Println("")
	fmt.Println("VM images downloaded successfully!")
	fmt.Println("Run 'mage vm:install' to install to libexec directory.")

	return nil
}

// QuickStart builds initramfs and installs (for development iteration)
func (VM) QuickStart() error {
	mg.Deps(Build)

	// Check if kernel exists
	cfg := readConfig()
	kernelPath := filepath.Join(cfg.Libexecdir, "tempest", "vm", "kernel")

	if _, err := os.Stat(kernelPath); os.IsNotExist(err) {
		fmt.Println("Kernel not found. You need to get it first:")
		fmt.Println("")
		fmt.Println("  Option 1 (fastest): mage vm:download && mage vm:install")
		fmt.Println("  Option 2 (Docker):  mage vm:docker")
		fmt.Println("  Option 3 (Linux):   ./vm/build.sh kernel && ./vm/build.sh install")
		fmt.Println("")
		fmt.Println("For now, building initramfs only...")
	}

	// Build and install initramfs
	if err := sh.RunV("./vm/build.sh", "initramfs"); err != nil {
		return err
	}

	return sh.RunV("./vm/build.sh", "install")
}
