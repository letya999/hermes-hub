package main

import (
	"archive/zip"
	"crypto/sha256"
	"fmt"
	"io"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
)

var sourcePaths = []string{".github", ".work", "cmd", "config", "docker", "docs", "internal", "specs", ".dockerignore", ".gitignore", "AGENTS.md", "CHANGELOG.md", "CODE_OF_CONDUCT.md", "CONTRIBUTING.md", "LICENSE", "NOTICE", "README.md", "SECURITY.md", "SETUP.md", "START_HERE.ru.md", "THIRD_PARTY.md", "THIRD_PARTY_LICENSES.txt", "go.mod", "go.sum", "justfile"}
var buildBinary = build

func main() {
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
		body, err := os.ReadFile(name)
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
	file, err := os.Create(name)
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
	for _, root := range sourcePaths {
		err := filepath.WalkDir(root, func(path string, entry fs.DirEntry, err error) error {
			if err != nil {
				return err
			}
			if entry.IsDir() {
				return nil
			}
			info, err := entry.Info()
			if err != nil {
				return err
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
			source, err := os.Open(path)
			if err != nil {
				return err
			}
			_, copyErr := io.Copy(destination, source)
			closeErr := source.Close()
			if copyErr != nil {
				return copyErr
			}
			return closeErr
		})
		if err != nil {
			return err
		}
	}
	return nil
}
