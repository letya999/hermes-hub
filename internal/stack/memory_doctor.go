package stack

import (
	"os"
	"path/filepath"

	memorysearch "github.com/letya999/hermes-hub/internal/memory/search"
)

// MemoryDoctor reports memory-vault index freshness for the operator.
// Read-only advisory — a stale index self-heals on the next query, the line
// just names it (SPEC-0085 R3.6).
func MemoryDoctor(dir string) ([]string, error) {
	root := filepath.Join(dir, "vault")
	if info, err := os.Stat(root); err != nil || !info.IsDir() {
		return nil, nil // feature off or vault not initialized — nothing to report
	}
	idxr := memorysearch.NewIndexer(root)
	stale, err := idxr.Stale()
	if err != nil {
		return []string{"Memory index: " + err.Error()}, nil
	}
	if stale {
		return []string{"Memory index: stale — run hubctl memory search --reindex"}, nil
	}
	if _, rebuilt, err := idxr.Ensure(); err == nil && !rebuilt {
		return []string{"Memory index: fresh"}, nil
	}
	return []string{"Memory index: fresh"}, nil
}
