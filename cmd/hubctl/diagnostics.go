package main

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

// exportDiagnostics snapshots the retained Docker logs for every Hermes
// container into one local, Git-ignored text file. The source logs stay in
// Docker's bounded local log store.
func exportDiagnostics(ctx context.Context, root string, run func(context.Context, ...string) ([]byte, error)) (string, error) {
	listed, err := run(ctx, "ps", "-a", "--format", "{{.Names}}")
	if err != nil {
		return "", fmt.Errorf("list Docker containers: %w", err)
	}
	var names []string
	for _, name := range strings.Fields(string(listed)) {
		if strings.HasPrefix(name, "hermes-") {
			names = append(names, name)
		}
	}
	sort.Strings(names)
	if len(names) == 0 {
		return "", fmt.Errorf("no Hermes containers found")
	}
	path := filepath.Join(root, ".local", "hermes-diagnostics.txt")
	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		return "", err
	}
	f, err := os.CreateTemp(filepath.Dir(path), "diagnostics-*.tmp")
	if err != nil {
		return "", err
	}
	defer os.Remove(f.Name())
	defer f.Close()
	if err := f.Chmod(0600); err != nil {
		return "", err
	}
	if _, err := fmt.Fprintf(f, "Hermes diagnostic snapshot %s (last 1000 lines per container)\n", time.Now().UTC().Format(time.RFC3339)); err != nil {
		return "", err
	}
	for _, name := range names {
		body, err := run(ctx, "logs", "--timestamps", "--tail", "1000", name)
		if err != nil {
			return "", fmt.Errorf("docker logs %s: %w", name, err)
		}
		if _, err := fmt.Fprintf(f, "\n===== %s =====\n%s\n", name, body); err != nil {
			return "", err
		}
	}
	if err := f.Close(); err != nil {
		return "", err
	}
	// Windows cannot atomically replace an existing destination with os.Rename.
	if err := os.Remove(path); err != nil && !os.IsNotExist(err) {
		return "", err
	}
	if err := os.Rename(f.Name(), path); err != nil {
		return "", err
	}
	return path, nil
}
