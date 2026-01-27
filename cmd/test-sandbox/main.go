// test-sandbox tests Rosetta x86_64 translation matching sandbox-launcher behavior.
// This runs inside the VM to debug issues with Rosetta after pivot_root.
package main

import (
	"fmt"
	"os"
	"os/exec"
	"syscall"
)

const (
	// These match sandbox-launcher.c config
	SANDSTORM_STATE = "/sandstorm"
	IMAGE_DIR       = SANDSTORM_STATE + "/apps"
	SANDBOX_DIR     = SANDSTORM_STATE + "/grains"
	CHROOT_MNT      = SANDSTORM_STATE + "/mnt"
)

func main() {
	// Check if we're running the inner Go exec.Command test
	if len(os.Args) > 1 && os.Args[1] == "--go-exec-test" {
		runGoExecTest()
		return
	}

	fmt.Println("=== Sandbox Rosetta Test (matching sandbox-launcher) ===")
	fmt.Println()

	if os.Getuid() != 0 {
		fmt.Println("ERROR: Must run as root")
		os.Exit(1)
	}

	// Test 1: Check Rosetta availability in VM
	fmt.Println("[1] Checking Rosetta availability...")
	rosettaPath := "/tmp/lima-rosetta/rosetta"
	if _, err := os.Stat(rosettaPath); err != nil {
		fmt.Printf("    ERROR: Rosetta not found at %s: %v\n", rosettaPath, err)
		os.Exit(1)
	}
	fmt.Printf("    OK: Rosetta binary exists at %s\n", rosettaPath)

	// Show mount info
	fmt.Println()
	fmt.Println("[2] Current Rosetta mount info...")
	showMountInfo("rosetta")

	// Test 2: Baseline - Rosetta works in VM
	fmt.Println()
	fmt.Println("[3] Testing Rosetta directly (baseline)...")
	if testRosetta("/tmp/lima-rosetta/rosetta") {
		fmt.Println("    OK: Rosetta runs correctly")
	} else {
		fmt.Println("    FAIL: Rosetta failed")
	}

	// Test 3: Simulate sandbox-launcher behavior exactly (shell-based)
	fmt.Println()
	fmt.Println("[4] Simulating sandbox-launcher (full namespace + pivot_root)...")
	testSandboxLauncherStyle()

	// Test 4: Test Go exec.Command in sandbox (matches grain-agent behavior)
	fmt.Println()
	fmt.Println("[6] Testing Go exec.Command in sandbox (grain-agent style)...")
	testGoExecCommandStyle()

	fmt.Println()
	fmt.Println("=== Test Complete ===")
}

// testGoExecCommandStyle tests running x86_64 binary via Go's exec.Command
// This matches how grain-agent runs apps
func testGoExecCommandStyle() {
	// This test runs in a child process with namespaces
	// The child will do the sandbox setup and then use exec.Command

	cmd := exec.Command("/bin/sh", "-c", `
set -e
mount --make-rslave /

# Setup minimal sandbox
mkdir -p /sandstorm/mnt
mount -t tmpfs tmpfs /sandstorm/mnt
mkdir -p /sandstorm/mnt/bin /sandstorm/mnt/tmp /sandstorm/mnt/proc /sandstorm/mnt/dev

# Copy busybox and create symlinks
cp /bin/busybox /sandstorm/mnt/bin/
cd /sandstorm/mnt/bin
for cmd in sh cat ls mount grep head echo; do
    ln -sf busybox "$cmd"
done
cd /

# Mount tmpfs on /tmp
mount -t tmpfs -o size=16m tmpfs /sandstorm/mnt/tmp

# Mount proc
mount -t proc proc /sandstorm/mnt/proc

# Bind mount Rosetta
mkdir -p /sandstorm/mnt/tmp/lima-rosetta
mount --bind /tmp/lima-rosetta /sandstorm/mnt/tmp/lima-rosetta

# Copy test binary into sandbox
if [ -f /bin/hello-x86_64 ]; then
    cp /bin/hello-x86_64 /sandstorm/mnt/tmp/hello-x86_64
fi

# Copy our test binary into sandbox (for the Go exec.Command test)
cp /bin/test-sandbox /sandstorm/mnt/bin/test-sandbox-inner

# Setup /dev
mkdir -p /sandstorm/mnt/dev
mount -t tmpfs tmpfs /sandstorm/mnt/dev
mknod /sandstorm/mnt/dev/null c 1 3 2>/dev/null || true
mknod /sandstorm/mnt/dev/zero c 1 5 2>/dev/null || true
mknod /sandstorm/mnt/dev/urandom c 1 9 2>/dev/null || true

# pivot_root
cd /sandstorm/mnt
mkdir -p /sandstorm/mnt/tmp/oldroot
pivot_root . tmp/oldroot
umount -l /tmp/oldroot 2>/dev/null || true

# Now run the Go exec.Command test from inside the sandbox
exec /bin/test-sandbox-inner --go-exec-test
`)
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	cmd.SysProcAttr = &syscall.SysProcAttr{
		Cloneflags: syscall.CLONE_NEWNS |
			syscall.CLONE_NEWIPC |
			syscall.CLONE_NEWUTS |
			syscall.CLONE_NEWNET |
			syscall.CLONE_NEWCGROUP |
			syscall.CLONE_NEWPID,
	}

	if err := cmd.Run(); err != nil {
		fmt.Printf("    Error: %v\n", err)
	}
}

