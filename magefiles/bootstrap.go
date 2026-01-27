//go:build mage

package main

import (
	"archive/tar"
	"compress/gzip"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"runtime"
	"strings"

	"github.com/magefile/mage/mg"
	"github.com/schollz/progressbar/v3"
)

const (
	goVersion         = "1.25.6"
	goExpectedSha256  = "f022b6aad78e362bcba9b0b94d09ad58c5a70c6ba3b7582905fababf5fe0181a"  // linux-amd64
	goExpectedSha256Darwin = "984521ae978a5377c7d782fd2dd953291840d7d3d0bd95781a1f32f16d94a006"  // darwin-arm64
	downloadUserAgent = "tempest-bootstrap"
)

// Bootstrap namespace for toolchain setup targets
type Bootstrap mg.Namespace

// Go downloads and installs the Go toolchain to ./toolchain
func (Bootstrap) Go() error {
	// Check platform support early
	platform := runtime.GOOS + "-" + runtime.GOARCH
	if platform != "linux-amd64" && platform != "darwin-arm64" {
		return fmt.Errorf("unsupported platform: %s. Only linux-amd64 and darwin-arm64 are supported", platform)
	}

	goInstallDir := filepath.Join(toolchainDir, fmt.Sprintf("go-%s", goVersion))
	goExecutable := filepath.Join(goInstallDir, "bin", "go")

	// Check if already installed
	if _, err := os.Stat(goExecutable); err == nil {
		fmt.Printf("Go %s already installed at %s\n", goVersion, goInstallDir)
		return nil
	}

	fmt.Printf("Setting up Go %s...\n", goVersion)

	// Determine the download file based on platform
	var downloadFile string
	switch runtime.GOOS {
	case "linux":
		downloadFile = fmt.Sprintf("go%s.linux-%s.tar.gz", goVersion, runtime.GOARCH)
	case "darwin":
		downloadFile = fmt.Sprintf("go%s.darwin-%s.tar.gz", goVersion, runtime.GOARCH)
	default:
		return fmt.Errorf("unsupported operating system: %s", runtime.GOOS)
	}

	downloadURL := fmt.Sprintf("https://go.dev/dl/%s", downloadFile)

	// Create download cache directory
	cacheDir := getCacheDir()
	if err := os.MkdirAll(cacheDir, 0755); err != nil {
		return fmt.Errorf("failed to create cache directory: %w", err)
	}

	downloadPath := filepath.Join(cacheDir, downloadFile)

	// Download if not cached
	if _, err := os.Stat(downloadPath); os.IsNotExist(err) {
		if err := downloadFile_Go(downloadURL, downloadPath); err != nil {
			return fmt.Errorf("failed to download Go: %w", err)
		}
	}

	// Verify SHA256
	var expectedSha256 string
	switch runtime.GOOS + "-" + runtime.GOARCH {
	case "linux-amd64":
		expectedSha256 = goExpectedSha256
	case "darwin-arm64":
		expectedSha256 = goExpectedSha256Darwin
	default:
		return fmt.Errorf("unsupported platform: %s/%s. Only linux-amd64 and darwin-arm64 are supported", runtime.GOOS, runtime.GOARCH)
	}

	if err := verifySha256(expectedSha256, downloadPath); err != nil {
		os.Remove(downloadPath)
		return fmt.Errorf("SHA256 verification failed: %w", err)
	}
	fmt.Println("SHA256 verified")

	// Extract
	fmt.Printf("Extracting to %s...\n", goInstallDir)
	if err := os.MkdirAll(goInstallDir, 0755); err != nil {
		return err
	}

	if err := extractGoTarball(downloadPath, goInstallDir); err != nil {
		os.RemoveAll(goInstallDir)
		return fmt.Errorf("failed to extract Go: %w", err)
	}

	// Update toolchain.toml
	if err := updateToolchainToml("go", goVersion, filepath.Join(fmt.Sprintf("go-%s", goVersion), "bin", "go")); err != nil {
		return fmt.Errorf("failed to update toolchain.toml: %w", err)
	}

	fmt.Printf("Go %s installed successfully\n", goVersion)
	return nil
}

// All sets up the complete toolchain
func (Bootstrap) All() error {
	mg.SerialDeps(
		Bootstrap.Go,
		Bootstrap.Bison,
		Bootstrap.Flex,
		Bootstrap.CapnProto,
		Bootstrap.GoCapnp,
		Bootstrap.BpfAsm,
		Bootstrap.TinyGo,
	)
	return nil
}

// Toolchain is an alias for Bootstrap:All
func Toolchain() error {
	return Bootstrap.All(Bootstrap{})
}

