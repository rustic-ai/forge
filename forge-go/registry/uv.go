package registry

import (
	"archive/tar"
	"archive/zip"
	"bytes"
	"compress/gzip"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
)

var (
	execLookPath          = exec.LookPath
	currentExecutablePath = os.Executable
)

func uvxExecutableName() string {
	if runtime.GOOS == "windows" {
		return "uvx.exe"
	}
	return "uvx"
}

func bundledUVXPath() string {
	executablePath, err := currentExecutablePath()
	if err != nil || executablePath == "" {
		return ""
	}
	uvxPath := filepath.Join(filepath.Dir(executablePath), uvxExecutableName())
	if info, err := os.Stat(uvxPath); err == nil && !info.IsDir() {
		return uvxPath
	}
	return ""
}

func forgeBinUVXPath() string {
	homeDir, err := os.UserHomeDir()
	if err != nil || homeDir == "" {
		return ""
	}
	uvxPath := filepath.Join(homeDir, ".forge", "bin", uvxExecutableName())
	if info, err := os.Stat(uvxPath); err == nil && !info.IsDir() {
		return uvxPath
	}
	return ""
}

func configuredUVXPath() string {
	uvxPath := strings.TrimSpace(os.Getenv("FORGE_UVX_PATH"))
	if uvxPath == "" {
		return ""
	}
	if info, err := os.Stat(uvxPath); err == nil && !info.IsDir() {
		return uvxPath
	}
	return ""
}

// ResolveUVXCommand returns the uvx executable Forge should invoke.
// Preference order:
// 1. bundled uvx next to the running Forge binary
// 2. uvx already available on PATH
// 3. ~/.forge/bin/uvx
// 4. FORGE_UVX_PATH
func ResolveUVXCommand() string {
	if uvxPath := bundledUVXPath(); uvxPath != "" {
		return uvxPath
	}
	if _, err := execLookPath("uvx"); err == nil {
		return "uvx"
	}
	if uvxPath := forgeBinUVXPath(); uvxPath != "" {
		return uvxPath
	}
	if uvxPath := configuredUVXPath(); uvxPath != "" {
		return uvxPath
	}
	return "uvx"
}

// EnsureUV checks if `uvx` is on the PATH. If not, it attempts to download and
// install `uv` (which includes `uvx`) to ~/.forge/bin and prepends it to the PATH
// for the current process.
func EnsureUV() error {
	if uvxPath := bundledUVXPath(); uvxPath != "" {
		return addToPath(filepath.Dir(uvxPath))
	}
	if _, err := execLookPath("uvx"); err == nil {
		// uvx is already available
		return nil
	}
	if uvxPath := forgeBinUVXPath(); uvxPath != "" {
		return addToPath(filepath.Dir(uvxPath))
	}
	if uvxPath := configuredUVXPath(); uvxPath != "" {
		return addToPath(filepath.Dir(uvxPath))
	}

	homeDir, err := os.UserHomeDir()
	if err != nil {
		return fmt.Errorf("failed to get user home directory: %w", err)
	}

	forgeBin := filepath.Join(homeDir, ".forge", "bin")

	if err := os.MkdirAll(forgeBin, 0755); err != nil {
		return fmt.Errorf("failed to create %s: %w", forgeBin, err)
	}

	fmt.Printf("uvx not found on PATH. Downloading astral-sh/uv to %s...\n", forgeBin)

	downloadURL, targetDirName, err := getUVReleaseURL()
	if err != nil {
		return err
	}

	isZip := strings.HasSuffix(downloadURL, ".zip")
	if err := downloadAndExtract(downloadURL, targetDirName, forgeBin, isZip); err != nil {
		return err
	}

	fmt.Println("uv downloaded successfully.")

	return addToPath(forgeBin)
}

func addToPath(binDir string) error {
	currentPath := os.Getenv("PATH")
	if !strings.Contains(currentPath, binDir) {
		newPath := fmt.Sprintf("%s%c%s", binDir, os.PathListSeparator, currentPath)
		if err := os.Setenv("PATH", newPath); err != nil {
			return fmt.Errorf("failed to set PATH: %w", err)
		}
	}
	return nil
}

