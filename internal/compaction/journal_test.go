package compaction

import (
	"testing"
	"time"
)

func TestJournalRoundTrip(t *testing.T) {
	j, err := Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	at := time.Date(2026, 2, 1, 12, 0, 0, 0, time.UTC)
	before, after := int64(90000), int64(12000)
	e := Event{
		At: at, SessionID: "hub-abc", Trigger: TriggerAutoInLoop,
		Engine: EngineNative, Method: MethodInLoop,
		TokensBefore: &before, TokensAfter: &after,
	}
	if err := j.Append(e); err != nil {
		t.Fatalf("append: %v", err)
	}
	events, err := j.Events("hub-abc")
	if err != nil || len(events) != 1 {
		t.Fatalf("events: %v %v", events, err)
	}
	got := events[0]
	if got.Trigger != TriggerAutoInLoop || got.Engine != EngineNative || got.Method != MethodInLoop {
		t.Fatalf("event fields corrupted: %+v", got)
	}
	if got.TokensBefore == nil || *got.TokensBefore != 90000 {
		t.Fatalf("tokens_before_est: %+v", got.TokensBefore)
	}
	// Other sessions are invisible — the journal is the only cross-turn memory
	// and never leaks one context's rows into another's view.
	foreign, err := j.Events("hub-zzz")
	if err != nil || len(foreign) != 0 {
		t.Fatalf("foreign session leaked: %v %v", foreign, err)
	}
	// Bounded vocabulary: unknown trigger/engine values are refused at write
	// time so a corrupted caller cannot poison status provenance.
	for _, bad := range []Event{
		{At: at, SessionID: "hub-abc", Trigger: "weird", Engine: EngineHub, Method: MethodInLoop},
		{At: at, SessionID: "hub-abc", Trigger: TriggerManual, Engine: "sql;", Method: MethodInLoop},
		{At: at, SessionID: "", Trigger: TriggerManual, Engine: EngineHub, Method: MethodInLoop},
		{At: at, SessionID: "hub-abc", Trigger: TriggerManual, Engine: EngineHub, Method: MethodInLoop, FailureClass: "DROP TABLE x with a very long verbose message that carries transcript text"},
	} {
		if err := j.Append(bad); err == nil {
			t.Fatalf("invalid event accepted: %+v", bad)
		}
	}
	events, err = j.Events("hub-abc")
	if err != nil || len(events) != 1 {
		t.Fatalf("rejected rows still landed: %v", events)
	}
}
