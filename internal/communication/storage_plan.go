package communication

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strconv"

	"github.com/letya999/hermes-hub/internal/storagebudget"
)

// ErrStalePlan rejects an apply whose recomputed candidate set no longer
// matches the dry-run digest — content drifted or the digest belongs to a
// foreign scope.
var ErrStalePlan = errors.New("stale storage plan")

// StoragePlan is the dry-run view of terminal compaction: a digest binding
// the owner scope and every owned entry apply would see. It reports counts
// and hashes only — never names.
type StoragePlan struct {
	Scope  string `json:"scope"`
	Digest string `json:"digest"`
	Files  int    `json:"files"`
	Bytes  int64  `json:"bytes"`
}

// StoragePlan fingerprints every owned entry (path, size) under the spool
// root and binds it with the scope into one digest. Symlinks fail closed
// the same way the writer path refuses them — a plan never blesses a path
// apply could not trust.
func (s *Spool) StoragePlan() (StoragePlan, error) {
	plan := StoragePlan{Scope: "communication-spool"}
	h := sha256.New()
	h.Write([]byte(plan.Scope))
	h.Write([]byte{0})
	var entries []string
	err := filepath.WalkDir(s.root, func(path string, d fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if path == s.root {
			return nil
		}
		rel, err := filepath.Rel(s.root, path)
		if err != nil {
			return err
		}
		if d.Type()&os.ModeSymlink != 0 {
			return storagebudget.ErrUnsafe
		}
		if d.IsDir() {
			return nil
		}
		// The writer lock is part of the apply protocol itself, not a
		// compaction candidate — holding it must not invalidate the plan.
		if rel == ".writer.lock" {
			return nil
		}
		info, err := d.Info()
		if err != nil {
			return err
		}
		plan.Files++
		plan.Bytes += info.Size()
		entries = append(entries, rel+":"+strconv.FormatInt(info.Size(), 10))
		return nil
	})
	if err != nil {
		return StoragePlan{}, err
	}
	sort.Strings(entries)
	for _, entry := range entries {
		h.Write([]byte(entry))
		h.Write([]byte{0})
	}
	plan.Digest = "sha256:" + hex.EncodeToString(h.Sum(nil))
	return plan, nil
}
