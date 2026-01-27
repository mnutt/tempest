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

**Resolved Issues:**
- Rosetta verification ioctl was being blocked by seccomp filter
- Fixed by identifying all required ioctls via strace and adding them to `c/filter.s`

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

### Test Tools

- `cmd/test-vm-sandbox/main.go` - Standalone macOS binary that boots VM and runs sandbox tests
- `cmd/test-sandbox/main.go` - Linux binary that runs inside VM to test Rosetta in sandbox

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
# 4. Builds hello-x86_64 test binary (for binfmt_misc testing)
# 5. Builds the initramfs with busybox + tempest binaries
# 6. Installs to /tmp/tempest/libexec/tempest/vm/

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
│       │   ├── tempest-grain-agent
│       │   └── test-sandbox (debugging tool)
│       └── virtio-fs mounts
│           ├── packages → /sandstorm/apps
│           ├── grains → /sandstorm/grains
│           └── rosetta → /tmp/lima-rosetta
```

## Rosetta Integration

Rosetta enables running x86_64 Linux binaries on ARM64. The setup:

1. **Host side**: Virtualization.framework shares Rosetta via virtio-fs
2. **VM init**: Mounts Rosetta at `/tmp/lima-rosetta`, registers binfmt_misc with F flag
3. **Sandbox**: Bind mounts Rosetta from VM's `/tmp/lima-rosetta` into the sandbox

### binfmt_misc Registration

```bash
echo ':rosetta:M::\x7fELF\x02\x01\x01\x00\x00\x00\x00\x00\x00\x00\x00\x00\x02\x00\x3e\x00:\xff\xff\xff\xff\xff\xfe\xfe\x00\xff\xff\xff\xff\xff\xff\xff\xff\xfe\xff\xff\xff:/tmp/lima-rosetta/rosetta:OCF' > /proc/sys/fs/binfmt_misc/register
```

Flags:
- `O` - Open binary
- `C` - Credentials
- `F` - Fix binary (kernel opens interpreter at registration, caches fd)

### The Problem

Rosetta opens `/proc/self/exe` to verify itself via ioctl. The ioctl must return a specific key from macOS. This fails after pivot_root when using a fresh virtiofs mount.

**Solution found:** Bind mounting from the original virtiofs mount (rather than creating a new virtiofs mount) preserves the ioctl context and allows Rosetta to work.

### Sandbox Launcher Rosetta Mount

The sandbox-launcher now tries bind mount first, falling back to virtiofs:

```c
// c/sandbox-launcher.c lines 240-270
// Try bind mount from existing VM mount first
if (mount("/tmp/lima-rosetta", CHROOT_MNT "/tmp/lima-rosetta", "", MS_BIND, "") != 0) {
    // Fall back to direct virtiofs mount
    mount("rosetta", CHROOT_MNT "/tmp/lima-rosetta", "virtiofs", 0, "");
}
```

## Seccomp Filter

The sandbox uses seccomp-bpf to restrict syscalls. Rosetta requires 4 specific ioctls for x86_64 binary translation:

```assembly
// c/filter.s - Rosetta ioctls
jeq #0x80456122, allow  // _IOC(_IOC_READ, 0x61, 0x22, 0x45) - verification
jeq #0x80806123, allow  // _IOC(_IOC_READ, 0x61, 0x23, 0x80) - runtime translation
jeq #0x00006124, allow  // _IOC(_IOC_NONE, 0x61, 0x24, 0) - runtime control
jeq #0x80456125, allow  // _IOC(_IOC_READ, 0x61, 0x25, 0x45) - runtime verification
```

See [Seccomp Filter Fixed](#seccomp-filter-fixed-rosetta-ioctls-identified-2026-01-27) for security analysis.

## Testing / Debugging

### Standalone Sandbox Test (Recommended)

The easiest way to test Rosetta sandbox behavior is with the standalone test binary:

```bash
# Build everything including the test binary
mage vm:docker

