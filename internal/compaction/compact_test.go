package compaction

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"
)

// A compact request against a session upstream does not know refuses —
// manual compaction never creates a session.
func TestCompactNeverCreatesSession(t *testing.T) {
	calls := stubRunner(t, map[string]string{
		"estimate_tokens": `{"ok":false,"error":"session_missing"}`,
	})
	result, err := Compact(context.Background(), CompactOptions{
		StateDir: t.TempDir(), SessionID: "hub-ghost", Holder: "hub-compact",
		Engine: EngineHub, ResolvedModel: "m1",
		Now: func() time.Time { return time.Date(2026, 2, 1, 12, 0, 0, 0, time.UTC) },
	})
	if err != nil {
		t.Fatal(err)
	}
	if result.Applied || result.FailureClass != "session_missing" {
		t.Fatalf("ghost session compacted: %+v", result)
	}
	for _, req := range *calls {
		op, _ := req["op"].(string)
		if op != "estimate_tokens" {
			t.Fatalf("compact mutated a missing session: %v", req)
		}
	}
}

// A compact during an active run refuses deterministically — one bounded
// class, no queued surprise, no mutation.
func TestBusyDeterministic(t *testing.T) {
	calls := stubRunner(t, map[string]string{
		"estimate_tokens": `{"ok":true,"tokens":300000,"message_count":80}`,
		"lease":           `{"ok":true,"acquired":false}`,
	})
	result, err := Compact(context.Background(), CompactOptions{
		StateDir: t.TempDir(), SessionID: "hub-abc", Holder: "hub-compact",
		Engine: EngineHub, ResolvedModel: "m1",
		Now: func() time.Time { return time.Date(2026, 2, 1, 12, 0, 0, 0, time.UTC) },
	})
	if err != nil {
		t.Fatal(err)
	}
	if result.Applied || result.FailureClass != RefusalLeaseHeld {
		t.Fatalf("busy compact: %+v", result)
	}
	for _, req := range *calls {
		op, _ := req["op"].(string)
		if op != "estimate_tokens" && op != "lease" {
			t.Fatalf("busy compact mutated the session: %v", req)
		}
	}
}

// The compression lock is the second gate: when an in-flight compaction
// holds it the request refuses and touches nothing else.
func TestCompactLockedRefusal(t *testing.T) {
	calls := stubRunner(t, map[string]string{
		"estimate_tokens": `{"ok":true,"tokens":300000,"message_count":80}`,
		"lease":           `{"ok":true,"acquired":true}`,
		"lock":            `{"ok":true,"locked":false}`,
		"unlease":         `{"ok":true}`,
	})
	result, err := Compact(context.Background(), CompactOptions{
		StateDir: t.TempDir(), SessionID: "hub-abc", Holder: "hub-compact",
		Engine: EngineHub, ResolvedModel: "m1",
		Now: func() time.Time { return time.Date(2026, 2, 1, 12, 0, 0, 0, time.UTC) },
	})
	if err != nil {
		t.Fatal(err)
	}
	if result.Applied || result.FailureClass != "locked" {
		t.Fatalf("locked compact: %+v", result)
	}
	for _, req := range *calls {
		op, _ := req["op"].(string)
		if op == "read_transcript" || op == "apply_transcript" {
			t.Fatalf("locked compact reached the transcript: %v", req)
		}
	}
}

