##
## Tempest Build System - Makefile Wrapper
##
## This Makefile provides backwards compatibility with the old build system.
## The actual build logic has been migrated to Mage (see magefiles/).
##
## To use mage directly:
##   go run magefiles/*.go <target>
##   # or install mage: go install github.com/magefile/mage@latest
##   mage <target>
##

# Check if mage is installed
MAGE := $(shell command -v mage 2> /dev/null)

# Use mage if available, otherwise use go run
ifdef MAGE
MAGE_CMD := mage
else
MAGE_CMD := go run magefiles/*.go
endif

##
## Targets
##

.PHONY: help
help:
	@echo "Usage: make <target>"
	@echo
	@echo "Primary targets (via Mage):"
	@echo "    build        Build the project"
	@echo "    check        Run project tests"
	@echo "    clean        Remove build artifacts"
	@echo "    nuke         Remove build artifacts and configuration"
	@echo "    toolchain    Download and set up the toolchain"
	@echo "    install      Install binaries"
	@echo "    dev          Install and run tempest"
	@echo
	@echo "Other targets:"
	@echo "    format       Format the source files"
	@echo "    lint         Run the linters"
	@echo "    update-deps  Update dependencies"
	@echo
	@echo "Note: Build system has migrated to Mage. Run 'mage -l' for all targets."
	@echo

.PHONY: all
all: build

#
# Mage wrapper targets
#

.PHONY: build
build:
	$(MAGE_CMD) build

.PHONY: clean
clean:
	$(MAGE_CMD) clean

.PHONY: nuke
nuke:
	$(MAGE_CMD) nuke

.PHONY: check test
check test:
	$(MAGE_CMD) test

.PHONY: toolchain
toolchain:
	$(MAGE_CMD) toolchain

.PHONY: install
install:
	$(MAGE_CMD) install

.PHONY: dev
dev:
	$(MAGE_CMD) dev

.PHONY: test-app
test-app:
	$(MAGE_CMD) testApp

.PHONY: export-import
export-import:
	$(MAGE_CMD) exportImport

.PHONY: format
format:
	$(MAGE_CMD) format

.PHONY: lint
lint:
	$(MAGE_CMD) lint

#
# Bootstrap targets (for individual tool bootstrapping)
#

.PHONY: bootstrap-go
bootstrap-go:
	$(MAGE_CMD) bootstrap:go

.PHONY: bootstrap-bison
bootstrap-bison:
	$(MAGE_CMD) bootstrap:bison

.PHONY: bootstrap-flex
bootstrap-flex:
	$(MAGE_CMD) bootstrap:flex

.PHONY: bootstrap-capnproto
bootstrap-capnproto:
	$(MAGE_CMD) bootstrap:capnProto

.PHONY: bootstrap-go-capnp
bootstrap-go-capnp:
	$(MAGE_CMD) bootstrap:goCapnp

.PHONY: bootstrap-bpf_asm
bootstrap-bpf_asm:
	$(MAGE_CMD) bootstrap:bpfAsm

.PHONY: bootstrap-tinygo
bootstrap-tinygo:
	$(MAGE_CMD) bootstrap:tinyGo

#
# Update Targets (kept in Makefile for simplicity)
#

.PHONY: update-deps
update-deps:
	go get capnproto.org/go/capnp/v3
	go get zenhack.net/go/util
	go get zenhack.net/go/tea
	go get zenhack.net/go/websocket-capnp
	go mod tidy
