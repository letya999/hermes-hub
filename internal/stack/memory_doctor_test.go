package stack

import (
	"path/filepath"
	"testing"
	"time"

	"github.com/letya999/hermes-hub/internal/memory"
	memorysearch "github.com/letya999/hermes-hub/internal/memory/search"
	"github.com/letya999/hermes-hub/internal/vault"
)

// R3.6: doctor reports a stale index — sources that changed after the last
// persisted build are named, never silently served.
func TestDoctorStaleIndex(t *testing.T) {
	dir := t.TempDir()
	root := filepath.Join(dir, "vault")
	if err := vault.Ensure(root); err != nil {
		t.Fatal(err)
	}
	daily, err := vault.OpenDaily(root, time.UTC)
	if err != nil {
		t.Fatal(err)
	}
	appendEntry := func(id, body string) {
		t.Helper()
		if err := daily.Append(memory.Entry{ID: id, Kind: memory.KindInteractive,
			Provenance: memory.ProvenanceOwner, Audience: memory.AudiencePrivate,
			Body: body, ObservedAt: time.Now()}, "telegram"); err != nil {
			t.Fatal(err)
		}
	}
	appendEntry("e1", "earl grey decision")
	if _, _, err := memorysearch.NewIndexer(root).Ensure(); err != nil {
		t.Fatal(err)
	}
	lines, err := MemoryDoctor(dir)
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, l := range lines {
		if l == "Memory index: fresh" || l == "Memory index: fresh (1 docs)" {
			found = true
		}
	}
	if !found {
		t.Fatalf("fresh index not reported: %v", lines)
	}
	appendEntry("e2", "oolong follow-up")
	lines, _ = MemoryDoctor(dir)
	found = false
	for _, l := range lines {
		if l == "Memory index: stale — run hubctl memory search --reindex" {
			found = true
		}
	}
	if !found {
		t.Fatalf("stale index not reported: %v", lines)
	}
}
