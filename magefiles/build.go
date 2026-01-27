//go:build mage

package main

import (
	"bytes"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"io"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"time"

	"github.com/magefile/mage/sh"
)

// Config represents the project configuration
type Config struct {
	User, Group   string
	Prefix        string
	ExecPrefix    string
	Bindir        string
	Libexecdir    string
	Localstatedir string
	WithGoCapnp   string
	WithWasmExecJs string
	TinyGo        bool
	Args          []string
}

// BuildRecord tracks file modifications for incremental builds
type BuildRecord struct {
	Files map[string]FileSig
}

type FileStamp struct {
	Size    int64
	Mode    fs.FileMode
	ModTime time.Time
}

type FileSig struct {
	Stamp FileStamp
	Hash  []byte
}

const buildRecordPath = "_build/build_record.json"

// buildConfig generates configuration files
func buildConfig() error {
	maybeConfigure()

	r := getBuildRecord()
	defer r.Save()

	if !r.IsModified("./config.json") {
		return nil
	}

	fmt.Println("Generating config files...")
	cfg := readConfig()

	// Generate Go config
	goConfig := fmt.Sprintf(`package config

const (
	Prefix = %q
	Libexecdir = %q
	Localstatedir = %q
)
`, cfg.Prefix, cfg.Libexecdir, cfg.Localstatedir)

	goConfigPath := "./internal/config/config.go"
	if err := os.MkdirAll(filepath.Dir(goConfigPath), 0755); err != nil {
		return err
	}
	if err := os.WriteFile(goConfigPath, []byte(goConfig), 0600); err != nil {
		return err
	}
	r.RecordFile(goConfigPath)

	// Generate C config
	// Use #ifndef guards so command-line -D flags can override these values
	// (needed for VM builds where paths are different)
	cConfig := fmt.Sprintf(`
#pragma once

#ifndef PREFIX
#define PREFIX %q
#endif

#ifndef LIBEXECDIR
#define LIBEXECDIR %q
#endif

#ifndef LOCALSTATEDIR
#define LOCALSTATEDIR %q
#endif
`, cfg.Prefix, cfg.Libexecdir, cfg.Localstatedir)

	cConfigPath := "./c/config.h"
	if err := os.WriteFile(cConfigPath, []byte(cConfig), 0600); err != nil {
		return err
	}
	r.RecordFile(cConfigPath)

	return nil
}

// buildGo compiles all Go binaries
func buildGo() error {
	r := getBuildRecord()
	defer r.Save()

	// Build capnp first
	if err := buildCapnp(r); err != nil {
		return err
	}

	// Build webui
	cfg := readConfig()
	if err := buildWebui(r, cfg); err != nil {
		return err
	}

	// Build executables
	exes := []struct {
		name   string
		static bool
	}{
		{"sandstorm-import-tool", false},
		{"tempest", false},
		{"tempest-make-user", false},
		{"tempest-grain-agent", true},
		{"test-app", true},
		{"test-vm-sandbox", false},
	}

	for _, exe := range exes {
		if err := compileGoExe(exe.name, exe.static); err != nil {
			return err
		}
	}

	// Build Linux-only executables (for VM)
	if err := compileLinuxExe("tempest-vm-daemon"); err != nil {
		return err
	}
	if err := compileLinuxExe("tempest-grain-agent"); err != nil {
		return err
	}
	if err := compileLinuxExe("test-sandbox"); err != nil {
		return err
	}

	return nil
}

