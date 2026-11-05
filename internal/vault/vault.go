// Package vault owns the git-backed memory tree at spaces/<id>/vault
// (ADR-0086 D1): a fixed scaffold of human-readable Markdown under memory/,
// one governed writer, and path rules identical to scope.yaml — every path
// resolves below the vault root and no symlink component is ever followed.
package vault

import (
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
)

// scaffoldDirs is the fixed vault tree (SPEC-0085 R1.1). The list is a
// whitelist: nothing else is ever created, which is what makes secret-named
// files (*.env, *.auth, key material) impossible in the scaffold by
// construction.
var scaffoldDirs = []string{
	"memory/daily",
	"memory/cards/contacts", "memory/cards/projects", "memory/cards/decisions",
	"memory/cards/ideas", "memory/cards/notes",
	"memory/summaries/daily", "memory/summaries/weekly",
	"memory/summaries/monthly", "memory/summaries/yearly",
	"memory/.graph",
	"memory/attachments",
}

var scaffoldFiles = map[string]string{
	"memory/CORE.md":         "",
	"memory/CORE.history.md": "",
	"memory/schema.json":     cardSchema + "\n",
	".gitignore":             "",
	// OpenKnowledge watches the vault in M20.8+: .git internals, the derived
	// graph, OK's own state and runtime sidecars are never external content
	// (ADR-0086 D8 spike).
	".okignore": ".git/\n.graph/\n.ok/\nruntime/\n",
}

// Ensure materializes the vault scaffold under root. It creates only missing
// entries — existing files are never opened for write — so it is safe to run
// on every init/render.
func Ensure(root string) error {
	for _, part := range strings.FieldsFunc(root, func(r rune) bool { return r == '/' || r == '\\' }) {
		if part == ".." {
			return errors.New("vault: path traversal is not allowed")
		}
	}
	if err := noSymlinkPath(root); err != nil {
		return err
	}
	if err := os.MkdirAll(root, 0700); err != nil {
		return err
	}
	for _, dir := range scaffoldDirs {
		if err := os.MkdirAll(filepath.Join(root, dir), 0700); err != nil {
			return err
		}
	}
	for name, body := range scaffoldFiles {
		path := filepath.Join(root, name)
		if _, err := os.Lstat(path); err == nil {
			continue // create-only: an existing entry — file or symlink — wins.
		} else if !errors.Is(err, os.ErrNotExist) {
			return err
		}
		if err := os.WriteFile(path, []byte(body), 0600); err != nil {
			return err
		}
	}
	return ensureGit(root)
}

// gitCmd builds a git invocation pinned to the vault root. The governed
// writer owns this repository by contract (ADR-0086 D1) but may run under a
// fixed container uid while the bind-mounted checkout stays host-owned: git's
// dubious-ownership guard would then refuse every operation, so each call
// carries -c safe.directory=<abs root>. The slash form keeps the pin valid on
// Windows checkouts too.
func gitCmd(root string, args ...string) *exec.Cmd {
	abs, err := filepath.Abs(root)
	if err != nil {
		abs = root
	}
	full := append([]string{"-c", "safe.directory=" + filepath.ToSlash(abs)}, args...)
	cmd := exec.Command("git", full...)
	cmd.Dir = root
	return cmd
}

// ensureGit initializes the vault repository and records the scaffold commit
// on first run. An existing .git is left alone — history is never reset.
func ensureGit(root string) error {
	if _, err := os.Stat(filepath.Join(root, ".git")); err == nil {
		return nil
	} else if !errors.Is(err, os.ErrNotExist) {
		return err
	}
	for _, args := range [][]string{
		{"init", "--quiet"},
		{"add", "-A"},
		{"-c", "user.name=hermes-vault", "-c", "user.email=vault@localhost", "commit", "--quiet", "-m", "vault: scaffold"},
	} {
		cmd := gitCmd(root, args...)
		if out, err := cmd.CombinedOutput(); err != nil {
			return fmt.Errorf("vault git %s: %w (%s)", args[0], err, out)
		}
	}
	return nil
}

// noSymlinkPath rejects any symlink component along path, same rule as
// scope.yaml: the vault never follows links out of its root.
func noSymlinkPath(path string) error {
	abs, err := filepath.Abs(path)
	if err != nil {
		return err
	}
	volume := filepath.VolumeName(abs)
	current := volume + string(filepath.Separator)
	for _, part := range strings.Split(strings.TrimPrefix(abs, current), string(filepath.Separator)) {
		if part == "" {
			continue
		}
		current = filepath.Join(current, part)
		info, err := os.Lstat(current) // #nosec G703 -- walk over the vault root
		if errors.Is(err, os.ErrNotExist) {
			continue
		}
		if err != nil {
			return err
		}
		if info.Mode()&os.ModeSymlink != 0 {
			return fmt.Errorf("vault: symlink path component is not allowed: %s", current)
		}
	}
	return nil
}
