//go:build mage

package main

import (
	"fmt"
	"os"
	"path/filepath"

	"github.com/magefile/mage/mg"
	"github.com/magefile/mage/sh"
)

// Tool versions (matching Makefile)
const (
	bisonVersion   = "3.8.2"
	bpfAsmVersion  = "6.13.8"
	capnpVersion   = "1.1.0"
	flexVersion    = "2.6.4"
	goCapnpVersion = "3.1.0-alpha.1"
	tinyGoVersion  = "0.37.0"
)

// Bison builds bison from source
func (Bootstrap) Bison() error {
	mg.Deps(Bootstrap.Go)

	bisonDir := filepath.Join(toolchainDir, fmt.Sprintf("bison-%s", bisonVersion))
	bisonExe := filepath.Join(bisonDir, "tests", "bison")

	if _, err := os.Stat(bisonExe); err == nil {
		fmt.Printf("Bison %s already installed\n", bisonVersion)
		return nil
	}

	fmt.Printf("Building Bison %s...\n", bisonVersion)
	return runBuildTool("bootstrap-bison")
}

// Flex builds flex from source
func (Bootstrap) Flex() error {
	mg.Deps(Bootstrap.Go)

	flexDir := filepath.Join(toolchainDir, fmt.Sprintf("flex-%s", flexVersion))
	flexExe := filepath.Join(flexDir, "src", "flex")

	if _, err := os.Stat(flexExe); err == nil {
		fmt.Printf("Flex %s already installed\n", flexVersion)
		return nil
	}

	fmt.Printf("Building Flex %s...\n", flexVersion)
	return runBuildTool("bootstrap-flex")
}

// CapnProto builds Cap'n Proto from source
func (Bootstrap) CapnProto() error {
	mg.Deps(Bootstrap.Go)

	// Note: directory uses "capnp-" prefix, not "capnproto-"
	capnpDir := filepath.Join(toolchainDir, fmt.Sprintf("capnp-%s", capnpVersion))
	capnpExe := filepath.Join(capnpDir, "capnp")

	if _, err := os.Stat(capnpExe); err == nil {
		fmt.Printf("Cap'n Proto %s already installed\n", capnpVersion)
		return nil
	}

	fmt.Printf("Building Cap'n Proto %s...\n", capnpVersion)
	return runBuildTool("bootstrap-capnproto")
}

// GoCapnp sets up go-capnp
func (Bootstrap) GoCapnp() error {
	mg.Deps(Bootstrap.Go)

	goCapnpDir := filepath.Join(toolchainDir, fmt.Sprintf("go-capnp-%s", goCapnpVersion))
	// Check for the capnpc-go source directory as indicator of installation
	goCapnpCheck := filepath.Join(goCapnpDir, "capnpc-go")

	if _, err := os.Stat(goCapnpCheck); err == nil {
		fmt.Printf("go-capnp %s already installed\n", goCapnpVersion)
		return nil
	}

	fmt.Printf("Setting up go-capnp %s...\n", goCapnpVersion)
	return runBuildTool("bootstrap-go-capnp")
}

// BpfAsm builds bpf_asm from Linux kernel source
func (Bootstrap) BpfAsm() error {
	mg.Deps(Bootstrap.Go, Bootstrap.Bison, Bootstrap.Flex)

	bpfAsmDir := filepath.Join(toolchainDir, fmt.Sprintf("bpf_asm-%s", bpfAsmVersion))
	bpfAsmExe := filepath.Join(bpfAsmDir, "tools", "bpf", "bpf_asm")

	if _, err := os.Stat(bpfAsmExe); err == nil {
		fmt.Printf("bpf_asm %s already installed\n", bpfAsmVersion)
		return nil
	}

	fmt.Printf("Building bpf_asm from Linux %s...\n", bpfAsmVersion)
	return runBuildTool("bootstrap-bpf_asm")
}

// TinyGo sets up TinyGo
func (Bootstrap) TinyGo() error {
	mg.Deps(Bootstrap.Go)

	tinyGoDir := filepath.Join(toolchainDir, fmt.Sprintf("tinygo-%s", tinyGoVersion))
	tinyGoExe := filepath.Join(tinyGoDir, "bin", "tinygo")

	if _, err := os.Stat(tinyGoExe); err == nil {
		fmt.Printf("TinyGo %s already installed\n", tinyGoVersion)
		return nil
	}

	fmt.Printf("Setting up TinyGo %s...\n", tinyGoVersion)
	return runBuildTool("bootstrap-tinygo")
}

// runBuildTool runs the build-tool with the specified command
func runBuildTool(command string) error {
	buildTool := filepath.Join(buildDir, "build-tool")

	// Build the build-tool if it doesn't exist
	if _, err := os.Stat(buildTool); os.IsNotExist(err) {
		if err := buildBuildTool(); err != nil {
			return err
		}
	}

	return sh.RunV(buildTool, command)
}

// buildBuildTool compiles the build-tool binary
func buildBuildTool() error {
	if err := os.MkdirAll(buildDir, 0755); err != nil {
		return err
	}

	goExe := getToolchainGo()
	goPath := getGoPath()

	fmt.Println("Building build-tool...")

	// Get dependencies
	env := map[string]string{"GOPATH": goPath}
	if err := sh.RunWith(env, goExe, "get", "./internal/build-tool"); err != nil {
		return err
	}

	// Build the tool
	buildTool := filepath.Join(buildDir, "build-tool")
	return sh.RunWith(env, goExe, "build", "-o", buildTool, "cmd/build-tool/main.go")
}

// getToolchainGo returns the path to the toolchain Go executable
func getToolchainGo() string {
	toolchainGo := filepath.Join(toolchainDir, fmt.Sprintf("go-%s", goVersion), "bin", "go")
	if _, err := os.Stat(toolchainGo); err == nil {
		return toolchainGo
	}
	// Fall back to system Go
	return "go"
}

// getGoPath returns the GOPATH for the toolchain
func getGoPath() string {
	absToolchain, err := filepath.Abs(toolchainDir)
	if err != nil {
		absToolchain = toolchainDir
	}
	return filepath.Join(absToolchain, fmt.Sprintf("gopath-%s", goVersion))
}
