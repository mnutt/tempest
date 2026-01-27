//go:build mage

package main

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"

	"github.com/magefile/mage/mg"
	"github.com/magefile/mage/sh"
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

	// Build script - now much simpler since packages and bpf_asm are pre-installed
	buildScript := `
set -e

# Build the C sandbox-launcher
echo "Building C sandbox-launcher..."
mkdir -p _build

# Clean any existing C build artifacts (may be wrong architecture)
rm -f _build/gen-clean-h _build/constants.h _build/filter_preproc.s _build/bpf_filter.h
rm -f _build/tempest-sandbox-launcher c/sandbox-launcher.o

# Generate constants.h
cc -o _build/gen-clean-h c/gen-clean-h.c
_build/gen-clean-h > _build/constants.h

# Preprocess and assemble BPF filter
cpp -I _build c/filter.s -o _build/filter_preproc.s

# Use pre-built bpf_asm from the Docker image
/opt/bpf_build/bpf_asm -c < _build/filter_preproc.s > _build/bpf_filter.h

# Compile sandbox-launcher with VM-specific paths
# Inside the VM:
#   - binaries are in /bin (LIBEXECDIR)
#   - apps/grains are in /sandstorm (LOCALSTATEDIR="")
cc -I _build -std=c11 -Wall -Wextra -static -DLIBEXECDIR='"/bin"' -DLOCALSTATEDIR='""' -c -o c/sandbox-launcher.o c/sandbox-launcher.c
cc -static -o _build/tempest-sandbox-launcher c/sandbox-launcher.o

echo "Built _build/tempest-sandbox-launcher"

# Build test-mount if it exists
if [ -f c/test-mount.c ]; then
    cc -I _build -std=c11 -Wall -Wextra -static -DLIBEXECDIR='"/bin"' -DLOCALSTATEDIR='""' -o _build/test-mount c/test-mount.c
    echo "Built _build/test-mount"
fi

# Build x86_64 test binary for binfmt_misc testing
if [ -f c/hello-x86_64.c ]; then
    x86_64-linux-gnu-gcc -static -o _build/hello-x86_64 c/hello-x86_64.c
    echo "Built _build/hello-x86_64 (x86_64 static binary for binfmt_misc test)"
fi

# Build initramfs
./vm/build.sh initramfs
`

	// Run build in the cached container
	cmd := exec.Command("docker", "run", "--rm",
		"-v", cwd+":/src",
		"-w", "/src",
		"--platform", platform,
		imageName,
		"bash", "-c", buildScript,
	)
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr

	if err := cmd.Run(); err != nil {
		return fmt.Errorf("docker build failed: %w", err)
	}

	// Install the built images
	return sh.RunV("./vm/build.sh", "install")
}

// QuickStart builds initramfs and installs (for development iteration)
func (VM) QuickStart() error {
	mg.Deps(Build)

	// Check if kernel exists
	cfg := readConfig()
	kernelPath := filepath.Join(cfg.Libexecdir, "tempest", "vm", "kernel")

	if _, err := os.Stat(kernelPath); os.IsNotExist(err) {
		fmt.Println("Kernel not found. You need to build it first:")
		fmt.Println("")
		fmt.Println("  Option 1 (Docker): mage vm:docker")
		fmt.Println("  Option 2 (Linux):  ./vm/build.sh kernel && ./vm/build.sh install")
		fmt.Println("")
		fmt.Println("For now, building initramfs only...")
	}

	// Build and install initramfs
	if err := sh.RunV("./vm/build.sh", "initramfs"); err != nil {
		return err
	}

	return sh.RunV("./vm/build.sh", "install")
}
