#!/bin/bash
# Build script for Tempest VM image (kernel + initramfs)
#
# This creates a minimal Linux VM image for running grain containers
# on macOS via Apple Virtualization Framework.
#
# Requirements:
# - Linux build environment (or cross-compilation tools on macOS)
# - curl, tar, make, gcc (or cross-compiler)
# - About 2GB disk space for kernel build
#
# Usage:
#   ./vm/build.sh              # Build everything
#   ./vm/build.sh kernel       # Build only kernel
#   ./vm/build.sh initramfs    # Build only initramfs
#   ./vm/build.sh install      # Install to libexec directory

set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
PROJECT_ROOT="$(cd "$SCRIPT_DIR/.." && pwd)"
BUILD_DIR="${PROJECT_ROOT}/_build/vm"
KERNEL_VERSION="${KERNEL_VERSION:-6.6.70}"
BUSYBOX_VERSION="${BUSYBOX_VERSION:-1.36.1}"

# Target architecture (arm64 for Apple Silicon, x86_64 for Intel)
if [[ "$(uname -m)" == "arm64" ]]; then
    ARCH="arm64"
    CROSS_COMPILE=""
    KERNEL_ARCH="arm64"
else
    ARCH="x86_64"
    CROSS_COMPILE=""
    KERNEL_ARCH="x86_64"
fi

# Colors for output
RED='\033[0;31m'
GREEN='\033[0;32m'
YELLOW='\033[1;33m'
NC='\033[0m' # No Color

log_info() { echo -e "${GREEN}[INFO]${NC} $*"; }
log_warn() { echo -e "${YELLOW}[WARN]${NC} $*"; }
log_error() { echo -e "${RED}[ERROR]${NC} $*"; }

# Check if running on Linux (required for kernel build from source)
check_build_environment() {
    if [[ "$(uname)" != "Linux" ]]; then
        log_warn "Kernel build from source requires Linux."
        log_warn "Use 'download-kernel' to get a pre-built kernel instead."
        return 1
    fi
    return 0
}

# Download a pre-built Linux kernel for VMs
# We use the Kata Containers kernel which has vsock support built-in,
# required for host-guest communication with Apple Virtualization Framework.
download_kernel() {
    local output_dir="$BUILD_DIR/output"
    mkdir -p "$output_dir"

    if [[ -f "$output_dir/kernel" ]]; then
        log_info "Kernel already exists at $output_dir/kernel"
        return 0
    fi

    log_info "Downloading pre-built Linux kernel for VMs..."

    # Use Kata Containers kernel - it has vsock support built-in which is
    # required for Apple Virtualization Framework communication
    local kata_version="3.20.0"
    local tmp_dir="$BUILD_DIR/kernel-download"
    mkdir -p "$tmp_dir"
    cd "$tmp_dir"

    local kata_url
    if [[ "$ARCH" == "arm64" ]]; then
        kata_url="https://github.com/kata-containers/kata-containers/releases/download/${kata_version}/kata-static-${kata_version}-arm64.tar.xz"
    else
        kata_url="https://github.com/kata-containers/kata-containers/releases/download/${kata_version}/kata-static-${kata_version}-amd64.tar.xz"
    fi

    log_info "Downloading Kata Containers kernel from: $kata_url"
    if ! curl -fL -o kata.tar.xz "$kata_url"; then
        log_error "Failed to download Kata Containers release."
        return 1
    fi

    log_info "Extracting kernel..."
    tar -xf kata.tar.xz

    # Find and copy the kernel
    local kernel_file
    kernel_file=$(find . -name "vmlinux-*" -not -name "*dragonball*" -not -name "*nvidia*" | head -1)
    if [[ -z "$kernel_file" ]]; then
        log_error "Could not find kernel in Kata release"
        return 1
    fi

    cp "$kernel_file" "$output_dir/kernel"

    # Clean up
    cd "$BUILD_DIR"
    rm -rf "$tmp_dir"

    log_info "Kernel downloaded: $output_dir/kernel"
    log_info "Note: This is the Kata Containers kernel with vsock support."
}

