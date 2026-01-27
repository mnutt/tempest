# VM Sandbox for macOS (Apple Silicon)

This document describes the VM-based sandbox implementation for running Sandstorm grains on macOS with Apple Silicon.

## Overview

On macOS, we can't use Linux namespaces directly, so we run grains inside a lightweight Linux VM using Apple's Virtualization.framework. The VM runs a minimal Linux kernel with an initramfs containing the sandbox tooling.

## Current Status

**Working:**
- VM boots successfully with Linux kernel
- virtio-fs shares for packages and grains
- Rosetta directory share configured
- binfmt_misc registration for x86_64 binaries
- Grain spawning via sandbox-launcher
- Rosetta x86_64 binary translation in sandboxed grains
- Seccomp filter with specific Rosetta ioctls allowed

## Key Files

### VM Configuration (Go)

- `internal/server/vm/manager_darwin.go` - VM lifecycle management, configures Virtualization.framework
- `internal/server/vm/manager.go` - Interface for VM manager
- `internal/server/container/backend_remote.go` - Communicates with VM daemon over vsock

### VM Init & Daemon

- `vm/initramfs/init` - Shell script that runs as PID 1 in the VM, mounts filesystems, registers binfmt_misc, starts daemon
- `cmd/tempest-vm-daemon/main.go` - Daemon running inside VM that receives grain spawn requests

### Sandbox Launcher (C)

- `c/sandbox-launcher.c` - Sets up the grain sandbox (namespaces, mounts, pivot_root, seccomp)
- `c/filter.s` - Seccomp BPF filter rules (assembly)
- `c/config.h` - Generated config with paths

### Grain Agent (Go)

- `internal/server/grain-agent/main/main.go` - Runs inside sandbox, launches the actual app

### Build System

- `magefiles/vm.go` - Mage targets for VM builds
- `magefiles/build.go` - General build targets

## Build Commands

```bash
# Full VM build (uses Docker for cross-compilation)
mage vm:docker

# This command:
# 1. Builds Go binaries for Linux/ARM64
# 2. Cross-compiles sandbox-launcher.c for Linux
# 3. Compiles the seccomp BPF filter
# 4. Builds the initramfs with busybox + tempest binaries
# 5. Installs to /tmp/tempest/libexec/tempest/vm/

# Run tempest (starts VM automatically)
_build/tempest

# View VM console output
tail -f /tmp/tempest-vm-console.log
```

### Rebuilding the Docker Image

The Docker image (`tempest-vm-builder`) is cached for faster rebuilds. You need to
rebuild it when:

- `vm/Dockerfile.build` changes
- New cross-compilation tools are needed

```bash
# Remove the cached Docker image to force a rebuild
docker rmi tempest-vm-builder

# Rebuild everything (will create new Docker image)
mage vm:docker
```

## Architecture

```
macOS Host
├── tempest (main server)
│   └── Virtualization.framework VM
│       ├── Linux kernel (arm64)
│       ├── initramfs
│       │   ├── tempest-vm-daemon (listens on vsock:5000)
│       │   ├── tempest-sandbox-launcher
│       │   └── tempest-grain-agent
│       └── virtio-fs mounts
│           ├── packages → /sandstorm/apps
│           ├── grains → /sandstorm/grains
│           └── rosetta → /tmp/rosetta
```

## Rosetta Integration

Rosetta enables running x86_64 Linux binaries on ARM64. The setup:

1. **Host side**: Virtualization.framework shares Rosetta via virtio-fs
2. **VM init**: Mounts Rosetta at `/tmp/rosetta`, registers binfmt_misc with F flag
3. **Sandbox**: Bind mounts Rosetta from VM's `/tmp/rosetta` into the sandbox

### binfmt_misc Registration

```bash
echo ':rosetta:M::\x7fELF\x02\x01\x01\x00\x00\x00\x00\x00\x00\x00\x00\x00\x02\x00\x3e\x00:\xff\xff\xff\xff\xff\xfe\xfe\x00\xff\xff\xff\xff\xff\xff\xff\xff\xfe\xff\xff\xff:/tmp/rosetta/rosetta:OCF' > /proc/sys/fs/binfmt_misc/register
```

Flags:
- `O` - Open binary
- `C` - Credentials
- `F` - Fix binary (kernel opens interpreter at registration, caches fd)

### Rosetta in Sandboxes

The sandbox-launcher bind mounts `/tmp/rosetta` from the VM's root namespace into each sandbox. This preserves the virtiofs ioctl context that Rosetta needs for verification.

## Seccomp Filter

The sandbox uses seccomp-bpf to restrict syscalls. Rosetta requires 4 specific ioctls for x86_64 binary translation:

```assembly
// c/filter.s - Rosetta ioctls
jeq #0x80456122, allow  // _IOC(_IOC_READ, 0x61, 0x22, 0x45) - verification
jeq #0x80806123, allow  // _IOC(_IOC_READ, 0x61, 0x23, 0x80) - runtime translation
jeq #0x00006124, allow  // _IOC(_IOC_NONE, 0x61, 0x24, 0) - runtime control
jeq #0x80456125, allow  // _IOC(_IOC_READ, 0x61, 0x25, 0x45) - runtime verification
```

These ioctls use type 0x61 ('a'), which is registered for ATM/QAT in Linux but neither exists in Apple VMs - these are Rosetta-specific ioctls that communicate with the macOS hypervisor.

## Troubleshooting

### Rosetta Verification Errors

If grains fail with:
```
rosetta error: Rosetta is only intended to run on Apple Silicon with a macOS host using Virtualization.framework with Rosetta mode enabled
```

This typically means:
1. The Rosetta virtiofs share isn't mounted correctly
2. The bind mount from `/tmp/rosetta` failed
3. A required ioctl is being blocked by the seccomp filter

Check the VM console log for mount errors:
```bash
tail -f /tmp/tempest-vm-console.log
```

### Adding New Rosetta Ioctls

If Rosetta is updated and starts using additional ioctls, you may see:
```
rosetta error: Unexpected ioctl error when communicating with hypervisor: 25
```

Error 25 is ENOTTY, returned by the seccomp filter for unrecognized ioctls. To fix:
1. Use strace to capture the new ioctl numbers
2. Add the new ioctl hex values to `c/filter.s`
3. Rebuild with `mage vm:docker`

## References

- Lima Rosetta implementation: https://github.com/lima-vm/lima/blob/main/pkg/driver/vz/rosetta_directory_share_arm64.go
- Lima binfmt script: https://github.com/lima-vm/lima/blob/main/pkg/driver/vz/boot/05-rosetta-volume.sh
