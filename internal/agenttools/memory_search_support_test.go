package agenttools

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/letya999/hermes-hub/internal/memory"
	memorysearch "github.com/letya999/hermes-hub/internal/memory/search"
	"github.com/letya999/hermes-hub/internal/vault"
)

func memoryVaultFixture(t *testing.T) string {
	t.Helper()
	root := t.TempDir()
	if err := vault.Ensure(root); err != nil {
		t.Fatalf("ensure: %v", err)
	}
	daily, err := vault.OpenDaily(root, time.UTC)
	if err != nil {
		t.Fatal(err)
	}
	at := time.Date(2026, 4, 19, 15, 0, 0, 0, time.UTC)
	for _, e := range []memory.Entry{
		{ID: "turn-1", Kind: memory.KindInteractive, Provenance: memory.ProvenanceOwner, Audience: memory.AudiencePrivate, Body: "we picked the earl grey supplier", ObservedAt: at},
		{ID: "turn-2", Kind: memory.KindInteractive, Provenance: memory.ProvenanceOwner, Audience: memory.AudienceGroup, Body: "group chat about weekend plans", ObservedAt: at},
	} {
		if err := daily.Append(e, "telegram"); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.MkdirAll(filepath.Join(root, "runtime", "run-scope"), 0o755); err != nil {
		t.Fatal(err)
	}
	return root
}

// testSearch answers the card tests' search assertions straight through the
// shared indexer: #515 moved the cataloged memory_search inside ToolHub, so
// nothing in-runtime may serve it — the tests only need the index view.
func (t *Tools) testSearch(r Input) (map[string]any, error) {
	idx, rebuilt, err := memorysearch.NewIndexer(t.VaultDir).Ensure()
	if err != nil {
		return map[string]any{"status": "unavailable", "hits": []map[string]any{}}, nil
	}
	limit := r.Limit
	if limit <= 0 || limit > 50 {
		limit = 10
	}
	hits := idx.Search(strings.TrimSpace(r.Query), memorysearch.Options{
		Audience: string(memory.AudiencePrivate), Kinds: r.Kinds, From: r.From, To: r.To,
		History: r.History, Limit: limit, Now: time.Now(),
	})
	out := []map[string]any{}
	for _, h := range hits {
		out = append(out, map[string]any{"id": h.Doc.ID, "evidence": h.Evidence, "date": h.Doc.Date,
			"kind": h.Doc.Kind, "score": h.Score, "untrusted": h.Untrusted})
	}
	status := "ok"
	if rebuilt {
		status = "degraded"
	}
	return map[string]any{"status": status, "hits": out}, nil
}