// runGoExecTest is called when --go-exec-test is passed
// This runs inside the sandbox after pivot_root
func runGoExecTest() {
	fmt.Println("    [Go exec.Command test - running inside sandbox after pivot_root]")

	// Check environment
	fmt.Println("    Checking environment...")
	if link, err := os.Readlink("/proc/self/exe"); err == nil {
		fmt.Printf("    /proc/self/exe -> %s\n", link)
	}

	// Check Rosetta mount
	if _, err := os.Stat("/tmp/lima-rosetta/rosetta"); err == nil {
		fmt.Println("    Rosetta available at /tmp/lima-rosetta/rosetta")
	} else {
		fmt.Printf("    ERROR: Rosetta not found: %v\n", err)
		return
	}

	// Test 1: Run Rosetta directly via exec.Command
	fmt.Println("    Testing Rosetta via exec.Command...")
	rosettaCmd := exec.Command("/tmp/lima-rosetta/rosetta")
	rosettaOutput, rosettaErr := rosettaCmd.CombinedOutput()
	outputStr := string(rosettaOutput)
	if contains(outputStr, "Usage:") {
		fmt.Println("    *** SUCCESS: Rosetta works via exec.Command! ***")
	} else if contains(outputStr, "Rosetta is only intended") {
		fmt.Println("    *** FAIL: Rosetta verification failed via exec.Command ***")
		fmt.Printf("    Output: %s\n", outputStr)
	} else {
		fmt.Printf("    Rosetta output: %s, err: %v\n", outputStr, rosettaErr)
	}

	// Test 2: Run hello-x86_64 via exec.Command (binfmt_misc path)
	if _, err := os.Stat("/tmp/hello-x86_64"); err == nil {
		fmt.Println("    Testing hello-x86_64 via exec.Command (binfmt_misc)...")
		helloCmd := exec.Command("/tmp/hello-x86_64")
		helloOutput, helloErr := helloCmd.CombinedOutput()
		helloStr := string(helloOutput)
		if contains(helloStr, "Hello from x86_64") {
			fmt.Println("    *** SUCCESS: hello-x86_64 works via exec.Command! ***")
		} else if contains(helloStr, "Rosetta is only intended") {
			fmt.Println("    *** FAIL: Rosetta verification failed for hello-x86_64 via exec.Command ***")
			fmt.Printf("    Output: %s\n", helloStr)
		} else {
			fmt.Printf("    hello-x86_64 output: %s, err: %v\n", helloStr, helloErr)
		}
	} else {
		fmt.Println("    hello-x86_64 not found in sandbox")
	}
}

func showMountInfo(pattern string) {
	data, err := os.ReadFile("/proc/self/mountinfo")
	if err != nil {
		fmt.Printf("    ERROR reading mountinfo: %v\n", err)
		return
	}
	for _, line := range splitLines(string(data)) {
		if contains(line, pattern) {
			fmt.Printf("    %s\n", line)
		}
	}
}

func testRosetta(path string) bool {
	cmd := exec.Command(path)
	output, _ := cmd.CombinedOutput()
	return contains(string(output), "Usage:")
}

