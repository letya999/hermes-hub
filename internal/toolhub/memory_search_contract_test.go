package toolhub

import (
	"path/filepath"
	"testing"
	"time"

	"github.com/letya999/hermes-hub/internal/identity"
	"github.com/letya999/hermes-hub/internal/memory"
	"github.com/letya999/hermes-hub/internal/vault"
)

// searchVaultFixture materializes a real enrolled vault with one private
// leaf — the #515 handler's proof needs a hit the indexer can evidence.
func searchVaultFixture(t *testing.T, vaults, passports, user, environment string) {
	t.Helper()
	writeMemoryPassportEnv(t, passports, user, environment, true)
	dir := filepath.Join(vaults, user)
	if err := vault.Ensure(dir); err != nil {
		t.Fatalf("ensure: %v", err)
	}
	daily, err := vault.OpenDaily(dir, time.UTC)
	if err != nil {
		t.Fatal(err)
	}
	entry := memory.Entry{ID: "turn-1", Kind: memory.KindInteractive, Provenance: memory.ProvenanceOwner,
		Audience: memory.AudiencePrivate, Body: "we picked the earl grey supplier", ObservedAt: time.Now()}
	if err := daily.Append(entry, "telegram"); err != nil {
		t.Fatal(err)
	}
}

// R3.3/R3.5 on the #515 path: the ToolHub-owned memory_search returns
// evidence-linked private hits with the same contract the in-runtime
// handler served — file:line refs, honest status, untrusted flag.
func TestSearchToolEvidenceRefs(t *testing.T) {
	vaults, passports := t.TempDir(), t.TempDir()
	searchVaultFixture(t, vaults, passports, "alice", "dev")
	resolver, err := NewVaultResolver(vaults, passports)
	if err != nil {
		t.Fatal(err)
	}
	g := &Gateway{Vaults: resolver}
	auth := identity.Envelope{PrincipalID: "alice", Environment: "dev"}
	if _, err := g.memorySearch(auth, map[string]any{"query": "earl grey"}); err != nil {
		t.Fatalf("warm: %v", err)
	}
	out, err := g.memorySearch(auth, map[string]any{"query": "earl grey"})
	if err != nil {
		t.Fatalf("search: %v", err)
	}
	hits, _ := out["hits"].([]map[string]any)
	if len(hits) != 1 {
		t.Fatalf("hits %v want 1", out["hits"])
	}
	if ev, _ := hits[0]["evidence"].(string); ev == "" {
		t.Fatalf("hit missing evidence: %v", hits[0])
	}
	if _, present := hits[0]["untrusted"]; !present {
		t.Fatalf("hit missing untrusted flag: %v", hits[0])
	}
	if out["status"] != "ok" {
		t.Fatalf("status %v", out["status"])
	}
}