# Run the standalone test (boots VM, runs tests, exits)
timeout 60 _build/test-vm-sandbox
```

This binary:
1. Boots a Linux VM using Apple Virtualization.framework
2. Mounts Rosetta via virtio-fs
3. Runs `test-sandbox` inside the VM which simulates sandbox-launcher behavior
4. Reports whether Rosetta works after pivot_root
5. Shuts down the VM and exits

Example output:
```
[1] Checking Rosetta availability...
    OK: Rosetta binary exists at /tmp/lima-rosetta/rosetta

[2] Current Rosetta mount info...
    34 31 0:30 / /tmp/lima-rosetta rw,relatime - virtiofs rosetta rw

[3] Testing Rosetta directly (baseline)...
    OK: Rosetta runs correctly

[4] Simulating sandbox-launcher (full namespace + pivot_root)...
    BIND MOUNT SUCCEEDED!
    *** SUCCESS: Rosetta works after pivot_root! (mount method: bind) ***
```

### Quick Rosetta Check (Automatic)

Every time the VM starts, it runs a quick Rosetta sanity check and outputs the result to the console log:

```bash
# Start tempest
_build/tempest

# In another terminal, check the console log
tail -50 /tmp/tempest-vm-console.log

# Look for the "Quick Rosetta Check" section:
# === Quick Rosetta Check ===
# Rosetta binary: OK
# (rosetta output or error here)
# ===========================
```

### Full Sandbox Test via Kernel Cmdline

To run the full sandbox test when the VM boots (instead of starting the daemon):

Edit `internal/server/vm/manager_darwin.go` line 58:

```go
// Change this:
vz.WithCommandLine("console=ttyAMA0 console=hvc0 rdinit=/init panic=-1 loglevel=7"),