func getUVReleaseURL() (string, string, error) {
	targets := map[string]string{
		"linux/amd64":   "x86_64-unknown-linux-gnu",
		"linux/arm64":   "aarch64-unknown-linux-gnu",
		"darwin/amd64":  "x86_64-apple-darwin",
		"darwin/arm64":  "aarch64-apple-darwin",
		"windows/amd64": "x86_64-pc-windows-msvc",
		"windows/arm64": "aarch64-pc-windows-msvc",
	}

	key := runtime.GOOS + "/" + runtime.GOARCH
	target, ok := targets[key]
	if !ok {
		return "", "", fmt.Errorf("unsupported OS/Arch combination for automatic uv download: %s %s", runtime.GOOS, runtime.GOARCH)
	}

	extension := "tar.gz"
	if runtime.GOOS == "windows" {
		extension = "zip"
	}

	dirName := fmt.Sprintf("uv-%s", target)
	url := fmt.Sprintf("https://github.com/astral-sh/uv/releases/latest/download/%s.%s", dirName, extension)

	return url, dirName, nil
}

func downloadAndExtract(url, targetDirName, destBin string, isZip bool) error {
	resp, err := http.Get(url)
	if err != nil {
		return fmt.Errorf("failed to download uv from %s: %w", url, err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("bad status: %s when downloading %s", resp.Status, url)
	}

	if isZip {
		bodyBytes, err := io.ReadAll(resp.Body)
		if err != nil {
			return fmt.Errorf("failed to read zip body: %w", err)
		}
		zr, err := zip.NewReader(bytes.NewReader(bodyBytes), int64(len(bodyBytes)))
		if err != nil {
			return fmt.Errorf("failed to read zip: %w", err)
		}
		found := 0
		for _, file := range zr.File {
			name := filepath.Base(file.Name)
			if name == "uv.exe" || name == "uvx.exe" {
				outPath := filepath.Join(destBin, name)
				outFile, err := os.OpenFile(outPath, os.O_CREATE|os.O_RDWR, 0755)
				if err != nil {
					return fmt.Errorf("failed to create binary file %s: %w", outPath, err)
				}
				rc, err := file.Open()
				if err != nil {
					_ = outFile.Close()
					return fmt.Errorf("failed to open zip file %s: %w", file.Name, err)
				}
				if _, err := io.Copy(outFile, rc); err != nil {
					_ = rc.Close()
					_ = outFile.Close()
					return fmt.Errorf("failed to write %s: %w", name, err)
				}
				if err := rc.Close(); err != nil {
					_ = outFile.Close()
					return fmt.Errorf("failed to close zip file stream %s: %w", file.Name, err)
				}
				if err := outFile.Close(); err != nil {
					return fmt.Errorf("failed to close output file %s: %w", outPath, err)
				}
				found++
			}
		}
		if found < 2 {
			return fmt.Errorf("failed to find uv and uvx binaries in the zip")
		}
		return nil
	}

	gzr, err := gzip.NewReader(resp.Body)
	if err != nil {
		return fmt.Errorf("failed to read gzip stream: %w", err)
	}
	defer gzr.Close()

	tr := tar.NewReader(gzr)

	found := 0
	for {
		header, err := tr.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			return fmt.Errorf("failed reading tar: %w", err)
		}

		if header.Typeflag == tar.TypeReg {
			name := filepath.Base(header.Name)
			if name == "uv" || name == "uvx" {
				outPath := filepath.Join(destBin, name)
				outFile, err := os.OpenFile(outPath, os.O_CREATE|os.O_RDWR, 0755)
				if err != nil {
					return fmt.Errorf("failed to create binary file %s: %w", outPath, err)
				}
				if _, err := io.Copy(outFile, tr); err != nil {
					_ = outFile.Close()
					return fmt.Errorf("failed to write %s: %w", name, err)
				}
				if err := outFile.Close(); err != nil {
					return fmt.Errorf("failed to close output file %s: %w", outPath, err)
				}
				found++
			}
		}
	}

	if found < 2 {
		return fmt.Errorf("failed to find uv and uvx binaries in the tarball")
	}

	return nil
}
