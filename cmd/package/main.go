package main

import (
	"archive/zip"
	"crypto/sha256"
	"fmt"
	"io"
	"log"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"

	"github.com/letya999/hermes-hub/shared/locale"
)

var sourcePaths = []string{".github", ".work", "cmd", "config", "contracts", "docker", "docs", "internal", "platform", "services/credential-broker", "services/README.template.md", "shared", "specs", ".dockerignore", ".gitignore", "AGENTS.md", "CHANGELOG.md", "CODE_OF_CONDUCT.md", "CONTRIBUTING.md", "LICENSE", "NOTICE", "README.md", "SECURITY.md", "SETUP.md", "START_HERE.ru.md", "THIRD_PARTY.md", "THIRD_PARTY_LICENSES.txt", "go.mod", "go.sum", "go.work", "go.work.sum", "justfile"}
var buildBinary = build

func main() {
	log.SetFlags(log.LstdFlags | log.LUTC)
	if len(os.Args) != 2 {
		fmt.Fprintln(os.Stderr, "usage: go run ./cmd/package VERSION")
		os.Exit(2)
	}
	if err := pack(os.Args[1]); err != nil {
		fmt.Fprintln(os.Stderr, "package:", err)
		os.Exit(1)
	}
}

func pack(version string) error {
	if strings.TrimSpace(version) == "" || strings.ContainsAny(version, `/\\`) {
		return fmt.Errorf("invalid version %q", version)
	}
	if err := os.MkdirAll("dist", 0755); err != nil {
		return err
	}
	var outputs []string
	for _, target := range [][2]string{{"windows", "amd64"}, {"linux", "amd64"}, {"linux", "arm64"}, {"darwin", "arm64"}} {
		name := filepath.Join("dist", "hubctl-"+target[0]+"-"+target[1])
		if target[0] == "windows" {
			name += ".exe"
		}
		if err := buildBinary(name, "./cmd/hubctl", target[0], target[1]); err != nil {
			return err
		}
		outputs = append(outputs, name)
	}
	for _, arch := range []string{"amd64", "arm64"} {
		name := filepath.Join("dist", "hub-runtime-linux-"+arch)
		if err := buildBinary(name, "./cmd/runtime", "linux", arch); err != nil {
			return err
		}
		outputs = append(outputs, name)
		name = filepath.Join("dist", "communication-hub-linux-"+arch)
		if err := buildBinary(name, "./cmd/communication", "linux", arch); err != nil {
			return err
		}
		outputs = append(outputs, name)
	}
	archive := filepath.Join("dist", "hermes-hub-"+version+"-source.zip")
	if err := zipSources(archive); err != nil {
		return err
	}
	outputs = append(outputs, archive)
	sort.Strings(outputs)
	checksums, err := os.Create(filepath.Join("dist", "SHA256SUMS"))
	if err != nil {
		return err
	}
	defer checksums.Close()
	for _, name := range outputs {
		body, err := os.ReadFile(name) // #nosec G703 -- output names from the package manifest
		if err != nil {
			return err
		}
		fmt.Fprintf(checksums, "%x  %s\n", sha256.Sum256(body), filepath.Base(name))
	}
	return nil
}

func build(output, pkg, goos, goarch string) error {
	cmd := exec.Command("go", "build", "-buildvcs=false", "-trimpath", "-ldflags=-s -w", "-o", output, pkg)
	cmd.Env = append(os.Environ(), "CGO_ENABLED=0", "GOOS="+goos, "GOARCH="+goarch)
	cmd.Stdout, cmd.Stderr = os.Stdout, os.Stderr
	return cmd.Run()
}

func zipSources(name string) (result error) {
	file, err := os.Create(name) // #nosec G703 -- output names from the package manifest
	if err != nil {
		return err
	}
	defer func() {
		if err := file.Close(); result == nil {
			result = err
		}
	}()
	archive := zip.NewWriter(file)
	defer func() {
		if err := archive.Close(); result == nil {
			result = err
		}
	}()
	root, err := os.OpenRoot(".")
	if err != nil {
		return err
	}
	defer root.Close()
	args := append([]string{"ls-files", "-z", "--"}, sourcePaths...)
	paths, err := exec.Command("git", args...).Output()
	if err != nil {
		return fmt.Errorf("%s: %w", locale.T(locale.Operator(), "package.sources-unavailable"), err)
	}
	for _, path := range strings.Split(strings.TrimSuffix(string(paths), "\x00"), "\x00") {
		if path == "" {
			continue
		}
		// Tracked inputs only; refuse symlinks in every component before opening.
		var info os.FileInfo
		for current := path; current != "."; current = filepath.Dir(current) {
			item, err := os.Lstat(current)
			if err != nil {
				return err
			}
			if item.Mode()&os.ModeSymlink != 0 {
				return fmt.Errorf("%s", locale.T(locale.Operator(), "package.source-invalid"))
			}
			if current == path {
				info = item
			}
		}
		if !info.Mode().IsRegular() {
			return fmt.Errorf("%s", locale.T(locale.Operator(), "package.source-invalid"))
		}
		header, err := zip.FileInfoHeader(info)
		if err != nil {
			return err
		}
		header.Name = filepath.ToSlash(path)
		header.Method = zip.Deflate
		destination, err := archive.CreateHeader(header)
		if err != nil {
			return err
		}
		source, err := root.Open(path)
		if err != nil {
			return err
		}
		opened, err := source.Stat()
		if err != nil || !os.SameFile(info, opened) {
			_ = source.Close()
			return fmt.Errorf("%s", locale.T(locale.Operator(), "package.source-invalid"))
		}
		_, copyErr := io.Copy(destination, source)
		closeErr := source.Close()
		if copyErr != nil {
			return copyErr
		}
		if closeErr != nil {
			return closeErr
		}
	}
	return nil
}