# Download and extract kernel source (for building from source)
download_kernel_source() {
    local kernel_dir="$BUILD_DIR/linux-$KERNEL_VERSION"

    if [[ -d "$kernel_dir" ]]; then
        log_info "Kernel source already exists at $kernel_dir"
        return 0
    fi

    log_info "Downloading Linux kernel $KERNEL_VERSION source..."
    mkdir -p "$BUILD_DIR"

    local major_version="${KERNEL_VERSION%%.*}"
    local kernel_url="https://cdn.kernel.org/pub/linux/kernel/v${major_version}.x/linux-${KERNEL_VERSION}.tar.xz"

    curl -L -o "$BUILD_DIR/linux-${KERNEL_VERSION}.tar.xz" "$kernel_url"
    log_info "Extracting kernel source..."
    tar -xf "$BUILD_DIR/linux-${KERNEL_VERSION}.tar.xz" -C "$BUILD_DIR"
    rm "$BUILD_DIR/linux-${KERNEL_VERSION}.tar.xz"
}

# Build the kernel
build_kernel() {
    local kernel_dir="$BUILD_DIR/linux-$KERNEL_VERSION"

    log_info "Configuring kernel..."
    cp "$SCRIPT_DIR/kernel.config" "$kernel_dir/.config"

    cd "$kernel_dir"

    # Update config for current kernel version
    make ARCH="$KERNEL_ARCH" olddefconfig

    log_info "Building kernel (this may take a while)..."
    make ARCH="$KERNEL_ARCH" ${CROSS_COMPILE:+CROSS_COMPILE=$CROSS_COMPILE} -j"$(nproc 2>/dev/null || sysctl -n hw.ncpu)" bzImage

    # Copy the kernel image
    mkdir -p "$BUILD_DIR/output"
    if [[ "$KERNEL_ARCH" == "arm64" ]]; then
        cp "$kernel_dir/arch/arm64/boot/Image" "$BUILD_DIR/output/kernel"
    else
        cp "$kernel_dir/arch/x86/boot/bzImage" "$BUILD_DIR/output/kernel"
    fi

    log_info "Kernel built: $BUILD_DIR/output/kernel"
}

# Download pre-built static busybox binary for Linux
download_busybox() {
    local busybox_bin="$BUILD_DIR/busybox"

    if [[ -f "$busybox_bin" ]]; then
        log_info "Busybox binary already exists"
        return 0
    fi

    log_info "Downloading pre-built static Busybox for Linux..."
    mkdir -p "$BUILD_DIR"

    # Detect actual runtime architecture
    local runtime_arch
    runtime_arch=$(uname -m)

    local busybox_url
    if [[ "$runtime_arch" == "aarch64" || "$runtime_arch" == "arm64" ]]; then
        # ARM64 static busybox from Alpine
        busybox_url="https://dl-cdn.alpinelinux.org/alpine/v3.21/main/aarch64/busybox-static-1.37.0-r12.apk"
        log_info "Downloading ARM64 busybox from Alpine..."
        curl -L -o "$BUILD_DIR/busybox.apk" "$busybox_url"
        # Extract busybox from the APK (it's a tar.gz with specific structure)
        cd "$BUILD_DIR"
        tar -xzf busybox.apk bin/busybox.static 2>/dev/null || true
        mv bin/busybox.static busybox 2>/dev/null || true
        rm -rf bin busybox.apk .PKGINFO .SIGN.* 2>/dev/null || true
        chmod +x busybox
    else
        # x86_64 static busybox
        busybox_url="https://busybox.net/downloads/binaries/1.35.0-x86_64-linux-musl/busybox"
        curl -L -o "$busybox_bin" "$busybox_url"
        chmod +x "$busybox_bin"
    fi

    log_info "Busybox downloaded: $busybox_bin"
}

# Build busybox - prefer downloading pre-built binary
build_busybox() {
    # Always prefer downloading pre-built binary (faster and avoids permission issues)
    download_busybox
}