// buildCapnp compiles Cap'n Proto schemas
func buildCapnp(r *BuildRecord) error {
	fmt.Println("Compiling capnp schema...")
	cfg := readConfig()
	dirs := []string{"capnp", "internal/capnp"}

	// Get toolchain executables
	capnpExe := getToolchainCapnp()
	capnpcGoExe := getToolchainCapnpcGo()

	for _, d := range dirs {
		pattern := filepath.Join(d, "*.capnp")
		files, err := filepath.Glob(pattern)
		if err != nil {
			return err
		}

		for _, file := range files {
			dir := file[:len(file)-len(".capnp")]
			if err := os.MkdirAll(dir, 0755); err != nil {
				return err
			}

			cmd := exec.Command(capnpExe,
				"compile",
				"-o-",
				"--src-prefix="+d+"/",
				"-I", cfg.WithGoCapnp+"/std",
				"-I", "capnp",
				file,
			)
			cmd.Stderr = os.Stderr
			cgr, err := cmd.Output()
			if err != nil {
				return fmt.Errorf("capnp compile failed for %s: %w", file, err)
			}

			_ = capnpcGoExe // used below

			cgrPath := file + ".cgr"
			oldSig, ok := r.Files[cgrPath]
			if !ok || oldSig.Stamp.Size != int64(len(cgr)) {
				hash := sha256.Sum256(cgr)
				if !bytes.Equal(hash[:], oldSig.Hash) {
					fmt.Printf("Generating go code for %q\n", file)
					if err := os.WriteFile(cgrPath, cgr, 0644); err != nil {
						return err
					}

					cmd := exec.Command(capnpcGoExe)
					cmd.Dir = dir
					stdin, err := os.Open(cgrPath)
					if err != nil {
						return err
					}
					cmd.Stdin = stdin
					cmd.Stdout = os.Stdout
					cmd.Stderr = os.Stderr
					if err := cmd.Run(); err != nil {
						stdin.Close()
						return fmt.Errorf("capnpc-go failed: %w", err)
					}
					stdin.Close()
					r.RecordFile(cgrPath)
				}
			}
		}
	}

	return nil
}

// buildWebui builds the WebAssembly UI
func buildWebui(r *BuildRecord, cfg Config) error {
	const (
		tmpPath   = "_build/webui.wasm"
		finalPath = "internal/server/embed/webui.wasm"
		srcDir    = "./cmd/webui"
	)

	wasmExecSrc, err := findWasmExecJs(cfg)
	if err != nil {
		return err
	}

	fmt.Println("Building wasm binary...")

	if cfg.TinyGo {
		tinyGo := filepath.Join(toolchainDir, fmt.Sprintf("tinygo-%s", tinyGoVersion), "bin", "tinygo")
		if _, err := os.Stat(tinyGo); os.IsNotExist(err) {
			tinyGo = "tinygo"
		}

		if err := sh.RunV(tinyGo, "build",
			"-target", "wasm",
			"-panic", "trap",
			"-no-debug",
			"-o="+tmpPath,
			srcDir); err != nil {
			return err
		}
	} else {
		goExe := getToolchainGo()
		env := map[string]string{
			"GOOS":   "js",
			"GOARCH": "wasm",
		}
		if err := sh.RunWith(env, goExe, "build", "-o", tmpPath, srcDir); err != nil {
			return err
		}
	}

	if !r.IsModified(tmpPath) {
		return nil
	}
	r.RecordFile(tmpPath)

	// Show size
	sh.RunV("du", "-hs", tmpPath)

	// Copy to final location
	if err := os.MkdirAll(filepath.Dir(finalPath), 0755); err != nil {
		return err
	}
	if err := copyFile(finalPath, tmpPath); err != nil {
		return err
	}

	return copyFile("internal/server/embed/wasm_exec.js", wasmExecSrc)
}

// findWasmExecJs finds the wasm_exec.js file
func findWasmExecJs(cfg Config) (string, error) {
	if cfg.WithWasmExecJs != "" {
		return cfg.WithWasmExecJs, nil
	}

	if cfg.TinyGo {
		// Try toolchain tinygo first
		tinyGoDir := filepath.Join(toolchainDir, fmt.Sprintf("tinygo-%s", tinyGoVersion))
		wasmExec := filepath.Join(tinyGoDir, "targets", "wasm_exec.js")
		if _, err := os.Stat(wasmExec); err == nil {
			return wasmExec, nil
		}

		// Try system tinygo
		tinygoExe, err := exec.LookPath("tinygo")
		if err != nil {
			return "", fmt.Errorf("can't find tinygo executable: %w", err)
		}
		prefix := filepath.Dir(filepath.Dir(tinygoExe))
		candidates := []string{"/lib", "/lib32", "/lib64", "/share"}
		suffix := "/tinygo/targets/wasm_exec.js"
		for _, c := range candidates {
			path := prefix + c + suffix
			if _, err := os.Stat(path); err == nil {
				return path, nil
			}
		}
		return "", fmt.Errorf("failed to find wasm_exec.js")
	}

	// Regular go toolchain
	goExe := getToolchainGo()
	cmd := exec.Command(goExe, "env", "GOROOT")
	goroot, err := cmd.Output()
	if err != nil {
		return "", fmt.Errorf("could not determine GOROOT: %v", err)
	}
	gorootStr := strings.TrimSpace(string(goroot))
	// Try both locations: Go 1.25+ uses lib/wasm, older versions use misc/wasm
	candidates := []string{
		gorootStr + "/lib/wasm/wasm_exec.js",
		gorootStr + "/misc/wasm/wasm_exec.js",
	}
	for _, path := range candidates {
		if _, err := os.Stat(path); err == nil {
			return path, nil
		}
	}
	return "", fmt.Errorf("could not find wasm_exec.js in %s/lib/wasm or %s/misc/wasm", gorootStr, gorootStr)
}

