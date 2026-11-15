package prune

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/letya999/hermes-hub/internal/memory"
	"github.com/letya999/hermes-hub/internal/vault"
)

// fixture builds a vault whose past-day leaf holds one interactive owner
// block plus audit-kind noise, and marks the day fully rolled up.
func fixture(t *testing.T, day time.Time) string {
	t.Helper()
	root := filepath.Join(t.TempDir(), "vault")
	if err := vault.Ensure(root); err != nil {
		t.Fatal(err)
	}
	daily, err := vault.OpenDaily(root, time.UTC)
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range []memory.Entry{
		{ID: "turn-1", Kind: memory.KindInteractive, Provenance: memory.ProvenanceOwner, Audience: memory.AudiencePrivate, Body: "remember the supplier is ACME", ObservedAt: day},
		{ID: "reply-1", Kind: memory.KindInteractive, Provenance: memory.ProvenanceAgent, Audience: memory.AudiencePrivate, Body: "noted", ObservedAt: day},
		{ID: "cron-1", Kind: memory.KindRoutine, Provenance: memory.ProvenanceSystem, Audience: memory.AudiencePrivate, Body: strings.Repeat("routine tick body ", 20), ObservedAt: day},
		{ID: "compact-1", Kind: memory.KindCompaction, Provenance: memory.ProvenanceAgent, Audience: memory.AudiencePrivate, Body: strings.Repeat("dropped transcript chunk ", 20), ObservedAt: day},
	} {
		if err := daily.Append(e, "test"); err != nil {
			t.Fatal(err)
		}
	}
	dayKey := day.Format("2006-01-02")
	cache := filepath.Join(root, "runtime", "memory", "night", dayKey+".json")
	if err := os.MkdirAll(filepath.Dir(cache), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(cache, []byte(`{"entries":{},"steps":{"facts":true,"links":true,"truths":true,"intents":true,"core":true,"summary":true,"journal":true}}`), 0600); err != nil {
		t.Fatal(err)
	}
	return root
}

func leaf(t *testing.T, root, dayKey string) string {
	t.Helper()
	b, err := os.ReadFile(filepath.Join(root, "memory", "daily", dayKey+".md"))
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

// SPEC-0085 R7.5: an oversized completed day folds audit-kind bodies in
// place — interactive blocks stay verbatim, every mem:id survives, and the
// stub marker makes a refold a no-op.
func TestFoldStubsAuditKeepsEligible(t *testing.T) {
	day := time.Date(2026, 4, 10, 12, 0, 0, 0, time.UTC)
	root := fixture(t, day)
	dayKey := day.Format("2006-01-02")
	before := leaf(t, root, dayKey)

	rep, err := Apply(root, Options{FoldBytes: 1, Now: day.AddDate(0, 0, 3), TZ: time.UTC})
	if err != nil {
		t.Fatal(err)
	}
	if len(rep.Folded) != 1 || rep.Folded[0].Day != dayKey {
		t.Fatalf("folded = %+v", rep.Folded)
	}
	if rep.Folded[0].Stubs != 2 {
		t.Fatalf("stubs = %d, want 2 (routine+compaction)", rep.Folded[0].Stubs)
	}
	after := leaf(t, root, dayKey)
	if len(after) >= len(before) {
		t.Fatalf("leaf did not shrink: %d -> %d", len(before), len(after))
	}
	if !strings.Contains(after, "remember the supplier is ACME") {
		t.Fatal("interactive owner body was folded")
	}
	if strings.Contains(after, "routine tick body") || strings.Contains(after, "dropped transcript chunk") {
		t.Fatal("audit body survived the fold")
	}
	for _, id := range []string{"turn-1", "reply-1", "cron-1", "compact-1"} {
		if !strings.Contains(after, "mem:id="+id) {
			t.Fatalf("mem:id %s lost in fold", id)
		}
	}

	rep2, err := Apply(root, Options{FoldBytes: 1, Now: day.AddDate(0, 0, 4), TZ: time.UTC})
	if err != nil {
		t.Fatal(err)
	}
	if len(rep2.Folded) != 0 {
		t.Fatalf("refold not idempotent: %+v", rep2.Folded)
	}
}

// SPEC-0085 R7.5: an oversized leaf that never finished its rollup keeps
// every byte — the leaf is the only evidence a late rollup can still use.
func TestFoldOnlyCompleted(t *testing.T) {
	day := time.Date(2026, 4, 10, 12, 0, 0, 0, time.UTC)
	root := fixture(t, day)
	dayKey := day.Format("2006-01-02")
	// Drop the day-state: the leaf now looks rolled-up only by size.
	if err := os.Remove(filepath.Join(root, "runtime", "memory", "night", dayKey+".json")); err != nil {
		t.Fatal(err)
	}
	before := leaf(t, root, dayKey)

	rep, err := Apply(root, Options{FoldBytes: 1, Now: day.AddDate(0, 0, 3), TZ: time.UTC})
	if err != nil {
		t.Fatal(err)
	}
	if len(rep.Folded) != 0 {
		t.Fatalf("unrolled day folded: %+v", rep.Folded)
	}
	if after := leaf(t, root, dayKey); after != before {
		t.Fatal("unrolled leaf bytes changed")
	}
}

// SPEC-0085 R7.5: a retention-age leaf whose rollup completed and whose
// day summary exists is removed through a governed commit, and its prov.db
// rows drop with it — the summary stays as the day's permanent residue.
func TestExpireDropsLeafAndProv(t *testing.T) {
	day := time.Date(2026, 3, 1, 12, 0, 0, 0, time.UTC)
	root := fixture(t, day)
	dayKey := day.Format("2006-01-02")
	summary := filepath.Join(root, "memory", "summaries", "daily", dayKey+".md")
	w := vault.NewWriter(root)
	if _, err := w.Apply(memory.WriteOp{Path: "memory/summaries/daily/" + dayKey + ".md",
		Content: []byte("# 2026-03-01\n\nday digest\n"), WantSHA: "",
		Actor: memory.ActorSystem}); err != nil {
		t.Fatal(err)
	}

	rep, err := Apply(root, Options{RetentionDays: 7, Now: day.AddDate(0, 0, 30), TZ: time.UTC})
	if err != nil {
		t.Fatal(err)
	}
	if len(rep.Expired) != 1 || rep.Expired[0] != "memory/daily/"+dayKey+".md" {
		t.Fatalf("expired = %+v", rep.Expired)
	}
	if _, err := os.Stat(filepath.Join(root, "memory", "daily", dayKey+".md")); !os.IsNotExist(err) {
		t.Fatal("leaf still present after expire")
	}
	if _, err := os.Stat(summary); err != nil {
		t.Fatal("day summary must survive expire")
	}
	prov, err := memory.OpenProvDB(filepath.Join(root, "runtime", "memory"))
	if err != nil {
		t.Fatal(err)
	}
	entries, err := prov.Entries()
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 0 {
		t.Fatalf("prov.db rows kept for expired day: %d", len(entries))
	}
}

// SPEC-0085 R7.5: a retention-age leaf that never completed its rollup is
// kept and reported — its bytes are the only record a late night can still
// distill; deleting them would lose the day entirely.
func TestExpireKeepsUnrolledOld(t *testing.T) {
	day := time.Date(2026, 3, 1, 12, 0, 0, 0, time.UTC)
	root := fixture(t, day)
	dayKey := day.Format("2006-01-02")
	if err := os.Remove(filepath.Join(root, "runtime", "memory", "night", dayKey+".json")); err != nil {
		t.Fatal(err)
	}

	rep, err := Apply(root, Options{RetentionDays: 7, Now: day.AddDate(0, 0, 30), TZ: time.UTC})
	if err != nil {
		t.Fatal(err)
	}
	if len(rep.Expired) != 0 {
		t.Fatalf("unrolled day expired: %+v", rep.Expired)
	}
	if len(rep.Kept) != 1 || rep.Kept[0].Day != dayKey {
		t.Fatalf("kept = %+v", rep.Kept)
	}
	if _, err := os.Stat(filepath.Join(root, "memory", "daily", dayKey+".md")); err != nil {
		t.Fatal("unrolled leaf removed")
	}
	prov, err := memory.OpenProvDB(filepath.Join(root, "runtime", "memory"))
	if err != nil {
		t.Fatal(err)
	}
	entries, err := prov.Entries()
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 4 {
		t.Fatalf("prov rows = %d, want 4 kept", len(entries))
	}
}

// SPEC-0085 R7.5: a completed day without its summary on disk is kept —
// expire requires BOTH proofs of distillation.
func TestExpireRequiresSummary(t *testing.T) {
	day := time.Date(2026, 3, 1, 12, 0, 0, 0, time.UTC)
	root := fixture(t, day)

	rep, err := Apply(root, Options{RetentionDays: 7, Now: day.AddDate(0, 0, 30), TZ: time.UTC})
	if err != nil {
		t.Fatal(err)
	}
	if len(rep.Expired) != 0 {
		t.Fatalf("day without summary expired: %+v", rep.Expired)
	}
	if len(rep.Kept) != 1 || rep.Kept[0].Reason != "day summary missing" {
		t.Fatalf("kept = %+v", rep.Kept)
	}
}

// SPEC-0085 R7.5: a completed day inside the retention window is never
// expired — the age bound is the expire lane's only gate (fold is
// age-free by design: any completed oversized leaf may fold).
func TestNeverTouchesRecentLeaf(t *testing.T) {
	recent := time.Date(2026, 4, 20, 12, 0, 0, 0, time.UTC)
	root := fixture(t, recent)
	dayKey := recent.Format("2006-01-02")
	before := leaf(t, root, dayKey)

	rep, err := Apply(root, Options{RetentionDays: 7, Now: recent.AddDate(0, 0, 3), TZ: time.UTC})
	if err != nil {
		t.Fatal(err)
	}
	if len(rep.Expired) != 0 || len(rep.Kept) != 0 {
		t.Fatalf("recent day touched: expired=%v kept=%v", rep.Expired, rep.Kept)
	}
	if after := leaf(t, root, dayKey); after != before {
		t.Fatal("recent leaf changed")
	}
}

// SPEC-0085 R7.5: every prune action is journaled into memory/REVIEW.md
// under a "## prune <day>" section — the same audit discipline as the
// rollup journal, so a kept or cut byte is never silent.
func TestReviewPruneSection(t *testing.T) {
	day := time.Date(2026, 3, 1, 12, 0, 0, 0, time.UTC)
	root := fixture(t, day)
	dayKey := day.Format("2006-01-02")
	w := vault.NewWriter(root)
	if _, err := w.Apply(memory.WriteOp{Path: "memory/summaries/daily/" + dayKey + ".md",
		Content: []byte("---\ntype: daily-summary\nstatus: active\ntags: []\ncreated: " + dayKey + "\nupdated: " + dayKey + "\n---\n\nold digest\n"),
		Actor:   memory.ActorSystem}); err != nil {
		t.Fatal(err)
	}

	if _, err := Apply(root, Options{RetentionDays: 7, Now: day.AddDate(0, 0, 30), TZ: time.UTC}); err != nil {
		t.Fatal(err)
	}
	review, err := os.ReadFile(filepath.Join(root, "memory", "REVIEW.md"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(review), "## prune "+dayKey) {
		t.Fatalf("no prune section for %s:\n%s", dayKey, review)
	}
	if !strings.Contains(string(review), "expired") {
		t.Fatalf("expire action not journaled:\n%s", review)
	}
}

// SPEC-0085 R7.5: fold and kept actions journal the same way as expires —
// no byte is cut or held back silently.
func TestReviewPruneAllActions(t *testing.T) {
	day := time.Date(2026, 4, 10, 12, 0, 0, 0, time.UTC)
	root := fixture(t, day)
	dayKey := day.Format("2006-01-02")

	// A second, unrolled day old enough to hit the retention lane as kept.
	old := day.AddDate(0, 0, -20)
	daily, err := vault.OpenDaily(root, time.UTC)
	if err != nil {
		t.Fatal(err)
	}
	if err := daily.Append(memory.Entry{ID: "old-1", Kind: memory.KindInteractive,
		Provenance: memory.ProvenanceOwner, Audience: memory.AudiencePrivate,
		Body: "old unprocessed day", ObservedAt: old}, "test"); err != nil {
		t.Fatal(err)
	}
	oldKey := old.Format("2006-01-02")

	rep, err := Apply(root, Options{RetentionDays: 7, FoldBytes: 1, Now: day.AddDate(0, 0, 3), TZ: time.UTC})
	if err != nil {
		t.Fatal(err)
	}
	if len(rep.Folded) != 1 || len(rep.Kept) != 1 {
		t.Fatalf("rep = %+v", rep)
	}
	review, err := os.ReadFile(filepath.Join(root, "memory", "REVIEW.md"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(review), "## prune "+dayKey) || !strings.Contains(string(review), "folded leaf") {
		t.Fatalf("fold not journaled:\n%s", review)
	}
	if !strings.Contains(string(review), "## prune "+oldKey) || !strings.Contains(string(review), "kept leaf") {
		t.Fatalf("kept day not journaled:\n%s", review)
	}
}

// SPEC-0085 R7.5: Scan is the dry-run twin — it reports the same actions
// Apply would take while mutating nothing (no leaf writes, no REVIEW).
func TestScanReportsWithoutMutation(t *testing.T) {
	root := t.TempDir()
	if err := vault.Ensure(root); err != nil {
		t.Fatal(err)
	}
	old := time.Now().AddDate(0, 0, -30)
	daily, err := vault.OpenDaily(root, time.Local)
	if err != nil {
		t.Fatal(err)
	}
	if err := daily.Append(memory.Entry{ID: "old-1", Kind: memory.KindInteractive,
		Provenance: memory.ProvenanceOwner, Audience: memory.AudiencePrivate,
		Body: "old", ObservedAt: old}, "test"); err != nil {
		t.Fatal(err)
	}
	dayKey := old.Format("2006-01-02")
	cache := filepath.Join(root, "runtime", "memory", "night", dayKey+".json")
	if err := os.MkdirAll(filepath.Dir(cache), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(cache, []byte(`{"entries":{},"steps":{"facts":true,"links":true,"truths":true,"intents":true,"core":true,"summary":true,"journal":true}}`), 0600); err != nil {
		t.Fatal(err)
	}
	w := vault.NewWriter(root)
	if _, err := w.Apply(memory.WriteOp{Path: "memory/summaries/daily/" + dayKey + ".md",
		Content: []byte("---\ntype: daily-summary\nstatus: active\ntags: []\ncreated: " + dayKey + "\nupdated: " + dayKey + "\n---\n\nx\n"),
		Actor:   memory.ActorSystem}); err != nil {
		t.Fatal(err)
	}

	rep, err := Scan(root, Options{RetentionDays: 7})
	if err != nil {
		t.Fatal(err)
	}
	if len(rep.Expired) != 1 {
		t.Fatalf("scan should predict the expiry: %+v", rep)
	}
	leaf := filepath.Join(root, "memory", "daily", dayKey+".md")
	if _, err := os.Stat(leaf); err != nil {
		t.Fatalf("scan must not expire: %v", err)
	}
	if _, err := os.Stat(filepath.Join(root, "memory", "REVIEW.md")); !os.IsNotExist(err) {
		t.Fatal("scan must not journal")
	}
}

// SPEC-0085 R7.5: Scan parity — it predicts kept-with-reason and
// fold-with-stub-count exactly like Apply reports them.
func TestScanPredictsKeptAndFold(t *testing.T) {
	root := t.TempDir()
	if err := vault.Ensure(root); err != nil {
		t.Fatal(err)
	}
	// Kept: old leaf, no rollup state -> predicted keep.
	old := time.Now().AddDate(0, 0, -30)
	daily, err := vault.OpenDaily(root, time.Local)
	if err != nil {
		t.Fatal(err)
	}
	if err := daily.Append(memory.Entry{ID: "old-1", Kind: memory.KindInteractive,
		Provenance: memory.ProvenanceOwner, Audience: memory.AudiencePrivate,
		Body: "x", ObservedAt: old}, "test"); err != nil {
		t.Fatal(err)
	}
	// Fold: yesterday's complete oversized leaf with an audit body.
	yest := time.Now().AddDate(0, 0, -1)
	var big strings.Builder
	for i := 0; i < 200; i++ {
		big.WriteString("audit padding line\n")
	}
	if err := daily.Append(memory.Entry{ID: "sys-1", Kind: memory.KindSystem,
		Provenance: memory.ProvenanceSystem, Audience: memory.AudiencePrivate,
		Body: big.String(), ObservedAt: yest}, "test"); err != nil {
		t.Fatal(err)
	}
	yKey := yest.Format("2006-01-02")
	cache := filepath.Join(root, "runtime", "memory", "night", yKey+".json")
	if err := os.MkdirAll(filepath.Dir(cache), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(cache, []byte(`{"entries":{},"steps":{"facts":true,"links":true,"truths":true,"intents":true,"core":true,"summary":true,"journal":true}}`), 0600); err != nil {
		t.Fatal(err)
	}

	rep, err := Scan(root, Options{RetentionDays: 7, FoldBytes: 512, TZ: time.Local})
	if err != nil {
		t.Fatal(err)
	}
	if len(rep.Kept) != 1 || rep.Kept[0].Reason != "rollup incomplete" {
		t.Fatalf("kept prediction wrong: %+v", rep.Kept)
	}
	if len(rep.Folded) != 1 || rep.Folded[0].Day != yKey || rep.Folded[0].Stubs != 1 {
		t.Fatalf("fold prediction wrong: %+v", rep.Folded)
	}
}

// SPEC-0085 R7.5: non-leaf files in daily/ are skipped by name, and an
// expire with no prov.db on disk still completes — the audit spine is
// optional local state, never a gate.
func TestApplySkipsNonLeavesAndMissingProvDB(t *testing.T) {
	root := t.TempDir()
	if err := vault.Ensure(root); err != nil {
		t.Fatal(err)
	}
	old := time.Now().AddDate(0, 0, -30)
	daily, err := vault.OpenDaily(root, time.Local)
	if err != nil {
		t.Fatal(err)
	}
	if err := daily.Append(memory.Entry{ID: "old-1", Kind: memory.KindInteractive,
		Provenance: memory.ProvenanceOwner, Audience: memory.AudiencePrivate,
		Body: "x", ObservedAt: old}, "test"); err != nil {
		t.Fatal(err)
	}
	dayKey := old.Format("2006-01-02")
	dir := filepath.Join(root, "memory", "daily")
	for _, junk := range []string{"notes.txt", "not-a-day.md"} {
		if err := os.WriteFile(filepath.Join(dir, junk), []byte("junk\n"), 0600); err != nil {
			t.Fatal(err)
		}
	}
	cache := filepath.Join(root, "runtime", "memory", "night", dayKey+".json")
	if err := os.MkdirAll(filepath.Dir(cache), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(cache, []byte(`{"entries":{},"steps":{"facts":true,"links":true,"truths":true,"intents":true,"core":true,"summary":true,"journal":true}}`), 0600); err != nil {
		t.Fatal(err)
	}
	w := vault.NewWriter(root)
	if _, err := w.Apply(memory.WriteOp{Path: "memory/summaries/daily/" + dayKey + ".md",
		Content: []byte("---\ntype: daily-summary\nstatus: active\ntags: []\ncreated: " + dayKey + "\nupdated: " + dayKey + "\n---\n\nx\n"),
		Actor:   memory.ActorSystem}); err != nil {
		t.Fatal(err)
	}
	// No prov.db at all — expire must not fail on the absent spine.
	rep, err := Apply(root, Options{RetentionDays: 7})
	if err != nil {
		t.Fatal(err)
	}
	if len(rep.Expired) != 1 {
		t.Fatalf("expected the verified leaf to expire: %+v", rep)
	}
	if _, err := os.Stat(filepath.Join(dir, dayKey+".md")); !os.IsNotExist(err) {
		t.Fatal("leaf still present")
	}
}