# Build the initramfs
build_initramfs() {
    local initramfs_dir="$BUILD_DIR/initramfs"
    local output_dir="$BUILD_DIR/output"

    log_info "Building initramfs..."

    # Clean and create directory structure
    rm -rf "$initramfs_dir"
    mkdir -p "$initramfs_dir"/{bin,sbin,etc,proc,sys,dev,tmp,run}
    mkdir -p "$initramfs_dir"/{packages,grains}
    mkdir -p "$output_dir"

    # Find and copy busybox
    local busybox_src=""
    if [[ -f "$BUILD_DIR/busybox" ]]; then
        busybox_src="$BUILD_DIR/busybox"
    elif [[ -f "$BUILD_DIR/busybox-$BUSYBOX_VERSION/busybox" ]]; then
        busybox_src="$BUILD_DIR/busybox-$BUSYBOX_VERSION/busybox"
    else
        log_error "Busybox not found. Run 'build_busybox' first."
        exit 1
    fi

    cp "$busybox_src" "$initramfs_dir/bin/busybox"
    chmod +x "$initramfs_dir/bin/busybox"

    # Create busybox symlinks
    cd "$initramfs_dir/bin"
    for cmd in sh ash cat chmod cp dd df echo env false grep head kill ln ls mkdir \
               mknod mount mv pivot_root ps pwd rm rmdir sed sh sleep stat sync \
               tail tar touch true umount uname unshare vi; do
        ln -sf busybox "$cmd"
    done
    cd "$initramfs_dir/sbin"
    for cmd in halt init poweroff reboot switch_root; do
        ln -sf ../bin/busybox "$cmd"
    done

    # Copy tempest binaries (Linux builds use -linux suffix)
    log_info "Copying tempest binaries..."

    # tempest-vm-daemon (runs as init/service in VM)
    if [[ -f "$PROJECT_ROOT/_build/tempest-vm-daemon-linux" ]]; then
        cp "$PROJECT_ROOT/_build/tempest-vm-daemon-linux" "$initramfs_dir/bin/tempest-vm-daemon"
        chmod +x "$initramfs_dir/bin/tempest-vm-daemon"
    else
        log_error "tempest-vm-daemon-linux not found. Run 'mage build' first."
        exit 1
    fi

    # tempest-sandbox-launcher (spawns grain processes)
    # This needs to be built for Linux - check if it exists
    if [[ -f "$PROJECT_ROOT/_build/tempest-sandbox-launcher" ]]; then
        cp "$PROJECT_ROOT/_build/tempest-sandbox-launcher" "$initramfs_dir/bin/"
        chmod +x "$initramfs_dir/bin/tempest-sandbox-launcher"
    else
        log_warn "tempest-sandbox-launcher not found (Linux build required)"
        log_warn "Creating placeholder - sandbox launching won't work without it"
        echo '#!/bin/sh' > "$initramfs_dir/bin/tempest-sandbox-launcher"
        echo 'echo "ERROR: sandbox-launcher not available"' >> "$initramfs_dir/bin/tempest-sandbox-launcher"
        echo 'exit 1' >> "$initramfs_dir/bin/tempest-sandbox-launcher"
        chmod +x "$initramfs_dir/bin/tempest-sandbox-launcher"
    fi

    # tempest-grain-agent (runs inside grain sandbox)
    if [[ -f "$PROJECT_ROOT/_build/tempest-grain-agent-linux" ]]; then
        cp "$PROJECT_ROOT/_build/tempest-grain-agent-linux" "$initramfs_dir/bin/tempest-grain-agent"
        chmod +x "$initramfs_dir/bin/tempest-grain-agent"
    else
        log_warn "tempest-grain-agent-linux not found"
    fi

    # strace-static (for debugging syscalls - optional)
    if [[ -f "$PROJECT_ROOT/_build/strace-static" ]]; then
        cp "$PROJECT_ROOT/_build/strace-static" "$initramfs_dir/bin/strace"
        chmod +x "$initramfs_dir/bin/strace"
        log_info "Included strace for syscall debugging"
    elif [[ -f "/opt/strace/strace-static" ]]; then
        cp "/opt/strace/strace-static" "$initramfs_dir/bin/strace"
        chmod +x "$initramfs_dir/bin/strace"
        log_info "Included strace for syscall debugging"
    fi

    # Copy init script
    cp "$SCRIPT_DIR/initramfs/init" "$initramfs_dir/init"
    chmod +x "$initramfs_dir/init"

    # Create device nodes (only works on Linux as root, skip on macOS)
    # The VM uses devtmpfs which auto-populates /dev at boot
    if [[ "$(uname)" == "Linux" ]] && [[ $EUID -eq 0 ]]; then
        cd "$initramfs_dir/dev"
        mknod -m 622 console c 5 1 2>/dev/null || true
        mknod -m 666 null c 1 3 2>/dev/null || true
        mknod -m 666 zero c 1 5 2>/dev/null || true
        mknod -m 666 random c 1 8 2>/dev/null || true
        mknod -m 666 urandom c 1 9 2>/dev/null || true
    else
        log_warn "Skipping device node creation (requires Linux root). Using devtmpfs."
    fi

    # Create the initramfs cpio archive
    log_info "Creating initramfs archive..."
    cd "$initramfs_dir"
    if [[ "$(uname)" == "Darwin" ]]; then
        # macOS cpio has different options
        find . -print0 | cpio -o -0 -H newc 2>/dev/null | gzip -9 > "$output_dir/initrd"
    else
        find . -print0 | cpio --null -ov --format=newc 2>/dev/null | gzip -9 > "$output_dir/initrd"
    fi

    log_info "Initramfs built: $output_dir/initrd"
}

