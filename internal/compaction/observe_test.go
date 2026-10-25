package compaction

import (
	"testing"
	"time"
)

// A carrier scan reports what the transcript metadata proves happened. The
// observer turns the in-place count into journaled events exactly once —
// repeated scans of the same transcript invent nothing.
func TestCarrierScanCountsInPlace(t *testing.T) {
	j, err := Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	at := 1700000000.0
	scan := ScanResult{
		Lineage: []string{"hub-abc"}, Tip: "hub-abc",
		InPlace: 2, LastSummaryAt: &at,
	}
	appended, err := j.Observe("hub-abc", scan)
	if err != nil || appended != 2 {
		t.Fatalf("observe: %d %v", appended, err)
	}
	events, _ := j.Events("hub-abc")
	for _, e := range events {
		if e.Trigger != TriggerAutoInLoop || e.Engine != EngineNative || e.Method != MethodInLoop {
			t.Fatalf("observed event misattributed: %+v", e)
		}
		if e.TokensBefore != nil || e.TokensAfter != nil {
			t.Fatalf("observed event fabricated tokens: %+v", e)
		}
	}
	// Idempotent: the same scan observed again appends nothing.
	appended, err = j.Observe("hub-abc", scan)
	if err != nil || appended != 0 {
		t.Fatalf("re-observe appended %d", appended)
	}
	// A scan that grows appends only the delta.
	scan.InPlace = 4
	appended, err = j.Observe("hub-abc", scan)
	if err != nil || appended != 2 {
		t.Fatalf("delta observe: %d", appended)
	}
	events, _ = j.Events("hub-abc")
	if len(events) != 4 {
		t.Fatalf("events: %d", len(events))
	}
}

// Unknown is explicit null, never a fabricated zero or an empty string.
func TestContextStatusUnknownIsNull(t *testing.T) {
	j, err := Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	st, err := j.StatusFor("hub-abc", 0, false, nil, ProvenanceEstimated)
	if err != nil {
		t.Fatal(err)
	}
	if st.Compactions != nil {
		t.Fatalf("compactions not null: %v", *st.Compactions)
	}
	if st.LastEvent != nil {
		t.Fatalf("last_event not null: %+v", st.LastEvent)
	}
	if st.Tokens != nil || st.TokensProvenance != ProvenanceEstimated {
		t.Fatalf("tokens: %v %q", st.Tokens, st.TokensProvenance)
	}
	// The effective threshold always reports — undeclared means the system
	// default (below the upstream in-loop trigger), tagged default.
	if st.ThresholdProvenance != ProvenanceDefault || st.ThresholdTokens == nil || *st.ThresholdTokens != DefaultSweepThreshold {
		t.Fatalf("threshold: %v %q", st.ThresholdTokens, st.ThresholdProvenance)
	}
}

// A declared threshold rides with provenance; journaled rows fill the
// observed count and the last event.
func TestContextStatusReflectsJournal(t *testing.T) {
	j, err := Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	_ = j.Append(Event{At: time.Now().UTC(), SessionID: "hub-abc", Trigger: TriggerManual, Engine: EngineHub, Method: MethodArchiveCompct})
	_ = j.Append(Event{At: time.Now().UTC(), SessionID: "hub-abc", Trigger: TriggerAutoSweep, Engine: EngineHub, Method: MethodArchiveCompct})
	tokens := int64(54000)
	st, err := j.StatusFor("hub-abc", 120000, true, &tokens, ProvenanceEstimated)
	if err != nil {
		t.Fatal(err)
	}
	if st.Compactions == nil || *st.Compactions != 2 {
		t.Fatalf("compactions: %+v", st.Compactions)
	}
	if st.LastEvent == nil || st.LastEvent.Trigger != TriggerAutoSweep {
		t.Fatalf("last event: %+v", st.LastEvent)
	}
	if st.ThresholdTokens == nil || *st.ThresholdTokens != 120000 || st.ThresholdProvenance != ProvenanceDeclared {
		t.Fatalf("threshold: %v %q", st.ThresholdTokens, st.ThresholdProvenance)
	}
	if st.Tokens == nil || *st.Tokens != 54000 {
		t.Fatalf("tokens: %+v", st.Tokens)
	}
}

// Refusal rows are attempts, not compactions: they must not absorb the
// carrier-scan delta or the observed count shrinks below the truth.
func TestObserveIgnoresRefusalRows(t *testing.T) {
	j, err := Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	_ = j.Append(Event{SessionID: "hub-abc", Trigger: TriggerAutoSweep, Engine: EngineHub,
		Method: MethodArchiveCompct, FailureClass: "lease_held"})
	_ = j.Append(Event{SessionID: "hub-abc", Trigger: TriggerAutoSweep, Engine: EngineHub,
		Method: MethodArchiveCompct})
	scan := ScanResult{InPlace: 3}
	appended, err := j.Observe("hub-abc", scan)
	if err != nil || appended != 2 {
		t.Fatalf("observe counted refusal as compaction: %d %v", appended, err)
	}
}
