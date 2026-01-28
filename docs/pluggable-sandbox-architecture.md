# Pluggable Sandbox Architecture for macOS + Apple Virtualization Framework

## Overview

This document describes the pluggable sandbox architecture that enables Tempest to run on macOS by modularizing the container/sandbox layer.

## Architecture

```
┌─────────────────────────────────────────────────────────────────────┐
│                         Tempest Server                              │
│  (runs natively on macOS or Linux)                                  │
│                                                                     │
│  ┌─────────────────────────────────────────────────────────────┐   │
│  │                    Container Backend Interface               │   │
│  │  - Spawn(packageID, grainID, args) -> GrainHandle           │   │
│  │  - GrainHandle.Conn() -> net.Conn (for Cap'n Proto RPC)     │   │
│  │  - GrainHandle.Kill() / Wait()                               │   │
│  └─────────────────────────────────────────────────────────────┘   │
│                    │                           │                    │
│         ┌─────────┴─────────┐       ┌─────────┴─────────┐         │
│         │   Local Backend   │       │  Remote Backend   │         │
│         │   (Linux only)    │       │  (macOS + Linux)  │         │
│         └─────────┬─────────┘       └─────────┬─────────┘         │
└─────────────────────────────────────────────────────────────────────┘
                    │                           │
                    ▼                           ▼
        ┌───────────────────┐       ┌───────────────────────────┐
        │ Unix Socketpair   │       │ virtio-vsock connection   │
        │ + exec sandbox-   │       │ to VM daemon              │
        │   launcher        │       │                           │
        └───────────────────┘       └───────────────────────────┘
                                                │
                                                ▼
                                    ┌───────────────────────────┐
                                    │ Linux VM (Apple Virt.fw)  │
                                    │ - tempest-vm-daemon       │
                                    │ - sandbox-launcher        │
                                    │ - grain-agent             │
                                    │ - virtio-fs mounts        │
                                    └───────────────────────────┘
```

## Components

### Backend Interface (`internal/server/container/backend.go`)

```go
type Backend interface {
    Spawn(ctx context.Context, req SpawnRequest) (GrainHandle, error)
    Close() error
}

type GrainHandle interface {
    Conn() net.Conn      // RPC connection
    Kill() error         // Terminate grain
    Wait() error         // Block until exited
    Done() <-chan struct{}
}
```

### Local Backend (`backend_local.go`)

- Linux only (build-tagged)
- Uses unix socketpair + direct process spawning
- Runs `sandbox-launcher` directly
- Same behavior as original implementation

### Remote Backend (`backend_remote.go`)

- Cross-platform
- Connects to VM daemon via virtio-vsock
- Uses Cap'n Proto RPC for grain management
- Bridges vsock connections to grain RPC

### VM Daemon (`cmd/tempest-vm-daemon`)

Runs inside the Linux VM:
- Listens on vsock port 5000 for host connections
- Manages grain lifecycle (spawn/kill)
- Allocates vsock ports per grain (5001+)
- Bridges host vsock to grain's Unix socket
- Runs sandbox-launcher for each grain

### VM Manager (`internal/server/vm/`)

macOS-specific (Apple Virtualization Framework):
- Creates VM with Linux kernel + initrd
- Configures virtio-vsock device
- Sets up virtio-fs shares for packages/grains directories
- Manages VM lifecycle (start/stop)

## Configuration

Environment variables (in `settings.capnp`):

| Variable | Default | Description |
|----------|---------|-------------|
| `TEMPEST_CONTAINER_BACKEND` | `auto` | Backend type: auto, local, remote |
| `TEMPEST_VM_KERNEL` | `{Libexecdir}/tempest/vm/kernel` | Linux kernel path |
| `TEMPEST_VM_INITRD` | `{Libexecdir}/tempest/vm/initrd` | Initrd path |
| `TEMPEST_VM_MEMORY_MB` | `512` | VM memory in MB |
| `TEMPEST_VM_CPUS` | `2` | VM CPU count |

## Platform Behavior

### Linux

- Uses `LocalBackend` by default
- Direct process spawning with sandbox-launcher
- No VM required

### macOS

1. Checks for VM kernel/initrd at configured paths
2. If found: starts VM, creates `RemoteBackend`
3. If not found: falls back to stub backend (server starts but grains don't work)

## VM Image Requirements

To run grains on macOS, you need a Linux VM image containing:

### Kernel
- Linux kernel with:
  - virtio-vsock support (`CONFIG_VIRTIO_VSOCKETS`)
  - virtio-fs support (`CONFIG_VIRTIO_FS`)
  - Rosetta support for x86_64 binaries (on Apple Silicon)

### Initrd/Initramfs
- `tempest-vm-daemon` (as init or early boot service)
- `tempest-sandbox-launcher`
- `tempest-grain-agent`
- Minimal userspace (busybox or similar)
- Mount scripts for virtio-fs shares

### Virtio-FS Mounts

| Tag | Host Path | VM Mount | Purpose |
|-----|-----------|----------|---------|
| `packages` | `{Localstatedir}/sandstorm/apps` | `/packages` | Read-only app images |
| `grains` | `{Localstatedir}/sandstorm/grains` | `/grains` | Read-write grain storage |

## Files

### New Files

| Path | Purpose |
|------|---------|
| `internal/server/container/backend.go` | Interface definitions |
| `internal/server/container/backend_local.go` | Local Linux backend |
| `internal/server/container/backend_remote.go` | VM-based backend |
| `internal/server/container/backend_default_darwin.go` | macOS backend factory |
| `internal/server/container/backend_default_linux.go` | Linux backend factory |
| `internal/server/container/vsock_darwin.go` | macOS vsock dialer |
| `internal/server/container/vsock_linux.go` | Linux vsock dialer |
| `internal/server/vm/manager.go` | VM manager interface |
| `internal/server/vm/manager_darwin.go` | Apple Virtualization implementation |
| `internal/server/vm/manager_linux.go` | Linux stub |
| `internal/capnp/vmdaemon.capnp` | VM daemon RPC schema |
| `cmd/tempest-vm-daemon/main.go` | VM daemon binary |
| `internal/config/container.go` | Container config types |

### Modified Files

| Path | Changes |
|------|---------|
| `internal/server/container/container.go` | Uses Backend interface |
| `internal/server/main/containerset.go` | Passes Backend to Command |
| `internal/server/main/main.go` | Creates backend on startup |
| `internal/server/main/server.go` | Holds backend, closes on shutdown |
| `capnp/settings.capnp` | VM configuration settings |

## Future Work

### ARM64 Seccomp Filter

The current seccomp filter (`c/filter.s`) is x86_64-specific. For native ARM64 support:

1. Create `c/filter_arm64.s` with ARM64 syscall numbers
2. Use `AUDIT_ARCH_AARCH64` (0xc00000b7)
3. Build sandbox-launcher for ARM64

For now, use Rosetta to run x86_64 binaries on Apple Silicon.

### VM Image Build

Create a build process for the minimal Linux VM:
- Kernel configuration and build
- Initramfs generation with required binaries
- Package for distribution