# Install to libexec directory
install_vm() {
    local output_dir="$BUILD_DIR/output"

    # Read install prefix from config.json if it exists
    local install_dir="/tmp/tempest/libexec/tempest/vm"
    if [[ -f "$PROJECT_ROOT/config.json" ]]; then
        local libexecdir
        libexecdir=$(grep -o '"Libexecdir"[[:space:]]*:[[:space:]]*"[^"]*"' "$PROJECT_ROOT/config.json" | cut -d'"' -f4)
        if [[ -n "$libexecdir" ]]; then
            install_dir="$libexecdir/tempest/vm"
        fi
    fi

    log_info "Installing VM images to $install_dir..."
    mkdir -p "$install_dir"

    if [[ -f "$output_dir/kernel" ]]; then
        cp "$output_dir/kernel" "$install_dir/kernel"
        log_info "Installed kernel"
    else
        log_warn "Kernel not found in $output_dir"
    fi

    if [[ -f "$output_dir/initrd" ]]; then
        cp "$output_dir/initrd" "$install_dir/initrd"
        log_info "Installed initrd"
    else
        log_warn "Initrd not found in $output_dir"
    fi

    log_info "VM images installed to $install_dir"
}

# Clean build artifacts
clean() {
    log_info "Cleaning VM build artifacts..."
    rm -rf "$BUILD_DIR"
    log_info "Done"
}

# Print usage
usage() {
    echo "Usage: $0 [command]"
    echo ""
    echo "Commands:"
    echo "  all              Build everything (download kernel + build initramfs)"
    echo "  download-kernel  Download pre-built Linux kernel for VMs"
    echo "  build-kernel     Build kernel from source (Linux only)"
    echo "  initramfs        Build only the initramfs"
    echo "  busybox          Build/download busybox"
    echo "  install          Install VM images to libexec directory"
    echo "  clean            Remove build artifacts"
    echo ""
    echo "Environment variables:"
    echo "  KERNEL_VERSION   Linux kernel version for building from source (default: $KERNEL_VERSION)"
    echo "  BUSYBOX_VERSION  Busybox version (default: $BUSYBOX_VERSION)"
}

# Main
main() {
    local cmd="${1:-all}"

    case "$cmd" in
        all)
            # On macOS, download pre-built kernel; on Linux, build from source
            if check_build_environment; then
                download_kernel_source
                build_kernel
            else
                download_kernel
            fi
            build_busybox
            build_initramfs
            ;;
        download-kernel)
            download_kernel
            ;;
        build-kernel|kernel)
            check_build_environment || exit 1
            download_kernel_source
            build_kernel
            ;;
        initramfs)
            build_busybox
            build_initramfs
            ;;
        busybox)
            build_busybox
            ;;
        install)
            install_vm
            ;;
        clean)
            clean
            ;;
        help|--help|-h)
            usage
            ;;
        *)
            log_error "Unknown command: $cmd"
            usage
            exit 1
            ;;
    esac
}

main "$@"
