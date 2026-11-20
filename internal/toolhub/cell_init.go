package toolhub

import (
	"fmt"
	"io"
	"os"
	"path/filepath"

	"github.com/letya999/hermes-hub/internal/toolhub/api"
)

// The cellinit seed stays in ToolHub: the MCP workload path still mounts
// the helper into its cred-proxy companion (S6 moved the CLI-cell side of
// this to spawn-controller's Cells.cellInitSeed).
//
// cellInitHost returns the state-root copy of cellinit — the source the
// daemon resolves when a sibling bind mounts the helper.
func (c *genericController) cellInitHost() string {
	return api.DockerBindSource(filepath.Join(c.config.StateRoot, "cli-cellinit"))
}

// cellInitSeed copies the shipped cellinit into the state root so a sibling
// bind mount can source it. Re-seeded on every controller start so an
// upgraded binary replaces any stale or tampered copy.
func (c *genericController) cellInitSeed() error {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.cellInitOK {
		return nil
	}
	src, err := os.Open(c.config.CLI.CellInit)
	if err != nil {
		return fmt.Errorf("%w: cellinit binary: %v", api.ErrIsolation, err)
	}
	defer src.Close()
	info, err := src.Stat()
	if err != nil || !info.Mode().IsRegular() {
		return fmt.Errorf("%w: cellinit is not a regular file", api.ErrIsolation)
	}
	tmp, err := os.CreateTemp(c.config.StateRoot, ".cellinit-*")
	if err != nil {
		return err
	}
	tmpName := tmp.Name()
	defer os.Remove(tmpName)
	if _, err = io.Copy(tmp, io.LimitReader(src, 64<<20)); err != nil {
		_ = tmp.Close()
		return err
	}
	if err = tmp.Close(); err != nil {
		return err
	}
	if err = os.Chmod(tmpName, 0755); err != nil {
		return err
	}
	if err = os.Rename(tmpName, filepath.Join(c.config.StateRoot, "cli-cellinit")); err != nil {
		return err
	}
	c.cellInitOK = true
	return nil
}
