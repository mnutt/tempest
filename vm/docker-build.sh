#!/bin/bash
# Build script for VM components inside Docker container
# This is called by mage vm:docker
set -e

echo "Building C sandbox-launcher..."
mkdir -p _build

# Clean any existing C build artifacts (may be wrong architecture)
rm -f _build/gen-clean-h _build/constants.h _build/filter_preproc.s _build/bpf_filter.h
rm -f _build/tempest-sandbox-launcher c/sandbox-launcher.o

# Generate constants.h
cc -o _build/gen-clean-h c/gen-clean-h.c
_build/gen-clean-h > _build/constants.h

# Preprocess and assemble BPF filter
# Enable TEMPEST_ROSETTA_COMPAT for VM builds to allow Rosetta x86_64 translation ioctls
cpp -I _build -DTEMPEST_ROSETTA_COMPAT c/filter.s -o _build/filter_preproc.s

# Use pre-built bpf_asm from the Docker image
/opt/bpf_build/bpf_asm -c < _build/filter_preproc.s > _build/bpf_filter.h

# Compile sandbox-launcher with VM-specific paths
# Inside the VM:
#   - binaries are in /bin (LIBEXECDIR)
#   - apps/grains are in /sandstorm (LOCALSTATEDIR="")
# TEMPEST_ROSETTA_COMPAT enables Rosetta support (MS_SLAVE mounts, full procfs, etc.)
cc -I _build -std=c11 -Wall -Wextra -static -DLIBEXECDIR='"/bin"' -DLOCALSTATEDIR='""' \
    -DTEMPEST_ROSETTA_COMPAT -c -o c/sandbox-launcher.o c/sandbox-launcher.c
cc -static -o _build/tempest-sandbox-launcher c/sandbox-launcher.o

echo "Built _build/tempest-sandbox-launcher"

# Build initramfs
./vm/build.sh initramfs