// To this:
vz.WithCommandLine("console=ttyAMA0 console=hvc0 rdinit=/init panic=-1 loglevel=7 test-sandbox"),
```

Then rebuild and run:
```bash
mage vm:docker
_build/tempest
tail -100 /tmp/tempest-vm-console.log
```

The test output will appear in the console log, and the VM will halt after the test.

### Testing Rosetta Script

A shell-based test script is also available in the VM. Add `test-rosetta` to the kernel cmdline to run it:

```bash
# In manager_darwin.go:
vz.WithCommandLine("... test-rosetta"),
```

## What the Standalone Test Does

The `test-sandbox` binary (runs inside VM) simulates exactly what `sandbox-launcher.c` does:

1. Unshares namespaces (NEWNS, NEWIPC, NEWUTS, NEWNET, NEWCGROUP)
2. Makes mount namespace private with `mount --make-rprivate /`
3. Bind mounts a package image to `/sandstorm/mnt`
4. Mounts tmpfs at `/sandstorm/mnt/tmp`
5. Mounts proc at `/sandstorm/mnt/proc`
6. **Bind mounts** `/tmp/lima-rosetta` to `/sandstorm/mnt/tmp/lima-rosetta`
7. Sets up `/dev` with device nodes
8. Performs `pivot_root` to switch to the new root
9. Unmounts the old root
10. Tests if Rosetta still works

## References

- Lima Rosetta implementation: https://github.com/lima-vm/lima/blob/main/pkg/driver/vz/rosetta_directory_share_arm64.go
- Lima binfmt script: https://github.com/lima-vm/lima/blob/main/pkg/driver/vz/boot/05-rosetta-volume.sh
- Rosetta ioctl verification: Opens /proc/self/exe, does ioctl expecting "AppleRosettaKey"

---

## Investigation Summary (2026-01-27)

### What's Working

1. **Bind mount succeeds in sandbox-launcher**: Debug output confirms the bind mount from `/tmp/lima-rosetta` to `/sandstorm/mnt/tmp/lima-rosetta` succeeds:
   ```
   DEBUG: Bind mount of Rosetta succeeded!
   ```

2. **Direct rosetta execution works**: When running rosetta directly via `execve()` after pivot_root, the ioctl check passes and rosetta shows its usage message.

3. **All tests pass**: Both the shell-based test (`test-sandbox`) and C-based test (`test-mount`) succeed with bind mounts.

### What's Failing

When a real grain spawns and tries to execute an x86_64 binary (sandstorm-http-bridge), rosetta fails with:
```
rosetta error: Rosetta is only intended to run on Apple Silicon with a macOS host using Virtualization.framework with Rosetta mode enabled
```

### Things Tried

| Test | Method | Result |
|------|--------|--------|
| Shell test with MS_PRIVATE | `mount --make-rprivate /` | PASS |
| Shell test with MS_SLAVE | `mount --make-rslave /` | PASS |
| Shell test with CLONE_NEWPID | Added to Go SysProcAttr | PASS |
| Shell test with package bind mount | Real virtiofs package directory | PASS |
| Shell test with grain sandbox mount | Added grain sandbox bind mount | PASS |
| C test matching sandbox-launcher | Replicated all mount operations | PASS |
| C test with sandbox-launcher pivot_root | Used same "weird" pivot_root(A,A) | PASS |
| Real grain spawn | Via tempest server | **FAIL** |

### Key Difference Identified

The critical difference is **how rosetta is invoked**:

- **Tests (PASS)**: Rosetta is executed directly via `execve("/tmp/lima-rosetta/rosetta", ...)`
- **Real grain (FAIL)**: Rosetta is invoked via **binfmt_misc with F flag** when executing an x86_64 binary

The binfmt_misc F flag means:
1. Kernel opens rosetta at registration time (in VM init, before any sandbox)
2. The fd is cached and reused for all x86_64 binary translations
3. When an x86_64 binary is executed in a grain sandbox, the kernel uses the cached fd

### Hypothesis

The ioctl verification may fail because:
1. The binfmt_misc fd was opened from the VM's root namespace
2. When rosetta opens `/proc/self/exe` after pivot_root, the path resolution happens in the grain's namespace
3. The ioctl may behave differently when the fd used for execution differs from the fd obtained via `/proc/self/exe`

### Next Steps

1. Test with an actual x86_64 binary in the standalone test to verify binfmt_misc is the issue
2. Consider removing the F flag from binfmt_misc registration (may have performance impact)
3. Investigate if there's a way to re-register binfmt_misc per-sandbox
4. Check if Lima has a workaround for this scenario

---

## New Test Infrastructure (added 2026-01-27)

### x86_64 Test Binary

Added `hello-x86_64` - a minimal x86_64 static binary for testing binfmt_misc invocation of Rosetta:

```bash
# Build with Docker (part of mage vm:docker)
x86_64-linux-gnu-gcc -static -o _build/hello-x86_64 c/hello-x86_64.c
```

The test-sandbox now includes [5] which:
1. Copies hello-x86_64 into the sandbox
2. Attempts to run it after pivot_root
3. This triggers binfmt_misc to invoke Rosetta (not direct execve)
4. Reports whether it succeeds or fails

### Testing Without F Flag

To test if removing the F flag fixes the issue:

```bash
# 1. Rebuild Docker image to include x86_64 cross-compiler
docker rmi tempest-vm-builder

# 2. Rebuild everything (creates new Docker image + initramfs with hello-x86_64)
mage vm:docker

# 3. Run test with F flag (default) - this should show the binfmt_misc issue
timeout 60 _build/test-vm-sandbox

