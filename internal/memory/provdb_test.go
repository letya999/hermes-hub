package memory

import (
	"strings"
	"testing"
	"time"
)

// SPEC-0085 R2.2/R2.7: prov.db assigns each entry a stable <date>-<seq> id,
// fsyncs the append before it is acknowledged, and replays an identical id
// sequence — the daily leaves stay the provider-neutral source of truth.
func TestProvDBStableIDs(t *testing.T) {
	dir := t.TempDir()
	db, err := OpenProvDB(dir)
	if err != nil {
		t.Fatal(err)
	}
	at := time.Date(2026, 10, 31, 22, 30, 0, 0, time.UTC)
	e1 := Entry{Kind: KindInteractive, Provenance: ProvenanceOwner, Audience: AudiencePrivate, ObservedAt: at}
	e2 := Entry{Kind: KindInteractive, Provenance: ProvenanceAgent, Audience: AudiencePrivate, ObservedAt: at}
	if err := db.Append(&e1); err != nil {
		t.Fatal(err)
	}
	if err := db.Append(&e2); err != nil {
		t.Fatal(err)
	}
	if e1.ID != "2026-10-31-0001" || e2.ID != "2026-10-31-0002" {
		t.Fatalf("stable ids not minted: %q %q", e1.ID, e2.ID)
	}
	// A reopened sidecar continues the sequence — restart-safe.
	db2, err := OpenProvDB(dir)
	if err != nil {
		t.Fatal(err)
	}
	e3 := Entry{Kind: KindRoutine, Provenance: ProvenanceSystem, Audience: AudiencePrivate, ObservedAt: at.Add(time.Hour)}
	if err := db2.Append(&e3); err != nil {
		t.Fatal(err)
	}
	if e3.ID != "2026-10-31-0003" {
		t.Fatalf("reopened sidecar lost the sequence: %q", e3.ID)
	}
	// Replay: a clean sidecar fed the same entries mints the identical set.
	replay, err := OpenProvDB(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range []Entry{e1, e2, e3} {
		if err := replay.Append(&e); err != nil {
			t.Fatal(err)
		}
	}
	got, err := replay.Entries()
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 3 || got[0].ID != e1.ID || got[1].ID != e2.ID || got[2].ID != e3.ID {
		t.Fatalf("replay diverged: %+v", got)
	}
}

// SPEC-0085 R2.2: the sidecar record roundtrips every provenance field —
// replayed entries are byte-identical for migration between providers.
func TestEntryRoundtrip(t *testing.T) {
	dir := t.TempDir()
	db, err := OpenProvDB(dir)
	if err != nil {
		t.Fatal(err)
	}
	in := Entry{
		Scope: MintScope([]byte("k"), "prod", "alice", "private"),
		Kind:  KindInteractive, Provenance: ProvenanceOwner,
		Audience: AudiencePrivate, SourceRef: "job-42",
		Body: "hello\nwith newline", ObservedAt: time.Date(2026, 10, 31, 22, 30, 0, 0, time.UTC),
	}
	if err := db.Append(&in); err != nil {
		t.Fatal(err)
	}
	got, err := db.Entries()
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 || got[0] != in {
		t.Fatalf("roundtrip mismatch: %+v", got)
	}
}

// SPEC-0085 R2.3/R2.4: job triggers map to kinds — interactive turns may
// promote; routine/assignment/probe/goal/compaction/system are audit-only.
func TestKindTriggerMatrix(t *testing.T) {
	cases := []struct {
		trigger string
		assign  bool
		probe   bool
		want    Kind
	}{
		{"message", false, false, KindInteractive},
		{"cron", false, false, KindRoutine},
		{"cron", false, true, KindProbe},
		{"cron", true, false, KindAssignment},
		{"goal", false, false, KindGoal},
		{"compact", false, false, KindCompaction},
		{"wake", false, false, KindSystem},
		{"restart", false, false, KindSystem},
		{"auto-title", false, false, KindSystem},
		{"prepare", false, false, KindSystem},
		{"unknown-future", false, false, KindSystem},
	}
	for _, tc := range cases {
		if got := KindForTrigger(tc.trigger, tc.assign, tc.probe); got != tc.want {
			t.Errorf("KindForTrigger(%q,%v,%v)=%s want %s", tc.trigger, tc.assign, tc.probe, got, tc.want)
		}
	}
}

// SPEC-0085 R2.4 acceptance: a run of background triggers — wakes, restarts,
// title jobs, crons, assignments, probes — mints zero promotion-eligible
// entries no matter how many fire.
func TestBackgroundPromotionIneligible(t *testing.T) {
	triggers := []struct {
		trigger string
		assign  bool
		probe   bool
	}{
		{"wake", false, false}, {"restart", false, false}, {"auto-title", false, false},
		{"prepare", false, false}, {"cron", false, false}, {"cron", false, true},
		{"cron", true, false}, {"goal", false, false}, {"compact", false, false},
	}
	count := 0
	for i := 0; i < 100; i++ {
		tc := triggers[i%len(triggers)]
		e := Entry{Kind: KindForTrigger(tc.trigger, tc.assign, tc.probe), Provenance: ProvenanceSystem}
		if e.PromotionEligible() {
			count++
		}
	}
	if count != 0 {
		t.Fatalf("%d background entries promotion-eligible", count)
	}
}

// SPEC-0085 R2.3: untrusted comes from transport metadata, never from body
// text — a forwarded owner message is untrusted even when its text mimics an
// owner turn, and owner text containing forge-bait stays owner.
func TestProvenanceAtCapture(t *testing.T) {
	if got := ProvenanceForTurn(true, "message"); got != ProvenanceUntrusted {
		t.Fatalf("forwarded turn = %s want untrusted", got)
	}
	if got := ProvenanceForTurn(false, "message"); got != ProvenanceOwner {
		t.Fatalf("owner turn = %s want owner", got)
	}
	if got := ProvenanceForTurn(false, "cron"); got != ProvenanceOwner {
		t.Fatalf("owner-scheduled turn = %s want owner", got)
	}
	if got := ProvenanceForTurn(false, "wake"); got != ProvenanceSystem {
		t.Fatalf("hub wake = %s want system", got)
	}
}

// SPEC-0085 R2.5/R4.4: spans wrapped in the recalled-content marker are
// stripped before capture — injected recall can never re-enter a leaf as
// new content. A body that is only a recalled block yields nothing.
func TestStripRecalled(t *testing.T) {
	in := "owner said this\n" + RecalledOpen + "\nold fact one\nold fact two\n" + RecalledClose + "\nowner said that"
	got := StripRecalled(in)
	if got != "owner said this\n\nowner said that" {
		t.Fatalf("marked span not stripped: %q", got)
	}
	if out := StripRecalled(RecalledOpen + "\nentirely recalled\n" + RecalledClose); strings.TrimSpace(out) != "" {
		t.Fatalf("fully-recalled body must strip to nothing: %q", out)
	}
	if out := StripRecalled("no markers here"); out != "no markers here" {
		t.Fatalf("unmarked body changed: %q", out)
	}
}

// Capture retries re-append the same job-derived id — the sidecar must
// record it once (idempotent capture, SPEC-0085 R2.7 replay-determinism).
func TestProvDBIdempotentAppend(t *testing.T) {
	db, err := OpenProvDB(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	e := Entry{ID: "turn-job-1", Kind: KindInteractive, Provenance: ProvenanceOwner,
		Audience: AudiencePrivate, Body: "hi", ObservedAt: time.Now().UTC()}
	if err := db.Append(&e); err != nil {
		t.Fatal(err)
	}
	dup := e
	if err := db.Append(&dup); err != nil {
		t.Fatal(err)
	}
	got, _ := db.Entries()
	if len(got) != 1 {
		t.Fatalf("duplicate id appended twice: %d entries", len(got))
	}
}
