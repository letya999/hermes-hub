package vault

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"github.com/letya999/hermes-hub/internal/memory"
)

// Writer is the vault's only governed-write path (SPEC-0085 R1.4): every
// mutation is hash-checked against the on-disk content. Owner edits on disk
// always win — a stale WantSHA rejects the write instead of overwriting them.
type Writer struct {
	root string
}

// NewWriter returns the governed writer for the vault at root.
func NewWriter(root string) *Writer { return &Writer{root: root} }

// writeZones is the whitelist of subtrees the writer may mutate. Anything
// else — schema.json, history, sidecars, indexes, attachments — is refused.
var writeZones = []string{
	"memory/daily/",
	"memory/cards/",
	"memory/summaries/",
	"memory/CORE.md",
	"memory/REVIEW.md",
}

// Apply runs one governed write: confine → zone → hash-check → write →
// commit. A hash mismatch returns memory.ErrStaleHash and leaves the disk
// untouched.
func (w *Writer) Apply(op memory.WriteOp) (memory.WriteResult, error) {
	target, err := w.confine(op.Path)
	if err != nil {
		return memory.WriteResult{}, err
	}
	if !w.zoneAllowed(op.Path) {
		return memory.WriteResult{}, fmt.Errorf("vault: path %q is outside the governed zones", op.Path)
	}
	current, err := os.ReadFile(target)
	currentSHA := ""
	if errors.Is(err, os.ErrNotExist) {
		current = nil
	} else if err != nil {
		return memory.WriteResult{}, err
	} else {
		currentSHA = sha256Hex(current)
	}
	if currentSHA != op.WantSHA {
		w.flagPending(op)
		return memory.WriteResult{}, memory.ErrStaleHash
	}
	if err := os.MkdirAll(filepath.Dir(target), 0700); err != nil {
		return memory.WriteResult{}, err
	}
	if err := os.WriteFile(target, op.Content, 0600); err != nil {
		return memory.WriteResult{}, err
	}
	if err := w.commit(op); err != nil {
		return memory.WriteResult{}, err
	}
	return memory.WriteResult{SHA256: sha256Hex(op.Content)}, nil
}

// confine resolves op.Path strictly below the vault root and rejects any
// symlink component — the scope.yaml path rule applied to memory writes.
func (w *Writer) confine(rel string) (string, error) {
	clean := filepath.Clean(rel)
	if filepath.IsAbs(clean) || clean == ".." || strings.HasPrefix(clean, ".."+string(filepath.Separator)) {
		return "", fmt.Errorf("vault: path %q escapes the vault root", rel)
	}
	target := filepath.Join(w.root, clean)
	if err := noSymlinkPath(target); err != nil {
		return "", err
	}
	return target, nil
}

func (w *Writer) zoneAllowed(rel string) bool {
	normalized := filepath.ToSlash(filepath.Clean(rel))
	for _, zone := range writeZones {
		if strings.HasSuffix(zone, "/") {
			if strings.HasPrefix(normalized, zone) {
				return true
			}
		} else if normalized == zone {
			return true
		}
	}
	return false
}

// commit lands the write as a git commit authored by its actor class. The add
// is scoped to the written path, so runtime sidecars and journals can never
// be swept into vault history.
func (w *Writer) commit(op memory.WriteOp) error {
	actor := "hermes-" + string(op.Actor)
	message := "vault: " + string(op.Actor) + " write " + op.Path
	for _, args := range [][]string{
		{"add", "--", op.Path},
		{"-c", "user.name=" + actor, "-c", "user.email=" + string(op.Actor) + "@vault.local", "commit", "--quiet", "-m", message},
	} {
		cmd := exec.Command("git", args...)
		cmd.Dir = w.root
		if out, err := cmd.CombinedOutput(); err != nil {
			return fmt.Errorf("vault git %s: %w (%s)", args[0], err, out)
		}
	}
	return nil
}

// flagPending records a lost write in the writer-owned sidecar
// runtime/memory/truth_pending.jsonl (ADR-0086): the on-disk owner version
// stands, and the rejected proposal is queued for review instead of touching
// user markdown.
func (w *Writer) flagPending(op memory.WriteOp) {
	row, err := json.Marshal(map[string]any{"path": op.Path, "status": "truth_pending"})
	if err != nil {
		return
	}
	path := filepath.Join(w.root, "runtime", "memory", "truth_pending.jsonl")
	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		return
	}
	f, err := os.OpenFile(path, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0600)
	if err != nil {
		return
	}
	defer f.Close()
	_, _ = fmt.Fprintf(f, "%s\n", row)
}

func sha256Hex(b []byte) string {
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:])
}