// A bare /compact runs the lock → read → summarize → apply → journal path
// under the held turn lease: the applied set is the summary carrier plus a
// verbatim tail, and the journal records trigger=manual.
func TestCompactCommitHappyPath(t *testing.T) {
	t0 := time.Date(2026, 2, 1, 12, 0, 0, 0, time.UTC)
	bigUser := strings.Repeat("u", 800000)
	calls := stubRunner(t, map[string]string{
		"estimate_tokens":  `{"ok":true,"tokens":300000,"message_count":6}`,
		"lease":            `{"ok":true,"acquired":true}`,
		"lock":             `{"ok":true,"locked":true}`,
		"read_transcript":  `{"ok":true,"session_id":"hub-abc","watermark":6,"messages":[{"role":"user","content":"` + bigUser + `"},{"role":"assistant","content":"m2"},{"role":"user","content":"m3"},{"role":"assistant","content":"m4"},{"role":"user","content":"m5"},{"role":"assistant","content":"m6"}]}`,
		"apply_transcript": `{"ok":true,"active_count":4}`,
		"unlock":           `{"ok":true}`,
		"unlease":          `{"ok":true}`,
	})
	dir := t.TempDir()
	var gotFocus string
	result, err := Compact(context.Background(), CompactOptions{
		StateDir: dir, SessionID: "hub-abc", Holder: "hub-compact",
		Trigger: TriggerManual, Engine: EngineHub, ResolvedModel: "m1",
		Threshold: 160000, TailCount: 3, Args: CompactArgs{Focus: "deploy steps"},
		Now: func() time.Time { return t0 },
		Summarize: func(_ context.Context, _ string, _ []map[string]any, focus string) (string, error) {
			gotFocus = focus
			return "condensed summary", nil
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if !result.Applied || result.Method != "summary" || result.RetainedTail != 3 {
		t.Fatalf("compact result: %+v", result)
	}
	if result.TokensBefore == nil || *result.TokensBefore != 300000 {
		t.Fatalf("tokens_before: %+v", result.TokensBefore)
	}
	if result.TokensAfter == nil || *result.TokensAfter <= 0 || *result.TokensAfter >= *result.TokensBefore {
		t.Fatalf("tokens_after should be a shrinking estimate: %+v", result.TokensAfter)
	}
	var apply map[string]any
	for _, req := range *calls {
		if req["op"] == "apply_transcript" {
			apply = req
		}
	}
	if apply == nil {
		t.Fatal("no apply_transcript call")
	}
	msgs, _ := apply["messages"].([]any)
	if len(msgs) != 4 {
		t.Fatalf("applied %d messages, want carrier+3", len(msgs))
	}
	carrier, _ := msgs[0].(map[string]any)
	if carrier["_compressed_summary"] != true || carrier["role"] != "user" {
		t.Fatalf("carrier malformed: %v", carrier)
	}
	j, _ := Open(dir)
	events, _ := j.Events("hub-abc")
	if len(events) != 1 || events[0].Trigger != TriggerManual || events[0].Engine != EngineHub || events[0].Method != "summary" {
		t.Fatalf("journal: %+v", events)
	}
	if events[0].TokensAfter == nil {
		t.Fatal("journal missing the post-apply estimate")
	}
	if gotFocus != "deploy steps" {
		t.Fatalf("focus not forwarded: %q", gotFocus)
	}
}

// A transcript that already fits inside the retained tail has no head to
// compress — the request refuses without spending a summary call.
func TestCompactNothingToCompress(t *testing.T) {
	stubRunner(t, map[string]string{
		"estimate_tokens":  `{"ok":true,"tokens":900,"message_count":2}`,
		"lease":            `{"ok":true,"acquired":true}`,
		"lock":             `{"ok":true,"locked":true}`,
		"read_transcript":  `{"ok":true,"session_id":"hub-abc","watermark":2,"messages":[{"role":"user","content":"m1"},{"role":"assistant","content":"m2"}]}`,
		"apply_transcript": `{"ok":true,"active_count":3}`,
		"unlock":           `{"ok":true}`,
		"unlease":          `{"ok":true}`,
	})
	summarized := false
	result, err := Compact(context.Background(), CompactOptions{
		StateDir: t.TempDir(), SessionID: "hub-abc", Holder: "hub-compact",
		Engine: EngineHub, ResolvedModel: "m1", TailCount: 8,
		Now: func() time.Time { return time.Date(2026, 2, 1, 12, 0, 0, 0, time.UTC) },
		Summarize: func(context.Context, string, []map[string]any, string) (string, error) {
			summarized = true
			return "x", nil
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if result.Applied || result.FailureClass != "nothing_to_compress" || summarized {
		t.Fatalf("tiny transcript compacted: %+v summarized=%v", result, summarized)
	}
}

// A summary-backend failure preserves the original transcript — no partial
// commit — and the result names the bounded class, never the relay detail.
func TestRelayFailureNamesClass(t *testing.T) {
	bigUser := strings.Repeat("u", 800000)
	calls := stubRunner(t, map[string]string{
		"estimate_tokens": `{"ok":true,"tokens":300000,"message_count":6}`,
		"lease":           `{"ok":true,"acquired":true}`,
		"lock":            `{"ok":true,"locked":true}`,
		"read_transcript": `{"ok":true,"session_id":"hub-abc","watermark":6,"messages":[{"role":"user","content":"` + bigUser + `"},{"role":"assistant","content":"m2"},{"role":"user","content":"m3"},{"role":"assistant","content":"m4"},{"role":"user","content":"m5"},{"role":"assistant","content":"m6"}]}`,
		"unlock":          `{"ok":true}`,
		"unlease":         `{"ok":true}`,
	})
	dir := t.TempDir()
	result, err := Compact(context.Background(), CompactOptions{
		StateDir: dir, SessionID: "hub-abc", Holder: "hub-compact",
		Trigger: TriggerManual, Engine: EngineHub, ResolvedModel: "m1", TailCount: 3,
		Now: func() time.Time { return time.Date(2026, 2, 1, 12, 0, 0, 0, time.UTC) },
		Summarize: func(context.Context, string, []map[string]any, string) (string, error) {
			return "", errors.New("provider exploded with secret detail")
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if result.Applied || result.FailureClass != "summarize_failed" {
		t.Fatalf("summarize failure: %+v", result)
	}
	for _, req := range *calls {
		if req["op"] == "apply_transcript" {
			t.Fatal("failed summary still applied")
		}
	}
	j, _ := Open(dir)
	events, _ := j.Events("hub-abc")
	if len(events) != 1 || events[0].FailureClass != "summarize_failed" {
		t.Fatalf("failure not journaled: %+v", events)
	}
}

// The hub cascade is cheapest-first: when pruning the bulky tool outputs
// alone brings the estimate under the threshold, the summary call never
// fires and the journal names the free stage.
func TestCascadeOrder(t *testing.T) {
	big := strings.Repeat("x", 40000)
	calls := stubRunner(t, map[string]string{
		"estimate_tokens":  `{"ok":true,"tokens":200000,"message_count":5}`,
		"lease":            `{"ok":true,"acquired":true}`,
		"lock":             `{"ok":true,"locked":true}`,
		"read_transcript":  `{"ok":true,"session_id":"hub-abc","watermark":5,"messages":[{"role":"user","content":"m1"},{"role":"tool","content":"` + big + `"},{"role":"assistant","content":"m3"},{"role":"user","content":"m4"},{"role":"assistant","content":"m5"}]}`,
		"apply_transcript": `{"ok":true,"active_count":5}`,
		"unlock":           `{"ok":true}`,
		"unlease":          `{"ok":true}`,
	})
	dir := t.TempDir()
	summarized := false
	result, err := Compact(context.Background(), CompactOptions{
		StateDir: dir, SessionID: "hub-abc", Holder: "hub-compact",
		Trigger: TriggerManual, Engine: EngineHub, ResolvedModel: "m1",
		Threshold: 160000, TailCount: 2,
		Now: func() time.Time { return time.Date(2026, 2, 1, 12, 0, 0, 0, time.UTC) },
		Summarize: func(context.Context, string, []map[string]any, string) (string, error) {
			summarized = true
			return "x", nil
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if !result.Applied || result.Method != MethodPruneOnly || summarized {
		t.Fatalf("prune-sufficient compact paid a summary: %+v summarized=%v", result, summarized)
	}
	var apply map[string]any
	for _, req := range *calls {
		if req["op"] == "apply_transcript" {
			apply = req
		}
	}
	if apply == nil {
		t.Fatal("no apply")
	}
	msgs, _ := apply["messages"].([]any)
	stub, _ := msgs[1].(map[string]any)["content"].(string)
	if len(stub) > 200 || !strings.Contains(stub, "pruned") {
		t.Fatalf("tool output not stubbed: %.200q", stub)
	}
	j, _ := Open(dir)
	events, _ := j.Events("hub-abc")
	if len(events) != 1 || events[0].Method != MethodPruneOnly {
		t.Fatalf("journal: %+v", events)
	}
}

// Preview runs the dry-run pipeline — the same stage logic minus mutation —
// and reports the stage that would fire. Nothing is applied, no summary call
// is spent, no lock is taken.
func TestPreviewWouldMethod(t *testing.T) {
	bigUser := strings.Repeat("u", 800000)
	calls := stubRunner(t, map[string]string{
		"estimate_tokens":  `{"ok":true,"tokens":300000,"message_count":6}`,
		"lease":            `{"ok":true,"acquired":true}`,
		"lock":             `{"ok":true,"locked":true}`,
		"read_transcript":  `{"ok":true,"session_id":"hub-abc","watermark":6,"messages":[{"role":"user","content":"` + bigUser + `"},{"role":"assistant","content":"m2"},{"role":"user","content":"m3"},{"role":"assistant","content":"m4"},{"role":"user","content":"m5"},{"role":"assistant","content":"m6"}]}`,
		"apply_transcript": `{"ok":true,"active_count":4}`,
		"unlock":           `{"ok":true}`,
		"unlease":          `{"ok":true}`,
	})
	dir := t.TempDir()
	summarized := false
	result, err := Compact(context.Background(), CompactOptions{
		StateDir: dir, SessionID: "hub-abc", Holder: "hub-compact",
		Trigger: TriggerManual, Engine: EngineHub, ResolvedModel: "m1",
		Threshold: 160000, TailCount: 3, Args: CompactArgs{Preview: true},
		Now: func() time.Time { return time.Date(2026, 2, 1, 12, 0, 0, 0, time.UTC) },
		Summarize: func(context.Context, string, []map[string]any, string) (string, error) {
			summarized = true
			return "x", nil
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if result.Applied || result.WouldMethod != MethodSummary || summarized {
		t.Fatalf("preview: %+v summarized=%v", result, summarized)
	}
	if result.MessagesBefore != 6 || result.RetainedTail != 3 {
		t.Fatalf("preview counts wrong: %+v", result)
	}
	for _, req := range *calls {
		op, _ := req["op"].(string)
		if op == "apply_transcript" || op == "lock" || op == "lease" {
			t.Fatalf("preview mutated or locked: %v", req)
		}
	}
	j, _ := Open(dir)
	events, _ := j.Events("hub-abc")
	if len(events) != 0 {
		t.Fatalf("preview journaled: %+v", events)
	}
}

// When a free prune would already fit the bound the preview says so — the
// owner sees prune_only before paying for a summary.
func TestPreviewNamesPruneOnly(t *testing.T) {
	big := strings.Repeat("x", 40000)
	stubRunner(t, map[string]string{
		"estimate_tokens": `{"ok":true,"tokens":200000,"message_count":5}`,
		"read_transcript": `{"ok":true,"session_id":"hub-abc","watermark":5,"messages":[{"role":"user","content":"m1"},{"role":"tool","content":"` + big + `"},{"role":"assistant","content":"m3"},{"role":"user","content":"m4"},{"role":"assistant","content":"m5"}]}`,
	})
	result, err := Compact(context.Background(), CompactOptions{
		StateDir: t.TempDir(), SessionID: "hub-abc", Holder: "hub-compact",
		Trigger: TriggerManual, Engine: EngineHub, ResolvedModel: "m1",
		Threshold: 160000, TailCount: 2, Args: CompactArgs{Preview: true},
		Now: func() time.Time { return time.Date(2026, 2, 1, 12, 0, 0, 0, time.UTC) },
	})
	if err != nil {
		t.Fatal(err)
	}
	if result.WouldMethod != MethodPruneOnly {
		t.Fatalf("preview hid the free stage: %+v", result)
	}
}

// A compacted set that would grow the transcript never commits. When the
// summary and even the shrunk tail overshoot the original, the cascade
// falls back to the honest bounded-loss stage: the head is dropped behind a
// checkpoint marker and the method names it.
func TestDropCheckpointNamesLoss(t *testing.T) {
	head := `[{"role":"user","content":"` + strings.Repeat("h", 400) + `"},{"role":"assistant","content":"` + strings.Repeat("a", 400) + `"},{"role":"user","content":"` + strings.Repeat("h", 400) + `"},{"role":"assistant","content":"` + strings.Repeat("a", 400) + `"},`
	calls := stubRunner(t, map[string]string{
		"estimate_tokens":  `{"ok":true,"tokens":600,"message_count":6}`,
		"lease":            `{"ok":true,"acquired":true}`,
		"lock":             `{"ok":true,"locked":true}`,
		"read_transcript":  `{"ok":true,"session_id":"hub-abc","watermark":6,"messages":` + head + `{"role":"user","content":"` + strings.Repeat("t", 400) + `"},{"role":"assistant","content":"` + strings.Repeat("t", 400) + `"}]}`,
		"apply_transcript": `{"ok":true,"active_count":3}`,
		"unlock":           `{"ok":true}`,
		"unlease":          `{"ok":true}`,
	})
	dir := t.TempDir()
	result, err := Compact(context.Background(), CompactOptions{
		StateDir: dir, SessionID: "hub-abc", Holder: "hub-compact",
		Trigger: TriggerManual, Engine: EngineHub, ResolvedModel: "m1",
		Threshold: 160000, TailCount: 2,
		Now: func() time.Time { return time.Date(2026, 2, 1, 12, 0, 0, 0, time.UTC) },
		Summarize: func(context.Context, string, []map[string]any, string) (string, error) {
			return strings.Repeat("s", 4000), nil // summary far larger than the head
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if !result.Applied || result.Method != MethodDropCheckpoint {
		t.Fatalf("lossy fallback not named: %+v", result)
	}
	var apply map[string]any
	for _, req := range *calls {
		if req["op"] == "apply_transcript" {
			apply = req
		}
	}
	msgs, _ := apply["messages"].([]any)
	marker, _ := msgs[0].(map[string]any)["content"].(string)
	if !strings.Contains(marker, "checkpoint") || !strings.Contains(marker, "dropped") {
		t.Fatalf("drop marker unnamed: %q", marker)
	}
	j, _ := Open(dir)
	events, _ := j.Events("hub-abc")
	if len(events) != 1 || events[0].Method != MethodDropCheckpoint {
		t.Fatalf("journal: %+v", events)
	}
}

// When every candidate — summary, checkpoint drop — would grow the
// transcript, the request refuses outright: nothing commits, the class is
// bounded, the failure is journaled.
func TestHubCompactRefusesGrowth(t *testing.T) {
	calls := stubRunner(t, map[string]string{
		"estimate_tokens":  `{"ok":true,"tokens":40,"message_count":4}`,
		"lease":            `{"ok":true,"acquired":true}`,
		"lock":             `{"ok":true,"locked":true}`,
		"read_transcript":  `{"ok":true,"session_id":"hub-abc","watermark":4,"messages":[{"role":"user","content":"tiny"},{"role":"assistant","content":"` + strings.Repeat("t", 200) + `"},{"role":"user","content":"` + strings.Repeat("t", 200) + `"},{"role":"assistant","content":"last"}]}`,
		"apply_transcript": `{"ok":true,"active_count":4}`,
		"unlock":           `{"ok":true}`,
		"unlease":          `{"ok":true}`,
	})
	dir := t.TempDir()
	result, err := Compact(context.Background(), CompactOptions{
		StateDir: dir, SessionID: "hub-abc", Holder: "hub-compact",
		Trigger: TriggerManual, Engine: EngineHub, ResolvedModel: "m1",
		Threshold: 160000, TailCount: 3,
		Now: func() time.Time { return time.Date(2026, 2, 1, 12, 0, 0, 0, time.UTC) },
		Summarize: func(context.Context, string, []map[string]any, string) (string, error) {
			return strings.Repeat("s", 4000), nil
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if result.Applied || result.FailureClass != "would_grow" {
		t.Fatalf("growing compact committed: %+v", result)
	}
	for _, req := range *calls {
		if req["op"] == "apply_transcript" {
			t.Fatal("growth committed")
		}
	}
	j, _ := Open(dir)
	events, _ := j.Events("hub-abc")
	if len(events) != 1 || events[0].FailureClass != "would_grow" {
		t.Fatalf("growth refusal not journaled: %+v", events)
	}
}

// Role alternation is verified at the seam: when the retained tail opens
// with a user row, the summary carrier merges it (upstream's merged-handoff
// shape) instead of emitting two consecutive user messages.
func TestHubCompactAlternation(t *testing.T) {
	bigUser := strings.Repeat("u", 800000)
	var apply map[string]any
	calls := stubRunner(t, map[string]string{
		"estimate_tokens":  `{"ok":true,"tokens":300000,"message_count":6}`,
		"lease":            `{"ok":true,"acquired":true}`,
		"lock":             `{"ok":true,"locked":true}`,
		"read_transcript":  `{"ok":true,"session_id":"hub-abc","watermark":6,"messages":[{"role":"user","content":"` + bigUser + `"},{"role":"assistant","content":"m2"},{"role":"assistant","content":"m3"},{"role":"user","content":"tail-ask"},{"role":"assistant","content":"m5"},{"role":"assistant","content":"m6"}]}`,
		"apply_transcript": `{"ok":true,"active_count":3}`,
		"unlock":           `{"ok":true}`,
		"unlease":          `{"ok":true}`,
	})
	result, err := Compact(context.Background(), CompactOptions{
		StateDir: t.TempDir(), SessionID: "hub-abc", Holder: "hub-compact",
		Trigger: TriggerManual, Engine: EngineHub, ResolvedModel: "m1",
		Threshold: 160000, TailCount: 3,
		Now: func() time.Time { return time.Date(2026, 2, 1, 12, 0, 0, 0, time.UTC) },
		Summarize: func(context.Context, string, []map[string]any, string) (string, error) {
			return "condensed", nil
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if !result.Applied {
		t.Fatalf("compact: %+v", result)
	}
	for _, req := range *calls {
		if req["op"] == "apply_transcript" {
			apply = req
		}
	}
	msgs, _ := apply["messages"].([]any)
	for i := 1; i < len(msgs); i++ {
		prev, _ := msgs[i-1].(map[string]any)["role"].(string)
		cur, _ := msgs[i].(map[string]any)["role"].(string)
		if prev == "user" && cur == "user" {
			t.Fatalf("consecutive user rows at seam: %v", msgs)
		}
	}
	first, _ := msgs[0].(map[string]any)["content"].(string)
	if !strings.Contains(first, "tail-ask") {
		t.Fatalf("merged seam lost the live ask: %.200q", first)
	}
}

// The summary stage summarizes the already-pruned head — tool outputs are
// stubbed before the relay call, so the paid stage never re-reads the bulk.
func TestSummarySeesPrunedHead(t *testing.T) {
	big := strings.Repeat("x", 40000)
	stubRunner(t, map[string]string{
		"estimate_tokens":  `{"ok":true,"tokens":300000,"message_count":5}`,
		"lease":            `{"ok":true,"acquired":true}`,
		"lock":             `{"ok":true,"locked":true}`,
		"read_transcript":  `{"ok":true,"session_id":"hub-abc","watermark":5,"messages":[{"role":"tool","content":"` + big + `"},{"role":"user","content":"u2"},{"role":"user","content":"u3"},{"role":"assistant","content":"a4"},{"role":"assistant","content":"a5"}]}`,
		"apply_transcript": `{"ok":true,"active_count":4}`,
		"unlock":           `{"ok":true}`,
		"unlease":          `{"ok":true}`,
	})
	result, err := Compact(context.Background(), CompactOptions{
		StateDir: t.TempDir(), SessionID: "hub-abc", Holder: "hub-compact",
		Trigger: TriggerManual, Engine: EngineHub, ResolvedModel: "m1",
		Threshold: 160000, TailCount: 2, Args: CompactArgs{Focus: "x"},
		Now: func() time.Time { return time.Date(2026, 2, 1, 12, 0, 0, 0, time.UTC) },
		Summarize: func(_ context.Context, _ string, head []map[string]any, _ string) (string, error) {
			for _, m := range head {
				if c, _ := m["content"].(string); len(c) > 20000 {
					t.Fatalf("summarizer saw unpruned bulk: %d chars", len(c))
				}
			}
			return "condensed", nil
		},
	})
	if err != nil || !result.Applied {
		t.Fatalf("compact: %+v err=%v", result, err)
	}
}

// A speculative summary parked by the sweep precompute commits without a
// fresh model call when model and watermark still match — the cascade's
// precomputed_swap stage.
func TestCompactPrecomputedSwap(t *testing.T) {
	t0 := time.Date(2026, 2, 1, 12, 0, 0, 0, time.UTC)
	dir := t.TempDir()
	bigUser := strings.Repeat("u", 800000)
	if err := saveSessionSweep(dir, "hub-abc", SessionSweep{
		Held: &HeldSummary{Model: "m1", Watermark: 6, Summary: "parked", At: t0},
	}); err != nil {
		t.Fatal(err)
	}
	calls := stubRunner(t, map[string]string{
		"estimate_tokens":  `{"ok":true,"tokens":300000,"message_count":6}`,
		"lease":            `{"ok":true,"acquired":true}`,
		"lock":             `{"ok":true,"locked":true}`,
		"read_transcript":  `{"ok":true,"session_id":"hub-abc","watermark":6,"messages":[{"role":"user","content":"` + bigUser + `"},{"role":"assistant","content":"a2"},{"role":"user","content":"u3"},{"role":"assistant","content":"a4"},{"role":"assistant","content":"a5"},{"role":"assistant","content":"a6"}]}`,
		"apply_transcript": `{"ok":true,"active_count":4}`,
		"unlock":           `{"ok":true}`,
		"unlease":          `{"ok":true}`,
	})
	summarized := false
	result, err := Compact(context.Background(), CompactOptions{
		StateDir: dir, SessionID: "hub-abc", Holder: "hub-compact",
		Trigger: TriggerManual, Engine: EngineHub, ResolvedModel: "m1",
		Threshold: 160000, TailCount: 3,
		Now: func() time.Time { return t0 },
		Summarize: func(context.Context, string, []map[string]any, string) (string, error) {
			summarized = true
			return "fresh", nil
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if !result.Applied || result.Method != MethodPrecomputed || summarized {
		t.Fatalf("precomputed swap: %+v summarized=%v", result, summarized)
	}
	for _, req := range *calls {
		if req["op"] != "apply_transcript" {
			continue
		}
		first, _ := req["messages"].([]any)[0].(map[string]any)
		if c, _ := first["content"].(string); !strings.Contains(c, "parked") {
			t.Fatalf("carrier lost the held summary: %.100q", c)
		}
	}
	j, _ := Open(dir)
	events, _ := j.Events("hub-abc")
	if len(events) != 1 || events[0].Method != MethodPrecomputed {
		t.Fatalf("journal: %+v", events)
	}
	if held := loadSessionSweep(dir, "hub-abc").Held; held != nil {
		t.Fatalf("consumed hold still parked: %+v", held)
	}
}

// An explicit prune on a transcript with nothing prunable refuses cleanly —
// zero LLM calls, zero mutation, a bounded class.
func TestPruneOnlyZeroCalls(t *testing.T) {
	calls := stubRunner(t, map[string]string{
		"estimate_tokens":  `{"ok":true,"tokens":300000,"message_count":5}`,
		"lease":            `{"ok":true,"acquired":true}`,
		"lock":             `{"ok":true,"locked":true}`,
		"read_transcript":  `{"ok":true,"session_id":"hub-abc","watermark":5,"messages":[{"role":"user","content":"u1"},{"role":"assistant","content":"a2"},{"role":"user","content":"u3"},{"role":"assistant","content":"a4"},{"role":"assistant","content":"a5"}]}`,
		"apply_transcript": `{"ok":true,"active_count":5}`,
		"unlock":           `{"ok":true}`,
		"unlease":          `{"ok":true}`,
	})
	summarized := false
	result, err := Compact(context.Background(), CompactOptions{
		StateDir: t.TempDir(), SessionID: "hub-abc", Holder: "hub-compact",
		Trigger: TriggerManual, Engine: EngineHub, ResolvedModel: "m1",
		Threshold: 160000, TailCount: 2, Args: CompactArgs{Prune: true},
		Now: func() time.Time { return time.Date(2026, 2, 1, 12, 0, 0, 0, time.UTC) },
		Summarize: func(context.Context, string, []map[string]any, string) (string, error) {
			summarized = true
			return "x", nil
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if result.Applied || result.FailureClass != "nothing_to_prune" || summarized {
		t.Fatalf("empty prune: %+v summarized=%v", result, summarized)
	}
	for _, req := range *calls {
		if req["op"] == "apply_transcript" {
			t.Fatal("empty prune committed a no-op rewrite")
		}
	}
}

// Before the lossy checkpoint, the cascade retries with a shrunk tail: the
// summary survives and the method names the retry.
func TestCompactShrinkRetry(t *testing.T) {
	head := `[{"role":"user","content":"` + strings.Repeat("h", 900) + `"},{"role":"assistant","content":"` + strings.Repeat("a", 900) + `"},{"role":"user","content":"` + strings.Repeat("h", 900) + `"},{"role":"assistant","content":"` + strings.Repeat("a", 900) + `"},`
	calls := stubRunner(t, map[string]string{
		"estimate_tokens":  `{"ok":true,"tokens":1300,"message_count":6}`,
		"lease":            `{"ok":true,"acquired":true}`,
		"lock":             `{"ok":true,"locked":true}`,
		"read_transcript":  `{"ok":true,"session_id":"hub-abc","watermark":6,"messages":` + head + `{"role":"assistant","content":"` + strings.Repeat("t", 800) + `"},{"role":"assistant","content":"` + strings.Repeat("t", 800) + `"}]}`,
		"apply_transcript": `{"ok":true,"active_count":2}`,
		"unlock":           `{"ok":true}`,
		"unlease":          `{"ok":true}`,
	})
	result, err := Compact(context.Background(), CompactOptions{
		StateDir: t.TempDir(), SessionID: "hub-abc", Holder: "hub-compact",
		Trigger: TriggerManual, Engine: EngineHub, ResolvedModel: "m1",
		Threshold: 160000, TailCount: 2,
		Now: func() time.Time { return time.Date(2026, 2, 1, 12, 0, 0, 0, time.UTC) },
		Summarize: func(context.Context, string, []map[string]any, string) (string, error) {
			return strings.Repeat("s", 4000), nil
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if !result.Applied || result.Method != MethodShrinkRetry {
		t.Fatalf("shrink retry: %+v", result)
	}
	for _, req := range *calls {
		if req["op"] != "apply_transcript" {
			continue
		}
		msgs, _ := req["messages"].([]any)
		if len(msgs) != 2 {
			t.Fatalf("shrunk set = %d messages, want carrier+1", len(msgs))
		}
	}
}

// An apply failure — the upstream lock lapsed mid-flight — is a bounded
// class on the result, never a raw transport error.
func TestCompactApplyFailureClassed(t *testing.T) {
	bigUser := strings.Repeat("u", 800000)
	stubRunner(t, map[string]string{
		"estimate_tokens":  `{"ok":true,"tokens":300000,"message_count":4}`,
		"lease":            `{"ok":true,"acquired":true}`,
		"lock":             `{"ok":true,"locked":true}`,
		"read_transcript":  `{"ok":true,"session_id":"hub-abc","watermark":4,"messages":[{"role":"user","content":"` + bigUser + `"},{"role":"assistant","content":"a2"},{"role":"user","content":"u3"},{"role":"assistant","content":"a4"}]}`,
		"apply_transcript": `{"ok":false,"error":"locked"}`,
		"unlock":           `{"ok":true}`,
		"unlease":          `{"ok":true}`,
	})
	result, err := Compact(context.Background(), CompactOptions{
		StateDir: t.TempDir(), SessionID: "hub-abc", Holder: "hub-compact",
		Trigger: TriggerManual, Engine: EngineHub, ResolvedModel: "m1",
		Threshold: 160000, TailCount: 2,
		Now: func() time.Time { return time.Date(2026, 2, 1, 12, 0, 0, 0, time.UTC) },
		Summarize: func(context.Context, string, []map[string]any, string) (string, error) {
			return "s", nil
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if result.Applied || result.FailureClass != "apply_failed" {
		t.Fatalf("apply failure: %+v", result)
	}
}

// The native engine delegates through the sidecar: the turn lease and the
// compression lock are still the hub's contract, the compress itself is
// upstream's — args translate to upstream vocabulary (keep_last, focus).
func TestNativeCompressTranslatesArgs(t *testing.T) {
	var native map[string]any
	calls := stubRunner(t, map[string]string{
		"estimate_tokens":  `{"ok":true,"tokens":300000,"message_count":10}`,
		"lease":            `{"ok":true,"acquired":true}`,
		"lock":             `{"ok":true,"locked":true}`,
		"native_compress":  `{"ok":true,"applied":true,"active_count":4,"messages_before":10,"retained_tail":4,"tokens_after_est":900}`,
		"read_transcript":  `{"ok":true,"session_id":"hub-abc","watermark":10,"messages":[{"role":"user","content":"u1"},{"role":"assistant","content":"a2"}]}`,
		"apply_transcript": `{"ok":true,"active_count":3}`,
		"unlock":           `{"ok":true}`,
		"unlease":          `{"ok":true}`,
	})
	dir := t.TempDir()
	result, err := Compact(context.Background(), CompactOptions{
		StateDir: dir, SessionID: "hub-abc", Holder: "hub-compact",
		Trigger: TriggerManual, Engine: EngineNative, ResolvedModel: "m1",
		Threshold: 160000, Args: CompactArgs{Here: 2, Focus: "deploy"},
		Now: func() time.Time { return time.Date(2026, 2, 1, 12, 0, 0, 0, time.UTC) },
	})
	if err != nil {
		t.Fatal(err)
	}
	if !result.Applied || result.RetainedTail != 4 {
		t.Fatalf("native result: %+v", result)
	}
	if result.MessagesBefore != 10 || result.MessagesAfter != 4 {
		t.Fatalf("native counts: %+v", result)
	}
	for _, req := range *calls {
		if req["op"] == "native_compress" {
			native = req
		}
	}
	if native == nil {
		t.Fatal("native engine never reached the sidecar")
	}
	if k, _ := native["keep_last"].(float64); int(k) != 2 {
		t.Fatalf("keep_last = %v", native["keep_last"])
	}
	if native["focus"] != "deploy" || native["holder"] != "hub-compact" {
		t.Fatalf("native args: %v", native)
	}
	j, _ := Open(dir)
	events, _ := j.Events("hub-abc")
	if len(events) != 1 || events[0].Engine != EngineNative || events[0].Trigger != TriggerManual {
		t.Fatalf("journal: %+v", events)
	}
}

// A pinned upstream that lacks the compress machinery answers
// `unsupported` — the result names the class and the hub engine does not
// silently take over.
func TestNativeUnsupportedOnMissingSymbol(t *testing.T) {
	calls := stubRunner(t, map[string]string{
		"estimate_tokens": `{"ok":true,"tokens":300000,"message_count":10}`,
		"lease":           `{"ok":true,"acquired":true}`,
		"lock":            `{"ok":true,"locked":true}`,
		"native_compress": `{"ok":false,"error":"unsupported"}`,
		"unlock":          `{"ok":true}`,
		"unlease":         `{"ok":true}`,
	})
	summarized := false
	result, err := Compact(context.Background(), CompactOptions{
		StateDir: t.TempDir(), SessionID: "hub-abc", Holder: "hub-compact",
		Trigger: TriggerManual, Engine: EngineNative, ResolvedModel: "m1",
		Threshold: 160000,
		Now:       func() time.Time { return time.Date(2026, 2, 1, 12, 0, 0, 0, time.UTC) },
		Summarize: func(context.Context, string, []map[string]any, string) (string, error) {
			summarized = true
			return "hub took over", nil
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if result.Applied || result.FailureClass != RefusalUnsupported || summarized {
		t.Fatalf("unsupported native fell back to hub: %+v summarized=%v", result, summarized)
	}
	for _, req := range *calls {
		if req["op"] == "apply_transcript" {
			t.Fatal("unsupported native still mutated")
		}
	}
}

// A native compress that changed nothing is not a commit: nothing journals
// and the bounded class the sidecar named surfaces verbatim.
func TestNativeNoOpNotJournaled(t *testing.T) {
	stubRunner(t, map[string]string{
		"estimate_tokens": `{"ok":true,"tokens":500,"message_count":3}`,
		"lease":           `{"ok":true,"acquired":true}`,
		"lock":            `{"ok":true,"locked":true}`,
		"native_compress": `{"ok":true,"applied":false,"failure_class":"nothing_to_compress"}`,
		"unlock":          `{"ok":true}`,
		"unlease":         `{"ok":true}`,
	})
	dir := t.TempDir()
	result, err := Compact(context.Background(), CompactOptions{
		StateDir: dir, SessionID: "hub-abc", Holder: "hub-compact",
		Trigger: TriggerManual, Engine: EngineNative, ResolvedModel: "m1",
		Now: func() time.Time { return time.Date(2026, 2, 1, 12, 0, 0, 0, time.UTC) },
	})
	if err != nil {
		t.Fatal(err)
	}
	if result.Applied || result.FailureClass != "nothing_to_compress" {
		t.Fatalf("native no-op: %+v", result)
	}
	j, _ := Open(dir)
	events, _ := j.Events("hub-abc")
	if len(events) != 0 {
		t.Fatalf("no-op journaled a commit: %+v", events)
	}
}

// A native preview reports the native method and the sidecar's counts —
// and still touches neither lease, lock nor transcript.
func TestNativePreviewReportsNativeMethod(t *testing.T) {
	calls := stubRunner(t, map[string]string{
		"estimate_tokens": `{"ok":true,"tokens":300000,"message_count":8}`,
		"native_compress": `{"ok":true,"applied":false,"preview":true,"messages_before":8,"active_count":8,"retained_tail":3}`,
	})
	result, err := Compact(context.Background(), CompactOptions{
		StateDir: t.TempDir(), SessionID: "hub-abc", Holder: "hub-compact",
		Trigger: TriggerManual, Engine: EngineNative, Args: CompactArgs{Preview: true},
		Now: func() time.Time { return time.Date(2026, 2, 1, 12, 0, 0, 0, time.UTC) },
	})
	if err != nil {
		t.Fatal(err)
	}
	if result.Applied || result.WouldMethod != MethodArchiveCompct {
		t.Fatalf("native preview: %+v", result)
	}
	if result.MessagesBefore != 8 || result.RetainedTail != 3 {
		t.Fatalf("native preview counts: %+v", result)
	}
	for _, c := range *calls {
		if op := c["op"]; op != "estimate_tokens" && op != "native_compress" {
			t.Fatalf("preview touched %v", op)
		}
	}
}

// A queued compact is a bounded pending record: written once while busy,
// taken once by the post-job hook — a replayed request can never re-run it.
func TestPendingCompactQueue(t *testing.T) {
	dir := t.TempDir()
	at := time.Date(2026, 2, 1, 12, 0, 0, 0, time.UTC)
	pending := PendingCompact{Args: CompactArgs{Here: 2, Focus: "deploy"}, Engine: EngineNative, At: at}
	if err := QueueCompact(dir, "hub-abc", pending); err != nil {
		t.Fatal(err)
	}
	got := TakePending(dir, "hub-abc")
	if got == nil || got.Engine != EngineNative || got.Args.Here != 2 || got.Args.Focus != "deploy" || !got.At.Equal(at) {
		t.Fatalf("pending: %+v", got)
	}
	if TakePending(dir, "hub-abc") != nil {
		t.Fatal("pending taken twice")
	}
}

// `/compact preview prune` previews the prune stage — the arg pins the
// would-method even when pruning alone wouldn't fit under the threshold.
func TestPreviewPruneReportsPruneOnly(t *testing.T) {
	stubRunner(t, map[string]string{
		"estimate_tokens": `{"ok":true,"tokens":300000,"message_count":5}`,
		"read_transcript": `{"ok":true,"session_id":"hub-abc","watermark":5,"messages":[{"role":"user","content":"` + strings.Repeat("u", 800000) + `"},{"role":"tool","content":"` + strings.Repeat("x", 40000) + `"},{"role":"assistant","content":"a3"},{"role":"user","content":"u4"},{"role":"assistant","content":"a5"}]}`,
	})
	result, err := Compact(context.Background(), CompactOptions{
		StateDir: t.TempDir(), SessionID: "hub-abc", Holder: "hub-compact",
		Trigger: TriggerManual, Engine: EngineHub, ResolvedModel: "m1",
		Threshold: 160000, TailCount: 2, Args: CompactArgs{Preview: true, Prune: true},
		Now: func() time.Time { return time.Date(2026, 2, 1, 12, 0, 0, 0, time.UTC) },
	})
	if err != nil {
		t.Fatal(err)
	}
	if result.Applied || result.WouldMethod != MethodPruneOnly {
		t.Fatalf("preview prune: %+v", result)
	}
}

// `/compact here 2` keeps the last two user exchanges verbatim — the split
// is boundary-aware like upstream, not a raw message count.
func TestCompactHereKeepsExchanges(t *testing.T) {
	bigUser := strings.Repeat("u", 800000)
	var apply map[string]any
	calls := stubRunner(t, map[string]string{
		"estimate_tokens":  `{"ok":true,"tokens":300000,"message_count":8}`,
		"lease":            `{"ok":true,"acquired":true}`,
		"lock":             `{"ok":true,"locked":true}`,
		"read_transcript":  `{"ok":true,"session_id":"hub-abc","watermark":8,"messages":[{"role":"user","content":"` + bigUser + `"},{"role":"assistant","content":"a2"},{"role":"user","content":"u3"},{"role":"assistant","content":"a4"},{"role":"user","content":"u5"},{"role":"assistant","content":"a6"},{"role":"user","content":"u7"},{"role":"assistant","content":"a8"}]}`,
		"apply_transcript": `{"ok":true,"active_count":5}`,
		"unlock":           `{"ok":true}`,
		"unlease":          `{"ok":true}`,
	})
	result, err := Compact(context.Background(), CompactOptions{
		StateDir: t.TempDir(), SessionID: "hub-abc", Holder: "hub-compact",
		Trigger: TriggerManual, Engine: EngineHub, ResolvedModel: "m1",
		Threshold: 160000, TailCount: 2, Args: CompactArgs{Here: 2},
		Now: func() time.Time { return time.Date(2026, 2, 1, 12, 0, 0, 0, time.UTC) },
		Summarize: func(_ context.Context, _ string, head []map[string]any, _ string) (string, error) {
			if len(head) != 4 {
				t.Fatalf("here 2 summarized %d messages, want 4", len(head))
			}
			return "condensed", nil
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if !result.Applied || result.RetainedTail != 4 {
		t.Fatalf("here 2: %+v", result)
	}
	for _, req := range *calls {
		if req["op"] == "apply_transcript" {
			apply = req
		}
	}
	msgs, _ := apply["messages"].([]any)
	last, _ := msgs[len(msgs)-1].(map[string]any)["content"].(string)
	if last != "a8" {
		t.Fatalf("tail end wrong: %v", msgs[len(msgs)-1])
	}
	// u5 leads the tail, so it merges into the carrier row at the seam.
	first, _ := msgs[0].(map[string]any)["content"].(string)
	if !strings.Contains(first, "u5") {
		t.Fatalf("seam merge lost the tail's first exchange: %.200q", first)
	}
	if c, _ := msgs[1].(map[string]any)["content"].(string); c != "a6" {
		t.Fatalf("post-seam message should be a6, got %v", msgs[1])
	}
}

// `/compact prune` is the explicit hygiene mode: it commits the pruned set
// even when the result stays above threshold — the owner asked for stubbing,
// not for fitting a bound — and it never spends a summary call.
func TestPruneArgForcesStage(t *testing.T) {
	big := strings.Repeat("x", 40000)
	bigUser := strings.Repeat("u", 800000)
	stubRunner(t, map[string]string{
		"estimate_tokens":  `{"ok":true,"tokens":300000,"message_count":5}`,
		"lease":            `{"ok":true,"acquired":true}`,
		"lock":             `{"ok":true,"locked":true}`,
		"read_transcript":  `{"ok":true,"session_id":"hub-abc","watermark":5,"messages":[{"role":"user","content":"` + bigUser + `"},{"role":"tool","content":"` + big + `"},{"role":"assistant","content":"m3"},{"role":"user","content":"m4"},{"role":"assistant","content":"m5"}]}`,
		"apply_transcript": `{"ok":true,"active_count":5}`,
		"unlock":           `{"ok":true}`,
		"unlease":          `{"ok":true}`,
	})
	dir := t.TempDir()
	summarized := false
	result, err := Compact(context.Background(), CompactOptions{
		StateDir: dir, SessionID: "hub-abc", Holder: "hub-compact",
		Trigger: TriggerManual, Engine: EngineHub, ResolvedModel: "m1",
		Threshold: 160000, TailCount: 2, Args: CompactArgs{Prune: true},
		Now: func() time.Time { return time.Date(2026, 2, 1, 12, 0, 0, 0, time.UTC) },
		Summarize: func(context.Context, string, []map[string]any, string) (string, error) {
			summarized = true
			return "x", nil
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if !result.Applied || result.Method != MethodPruneOnly || summarized {
		t.Fatalf("prune arg: %+v summarized=%v", result, summarized)
	}
	j, _ := Open(dir)
	events, _ := j.Events("hub-abc")
	if len(events) != 1 || events[0].Method != MethodPruneOnly {
		t.Fatalf("journal: %+v", events)
	}
}