# 4. Run test WITHOUT F flag - if this passes, F flag is the problem
TEST_NO_F_FLAG=1 timeout 60 _build/test-vm-sandbox
```

The test output will show:
- `[4]` - Direct rosetta execution (should PASS)
- `[5]` - x86_64 binary via binfmt_misc (tests the hypothesis)

If `[5]` fails with F flag but passes without, the F flag is confirmed as the issue.

### Technical Background: Rosetta Verification

Rosetta performs an ioctl verification on startup:
1. Opens `/proc/self/exe` (which resolves to the rosetta binary)
2. Performs `ioctl(fd, 0x80456122, buffer)` on that fd
3. Expects virtiofs to return a secret key: "Our hard work\nby these words guarded\n..."
4. If verification fails: "Rosetta is only intended to run on Apple Silicon..."

The issue with binfmt_misc F flag:
- **F flag**: Kernel opens rosetta at registration time, caches fd
- When x86_64 binary runs in sandbox, kernel uses cached fd
- But rosetta still opens `/proc/self/exe` at runtime for verification
- The `/proc/self/exe` path is resolved in the sandbox's mount namespace
- If the bind mount doesn't properly forward the ioctl, verification fails

Reference: https://threedots.ovh/blog/2022/06/quick-look-at-rosetta-on-linux/

### Docker Desktop Fix

Docker Desktop had a similar issue (github.com/docker/for-mac#6973) which was fixed in version 4.26.0. The fix likely involved changes to how virtiofs or binfmt_misc handles the Rosetta verification in nested mount namespaces.

---

## Test Results (2026-01-27)

### binfmt_misc F Flag Hypothesis: DISPROVEN

Ran tests with an actual x86_64 binary (`hello-x86_64`) to test whether the binfmt_misc F flag was causing the Rosetta failure in sandboxes.

**Test WITH F flag:**
```
[4] Simulating sandbox-launcher (full namespace + pivot_root)...
    *** SUCCESS: Rosetta works after pivot_root! (mount method: bind) ***

[5] Testing x86_64 binary via binfmt_misc...
    Found hello-x86_64 in sandbox
    *** SUCCESS: x86_64 binary works via binfmt_misc! ***
```

**Test WITHOUT F flag:**
```
[4] Simulating sandbox-launcher (full namespace + pivot_root)...
    *** SUCCESS: Rosetta works after pivot_root! (mount method: bind) ***

[5] Testing x86_64 binary via binfmt_misc...
    Found hello-x86_64 in sandbox
    *** SUCCESS: x86_64 binary works via binfmt_misc! ***
```

**Conclusion:** Both tests pass. The binfmt_misc F flag is NOT the cause of the issue.

### What This Means

The test simulates exactly what `sandbox-launcher.c` does:
1. Unshare namespaces (NEWNS, NEWPID, NEWIPC, NEWUTS, NEWNET, NEWCGROUP)
2. Mount package read-only
3. Mount grain sandbox to /var
4. Mount tmpfs on /tmp
5. Bind mount Rosetta from `/tmp/lima-rosetta`
6. pivot_root to new root
7. Execute x86_64 binary (triggers binfmt_misc -> Rosetta)

The x86_64 binary successfully runs via binfmt_misc after pivot_root in the test.

### Remaining Mystery

Real grain spawns still fail with the Rosetta verification error. The difference between
the test and real grain spawns needs further investigation:

| Aspect | Test | Real Grain |
|--------|------|------------|
| x86_64 binary | hello-x86_64 (static, minimal) | sandstorm-http-bridge (complex) |
| Package | Empty test package | Real Sandstorm package |
| Invocation | Direct from test script | Via grain-agent |
| Environment | Minimal | Full grain environment |

### Next Investigation Steps

1. **Try with a real package**: Install a real Sandstorm app and test with its x86_64 binary
2. **Check grain-agent differences**: Compare how grain-agent invokes the app vs our test
3. **Examine sandstorm-http-bridge**: May have specific requirements that fail
4. **Add more debug output**: Capture the exact error when real grains fail

---

## Root Cause Found: Seccomp Filter (2026-01-27)

### Discovery

After extensive testing, the root cause of Rosetta failures in real grain spawns was identified: **the seccomp filter**.

**Test Results:**

| Seccomp State | Rosetta Direct | hello-x86_64 via binfmt_misc | Real App |
|---------------|----------------|------------------------------|----------|
| Disabled | ✓ Works | ✓ Works | ✓ Works |
| Enabled | ✗ Fails | ✗ Fails | ✗ Fails |

### Key Difference Between Test and Real Grain

The test-sandbox binary simulates sandbox-launcher behavior but **does NOT install the seccomp filter**. Real grain spawns via sandbox-launcher.c **do install the seccomp filter** (line 365).

### Debug Output Comparison

**With seccomp enabled (fails):**
```
time=... level=INFO msg="Testing Rosetta directly..."
time=... level=INFO msg="Rosetta direct test result" output="rosetta error: Rosetta is only intended to run on Apple Silicon with a macOS host using Virtualization.framework with Rosetta mode enabled\n"
time=... level=ERROR msg="hello-x86_64 test FAILED" output="rosetta error: Rosetta is only intended..."
```

**With seccomp disabled (works):**
```
time=... level=INFO msg="Testing Rosetta directly..."
time=... level=INFO msg="Rosetta direct test result" output="Usage: rosetta <x86_64 ELF to run>\n\nOptional environment variables:\n..."
time=... level=INFO msg="hello-x86_64 test SUCCEEDED" output="Hello from x86_64!\n"
```

### The Puzzle

The seccomp filter in `c/filter.s` already includes the Rosetta verification ioctl:

```assembly
sys_ioctl:
    ...
    // Rosetta x86_64 translation verification ioctl
    // _IOC(_IOC_READ, 0x61, 0x22, 0x45) = 0x80456122
    jeq #0x80456122, allow
