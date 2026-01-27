# Tempest VM Image Build

This directory contains scripts and configuration for building the Linux VM image
used to run grain containers on macOS.

## Overview

On macOS, Tempest uses Apple Virtualization Framework to run a lightweight Linux VM.
The VM contains:

- **Linux kernel** with virtio-vsock and virtio-fs support
- **tempest-vm-daemon** - manages grain containers inside the VM
- **tempest-sandbox-launcher** - spawns sandboxed grain processes
- **tempest-grain-agent** - runs inside each grain sandbox
- **busybox** - provides basic shell utilities

## Quick Start

### Option 1: Build on Linux (recommended)

The kernel must be built on Linux. You can use a Linux VM, container, or remote machine:

```bash
# On a Linux machine with the tempest repo cloned:
./vm/build.sh all
./vm/build.sh install
```

### Option 2: Build using Docker

```bash
# From macOS (requires Docker)
docker run --rm -v "$(pwd):/src" -w /src ubuntu:22.04 bash -c '
    apt-get update && apt-get install -y build-essential flex bison bc libelf-dev libssl-dev curl cpio gzip
    ./vm/build.sh all
'
./vm/build.sh install
```

### Option 3: Use pre-built images

Download pre-built kernel and initrd from [GitHub Releases](https://github.com/mnutt/tempest/releases):

```bash
curl -L -o tempest-vm.tar.gz https://github.com/mnutt/tempest/releases/latest/download/tempest-vm-arm64.tar.gz
tar -xzf tempest-vm.tar.gz
mkdir -p /tmp/tempest/libexec/tempest/vm
mv kernel initrd /tmp/tempest/libexec/tempest/vm/
```

## Build Commands

```bash
./vm/build.sh all        # Build everything (kernel + initramfs)
./vm/build.sh kernel     # Build only the Linux kernel
./vm/build.sh initramfs  # Build only the initramfs
./vm/build.sh busybox    # Build only busybox
./vm/build.sh install    # Install to libexec directory
./vm/build.sh clean      # Remove build artifacts
```

## Directory Structure

```
vm/
├── build.sh           # Main build script
├── kernel.config      # Minimal kernel configuration
├── initramfs/
│   └── init          # VM init script (runs as PID 1)
└── README.md         # This file

_build/vm/             # Build artifacts (created during build)
├── linux-X.X.X/      # Kernel source
├── busybox-X.X.X/    # Busybox source
├── initramfs/        # Initramfs staging directory
└── output/
    ├── kernel        # Built kernel image
    └── initrd        # Built initramfs
```

## Kernel Configuration

The kernel config (`kernel.config`) is minimal and includes only:

- **virtio-vsock** - Host-guest communication via socket
- **virtio-fs** - Shared filesystem access (packages, grains)
- **virtio-console** - Serial console output
- **namespaces** - User, PID, mount namespaces for sandboxing
- **seccomp** - Syscall filtering for security
- **cgroups** - Resource limits

Disabled:
- All hardware drivers (no PCI devices, USB, etc.)
- Networking stack (only Unix sockets and vsock)
- Graphics/display
- Sound
- Most filesystems (only ext4, proc, sys, tmpfs, devtmpfs, virtiofs)

## Initramfs Contents

The initramfs contains:

```
/
├── bin/
│   ├── busybox              # Shell utilities
│   ├── sh -> busybox        # Symlinks for common commands
│   ├── tempest-vm-daemon    # VM daemon
│   ├── tempest-sandbox-launcher
│   └── tempest-grain-agent
├── sbin/
│   └── init -> ../bin/busybox
├── dev/                     # Device nodes
├── proc/                    # Mountpoint for procfs
├── sys/                     # Mountpoint for sysfs
├── tmp/                     # Temporary files
├── packages/                # Mountpoint for virtiofs (read-only)
├── grains/                  # Mountpoint for virtiofs (read-write)
└── init                     # Init script
```

## How It Works

1. **Boot**: Kernel loads, mounts initramfs as root filesystem
2. **Init**: `/init` script runs as PID 1:
   - Mounts proc, sys, dev filesystems
   - Mounts virtio-fs shares for `/packages` and `/grains`
   - Starts `tempest-vm-daemon`
3. **Daemon**: `tempest-vm-daemon` listens on vsock port 5000
4. **Host**: Tempest server connects via vsock, sends RPC commands
5. **Grains**: Daemon spawns sandboxed grain processes on request

## Virtio-FS Mounts

The host shares two directories with the VM:

| Tag | Host Path | VM Mount | Mode |
|-----|-----------|----------|------|
| `packages` | `{localstatedir}/sandstorm/apps` | `/packages` | read-only |
| `grains` | `{localstatedir}/sandstorm/grains` | `/grains` | read-write |

## Environment Variables

| Variable | Default | Description |
|----------|---------|-------------|
| `TEMPEST_VM_MEMORY_MB` | 1024 | VM memory in megabytes |
| `TEMPEST_VM_CPU_COUNT` | 2 | Number of vCPUs |

## Troubleshooting

### Kernel build fails

Make sure you have build dependencies:
```bash
# Ubuntu/Debian
apt-get install build-essential flex bison bc libelf-dev libssl-dev

# Fedora/RHEL
dnf install gcc make flex bison bc elfutils-libelf-devel openssl-devel
```

### VM doesn't boot

Check the console output. The VM should print boot messages to the virtio console.
Common issues:
- Missing kernel config options
- Init script errors
- Missing device nodes

### Grains don't spawn

Check that:
1. `tempest-sandbox-launcher` is built for Linux and included in initramfs
2. `/packages` and `/grains` are mounted correctly
3. Package files exist in the shared directory

### Connection refused

The VM daemon may not be ready yet. The host should retry connections
with exponential backoff (implemented in `backend_default_darwin.go`).

## Development

To iterate quickly during development:

1. Build kernel once (slow, but only needed once)
2. Rebuild initramfs (fast) after changing tempest binaries
3. Use `./vm/build.sh install` to update installed images

```bash
# After changing tempest-vm-daemon:
mage build
./vm/build.sh initramfs
./vm/build.sh install
# Restart tempest server to pick up new VM image
```

## Future Improvements

- [x] ARM64 seccomp filter for native Apple Silicon support
- [x] Pre-built kernel/initrd in releases (GitHub Actions workflow)
- [x] Rosetta integration for x86_64 grains on ARM64
- [x] Minimal kernel config (disabled hardware, networking, graphics, etc.)
- [x] Memory/CPU configuration via environment variables (TEMPEST_VM_MEMORY_MB, TEMPEST_VM_CPU_COUNT)
