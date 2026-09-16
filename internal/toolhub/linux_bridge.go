package toolhub

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
)

const linuxELFMagic = "\x7fELF"

// ValidateLinuxBridgeBinary rejects Windows PE/hubctl.exe and any non-ELF
// file. The Docker fallback copies this binary into a Linux container.
func ValidateLinuxBridgeBinary(path string) error {
	if !filepath.IsAbs(path) || noSymlinkPath(path) != nil {
		return fmt.Errorf("%w: Linux ELF bridge binary required", ErrIsolation)
	}
	if strings.EqualFold(filepath.Ext(path), ".exe") {
		return fmt.Errorf("%w: Windows hubctl.exe cannot be copied as a Linux bridge", ErrIsolation)
	}
	file, err := os.Open(path) // #nosec G304 -- absolute operator-owned bridge path, symlink-checked above.
	if err != nil {
		return err
	}
	defer file.Close()
	header := make([]byte, 4)
	n, err := file.Read(header)
	if err != nil && n == 0 {
		return err
	}
	if n >= 2 && header[0] == 'M' && header[1] == 'Z' {
		return fmt.Errorf("%w: PE/Windows executable is not a Linux bridge", ErrIsolation)
	}
	if n < 4 || string(header) != linuxELFMagic {
		return fmt.Errorf("%w: Linux ELF bridge binary required", ErrIsolation)
	}
	return nil
}

func resolveLinuxBridgeBinary(configured string) (string, error) {
	if configured != "" {
		return configured, ValidateLinuxBridgeBinary(configured)
	}
	executable, err := os.Executable()
	if err != nil {
		return "", err
	}
	if err := ValidateLinuxBridgeBinary(executable); err == nil {
		return executable, nil
	}
	return "", fmt.Errorf("%w: set bridge_binary to a Linux ELF hubctl; the current executable is not a Linux bridge", ErrIsolation)
}

// BuildLinuxCompanionBridge cross-compiles the shipped hubctl companion/relay
// as a static Linux ELF. It is the Windows-safe alternative to copying hubctl.exe.
func BuildLinuxCompanionBridge(output string) error {
	if !filepath.IsAbs(output) || strings.EqualFold(filepath.Ext(output), ".exe") {
		return fmt.Errorf("%w: absolute Linux ELF output path required", ErrInvalid)
	}
	root, err := moduleRoot()
	if err != nil {
		return err
	}
	cmd := exec.Command("go", "build", "-trimpath", "-buildvcs=false", "-o", output, "./cmd/hubctl")
	cmd.Dir = root
	cmd.Env = append(os.Environ(), "GOOS=linux", "GOARCH="+linuxBridgeArch(), "CGO_ENABLED=0")
	body, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("%w: linux bridge build failed: %s", ErrIsolation, strings.TrimSpace(string(body)))
	}
	return ValidateLinuxBridgeBinary(output)
}

func linuxBridgeArch() string {
	if runtime.GOARCH == "arm64" {
		return "arm64"
	}
	return "amd64"
}

func moduleRoot() (string, error) {
	dir, err := os.Getwd()
	if err != nil {
		return "", err
	}
	for {
		if _, err := os.Stat(filepath.Join(dir, "go.mod")); err == nil {
			return dir, nil
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			return "", fmt.Errorf("%w: go.mod not found", ErrInvalid)
		}
		dir = parent
	}
}
