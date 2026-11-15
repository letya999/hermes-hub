package eval

import (
	"context"
	"errors"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/letya999/hermes-hub/internal/memory"
	memorysearch "github.com/letya999/hermes-hub/internal/memory/search"
	"github.com/letya999/hermes-hub/internal/vault"
)

// R7.2: the seeded fixture is the wave-1 gate — recall@4 over the scripted
// private interactive turns must reach 0.9, deterministically.
func TestEvalRecallBound(t *testing.T) {
	root := t.TempDir()
	res, err := Run(root)
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	if res.RecallAt4 < 0.9 {
		t.Fatalf("recall@4 = %.2f, want >= 0.9 (misses: %v)", res.RecallAt4, res.Misses)
	}
}

// R7.2: same seed → same assertions. Two runs in independent roots must
// produce identical metrics and identical search hit ordering.
func TestEvalFixtureDeterministic(t *testing.T) {
	a, err := Run(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	b, err := Run(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if a.RecallAt4 != b.RecallAt4 {
		t.Fatalf("recall@4 diverged: %v vs %v", a.RecallAt4, b.RecallAt4)
	}
	if len(a.Misses) != len(b.Misses) {
		t.Fatalf("miss sets diverged: %v vs %v", a.Misses, b.Misses)
	}
	for i := range a.Misses {
		if a.Misses[i] != b.Misses[i] {
			t.Fatalf("miss sets diverged: %v vs %v", a.Misses, b.Misses)
		}
	}
}

// R7.2: the fixture runs inside its own root — a neighbouring vault's index
// sees none of the fixture content.
func TestEvalIsolation(t *testing.T) {
	parent := t.TempDir()
	if _, err := Run(filepath.Join(parent, "fixture")); err != nil {
		t.Fatal(err)
	}
	// A second vault in the same parent must not observe fixture tokens.
	other := filepath.Join(parent, "production")
	if err := vault.Ensure(other); err != nil {
		t.Fatal(err)
	}
	daily, err := vault.OpenDaily(other, time.UTC)
	if err != nil {
		t.Fatal(err)
	}
	if err := daily.Append(memory.Entry{
		ID: "prod-1", Kind: memory.KindInteractive, Provenance: memory.ProvenanceOwner,
		Audience: memory.AudiencePrivate, Body: "unrelated owner note",
		ObservedAt: time.Date(2026, 4, 9, 9, 0, 0, 0, time.UTC),
	}, "test"); err != nil {
		t.Fatal(err)
	}
	idx, _, err := memorysearch.NewIndexer(other).Ensure()
	if err != nil {
		t.Fatal(err)
	}
	if hits := idx.Search("kepler marzipan tugboat", memorysearch.Options{Audience: "private", Limit: 8}); len(hits) != 0 {
		t.Fatalf("fixture tokens leaked into neighbouring index: %v", hits)
	}
}

// R7.2 forget persistence: a struck fixture entry is silent in normal search
// after a fresh index build (restart) yet still visible via history:true.
func TestEvalForgetSilence(t *testing.T) {
	root := t.TempDir()
	if _, err := Run(root); err != nil {
		t.Fatal(err)
	}
	if err := memory.OpenForgetSet(filepath.Join(root, "runtime", "memory")).Strike("eval-03"); err != nil {
		t.Fatal(err)
	}
	// Fresh indexer = process restart — the strike must survive it.
	idx, rebuilt, err := memorysearch.NewIndexer(root).Ensure()
	if err != nil {
		t.Fatal(err)
	}
	if !rebuilt {
		t.Fatal("strike must invalidate the index")
	}
	for _, h := range idx.Search("tugboat", memorysearch.Options{Audience: "private", Limit: 8}) {
		if h.Doc.ID == "eval-03" {
			t.Fatal("struck entry still searchable")
		}
	}
	found := false
	for _, h := range idx.Search("tugboat", memorysearch.Options{Audience: "private", Limit: 8, History: true}) {
		if h.Doc.ID == "eval-03" {
			found = true
		}
	}
	if !found {
		t.Fatal("struck entry missing from history:true search")
	}
}

// R7.2: a routine-kind scripted turn is captured for audit in prov.db but is
// structurally promotion-ineligible — background work can never lift into
// curated layers.
func TestEvalBackgroundExcluded(t *testing.T) {
	root := t.TempDir()
	if _, err := Run(root); err != nil {
		t.Fatal(err)
	}
	prov, err := memory.OpenProvDB(filepath.Join(root, "runtime", "memory"))
	if err != nil {
		t.Fatal(err)
	}
	entries, err := prov.Entries()
	if err != nil {
		t.Fatal(err)
	}
	var bg *memory.Entry
	for i := range entries {
		if entries[i].ID == "eval-b1" {
			bg = &entries[i]
		}
	}
	if bg == nil {
		t.Fatal("background turn missing from audit capture")
	}
	if bg.PromotionEligible() {
		t.Fatalf("background turn promotion-eligible: %+v", bg)
	}
	// It is captured, but the lane-1 recall (cards only) can never see it.
	idx, _, err := memorysearch.NewIndexer(root).Ensure()
	if err != nil {
		t.Fatal(err)
	}
	for _, h := range idx.Search("routine-beacon", memorysearch.Options{Audience: "private", Kinds: []string{"card"}, Limit: 4}) {
		if h.Doc.ID == "eval-b1" {
			t.Fatal("background entry surfaced in lane-1 recall")
		}
	}
}

// R7.2: an untrusted scripted turn is captured with its provenance stamped —
// search flags it as untrusted and it can never promote.
func TestEvalUntrustedNotPromoted(t *testing.T) {
	root := t.TempDir()
	if _, err := Run(root); err != nil {
		t.Fatal(err)
	}
	prov, err := memory.OpenProvDB(filepath.Join(root, "runtime", "memory"))
	if err != nil {
		t.Fatal(err)
	}
	entries, err := prov.Entries()
	if err != nil {
		t.Fatal(err)
	}
	var un *memory.Entry
	for i := range entries {
		if entries[i].ID == "eval-u1" {
			un = &entries[i]
		}
	}
	if un == nil {
		t.Fatal("untrusted turn missing from capture")
	}
	if un.Provenance != memory.ProvenanceUntrusted || un.PromotionEligible() {
		t.Fatalf("untrusted turn mis-stamped: %+v", un)
	}
	idx, _, err := memorysearch.NewIndexer(root).Ensure()
	if err != nil {
		t.Fatal(err)
	}
	hits := idx.Search("forwarded-anchor", memorysearch.Options{Audience: "private", Limit: 4})
	if len(hits) == 0 || !hits[0].Untrusted {
		t.Fatalf("untrusted hit not flagged: %v", hits)
	}
}

// R7.2 audience isolation: a group-audience scripted turn never surfaces in
// a private search, and stays visible to its own audience.
func TestEvalAudienceIsolation(t *testing.T) {
	root := t.TempDir()
	if _, err := Run(root); err != nil {
		t.Fatal(err)
	}
	idx, _, err := memorysearch.NewIndexer(root).Ensure()
	if err != nil {
		t.Fatal(err)
	}
	for _, h := range idx.Search("group-lantern", memorysearch.Options{Audience: "private", Limit: 8}) {
		if h.Doc.ID == "eval-g1" {
			t.Fatal("group entry leaked into private search")
		}
	}
	found := false
	for _, h := range idx.Search("group-lantern", memorysearch.Options{Audience: "group", Limit: 8}) {
		if h.Doc.ID == "eval-g1" {
			found = true
		}
	}
	if !found {
		t.Fatal("group entry missing from its own audience")
	}
}

// R7.2 supersede correctness: a superseded card is silent in normal search
// yet stays visible via history:true; the live successor answers the query.
func TestEvalSupersedeCorrectness(t *testing.T) {
	root := t.TempDir()
	if err := vault.Ensure(root); err != nil {
		t.Fatal(err)
	}
	w := vault.NewWriter(root)
	write := func(path, status, line string) {
		t.Helper()
		if _, err := w.Apply(memory.WriteOp{
			Path:    path,
			Content: []byte("---\ntype: note\nstatus: " + status + "\ntags: []\n---\n\n## Facts\n\n- " + line + "\n"),
			Actor:   memory.ActorOwner,
		}); err != nil {
			t.Fatal(err)
		}
	}
	write("memory/cards/notes/lattice-old.md", "superseded", "lattice-beam supplier was acme")
	write("memory/cards/notes/lattice-new.md", "active", "lattice-beam supplier is initech")

	idx, _, err := memorysearch.NewIndexer(root).Ensure()
	if err != nil {
		t.Fatal(err)
	}
	hits := idx.Search("lattice-beam supplier", memorysearch.Options{Audience: "private", Limit: 8})
	for _, h := range hits {
		if strings.Contains(h.Doc.ID, "lattice-old") {
			t.Fatal("superseded card surfaced in normal search")
		}
	}
	found := false
	for _, h := range hits {
		if strings.Contains(h.Doc.ID, "lattice-new") {
			found = true
		}
	}
	if !found {
		t.Fatal("live successor card missing from search")
	}
	hist := idx.Search("lattice-beam supplier", memorysearch.Options{Audience: "private", Limit: 8, History: true})
	found = false
	for _, h := range hist {
		if strings.Contains(h.Doc.ID, "lattice-old") {
			found = true
		}
	}
	if !found {
		t.Fatal("superseded card missing from history:true search")
	}
}

// R7.3: the live runner reports honestly — judged answers, evidence refs and
// accuracy, with the relay-provenance flag. This test uses a scripted asker;
// a real model-relay run is separately recorded as live.
func TestLiveEvalReportShape(t *testing.T) {
	qs := []Question{
		{ID: "q1", Category: "single-session-user", Lang: "en", Text: "which supplier did we sign?", Expect: []string{"memory/daily/2026-04-03.md"}},
		{ID: "q2", Category: "temporal-reasoning", Lang: "en", Text: "when was the flight?", Expect: []string{"memory/daily/2026-04-04.md"}},
	}
	ask := func(ctx context.Context, system, user string) (string, error) {
		if strings.Contains(user, "flight") {
			return "", errors.New("relay timeout")
		}
		return "The supplier was signed per memory/daily/2026-04-03.md:5-10.", nil
	}
	rep := RunLive(context.Background(), ask, qs)
	if !rep.Live {
		t.Fatal("report must mark live relay provenance")
	}
	if rep.Questions != 2 || len(rep.Results) != 2 {
		t.Fatalf("report shape: %+v", rep)
	}
	if !rep.Results[0].Correct || rep.Results[1].Correct {
		t.Fatalf("judgement wrong: %+v", rep.Results)
	}
	if len(rep.Results[0].Cited) != 1 || rep.Results[0].Cited[0] != "memory/daily/2026-04-03.md:5-10" {
		t.Fatalf("cited refs missing: %+v", rep.Results[0])
	}
	if rep.Correct != 1 || rep.Accuracy != 0.5 {
		t.Fatalf("accuracy: %+v", rep)
	}
	if rep.Results[1].Error == "" {
		t.Fatalf("relay failure must be recorded, not silent: %+v", rep.Results[1])
	}
}