func testSandboxLauncherStyle() {
	// This simulates exactly what sandbox-launcher.c does
	// Run in a child process with full namespace isolation

	script := `
set -e

echo "    Unsharing namespaces (NEWNS|NEWPID|NEWIPC|NEWUTS|NEWNET|NEWCGROUP)..."
# Use MS_SLAVE like sandbox-launcher.c line 193 (not MS_PRIVATE!)
# MS_SLAVE allows virtiofs ioctls to work while preventing our mounts from propagating back
mount --make-rslave /

echo "    Setting up loopback (simulating network namespace)..."
# Skip actual network setup in this test

echo "    Current working directory: $(pwd)"
echo "    CHROOT_MNT=/sandstorm/mnt"

# Check if we have a test package
PACKAGE_ID=""
for dir in /sandstorm/apps/*/; do
    if [ -d "$dir" ]; then
        # basename not available, extract manually
        PACKAGE_ID="${dir%/}"
        PACKAGE_ID="${PACKAGE_ID##*/}"
        break
    fi
done

if [ -z "$PACKAGE_ID" ]; then
    echo "    WARNING: No package found, using minimal root"
    # Create minimal chroot
    mkdir -p /sandstorm/mnt
    mount -t tmpfs tmpfs /sandstorm/mnt
    mkdir -p /sandstorm/mnt/bin /sandstorm/mnt/tmp /sandstorm/mnt/proc /sandstorm/mnt/dev
    cp /bin/busybox /sandstorm/mnt/bin/
    for cmd in sh cat ls mount grep head echo; do
        ln -sf busybox /sandstorm/mnt/bin/$cmd
    done
else
    echo "    Using package: $PACKAGE_ID"
    # Bind mount the package image (simulating sandbox-launcher line 220)
    mkdir -p /sandstorm/mnt
    mount --bind /sandstorm/apps/$PACKAGE_ID /sandstorm/mnt
    mount -o remount,ro,bind /sandstorm/mnt
fi

# Simulate sandbox-launcher lines 223-225: chdir to grain dir and mount sandbox
# This might affect subsequent mount operations
if [ -d /sandstorm/grains ]; then
    GRAIN_ID=""
    for dir in /sandstorm/grains/*/; do
        if [ -d "$dir" ]; then
            GRAIN_ID="${dir%/}"
            GRAIN_ID="${GRAIN_ID##*/}"
            break
        fi
    done
    if [ -n "$GRAIN_ID" ]; then
        echo "    Changing to grain directory: /sandstorm/grains/$GRAIN_ID"
        cd /sandstorm/grains/$GRAIN_ID
        if [ -d sandbox ]; then
            echo "    Mounting sandbox to /sandstorm/mnt/var"
            mkdir -p /sandstorm/mnt/var
            mount --bind sandbox /sandstorm/mnt/var
        fi
        echo "    Current working directory: $(pwd)"
    fi
fi

# Mount /tmp as tmpfs (line 234)
mkdir -p /sandstorm/mnt/tmp
mount -t tmpfs -o size=16m,nodev,nosuid tmpfs /sandstorm/mnt/tmp

# Mount /proc (line 231)
mkdir -p /sandstorm/mnt/proc
mount -t proc proc /sandstorm/mnt/proc

echo ""
echo "    Checking /tmp/lima-rosetta before Rosetta mount..."
if [ -d /tmp/lima-rosetta ]; then
    echo "    /tmp/lima-rosetta exists in VM root"
    ls -la /tmp/lima-rosetta/
else
    echo "    ERROR: /tmp/lima-rosetta does NOT exist!"
fi

# Mount Rosetta - try bind mount first (lines 240-272)
echo ""
echo "    Creating /sandstorm/mnt/tmp/lima-rosetta..."
mkdir -p /sandstorm/mnt/tmp/lima-rosetta

echo "    Attempting bind mount: /tmp/lima-rosetta -> /sandstorm/mnt/tmp/lima-rosetta"
if mount --bind /tmp/lima-rosetta /sandstorm/mnt/tmp/lima-rosetta 2>&1; then
    echo "    BIND MOUNT SUCCEEDED!"
    MOUNT_METHOD="bind"
else
    echo "    Bind mount failed, trying virtiofs..."
    if mount -t virtiofs rosetta /sandstorm/mnt/tmp/lima-rosetta 2>&1; then
        echo "    virtiofs mount succeeded"
        MOUNT_METHOD="virtiofs"
    else
        echo "    ERROR: Both bind mount and virtiofs failed!"
        MOUNT_METHOD="none"
    fi
fi

echo ""
echo "    Rosetta in chroot:"
ls -la /sandstorm/mnt/tmp/lima-rosetta/ 2>/dev/null || echo "    (directory empty or not mounted)"

# Copy hello-x86_64 into the sandbox for binfmt_misc testing
# Note: binary is already executable, no chmod needed
if [ -f /bin/hello-x86_64 ]; then
    cp /bin/hello-x86_64 /sandstorm/mnt/tmp/hello-x86_64
    echo "    Copied hello-x86_64 to /sandstorm/mnt/tmp/hello-x86_64"
fi

# Set up /dev (lines 274-278)
mkdir -p /sandstorm/mnt/dev
mount -t tmpfs -o nosuid tmpfs /sandstorm/mnt/dev
mknod /sandstorm/mnt/dev/null c 1 3 2>/dev/null || true
mknod /sandstorm/mnt/dev/zero c 1 5 2>/dev/null || true
mknod /sandstorm/mnt/dev/urandom c 1 9 2>/dev/null || true
mount -o remount,ro,nosuid /sandstorm/mnt/dev

echo ""
echo "    Mount info before pivot_root:"
cat /proc/self/mountinfo | grep -E "(lima-rosetta|sandstorm)" | head -5

# pivot_root (lines 302-310)
echo ""
echo "    Performing pivot_root..."
cd /sandstorm/mnt
# Create oldroot in /tmp (writable) since package root is read-only
mkdir -p /sandstorm/mnt/tmp/oldroot
pivot_root . tmp/oldroot

echo "    After pivot_root, now in new root"

# Unmount old root (line 306)
umount -l /tmp/oldroot 2>/dev/null || true

# Check if rosetta exists and test it
# Use test builtin instead of [ ] for portability
if test -f /tmp/lima-rosetta/rosetta; then
    echo "    Rosetta binary found at /tmp/lima-rosetta/rosetta"
    # Test Rosetta - capture output to check for success
    ROSETTA_OUTPUT=$(/tmp/lima-rosetta/rosetta 2>&1 || true)
    case "$ROSETTA_OUTPUT" in
        *Usage:*)
            echo "    *** SUCCESS: Rosetta works after pivot_root! (mount method: $MOUNT_METHOD) ***"
            ;;
        *"Rosetta is only intended"*)
            echo "    *** FAIL: Rosetta verification failed - ioctl check failed ***"
            echo "    Error: $ROSETTA_OUTPUT"
            ;;
        *)
            echo "    *** FAIL: Rosetta failed with unexpected output ***"
            echo "    Output: $ROSETTA_OUTPUT"
            ;;
    esac
else
    echo "    ERROR: /tmp/lima-rosetta/rosetta not found after pivot_root!"
fi

# Test 5: binfmt_misc test with x86_64 binary
# This tests whether rosetta works when invoked via binfmt_misc (not direct execve)
echo ""
echo "[5] Testing x86_64 binary via binfmt_misc..."

# Check if hello-x86_64 is in the sandbox
if test -f /tmp/hello-x86_64; then
    echo "    Found hello-x86_64 in sandbox"
    # Try to run it - this will trigger binfmt_misc -> rosetta
    HELLO_OUTPUT=$(/tmp/hello-x86_64 2>&1 || true)
    case "$HELLO_OUTPUT" in
        *"Hello from x86_64"*)
            echo "    *** SUCCESS: x86_64 binary works via binfmt_misc! ***"
            ;;
        *"Rosetta is only intended"*)
            echo "    *** FAIL: binfmt_misc rosetta verification failed ***"
            echo "    Error: $HELLO_OUTPUT"
            echo "    This confirms the hypothesis: binfmt_misc F flag causes the issue"
            ;;
        *"exec format error"*)
            echo "    *** FAIL: binfmt_misc not working (exec format error) ***"
            echo "    binfmt_misc may not be registered for x86_64"
            ;;
        *)
            echo "    *** FAIL: x86_64 binary failed with unexpected output ***"
            echo "    Output: $HELLO_OUTPUT"
            ;;
    esac
else
    echo "    WARNING: hello-x86_64 not found in /tmp"
    echo "    Trying /tmp/oldroot/bin/hello-x86_64..."
    if test -f /tmp/oldroot/bin/hello-x86_64; then
        # Copy to /tmp before oldroot is unmounted
        echo "    Skipping - oldroot already unmounted"
    else
        echo "    hello-x86_64 binary not available for binfmt_misc test"
        echo "    Build with: x86_64-linux-gnu-gcc -static -o hello-x86_64 c/hello-x86_64.c"
    fi
fi
`

	cmd := exec.Command("/bin/sh", "-c", script)
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	// Match sandbox-launcher.c exactly (lines 178-187)
	cmd.SysProcAttr = &syscall.SysProcAttr{
		Cloneflags: syscall.CLONE_NEWNS |
			syscall.CLONE_NEWIPC |
			syscall.CLONE_NEWUTS |
			syscall.CLONE_NEWNET |
			syscall.CLONE_NEWCGROUP |
			syscall.CLONE_NEWPID, // Added to match sandbox-launcher
	}

	if err := cmd.Run(); err != nil {
		fmt.Printf("    Error: %v\n", err)
	}
}

func splitLines(s string) []string {
	var lines []string
	start := 0
	for i := 0; i < len(s); i++ {
		if s[i] == '\n' {
			lines = append(lines, s[start:i])
			start = i + 1
		}
	}
	if start < len(s) {
		lines = append(lines, s[start:])
	}
	return lines
}

func contains(s, substr string) bool {
	for i := 0; i <= len(s)-len(substr); i++ {
		if s[i:i+len(substr)] == substr {
			return true
		}
	}
	return false
}
