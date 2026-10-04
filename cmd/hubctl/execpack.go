package main

import (
	"archive/tar"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path"
	"strings"

	"github.com/letya999/hermes-hub/internal/agenttools"
	"github.com/letya999/hermes-hub/internal/toolhub"
)

// runExecPack is the private runtime-side input materializer for the
// disposable scratch executor. It reads one ScratchPackRequest, narrows the
// workspace root to the admitted scope and streams a bounded tar of the exact
// admitted files — regular files only, no symlinks, capped count and size.
// The command accepts no authority fields and produces no workspace writes.
func runExecPack(ctx context.Context) error {
	var request toolhub.ScratchPackRequest
	if err := json.NewDecoder(io.LimitReader(os.Stdin, toolsExecMaxRequest)).Decode(&request); err != nil {
		return fmt.Errorf("exec-pack request: %w", err)
	}
	if !validPackPath(request.Path) {
		return errors.New("exec-pack: invalid path")
	}
	workspace := os.Getenv("HUB_WORKSPACE")
	if workspace == "" {
		workspace = "/workspace"
	}
	t, err := agenttools.OpenRoots(workspace, "", "")
	if err != nil {
		return err
	}
	defer t.Close()
	base := t.Workspace
	rel := request.Path
	for _, scope := range request.Scopes {
		if scope.Resource != "files" || scope.PathArgument != "path" || scope.PathPrefix == "" {
			continue
		}
		// The admitted boundary must contain the requested path; a scope is
		// never widened into a sibling or parent.
		if request.Path != scope.PathPrefix && !strings.HasPrefix(request.Path, scope.PathPrefix+"/") {
			return fmt.Errorf("exec-pack: path %q outside scope %q", request.Path, scope.PathPrefix)
		}
		sub, narrowed, err := agenttools.ScopedSub(base, scope.PathPrefix, request.Path)
		if err != nil {
			return fmt.Errorf("exec-pack scope %q: %w", scope.PathPrefix, err)
		}
		defer sub.Close()
		base, rel = sub, narrowed
	}
	if rel == "" {
		rel = "."
	}
	info, err := base.Lstat(rel)
	if err != nil {
		return fmt.Errorf("exec-pack path: %w", err)
	}
	writer := tar.NewWriter(os.Stdout)
	total := int64(0)
	seen := 0
	add := func(name, target string, info fs.FileInfo) error {
		if !info.Mode().IsRegular() || info.Size() > scratchPackFileMax {
			return nil
		}
		if seen++; seen > scratchPackMaxFiles {
			return errors.New("exec-pack: too many files")
		}
		if total += info.Size(); total > scratchPackMaxTotal {
			return errors.New("exec-pack: input budget exceeded")
		}
		file, err := base.Open(target)
		if err != nil {
			return err
		}
		defer file.Close()
		if err := writer.WriteHeader(&tar.Header{Name: name, Mode: 0o600, Size: info.Size(), Typeflag: tar.TypeReg}); err != nil {
			return err
		}
		_, err = io.CopyN(writer, file, info.Size())
		return err
	}
	if !info.IsDir() {
		if err := add(path.Base(rel), rel, info); err != nil {
			return err
		}
	} else {
		err := fs.WalkDir(base.FS(), rel, func(p string, entry fs.DirEntry, err error) error {
			if err != nil || ctx.Err() != nil {
				return err
			}
			if entry.Type()&fs.ModeSymlink != 0 {
				if entry.IsDir() {
					return fs.SkipDir
				}
				return nil
			}
			if p == rel || !entry.Type().IsRegular() {
				return nil
			}
			info, err := entry.Info()
			if err != nil {
				return err
			}
			return add(strings.TrimPrefix(p, rel+"/"), p, info)
		})
		if err != nil {
			return err
		}
	}
	return writer.Close()
}

const (
	scratchPackFileMax  = 2 << 20
	scratchPackMaxFiles = 256
	scratchPackMaxTotal = 8 << 20
)

func validPackPath(p string) bool {
	return p == "" || (!strings.HasPrefix(p, "/") && !strings.Contains(p, "..") && !strings.ContainsAny(p, "\\\x00"))
}
