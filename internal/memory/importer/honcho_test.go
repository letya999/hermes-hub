package importer

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// honchoExport is the verified Conclusion wire shape at the upstream v3 API
// (POST /v3/workspaces/{ws}/conclusions/list → Page[Conclusion].items):
// {id, content, observer_id, observed_id, session_id, level, source_ids,
// source_message_ids, times_derived, created_at}. The importer also accepts
// a bare array — what `honcho conclusion list --json` emits piped.
const honchoExport = `{"items":[
 {"id":"c1","content":"prefers earl grey tea","observer_id":"agent","observed_id":"alice",
  "session_id":"s1","level":"explicit","source_ids":[],"source_message_ids":["m1"],
  "times_derived":1,"created_at":"2026-10-01T10:00:00Z"},
 {"id":"c2","content":"lives in Oslo","observer_id":"agent","observed_id":"alice",
  "session_id":"s1","level":"deductive","source_ids":["c9"],"times_derived":2,
  "created_at":"2026-10-02T10:00:00Z"},
 {"id":"c3","content":"allergic to peanuts","observer_id":"agent","observed_id":"bob",
  "level":"explicit","times_derived":1,"created_at":"2026-10-03T10:00:00Z"}
]}`

// R11.5: a Honcho export becomes reviewable card drafts grouped per
// observed peer — staged outside the governed zone until an explicit Apply.
func TestHonchoImportReviewable(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "export.json")
	if err := os.WriteFile(path, []byte(honchoExport), 0o600); err != nil {
		t.Fatal(err)
	}
	rep, err := ParseHoncho(path)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if len(rep.Cards) != 2 {
		t.Fatalf("cards=%d want 2 (one per observed peer)", len(rep.Cards))
	}
	var alice, bob *CardDraft
	for i := range rep.Cards {
		switch rep.Cards[i].Name {
		case "honcho-alice":
			alice = &rep.Cards[i]
		case "honcho-bob":
			bob = &rep.Cards[i]
		}
	}
	if alice == nil || bob == nil {
		t.Fatalf("expected honcho-alice/honcho-bob cards: %+v", rep.Cards)
	}
	if len(alice.Facts) != 2 || len(bob.Facts) != 1 {
		t.Fatalf("facts drifted: alice=%v bob=%v", alice.Facts, bob.Facts)
	}
	// The derived level rides the fact so review sees what Honcho inferred
	// vs what a peer stated.
	if !strings.Contains(alice.Facts[1], "deductive") {
		t.Fatalf("deductive level lost: %v", alice.Facts)
	}
	// Staging lands nothing governed — only the review staging area.
	vaultDir := filepath.Join(dir, "vault")
	if err := os.MkdirAll(vaultDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := Stage(vaultDir, rep); err != nil {
		t.Fatalf("stage: %v", err)
	}
	staged, err := Staged(vaultDir)
	if err != nil || len(staged) != 2 {
		t.Fatalf("staged=%v err=%v", staged, err)
	}
	body, err := os.ReadFile(filepath.Join(vaultDir, "runtime", "memory", "import", "honcho-alice.md"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(body), "source: migrated") || !strings.Contains(string(body), "[src: honcho]") {
		t.Fatalf("staged card lost provenance: %s", body)
	}
}

// R11.5: the import is additive — re-parsing produces the same slugs and
// the bare array form (`honcho conclusion list --json` piped) parses the
// same; duplicate contents collapse so a re-export cannot double a fact.
func TestHonchoImportIdempotent(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "export.json")
	if err := os.WriteFile(path, []byte(`[
	  {"id":"c1","content":"fact one","observer_id":"a","observed_id":"alice","level":"explicit","times_derived":1,"created_at":"2026-10-01T10:00:00Z"},
	  {"id":"c2","content":"fact one","observer_id":"a","observed_id":"alice","level":"explicit","times_derived":2,"created_at":"2026-10-02T10:00:00Z"}
	]`), 0o600); err != nil {
		t.Fatal(err)
	}
	r1, err := ParseHoncho(path)
	if err != nil {
		t.Fatal(err)
	}
	r2, err := ParseHoncho(path)
	if err != nil {
		t.Fatal(err)
	}
	if len(r1.Cards) != 1 || len(r2.Cards) != 1 || r1.Cards[0].Name != r2.Cards[0].Name {
		t.Fatalf("non-deterministic slugs: %v vs %v", r1.Cards, r2.Cards)
	}
	if len(r1.Cards[0].Facts) != 1 {
		t.Fatalf("duplicate conclusion content must collapse: %v", r1.Cards[0].Facts)
	}
}