// compileGoExe compiles a Go executable
func compileGoExe(name string, static bool) error {
	fmt.Printf("Compiling go executable %q (static = %v)\n", name, static)

	goExe := getToolchainGo()
	env := map[string]string{}
	if static {
		env["CGO_ENABLED"] = "0"
	} else {
		env["CGO_ENABLED"] = "1"
	}

	if err := sh.RunWith(env, goExe, "build", "-v", "-o", "_build/"+name, "./cmd/"+name); err != nil {
		return err
	}

	// On macOS, sign binaries that use Virtualization framework
	if runtime.GOOS == "darwin" && (name == "tempest" || name == "test-vm-sandbox") {
		entitlements := "./cmd/tempest/tempest.entitlements"
		if _, err := os.Stat(entitlements); err == nil {
			fmt.Printf("Signing %q with entitlements for Virtualization framework\n", name)
			if err := sh.RunV("codesign", "--sign", "-", "--entitlements", entitlements, "--force", "_build/"+name); err != nil {
				return fmt.Errorf("failed to sign %s: %w", name, err)
			}
		}
	}

	return nil
}

// compileLinuxExe compiles a Go executable for Linux (static, cross-compiled)
// Uses the same architecture as the host for VM use cases
// Output is saved with -linux suffix to avoid overwriting macOS binaries
func compileLinuxExe(name string) error {
	// Use arm64 on Apple Silicon, amd64 otherwise
	goarch := "amd64"
	if runtime.GOARCH == "arm64" {
		goarch = "arm64"
	}

	fmt.Printf("Compiling Linux executable %q (arch=%s)\n", name, goarch)

	goExe := getToolchainGo()
	env := map[string]string{
		"CGO_ENABLED": "0",
		"GOOS":        "linux",
		"GOARCH":      goarch,
	}

	// Use -linux suffix for Linux binaries to avoid overwriting macOS ones
	outputName := "_build/" + name + "-linux"
	return sh.RunWith(env, goExe, "build", "-v", "-o", outputName, "./cmd/"+name)
}

// maybeConfigure runs configure if config.json doesn't exist
func maybeConfigure() {
	if _, err := os.Stat("./config.json"); err == nil {
		return
	}
	fmt.Println("'configure' has not been run; running with default options.")
	runConfigure(nil)
}

// runConfigure creates the default configuration
func runConfigure(args []string) {
	// TinyGo prebuilt binaries are only available for Linux
	useTinyGo := runtime.GOOS == "linux"

	cfg := Config{
		User:   "sandstorm",
		Group:  "sandstorm",
		Prefix: "/usr/local",
		TinyGo: useTinyGo,
	}

	// Set defaults
	cfg.ExecPrefix = cfg.Prefix
	cfg.Bindir = cfg.ExecPrefix + "/bin"
	cfg.Libexecdir = cfg.Prefix + "/libexec"
	cfg.Localstatedir = cfg.Prefix + "/var/lib"

	// Set WithGoCapnp from toolchain
	cfg.WithGoCapnp = filepath.Join(toolchainDir, fmt.Sprintf("go-capnp-%s", goCapnpVersion))

	jsonData, err := json.MarshalIndent(cfg, "", "  ")
	if err != nil {
		fmt.Fprintf(os.Stderr, "Failed to create config: %v\n", err)
		return
	}
	if err := os.WriteFile("./config.json", jsonData, 0600); err != nil {
		fmt.Fprintf(os.Stderr, "Failed to write config.json: %v\n", err)
	}
}

