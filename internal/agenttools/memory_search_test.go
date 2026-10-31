package agenttools

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/letya999/hermes-hub/internal/memory"
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
	// The gateway stamps the run's scope before dispatch — agent-read-only.
	if err := os.MkdirAll(filepath.Join(root, "runtime", "run-scope"), 0o755); err != nil {
		t.Fatal(err)
	}
	return root
}

func writeRunScope(t *testing.T, root, audience string) {
	t.Helper()
	body := []byte(`{"runs":{"j1":{"audience":"` + audience + `","expires":"` + time.Now().Add(time.Hour).UTC().Format(time.RFC3339) + `"}}}`)
	if err := os.WriteFile(filepath.Join(root, "runtime", "run-scope", "current"), body, 0o644); err != nil {
		t.Fatal(err)
	}
}

// R3.3/R3.5: the tool returns evidence-linked hits for the run's audience —
// a private run sees private leaves, each with file + line range.
func TestSearchToolEvidenceRefs(t *testing.T) {
	root := memoryVaultFixture(t)
	writeRunScope(t, root, "private")
	tls := &Tools{VaultDir: root}
	if _, err := tls.memorySearch(Input{Query: "earl grey"}); err != nil {
		t.Fatalf("warm: %v", err)
	}
	out, err := tls.memorySearch(Input{Query: "earl grey"})
	if err != nil {
		t.Fatalf("search: %v", err)
	}
	hits, _ := out["hits"].([]map[string]any)
	if len(hits) != 1 {
		t.Fatalf("hits %v want 1", out["hits"])
	}
	ev, _ := hits[0]["evidence"].(string)
	if ev == "" {
		t.Fatalf("hit missing evidence: %v", hits[0])
	}
	if out["status"] != "ok" {
		t.Fatalf("status %v", out["status"])
	}
}

// R3.4: a group-scope run never sees private hits — the stamp, not the
// query, confines the lane.
func TestSearchGroupScopeConfines(t *testing.T) {
	root := memoryVaultFixture(t)
	writeRunScope(t, root, "group")
	tls := &Tools{VaultDir: root}
	out, err := tls.memorySearch(Input{Query: "earl grey"})
	if err != nil {
		t.Fatal(err)
	}
	if hits, _ := out["hits"].([]map[string]any); len(hits) != 0 {
		t.Fatalf("group run saw private hits: %v", hits)
	}
}

// An expired run-scope stamp fails closed — a stale private stamp must not
// leak private hits into a later group run.
func TestSearchStaleScopeFailsClosed(t *testing.T) {
	root := memoryVaultFixture(t)
	stale := []byte(`{"runs":{"j0":{"audience":"private","expires":"2020-01-01T00:00:00Z"}}}`)
	if err := os.WriteFile(filepath.Join(root, "runtime", "run-scope", "current"), stale, 0o644); err != nil {
		t.Fatal(err)
	}
	tls := &Tools{VaultDir: root}
	out, err := tls.memorySearch(Input{Query: "earl grey"})
	if err != nil {
		t.Fatal(err)
	}
	if hits, _ := out["hits"].([]map[string]any); len(hits) != 0 {
		t.Fatalf("expired stamp leaked private hits: %v", hits)
	}
}

// A missing vault is "unavailable" — a distinct state, never an error that
// swallows the turn.
func TestSearchUnavailable(t *testing.T) {
	tls := &Tools{VaultDir: filepath.Join(t.TempDir(), "none")}
	out, err := tls.memorySearch(Input{Query: "tea"})
	if err != nil {
		t.Fatalf("unavailable must not error: %v", err)
	}
	if out["status"] != "unavailable" {
		t.Fatalf("status %v", out["status"])
	}
}

// A rebuilt index reports degraded — the self-heal is honest, not silent.
func TestSearchDegradedOnRebuild(t *testing.T) {
	root := memoryVaultFixture(t)
	writeRunScope(t, root, "private")
	tls := &Tools{VaultDir: root}
	out, err := tls.memorySearch(Input{Query: "earl grey"})
	if err != nil {
		t.Fatal(err)
	}
	if out["status"] != "degraded" {
		t.Fatalf("first-build status %v want degraded", out["status"])
	}
}

// Filters ride the input: kinds narrows, history surfaces superseded, from/to
// bound dates, limit caps results.
func TestSearchToolFilters(t *testing.T) {
	root := memoryVaultFixture(t)
	writeRunScope(t, root, "private")
	daily, _ := vault.OpenDaily(root, time.UTC)
	if err := daily.Append(memory.Entry{ID: "wake-1", Kind: memory.KindRoutine,
		Provenance: memory.ProvenanceAgent, Audience: memory.AudiencePrivate,
		Body: "routine wake checking the earl grey watch", ObservedAt: time.Date(2026, 4, 19, 3, 0, 0, 0, time.UTC)}, "system"); err != nil {
		t.Fatal(err)
	}
	tls := &Tools{VaultDir: root}
	out, err := tls.memorySearch(Input{Query: "earl grey", Kinds: []string{"interactive"}})
	if err != nil {
		t.Fatal(err)
	}
	for _, h := range out["hits"].([]map[string]any) {
		if h["kind"] == "routine" {
			t.Fatalf("kinds filter leaked routine hit: %v", h)
		}
	}
	// limit caps
	out, err = tls.memorySearch(Input{Query: "earl grey", Limit: 1})
	if err != nil {
		t.Fatal(err)
	}
	if n := len(out["hits"].([]map[string]any)); n != 1 {
		t.Fatalf("limit: %d hits", n)
	}
}
