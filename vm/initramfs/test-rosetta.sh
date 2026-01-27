#!/bin/sh
# Minimal Rosetta test script
# Run this directly in the VM to test Rosetta without full grain setup
#
# Usage: /bin/test-rosetta.sh [--no-pivot] [--no-unshare]

set -e

NO_PIVOT=0
NO_UNSHARE=0

for arg in "$@"; do
    case $arg in
        --no-pivot) NO_PIVOT=1 ;;
        --no-unshare) NO_UNSHARE=1 ;;
    esac
done

echo "=== Rosetta Test Script ==="
echo ""

# Check Rosetta is available
if [ ! -f /tmp/lima-rosetta/rosetta ]; then
    echo "ERROR: Rosetta not found at /tmp/lima-rosetta/rosetta"
    exit 1
fi
echo "OK: Rosetta binary exists"

# Check binfmt_misc
if [ -f /proc/sys/fs/binfmt_misc/rosetta ]; then
    echo "OK: binfmt_misc rosetta handler registered"
    cat /proc/sys/fs/binfmt_misc/rosetta
else
    echo "ERROR: binfmt_misc rosetta handler not registered"
    exit 1
fi

echo ""
echo "=== Testing Rosetta directly ==="
# Try running rosetta with --help or similar
/tmp/lima-rosetta/rosetta --help 2>&1 || echo "(rosetta returned non-zero, that's expected)"

echo ""
echo "=== Testing in namespace ==="

# Create a test that mimics what sandbox-launcher does
cat > /tmp/namespace-test.sh << 'EOF'
#!/bin/sh
echo "Inside namespace test..."
echo "Checking /tmp/lima-rosetta..."
ls -la /tmp/lima-rosetta/ 2>&1 || echo "Cannot list /tmp/lima-rosetta"

echo ""
echo "Checking mount info for rosetta..."
grep -i rosetta /proc/self/mountinfo || echo "No rosetta in mountinfo"

echo ""
echo "Trying to run rosetta directly..."
/tmp/lima-rosetta/rosetta --help 2>&1 || true

echo ""
echo "Done with namespace test"
EOF
chmod +x /tmp/namespace-test.sh

if [ "$NO_UNSHARE" = "1" ]; then
    echo "Skipping unshare (--no-unshare)"
    /tmp/namespace-test.sh
else
    echo "Running with unshare -m (mount namespace)..."
    unshare -m /tmp/namespace-test.sh
fi

echo ""
echo "=== Testing with pivot_root simulation ==="

if [ "$NO_PIVOT" = "1" ]; then
    echo "Skipping pivot_root test (--no-pivot)"
    exit 0
fi

# Create a minimal root for pivot_root test
mkdir -p /tmp/testroot
mount -t tmpfs tmpfs /tmp/testroot
mkdir -p /tmp/testroot/{bin,tmp,proc,dev}
cp /bin/busybox /tmp/testroot/bin/
ln -sf busybox /tmp/testroot/bin/sh
ln -sf busybox /tmp/testroot/bin/ls
ln -sf busybox /tmp/testroot/bin/cat
ln -sf busybox /tmp/testroot/bin/grep
ln -sf busybox /tmp/testroot/bin/mount

# Mount proc in new root
mount -t proc proc /tmp/testroot/proc

# Mount tmpfs for /tmp and create rosetta dir
mount -t tmpfs tmpfs /tmp/testroot/tmp
mkdir -p /tmp/testroot/tmp/lima-rosetta

# Try mounting rosetta virtiofs directly
echo "Mounting rosetta virtiofs in test root..."
if mount -t virtiofs rosetta /tmp/testroot/tmp/lima-rosetta; then
    echo "OK: virtiofs mount succeeded"
    ls -la /tmp/testroot/tmp/lima-rosetta/
else
    echo "WARN: virtiofs mount failed, trying bind mount..."
    mount --bind /tmp/lima-rosetta /tmp/testroot/tmp/lima-rosetta
fi

# Create test script inside the new root
cat > /tmp/testroot/test.sh << 'INNEREOF'
#!/bin/sh
echo "=== Inside pivot_root ==="
echo "PID: $$"
echo "Mount info:"
grep -i rosetta /proc/self/mountinfo || echo "No rosetta mount"
echo ""
echo "Rosetta binary:"
ls -la /tmp/lima-rosetta/ 2>&1
echo ""
echo "Trying rosetta:"
/tmp/lima-rosetta/rosetta --help 2>&1 || true
echo "=== End pivot_root test ==="
INNEREOF
chmod +x /tmp/testroot/test.sh

echo ""
echo "Entering new namespace with pivot_root..."

# Use unshare + pivot_root
unshare -m sh -c '
    # Make mounts private (this is what sandbox-launcher does)
    mount --make-rprivate /

    # Pivot root
    cd /tmp/testroot
    mkdir -p oldroot
    pivot_root . oldroot
    cd /

    # Unmount old root
    umount -l /oldroot
    rmdir /oldroot

    # Run test
    /bin/sh /test.sh
'

echo ""
echo "=== Test complete ==="