func getCacheDir() string {
	home := os.Getenv("HOME")
	if home == "" {
		home = "/tmp"
	}
	return filepath.Join(home, ".cache", "tempest-build-tool", "downloads")
}

func downloadFile_Go(url, destPath string) error {
	fmt.Printf("Downloading %s\n", url)

	client := &http.Client{}
	req, err := http.NewRequest("GET", url, nil)
	if err != nil {
		return err
	}
	req.Header.Set("User-Agent", downloadUserAgent)

	resp, err := client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("GET %s => %s", url, resp.Status)
	}

	// Create temp file for atomic download
	tempFile, err := os.CreateTemp(filepath.Dir(destPath), "download-")
	if err != nil {
		return err
	}
	defer func() {
		tempFile.Close()
		os.Remove(tempFile.Name())
	}()

	bar := progressbar.DefaultBytes(
		resp.ContentLength,
		"Downloading",
	)

	_, err = io.Copy(io.MultiWriter(tempFile, bar), resp.Body)
	if err != nil {
		return err
	}
	tempFile.Close()

	return os.Rename(tempFile.Name(), destPath)
}

func verifySha256(expected, path string) error {
	f, err := os.Open(path)
	if err != nil {
		return err
	}
	defer f.Close()

	h := sha256.New()
	if _, err := io.Copy(h, f); err != nil {
		return err
	}

	actual := hex.EncodeToString(h.Sum(nil))
	if actual != expected {
		return fmt.Errorf("SHA256 mismatch: expected %s, got %s", expected, actual)
	}
	return nil
}

func extractGoTarball(tarballPath, destDir string) error {
	f, err := os.Open(tarballPath)
	if err != nil {
		return err
	}
	defer f.Close()

	gzr, err := gzip.NewReader(f)
	if err != nil {
		return err
	}
	defer gzr.Close()

	tr := tar.NewReader(gzr)

	for {
		header, err := tr.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			return err
		}

		// Strip the leading "go/" directory
		name := header.Name
		if !strings.HasPrefix(name, "go/") {
			continue
		}
		name = strings.TrimPrefix(name, "go/")
		if name == "" {
			continue
		}

		target := filepath.Join(destDir, name)

		switch header.Typeflag {
		case tar.TypeDir:
			if err := os.MkdirAll(target, os.FileMode(header.Mode)); err != nil {
				return err
			}
		case tar.TypeReg:
			if err := os.MkdirAll(filepath.Dir(target), 0755); err != nil {
				return err
			}
			outFile, err := os.OpenFile(target, os.O_CREATE|os.O_RDWR, os.FileMode(header.Mode))
			if err != nil {
				return err
			}
			if _, err := io.Copy(outFile, tr); err != nil {
				outFile.Close()
				return err
			}
			outFile.Close()
		case tar.TypeSymlink:
			if err := os.MkdirAll(filepath.Dir(target), 0755); err != nil {
				return err
			}
			if err := os.Symlink(header.Linkname, target); err != nil {
				return err
			}
		}
	}

	return nil
}

func updateToolchainToml(tool, version, executable string) error {
	tomlPath := filepath.Join(toolchainDir, "toolchain.toml")

	// Read existing file or start fresh
	content := ""
	if data, err := os.ReadFile(tomlPath); err == nil {
		content = string(data)
	}

	// Build the new section
	section := fmt.Sprintf(`[%s]
  Executable = "%s"
  Version = "%s"
`, tool, executable, version)

	// Check if section already exists
	sectionHeader := fmt.Sprintf("[%s]", tool)
	if strings.Contains(content, sectionHeader) {
		// Replace existing section
		lines := strings.Split(content, "\n")
		var newLines []string
		inSection := false
		for _, line := range lines {
			if strings.HasPrefix(strings.TrimSpace(line), "[") {
				if strings.TrimSpace(line) == sectionHeader {
					inSection = true
					continue
				}
				if inSection {
					// We've reached the next section
					newLines = append(newLines, section)
					inSection = false
				}
			}
			if !inSection {
				newLines = append(newLines, line)
			}
		}
		if inSection {
			// Section was at the end
			newLines = append(newLines, section)
		}
		content = strings.Join(newLines, "\n")
	} else {
		// Add new section
		if content != "" && !strings.HasSuffix(content, "\n") {
			content += "\n"
		}
		content += section
	}

	// Add header if file is new
	if !strings.HasPrefix(content, "#") {
		header := "# This file is managed by the Tempest build system.\n# See magefiles/bootstrap.go\n\n"
		content = header + content
	}

	if err := os.MkdirAll(toolchainDir, 0755); err != nil {
		return err
	}
	return os.WriteFile(tomlPath, []byte(content), 0644)
}