```

Yet Rosetta still fails. Possible reasons:

1. **Different ioctl number**: Rosetta may use a different ioctl than 0x80456122
2. **Multiple ioctls**: Rosetta may need multiple ioctls, and we're only allowing one
3. **Other blocked syscalls**: Rosetta may need syscalls that are being blocked

### Next Steps to Fix

1. **Allow all ioctls temporarily** to confirm it's an ioctl issue vs other syscalls
2. **Use strace or seccomp logging** to capture exact syscalls Rosetta makes
3. **Find the correct ioctl(s)** and add them to the filter

### Temporary Workarounds

To disable seccomp for testing (NOT for production):

```c
// In c/sandbox-launcher.c, comment out line 365:
// REQUIRE(syscall(SYS_seccomp, SECCOMP_SET_MODE_FILTER, 0, &seccomp_fprog) == 0);
```

Or to allow all ioctls in the filter:

```assembly
// In c/filter.s, at the start of sys_ioctl:
sys_ioctl:
    jmp allow  // TEMPORARY: allow all ioctls
```

### Files Modified for Debugging

- `c/sandbox-launcher.c` - Added debug output, hello-x86_64 copy to sandbox
- `c/filter.s` - Rosetta ioctl already present (needs investigation)
- `internal/server/grain-agent/main/main.go` - Added Rosetta and hello-x86_64 tests before app launch
- `cmd/test-sandbox/main.go` - Added Go exec.Command test (test [6])

---

## Seccomp Filter Fixed: Rosetta Ioctls Identified (2026-01-27)

### Solution

Using strace inside the VM sandbox, we identified all ioctls that Rosetta needs for x86_64 binary translation. The seccomp filter now allows these specific ioctls:

| Ioctl | Hex Value | Description |
|-------|-----------|-------------|
| `_IOC(_IOC_READ, 0x61, 0x22, 0x45)` | 0x80456122 | Verification ioctl |
| `_IOC(_IOC_READ, 0x61, 0x23, 0x80)` | 0x80806123 | Runtime translation |
| `_IOC(_IOC_NONE, 0x61, 0x24, 0)` | 0x00006124 | Runtime control |
| `_IOC(_IOC_READ, 0x61, 0x25, 0x45)` | 0x80456125 | Runtime verification |

### How We Found Them

1. Added static strace to the VM initramfs (built from source with `--enable-mpers=no LDFLAGS="-static"`)
2. Modified `c/test-mount.c` to run strace on `hello-x86_64` before installing seccomp
3. Captured all ioctl calls during actual x86_64 binary execution via binfmt_misc

```bash
# Strace output showing all Rosetta ioctls:
ioctl(4, _IOC(_IOC_READ, 0x61, 0x25, 0x45), ...) = 1
ioctl(4, _IOC(_IOC_READ, 0x61, 0x23, 0x80), ...) = 1
ioctl(4, _IOC(_IOC_NONE, 0x61, 0x24, 0), ...) = 1
```

### Security Analysis

**Why type 0x61 ('a')?**

In the Linux kernel, ioctl type 0x61 is registered for:
- **ATM networking** - Asynchronous Transfer Mode (obsolete 1990s telecom technology)
- **Intel QAT** - QuickAssist Technology (hardware crypto accelerator)

**Why allowing these ioctls is safe:**

1. **No ATM/QAT hardware exists in Apple VMs**: Neither ATM interfaces nor Intel QAT accelerators are present in Apple Virtualization.framework VMs running on Apple Silicon.

2. **Ioctls communicate with hypervisor, not kernel**: These ioctls operate on file descriptors opened by Rosetta to `/tmp/lima-rosetta/rosetta`. They communicate with Apple's Virtualization.framework hypervisor, not with any Linux kernel subsystem.

3. **Rosetta is already trusted**: We mount Rosetta into the sandbox and allow it to translate x86_64 binaries. The ioctls are just part of Rosetta's normal operation.

4. **Specific ioctls only**: We allow only 4 specific ioctls, not the entire type 0x61 range. This minimizes any theoretical risk.

5. **Sandbox has no access to real hardware**: Even if ATM/QAT drivers were loaded (they're not), the sandbox has no access to device nodes that would allow communicating with them.

**Trust model:**
- The hypervisor (macOS/Virtualization.framework) is outside the VM and trusted
- Rosetta is Apple-signed code running via the hypervisor
- Blocking these ioctls would only break Rosetta, not add security

### Implementation

The seccomp filter in `c/filter.s`:

```assembly
sys_ioctl:
    // The request argument is 32-bit, so high should be zero.
    ld [OFF_ARG_1_HI]
    jne #0, einval

    ld [OFF_ARG_1_LO]

    // Rosetta x86_64 binary translation ioctls (Apple Virtualization.framework)
    //
    // These ioctls were identified via strace on actual x86_64 binary execution.
    // All use type 0x61 ('a'), which is registered for ATM/QAT in Linux but
    // neither exists in Apple VMs - these are Rosetta-specific ioctls.
    //
    // _IOC(_IOC_READ, 0x61, 0x22, 0x45) - verification ioctl
    jeq #0x80456122, allow
    // _IOC(_IOC_READ, 0x61, 0x23, 0x80) - runtime translation
    jeq #0x80806123, allow
    // _IOC(_IOC_NONE, 0x61, 0x24, 0) - runtime control
    jeq #0x00006124, allow
    // _IOC(_IOC_READ, 0x61, 0x25, 0x45) - runtime verification
    jeq #0x80456125, allow

    // ... rest of ioctl handling
```

### Test Results

With the specific ioctls allowed, all tests pass:

```
[12a] Running strace on hello-x86_64 to capture ALL Rosetta ioctls...
    strace on hello-x86_64 completed (exit=0)
[14] Testing hello-x86_64 via binfmt_misc...
    Found hello-x86_64 at /tmp/hello-x86_64
    *** SUCCESS: hello-x86_64 works via binfmt_misc! ***
```

Real grain spawns also work:
```
time=... level=INFO msg="hello-x86_64 test SUCCEEDED" output="Hello from x86_64!\n"
time=... level=INFO msg="App binary is x86_64, will use Rosetta via binfmt_misc"
```

### Future Considerations

If Rosetta is updated and starts using additional ioctls, grains will fail with:
```
rosetta error: Unexpected ioctl error when communicating with hypervisor: 25
```

Error 25 is ENOTTY, which our seccomp filter returns for unrecognized ioctls. To fix:
1. Run `TEST_MODE=test-mount _build/test-vm-sandbox` to capture new ioctls via strace
2. Add the new ioctl hex values to `c/filter.s`
3. Rebuild with `mage vm:docker`
