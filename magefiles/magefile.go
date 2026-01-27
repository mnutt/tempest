//go:build mage

// Tempest build system. Run 'mage -l' to list targets, 'mage build' to compile.
package main

import (
	"fmt"
	"os"
	"path/filepath"

	"github.com/magefile/mage/mg"
	"github.com/magefile/mage/sh"
)

// Default target when running mage without arguments
var Default = Build

const (
	buildDir     = "_build"
	toolchainDir = "toolchain"
)

// Build compiles the entire project
func Build() error {
	mg.Deps(Toolchain)
	mg.SerialDeps(
		buildConfig,
		buildC,
		buildGo,
	)
	return nil
}

// Clean removes build artifacts
func Clean() error {
	fmt.Println("Cleaning build artifacts...")

	// Remove _build directory
	if err := os.RemoveAll(buildDir); err != nil {
		return err
	}

	// Remove generated files
	filesToRemove := []string{
		"go/internal/server/embed/webui.wasm",
		"go/internal/server/embed/wasm_exec.js",
		"c/config.h",
		"internal/config/config.go",
	}
	for _, f := range filesToRemove {
		os.Remove(f)
	}

	// Remove .cgr files
	if err := removeByGlob("*/*.cgr"); err != nil {
		return err
	}
	if err := removeByGlob("*/*/*.cgr"); err != nil {
		return err
	}

	// Remove C object and dependency files
	if err := removeByGlob("c/*.o"); err != nil {
		return err
	}
	if err := removeByGlob("c/*.d"); err != nil {
		return err
	}

	// Remove empty directories
	return removeEmptyDirs(".")
}

// Nuke removes build artifacts and toolchain
func Nuke() error {
	mg.Deps(Clean)

	fmt.Println("Removing toolchain...")

	// Go get tends to create files with write bit cleared
	if _, err := os.Stat(toolchainDir); err == nil {
		err := filepath.Walk(toolchainDir, func(path string, info os.FileInfo, err error) error {
			if err != nil {
				return err
			}
			if info.Mode()&0200 == 0 {
				return os.Chmod(path, info.Mode()|0700)
			}
			return nil
		})
		if err != nil {
			return err
		}
	}

	if err := os.RemoveAll(toolchainDir); err != nil {
		return err
	}

	// Remove build tool cache
	home := os.Getenv("HOME")
	if home != "" {
		os.RemoveAll(filepath.Join(home, ".cache", "tempest-build-tool"))
	}

	// Remove config.json
	os.Remove("config.json")

	return nil
}

// Test runs the test suite
func Test() error {
	mg.Deps(Build)
	fmt.Println("Running tests...")
	return sh.RunV("go", "test",
		"./internal/server/...",
		"./internal/common/...",
		"./pkg/...",
	)
}

// Install installs binaries to the configured prefix
func Install() error {
	mg.Deps(Build)
	fmt.Println("Installing...")
	return sh.RunV("go", "run", "internal/make/make.go", "install")
}

// Dev installs and runs tempest
func Dev() error {
	mg.Deps(Install)
	fmt.Println("Running tempest in dev mode...")
	return sh.RunV("go", "run", "internal/make/make.go", "dev")
}

// Format formats the source files
func Format() error {
	fmt.Println("Formatting source files...")

	// Format Go files
	if err := sh.RunV("gofmt", "-l", "-w",
		"cmd/build-tool/main.go",
		"internal/build-tool",
	); err != nil {
		return err
	}

	// Format magefiles
	if err := sh.RunV("gofmt", "-l", "-w", "magefiles"); err != nil {
		return err
	}

	return nil
}

// Lint runs linters
func Lint() error {
	fmt.Println("Running linters...")
	// Could add more linters here
	return nil
}

// TestApp builds the test app
func TestApp() error {
	mg.Deps(Build)
	return sh.RunV("go", "run", "internal/make/make.go", "test-app")
}

// ExportImport runs export-import
func ExportImport() error {
	mg.Deps(Build)
	return sh.RunV("go", "run", "internal/make/make.go", "export-import")
}

// Helper functions

func removeByGlob(pattern string) error {
	matches, err := filepath.Glob(pattern)
	if err != nil {
		return err
	}
	for _, m := range matches {
		if err := os.Remove(m); err != nil && !os.IsNotExist(err) {
			return err
		}
	}
	return nil
}

func removeEmptyDirs(root string) error {
	return filepath.Walk(root, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		if !info.IsDir() {
			return nil
		}
		// Skip toolchain and .git directories
		if info.Name() == ".git" || info.Name() == "toolchain" {
			return filepath.SkipDir
		}
		entries, err := os.ReadDir(path)
		if err != nil {
			return err
		}
		if len(entries) == 0 && path != root {
			return os.Remove(path)
		}
		return nil
	})
}
