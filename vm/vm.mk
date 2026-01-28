# VM build targets for Tempest
# Include this from the main Makefile with: include vm/vm.mk

VM_RELEASE_VERSION := v0.0.3
VM_RELEASE_BASE_URL := https://github.com/mnutt/tempest/releases/download
VM_OUTPUT_DIR := $(BUILD_DIR)/vm/output

# VM is only supported on ARM64 (Apple Silicon Macs)
VM_ARCH := arm64

# Internal: Download kernel from Kata Containers if not present
$(VM_OUTPUT_DIR)/kernel:
	@mkdir -p $(VM_OUTPUT_DIR)
	@echo "Downloading kernel from Kata Containers..."
	./vm/build.sh download-kernel

# vm-docker: Build VM image using Docker (works on macOS)
# Builds sandbox-launcher and initramfs, downloads kernel from Kata if needed
.PHONY: vm-docker
vm-docker: build $(VM_OUTPUT_DIR)/kernel
	@echo "Building VM image in Docker..."
	@command -v docker >/dev/null 2>&1 || { echo "Error: docker not found in PATH"; exit 1; }
	@if ! docker image inspect tempest-vm-builder >/dev/null 2>&1; then \
		echo "Building Docker image (this is cached for future runs)..."; \
		docker build --platform linux/$(VM_ARCH) -t tempest-vm-builder -f vm/Dockerfile.build .; \
	else \
		echo "Using cached Docker image..."; \
	fi
	docker run --rm \
		-v "$$(pwd):/src" \
		-w /src \
		--platform linux/$(VM_ARCH) \
		tempest-vm-builder \
		./vm/docker-build.sh
	@echo ""
	@echo "VM image built successfully!"
	@echo "Run 'make vm-install' to install to libexec directory."

# vm-download: Download pre-built VM images from GitHub releases
# Downloads initramfs from release, kernel from Kata
.PHONY: vm-download
vm-download: $(VM_OUTPUT_DIR)/kernel
	@mkdir -p $(VM_OUTPUT_DIR)
	@if [ -f "$(VM_OUTPUT_DIR)/initrd" ]; then \
		echo "Initrd already exists. Run 'make vm-clean' first to re-download."; \
	else \
		echo "Downloading initrd from $(VM_RELEASE_BASE_URL)/$(VM_RELEASE_VERSION)/tempest-vm-$(VM_ARCH)-initrd.gz..."; \
		curl -fSL "$(VM_RELEASE_BASE_URL)/$(VM_RELEASE_VERSION)/tempest-vm-$(VM_ARCH)-initrd.gz" \
			-o "$(VM_OUTPUT_DIR)/initrd" || \
			{ echo "Error: Failed to download initrd. You may need to build with 'make vm-docker' instead."; exit 1; }; \
		echo ""; \
		echo "VM images downloaded successfully!"; \
	fi
	@echo "Run 'make vm-install' to install to libexec directory."

# vm-install: Install VM images to libexec directory
.PHONY: vm-install
vm-install:
	@echo "Installing VM images..."
	./vm/build.sh install

# vm-clean: Remove build artifacts and installed images
.PHONY: vm-clean
vm-clean:
	@echo "Cleaning VM build artifacts..."
	./vm/build.sh clean