// readConfig reads the configuration from config.json
func readConfig() Config {
	var c Config
	data, err := os.ReadFile("config.json")
	if err != nil {
		// Return defaults (TinyGo only available on Linux)
		return Config{
			User:          "sandstorm",
			Group:         "sandstorm",
			Prefix:        "/usr/local",
			Bindir:        "/usr/local/bin",
			Libexecdir:    "/usr/local/libexec",
			Localstatedir: "/usr/local/var/lib",
			WithGoCapnp:   filepath.Join(toolchainDir, fmt.Sprintf("go-capnp-%s", goCapnpVersion)),
			TinyGo:        runtime.GOOS == "linux",
		}
	}
	json.Unmarshal(data, &c)
	return c
}

// copyFile copies a file from src to dest
func copyFile(dest, src string) error {
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()

	out, err := os.Create(dest)
	if err != nil {
		return err
	}
	defer out.Close()

	_, err = io.Copy(out, in)
	return err
}

// BuildRecord methods

func getBuildRecord() *BuildRecord {
	empty := &BuildRecord{
		Files: make(map[string]FileSig),
	}
	data, err := os.ReadFile(buildRecordPath)
	if err != nil {
		return empty
	}
	var ret BuildRecord
	if err := json.Unmarshal(data, &ret); err != nil {
		return empty
	}
	if ret.Files == nil {
		ret.Files = make(map[string]FileSig)
	}
	return &ret
}

func (r *BuildRecord) Save() {
	data, err := json.Marshal(r)
	if err != nil {
		return
	}
	os.MkdirAll(filepath.Dir(buildRecordPath), 0755)
	os.WriteFile(buildRecordPath, data, 0644)
}

func (r *BuildRecord) IsModified(path string) bool {
	stamp, err := stampFile(path)
	if err != nil {
		return true
	}
	sig, ok := r.Files[path]
	if !ok {
		return true
	}
	if stamp == sig.Stamp {
		return false
	}
	hash, err := hashFile(path)
	if err != nil {
		return true
	}
	return !bytes.Equal(hash, sig.Hash)
}

func (r *BuildRecord) RecordFile(path string) error {
	stamp, err := stampFile(path)
	if err != nil {
		return err
	}
	hash, err := hashFile(path)
	if err != nil {
		return err
	}
	r.Files[path] = FileSig{
		Stamp: stamp,
		Hash:  hash,
	}
	return nil
}

func hashFile(path string) ([]byte, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	h := sha256.New()
	if _, err := io.Copy(h, f); err != nil {
		return nil, err
	}
	return h.Sum(nil), nil
}

func stampFile(path string) (FileStamp, error) {
	fi, err := os.Lstat(path)
	if err != nil {
		return FileStamp{}, err
	}
	return FileStamp{
		Size:    fi.Size(),
		Mode:    fi.Mode(),
		ModTime: fi.ModTime(),
	}, nil
}

// getToolchainCapnp returns the path to the toolchain capnp executable
func getToolchainCapnp() string {
	// Note: directory uses "capnp-" prefix
	capnpPath := filepath.Join(toolchainDir, fmt.Sprintf("capnp-%s", capnpVersion), "capnp")
	if _, err := os.Stat(capnpPath); err == nil {
		return capnpPath
	}
	// Fall back to system capnp
	return "capnp"
}

// getToolchainCapnpcGo returns the path to the toolchain capnpc-go executable
func getToolchainCapnpcGo() string {
	// capnpc-go is built in the go-capnp directory
	capnpcGoPath := filepath.Join(toolchainDir, fmt.Sprintf("go-capnp-%s", goCapnpVersion), "capnpc-go", "capnpc-go")
	absPath, err := filepath.Abs(capnpcGoPath)
	if err == nil {
		if _, err := os.Stat(absPath); err == nil {
			return absPath
		}
	}
	// Fall back to system capnpc-go
	return "capnpc-go"
}
