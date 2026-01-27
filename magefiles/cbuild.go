//go:build mage

package main

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"

	"github.com/magefile/mage/sh"
)

// buildC compiles the C sandbox launcher
func buildC() error {
	// The sandbox launcher requires Linux-specific headers
	if runtime.GOOS != "linux" {
		fmt.Printf("Skipping C build (sandbox launcher is Linux-only, current OS: %s)\n", runtime.GOOS)
		return nil
	}

	fmt.Println("Building C executable...")

	if err := os.MkdirAll(buildDir, 0755); err != nil {
		return err
	}

	// First, generate the BPF filter header
	if err := generateBpfFilter(); err != nil {
		return err
	}

	// Compile sandbox-launcher.o
	if err := compileObject("c/sandbox-launcher.c", "c/sandbox-launcher.o"); err != nil {
		return err
	}

	// Link tempest-sandbox-launcher
	sandboxLauncher := filepath.Join(buildDir, "tempest-sandbox-launcher")
	if err := linkExecutable([]string{"c/sandbox-launcher.o"}, sandboxLauncher); err != nil {
		return err
	}

	fmt.Println("Built tempest-sandbox-launcher")
	return nil
}

// compileObject compiles a C source file to an object file
func compileObject(src, obj string) error {
	cc := getCC()
	cflags := getCFlags()

	args := []string{}
	args = append(args, cflags...)
	args = append(args, "-I", buildDir, "-std=c11", "-Wall", "-Wextra", "-MMD", "-c", "-o", obj, src)

	fmt.Printf("CC %s\n", src)
	return sh.RunV(cc, args...)
}

// linkExecutable links object files into an executable
func linkExecutable(objs []string, out string) error {
	cc := getCC()
	ldflags := getLDFlags()

	args := []string{}
	args = append(args, ldflags...)
	args = append(args, "-o", out)
	args = append(args, objs...)

	fmt.Printf("LD %s\n", out)
	return sh.RunV(cc, args...)
}

// generateBpfFilter generates the BPF filter header file
func generateBpfFilter() error {
	// Step 1: Compile gen-clean-h
	if err := compileObject("c/gen-clean-h.c", "c/gen-clean-h.o"); err != nil {
		return err
	}

	genCleanH := filepath.Join(buildDir, "gen-clean-h")
	if err := linkExecutable([]string{"c/gen-clean-h.o"}, genCleanH); err != nil {
		return err
	}

	// Step 2: Run gen-clean-h to generate constants.h
	constantsH := filepath.Join(buildDir, "constants.h")
	fmt.Println("Generating constants.h")
	output, err := sh.Output(genCleanH)
	if err != nil {
		return fmt.Errorf("failed to run gen-clean-h: %w", err)
	}
	if err := os.WriteFile(constantsH, []byte(output), 0644); err != nil {
		return err
	}

	// Step 3: Run cpp to preprocess filter.s
	filterPreproc := filepath.Join(buildDir, "filter_preproc.s")
	fmt.Println("Preprocessing filter.s")
	cmd := exec.Command("cpp", "-I", buildDir, "c/filter.s", "-o", filterPreproc)
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("failed to preprocess filter.s: %w", err)
	}

	// Step 4: Run bpf_asm to generate bpf_filter.h
	bpfFilterH := filepath.Join(buildDir, "bpf_filter.h")
	bpfAsm := getBpfAsm()
	fmt.Println("Generating bpf_filter.h")

	// Read the preprocessed filter
	filterContent, err := os.ReadFile(filterPreproc)
	if err != nil {
		return err
	}

	// Run bpf_asm with stdin
	cmd = exec.Command(bpfAsm, "-c")
	cmd.Stdin = strings.NewReader(string(filterContent))
	output_bytes, err := cmd.Output()
	if err != nil {
		return fmt.Errorf("failed to run bpf_asm: %w", err)
	}

	if err := os.WriteFile(bpfFilterH, output_bytes, 0644); err != nil {
		return err
	}

	return nil
}

// getCC returns the C compiler to use
func getCC() string {
	if cc := os.Getenv("CC"); cc != "" {
		return cc
	}
	return "cc"
}

// getCFlags returns the C compiler flags
func getCFlags() []string {
	if cflags := os.Getenv("CFLAGS"); cflags != "" {
		return strings.Fields(cflags)
	}
	return nil
}

// getLDFlags returns the linker flags
func getLDFlags() []string {
	if ldflags := os.Getenv("LDFLAGS"); ldflags != "" {
		return strings.Fields(ldflags)
	}
	return nil
}

// getBpfAsm returns the path to bpf_asm
func getBpfAsm() string {
	// Try toolchain version first
	bpfAsmPath := filepath.Join(toolchainDir, fmt.Sprintf("bpf_asm-%s", bpfAsmVersion), "tools", "bpf", "bpf_asm")
	if _, err := os.Stat(bpfAsmPath); err == nil {
		return bpfAsmPath
	}
	// Fall back to system bpf_asm
	return "bpf_asm"
}
