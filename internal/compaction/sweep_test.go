package compaction

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"
)

// stubRunner records every sidecar request and answers per op.
func stubRunner(t *testing.T, replies map[string]string) *[]map[string]any {
	t.Helper()
	var calls []map[string]any
	old := Runner
	Runner = func(_ context.Context, payload []byte) ([]byte, error) {
		var req map[string]any
		if err := json.Unmarshal(payload, &req); err != nil {
			return nil, err
		}
		calls = append(calls, req)
		op, _ := req["op"].(string)
		body, ok := replies[op]
		if !ok {
			return []byte(`{"ok":false,"error":"internal"}`), nil
		}
		return []byte(body), nil
	}
	t.Cleanup(func() { Runner = old })
	return &calls
}

// Below the bound nothing may happen: no lock, no lease, no summarizer call,
// no journal row. The sweep is a quality knob, not a background tax.
func TestSweepSkipsUnderThreshold(t *testing.T) {
	calls := stubRunner(t, map[string]string{
		"estimate_tokens": `{"ok":true,"tokens":1000,"message_count":5}`,
	})
	dir := t.TempDir()
	summarized := false
	result, err := Sweep(context.Background(), SweepOptions{
		StateDir: dir, SessionID: "hub-abc", ResolvedModel: "m1",
		Gate: Gate{Enabled: true, Threshold: 160000, CooldownSeconds: 600},
		Now:  func() time.Time { return time.Date(2026, 2, 1, 12, 0, 0, 0, time.UTC) },
		Summarize: func(context.Context, string, []map[string]any) (string, error) {
			summarized = true
			return "summary", nil
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if result.Acted || result.Refusal != RefusalUnderThreshold {
		t.Fatalf("under-threshold sweep acted: %+v", result)
	}
	if summarized {
		t.Fatal("summarizer ran below threshold")
	}
	for _, req := range *calls {
		if req["op"] != "estimate_tokens" {
			t.Fatalf("sweep touched state under threshold: %v", req)
		}
	}
	j, _ := Open(dir)
	events, _ := j.Events("hub-abc")
	if len(events) != 0 {
		t.Fatalf("skip left journal rows: %v", events)
	}
}

// The turn lease is the "no active run" witness: when the acquisition is
// refused the sweep must not touch the session at all.
func TestSweepNeverDuringRun(t *testing.T) {
	calls := stubRunner(t, map[string]string{
		"estimate_tokens": `{"ok":true,"tokens":300000,"message_count":80}`,
		"lease":           `{"ok":true,"acquired":false}`,
	})
	dir := t.TempDir()
	result, err := Sweep(context.Background(), SweepOptions{
		StateDir: dir, SessionID: "hub-abc", ResolvedModel: "m1",
		Gate: Gate{Enabled: true, Threshold: 160000, CooldownSeconds: 600},
		Now:  func() time.Time { return time.Date(2026, 2, 1, 12, 0, 0, 0, time.UTC) },
	})
	if err != nil {
		t.Fatal(err)
	}
	if result.Acted || result.Refusal != RefusalLeaseHeld {
		t.Fatalf("sweep during active run: %+v", result)
	}
	for _, req := range *calls {
		op, _ := req["op"].(string)
		if op != "estimate_tokens" && op != "lease" {
			t.Fatalf("sweep mutated session under held lease: %v", req)
		}
	}
}

// A boundary inside the cooldown window refuses before touching the lease —
// cooldown is the cheap state check and must not churn the session.
func TestSweepCooldown(t *testing.T) {
	t0 := time.Date(2026, 2, 1, 12, 0, 0, 0, time.UTC)
	dir := t.TempDir()
	if err := saveSessionSweep(dir, "hub-abc", SessionSweep{LastAttemptAt: t0}); err != nil {
		t.Fatal(err)
	}
	calls := stubRunner(t, map[string]string{
		"estimate_tokens": `{"ok":true,"tokens":300000,"message_count":80}`,
		"lease":           `{"ok":true,"acquired":true}`,
	})
	result, err := Sweep(context.Background(), SweepOptions{
		StateDir: dir, SessionID: "hub-abc", ResolvedModel: "m1",
		Gate: Gate{Enabled: true, Threshold: 160000, CooldownSeconds: 600},
		Now:  func() time.Time { return t0.Add(300 * time.Second) },
	})
	if err != nil {
		t.Fatal(err)
	}
	if result.Acted || result.Refusal != RefusalCooldown {
		t.Fatalf("sweep inside cooldown: %+v", result)
	}
	for _, req := range *calls {
		if req["op"] == "lease" {
			t.Fatal("cooldown sweep touched the lease")
		}
	}
	j, _ := Open(dir)
	events, _ := j.Events("hub-abc")
	if len(events) != 1 || events[0].FailureClass != RefusalCooldown {
		t.Fatalf("cooldown refusal not journaled: %+v", events)
	}
}

// An over-threshold boundary compacts through the full lease → lock → read →
// summarize → apply → unlock → unlease sequence, journals a real event and
// arms the next-turn notice.
func TestSweepCompactsOverThreshold(t *testing.T) {
	t0 := time.Date(2026, 2, 1, 12, 0, 0, 0, time.UTC)
	calls := stubRunner(t, map[string]string{
		"estimate_tokens":  `{"ok":true,"tokens":300000,"message_count":80}`,
		"lease":            `{"ok":true,"acquired":true}`,
		"lock":             `{"ok":true,"locked":true}`,
		"read_transcript":  `{"ok":true,"session_id":"hub-abc","watermark":80,"messages":[{"role":"user","content":"m1"},{"role":"assistant","content":"m2"},{"role":"user","content":"m3"},{"role":"assistant","content":"m4"},{"role":"user","content":"m5"},{"role":"assistant","content":"m6"}]}`,
		"apply_transcript": `{"ok":true,"active_count":5}`,
		"unlock":           `{"ok":true}`,
		"unlease":          `{"ok":true}`,
	})
	dir := t.TempDir()
	var summaryModel string
	result, err := Sweep(context.Background(), SweepOptions{
		StateDir: dir, SessionID: "hub-abc", ResolvedModel: "m1", TailCount: 3,
		Gate: Gate{Enabled: true, Threshold: 160000, CooldownSeconds: 600},
		Now:  func() time.Time { return t0 },
		Summarize: func(_ context.Context, model string, messages []map[string]any) (string, error) {
			summaryModel = model
			return "condensed", nil
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if !result.Acted || result.Event == nil {
		t.Fatalf("over-threshold sweep did not act: %+v", result)
	}
	if ev := result.Event; ev.Trigger != TriggerAutoSweep || ev.Engine != EngineHub ||
		ev.Method != MethodArchiveCompct || ev.TokensBefore == nil || *ev.TokensBefore != 300000 {
		t.Fatalf("bad event: %+v", ev)
	}
	if summaryModel != "m1" {
		t.Fatalf("summarizer got model %q", summaryModel)
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
	if len(msgs) != 4 { // summary carrier + 3 tail copies
		t.Fatalf("apply messages = %v", msgs)
	}
	first, _ := msgs[0].(map[string]any)
	if first["_compressed_summary"] != true || first["role"] != "user" {
		t.Fatalf("summary carrier malformed: %v", first)
	}
	if got, _ := msgs[3].(map[string]any)["content"].(string); got != "m6" {
		t.Fatalf("tail copy wrong: %v", msgs[3])
	}
	j, _ := Open(dir)
	events, _ := j.Events("hub-abc")
	if len(events) != 1 || events[0].Trigger != TriggerAutoSweep {
		t.Fatalf("journal missing sweep event: %v", events)
	}
	st := loadSessionSweep(dir, "hub-abc")
	if !st.LastAttemptAt.Equal(t0) {
		t.Fatalf("sweep state not updated: %+v", st)
	}
}

// Precompute fires once the estimate crosses the precompute ratio of the
// sweep threshold: the summary parks in state.Held for the next attempt.
func TestPrecomputeStartsAtRatio(t *testing.T) {
	dir := t.TempDir()
	summarized := false
	stubRunner(t, map[string]string{
		"estimate_tokens": `{"ok":true,"tokens":140000,"message_count":60}`,
		"read_transcript": `{"ok":true,"session_id":"hub-abc","watermark":60,"messages":[{"role":"user","content":"m1"}]}`,
	})
	result, err := Sweep(context.Background(), SweepOptions{
		StateDir: dir, SessionID: "hub-abc", ResolvedModel: "m1",
		Gate:  Gate{Enabled: true, Precompute: true, Threshold: 160000, CooldownSeconds: 600},
		Now:   func() time.Time { return time.Date(2026, 2, 1, 12, 0, 0, 0, time.UTC) },
		Spawn: func(fn func()) { fn() }, // deterministic background for the test
		Summarize: func(context.Context, string, []map[string]any) (string, error) {
			summarized = true
			return "parked", nil
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if result.Acted {
		t.Fatal("precompute boundary acted")
	}
	if !summarized {
		t.Fatal("no background summary at 140k/160k")
	}
	held := loadSessionSweep(dir, "hub-abc").Held
	if held == nil || held.Summary != "parked" || held.Model != "m1" || held.Watermark != 60 {
		t.Fatalf("held summary wrong: %+v", held)
	}
}

// A summary parked under model m1 must never commit under m2 — summaries are
// model-specific artifacts. The swap discards the hold and summarizes fresh.
func TestPrecomputedSwapModelGuard(t *testing.T) {
	t0 := time.Date(2026, 2, 1, 12, 0, 0, 0, time.UTC)
	dir := t.TempDir()
	if err := saveSessionSweep(dir, "hub-abc", SessionSweep{
		Held: &HeldSummary{Model: "m1", Watermark: 80, Summary: "stale", At: t0},
	}); err != nil {
		t.Fatal(err)
	}
	stubRunner(t, map[string]string{
		"estimate_tokens":  `{"ok":true,"tokens":300000,"message_count":80}`,
		"lease":            `{"ok":true,"acquired":true}`,
		"lock":             `{"ok":true,"locked":true}`,
		"read_transcript":  `{"ok":true,"session_id":"hub-abc","watermark":80,"messages":[{"role":"user","content":"m1"}]}`,
		"apply_transcript": `{"ok":true,"active_count":2}`,
		"unlock":           `{"ok":true}`,
		"unlease":          `{"ok":true}`,
	})
	var freshModel string
	result, err := Sweep(context.Background(), SweepOptions{
		StateDir: dir, SessionID: "hub-abc", ResolvedModel: "m2",
		Gate: Gate{Enabled: true, Threshold: 160000, CooldownSeconds: 600},
		Now:  func() time.Time { return t0 },
		Summarize: func(_ context.Context, model string, _ []map[string]any) (string, error) {
			freshModel = model
			return "fresh", nil
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if !result.Acted || freshModel != "m2" {
		t.Fatalf("model swap reused stale hold: acted=%v fresh=%q", result.Acted, freshModel)
	}
}

// The matching hold — same model, same watermark — commits without spending a
// fresh summary call.
func TestPrecomputedHeldReuse(t *testing.T) {
	t0 := time.Date(2026, 2, 1, 12, 0, 0, 0, time.UTC)
	dir := t.TempDir()
	if err := saveSessionSweep(dir, "hub-abc", SessionSweep{
		Held: &HeldSummary{Model: "m1", Watermark: 80, Summary: "parked", At: t0},
	}); err != nil {
		t.Fatal(err)
	}
	calls := stubRunner(t, map[string]string{
		"estimate_tokens":  `{"ok":true,"tokens":300000,"message_count":80}`,
		"lease":            `{"ok":true,"acquired":true}`,
		"lock":             `{"ok":true,"locked":true}`,
		"read_transcript":  `{"ok":true,"session_id":"hub-abc","watermark":80,"messages":[{"role":"user","content":"m1"}]}`,
		"apply_transcript": `{"ok":true,"active_count":2}`,
		"unlock":           `{"ok":true}`,
		"unlease":          `{"ok":true}`,
	})
	summarized := false
	result, err := Sweep(context.Background(), SweepOptions{
		StateDir: dir, SessionID: "hub-abc", ResolvedModel: "m1",
		Gate: Gate{Enabled: true, Threshold: 160000, CooldownSeconds: 600},
		Now:  func() time.Time { return t0 },
		Summarize: func(context.Context, string, []map[string]any) (string, error) {
			summarized = true
			return "fresh", nil
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if !result.Acted || summarized {
		t.Fatalf("matching hold wasted a summary call: acted=%v summarized=%v", result.Acted, summarized)
	}
	for _, req := range *calls {
		if req["op"] != "apply_transcript" {
			continue
		}
		first, _ := req["messages"].([]any)[0].(map[string]any)
		content, _ := first["content"].(string)
		if !strings.Contains(content, "parked") {
			t.Fatalf("carrier lost the held summary: %q", content)
		}
	}
}

// Two consecutive background failures trip the breaker: the session stops
// spending summarizer calls until a real compaction resets the counter.
func TestPrecomputeFailureCap(t *testing.T) {
	dir := t.TempDir()
	if err := saveSessionSweep(dir, "hub-abc", SessionSweep{ConsecFailures: 2}); err != nil {
		t.Fatal(err)
	}
	stubRunner(t, map[string]string{
		"estimate_tokens": `{"ok":true,"tokens":140000,"message_count":60}`,
		"read_transcript": `{"ok":true,"session_id":"hub-abc","watermark":60,"messages":[{"role":"user","content":"m1"}]}`,
	})
	summarized := false
	_, err := Sweep(context.Background(), SweepOptions{
		StateDir: dir, SessionID: "hub-abc", ResolvedModel: "m1",
		Gate:  Gate{Enabled: true, Precompute: true, Threshold: 160000, CooldownSeconds: 600},
		Now:   func() time.Time { return time.Date(2026, 2, 1, 12, 0, 0, 0, time.UTC) },
		Spawn: func(fn func()) { fn() },
		Summarize: func(context.Context, string, []map[string]any) (string, error) {
			summarized = true
			return "", nil
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if summarized {
		t.Fatal("breaker open but precompute still ran")
	}
}

// A failed background attempt — transcript read or summarizer — increments
// the breaker so the next boundary sees it.
func TestPrecomputeFailureIncrements(t *testing.T) {
	dir := t.TempDir()
	replies := map[string]string{
		"estimate_tokens": `{"ok":true,"tokens":140000,"message_count":60}`,
		"read_transcript": `{"ok":false,"error":"internal"}`,
	}
	stubRunner(t, replies)
	failSummarize := false
	opts := func() SweepOptions {
		return SweepOptions{
			StateDir: dir, SessionID: "hub-abc", ResolvedModel: "m1",
			Gate:  Gate{Enabled: true, Precompute: true, Threshold: 160000, CooldownSeconds: 600},
			Now:   func() time.Time { return time.Date(2026, 2, 1, 12, 0, 0, 0, time.UTC) },
			Spawn: func(fn func()) { fn() },
			Summarize: func(context.Context, string, []map[string]any) (string, error) {
				if failSummarize {
					return "", context.DeadlineExceeded
				}
				return "", nil
			},
		}
	}
	if _, err := Sweep(context.Background(), opts()); err != nil {
		t.Fatal(err)
	}
	replies["read_transcript"] = `{"ok":true,"session_id":"hub-abc","watermark":60,"messages":[{"role":"user","content":"m1"}]}`
	failSummarize = true
	if _, err := Sweep(context.Background(), opts()); err != nil {
		t.Fatal(err)
	}
	if got := loadSessionSweep(dir, "hub-abc").ConsecFailures; got != 2 {
		t.Fatalf("failures not counted: %d", got)
	}
}

// A summary that lands late must never stall the job boundary: Sweep returns
// while the background call is still in flight, and the result parks for a
// later attempt rather than forcing a wait.
func TestPrecomputedLateNeverBlocks(t *testing.T) {
	dir := t.TempDir()
	stubRunner(t, map[string]string{
		"estimate_tokens": `{"ok":true,"tokens":140000,"message_count":60}`,
		"read_transcript": `{"ok":true,"session_id":"hub-abc","watermark":60,"messages":[{"role":"user","content":"m1"}]}`,
	})
	release := make(chan struct{})
	started := make(chan struct{}, 1)
	done := make(chan struct{})
	go func() {
		result, err := Sweep(context.Background(), SweepOptions{
			StateDir: dir, SessionID: "hub-abc", ResolvedModel: "m1",
			Gate: Gate{Enabled: true, Precompute: true, Threshold: 160000, CooldownSeconds: 600},
			Now:  func() time.Time { return time.Date(2026, 2, 1, 12, 0, 0, 0, time.UTC) },
			// Spawn nil → real goroutine; the summarizer blocks on release.
			Summarize: func(context.Context, string, []map[string]any) (string, error) {
				started <- struct{}{}
				<-release
				return "late", nil
			},
		})
		if err != nil || result.Acted {
			t.Errorf("precompute boundary: %+v err=%v", result, err)
		}
		close(done)
	}()
	select {
	case <-started:
	case <-time.After(5 * time.Second):
		t.Fatal("background summarize never started")
	}
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("Sweep blocked on in-flight summary")
	}
	close(release)
	deadline := time.Now().Add(5 * time.Second)
	for loadSessionSweep(dir, "hub-abc").Held == nil {
		if time.Now().After(deadline) {
			t.Fatal("late summary never parked")
		}
		time.Sleep(10 * time.Millisecond)
	}
}

// A committed compaction arms a one-shot notice the next turn consumes. The
// notice carries bounded metadata only — never summary or transcript text —
// and it fires exactly once.
func TestPostCompactNoticeBounded(t *testing.T) {
	t0 := time.Date(2026, 2, 1, 12, 0, 0, 0, time.UTC)
	stubRunner(t, map[string]string{
		"estimate_tokens":  `{"ok":true,"tokens":300000,"message_count":80}`,
		"lease":            `{"ok":true,"acquired":true}`,
		"lock":             `{"ok":true,"locked":true}`,
		"read_transcript":  `{"ok":true,"session_id":"hub-abc","watermark":80,"messages":[{"role":"user","content":"m1"}]}`,
		"apply_transcript": `{"ok":true,"active_count":2}`,
		"unlock":           `{"ok":true}`,
		"unlease":          `{"ok":true}`,
	})
	dir := t.TempDir()
	result, err := Sweep(context.Background(), SweepOptions{
		StateDir: dir, SessionID: "hub-abc", ResolvedModel: "m1",
		Gate:      Gate{Enabled: true, Threshold: 160000, CooldownSeconds: 600},
		Now:       func() time.Time { return t0 },
		Summarize: func(context.Context, string, []map[string]any) (string, error) { return "x", nil },
	})
	if err != nil || !result.Acted {
		t.Fatalf("sweep: %+v err=%v", result, err)
	}
	notice := TakeNotice(dir, "hub-abc")
	if notice == nil {
		t.Fatal("no notice after commit")
	}
	if notice.Trigger != TriggerAutoSweep || !notice.At.Equal(t0) {
		t.Fatalf("bad notice: %+v", notice)
	}
	if TakeNotice(dir, "hub-abc") != nil {
		t.Fatal("notice fired twice")
	}
}

// The managed declaration resolves into the gate: undeclared spaces keep the
// reviewed defaults, declared leaves win, and the precompute default is on.
func TestSweepGateFromManaged(t *testing.T) {
	base := (&ManagedContext{}).SweepGate()
	if base.Enabled || base.Threshold != DefaultSweepThreshold || !base.Precompute || base.CooldownSeconds <= 0 {
		t.Fatalf("zero-context gate wrong: %+v", base)
	}
	mc := &ManagedContext{Compression: map[string]any{
		"hub_sweep": map[string]any{
			"enabled":          true,
			"threshold_tokens": float64(120000),
			"cooldown_seconds": float64(300),
			"precompute":       false,
		},
	}}
	g := mc.SweepGate()
	if !g.Enabled || g.Threshold != 120000 || g.CooldownSeconds != 300 || g.Precompute {
		t.Fatalf("declared gate wrong: %+v", g)
	}
}

// Engine resolution for a manual request: the declared preference resolves
// (auto -> hub), the granted list bounds any explicit ask, and an ungranted
// engine is denied by name rather than silently substituted.
func TestCompactEngineResolution(t *testing.T) {
	cases := []struct {
		name      string
		mc        *ManagedContext
		requested string
		engine    string
		granted   bool
	}{
		{"undeclared defaults to hub", &ManagedContext{}, "", EngineHub, true},
		{"auto resolves to hub", &ManagedContext{CompactEngine: "auto"}, "", EngineHub, true},
		{"pin resolves native", &ManagedContext{CompactEngine: "native"}, "", EngineNative, true},
		{"pin denies hub ask", &ManagedContext{CompactEngine: "native"}, "hub", "", false},
		{"grant list admits ask", &ManagedContext{CompactEngines: []string{"hub", "native"}}, "native", EngineNative, true},
		{"grant list denies ask", &ManagedContext{CompactEngines: []string{"native"}}, "hub", "", false},
		{"unknown engine denied", &ManagedContext{}, "bogus", "", false},
	}
	for _, tc := range cases {
		engine, granted := tc.mc.CompactEngineFor(tc.requested)
		if engine != tc.engine || granted != tc.granted {
			t.Fatalf("%s: got (%q,%v)", tc.name, engine, granted)
		}
	}
}

// A refused attempt is still a journaled attempt: the refusal row records
// the bounded class, and the compaction count only counts commits.
func TestSweepJournalsRefusal(t *testing.T) {
	dir := t.TempDir()
	stubRunner(t, map[string]string{
		"estimate_tokens": `{"ok":true,"tokens":300000,"message_count":80}`,
		"lease":           `{"ok":true,"acquired":false}`,
	})
	result, err := Sweep(context.Background(), SweepOptions{
		StateDir: dir, SessionID: "hub-abc", ResolvedModel: "m1",
		Gate: Gate{Enabled: true, Threshold: 160000, CooldownSeconds: 600},
		Now:  func() time.Time { return time.Date(2026, 2, 1, 12, 0, 0, 0, time.UTC) },
	})
	if err != nil {
		t.Fatal(err)
	}
	if result.Refusal != RefusalLeaseHeld {
		t.Fatalf("expected lease refusal, got %+v", result)
	}
	j, _ := Open(dir)
	events, _ := j.Events("hub-abc")
	if len(events) != 1 || events[0].FailureClass != RefusalLeaseHeld || events[0].Trigger != TriggerAutoSweep {
		t.Fatalf("refusal not journaled: %+v", events)
	}
	status, err := j.StatusFor("hub-abc", 0, false, nil, "")
	if err != nil {
		t.Fatal(err)
	}
	if status.Compactions != nil && *status.Compactions != 0 {
		t.Fatalf("refusal counted as compaction: %+v", status)
	}
}

// A mid-flight refusal — the compression lock already held — journals the
// bounded class and releases nothing it never took.
func TestSweepJournalsLockedRefusal(t *testing.T) {
	dir := t.TempDir()
	calls := stubRunner(t, map[string]string{
		"estimate_tokens": `{"ok":true,"tokens":300000,"message_count":80}`,
		"lease":           `{"ok":true,"acquired":true}`,
		"lock":            `{"ok":true,"locked":false}`,
		"unlease":         `{"ok":true}`,
	})
	result, err := Sweep(context.Background(), SweepOptions{
		StateDir: dir, SessionID: "hub-abc", ResolvedModel: "m1",
		Gate: Gate{Enabled: true, Threshold: 160000, CooldownSeconds: 600},
		Now:  func() time.Time { return time.Date(2026, 2, 1, 12, 0, 0, 0, time.UTC) },
	})
	if err != nil {
		t.Fatal(err)
	}
	if result.Refusal != "locked" || result.Acted {
		t.Fatalf("locked sweep: %+v", result)
	}
	j, _ := Open(dir)
	events, _ := j.Events("hub-abc")
	if len(events) != 1 || events[0].FailureClass != "locked" {
		t.Fatalf("locked refusal not journaled: %+v", events)
	}
	unlocked := false
	for _, req := range *calls {
		if req["op"] == "unlock" {
			unlocked = true
		}
	}
	if unlocked {
		t.Fatal("released a lock it never took")
	}
}

// Every mid-flight failure journals its bounded class — an attempt that died
// silently would be invisible to /usage and context_status.
func TestSweepJournalsMidflightRefusals(t *testing.T) {
	base := map[string]string{
		"estimate_tokens":  `{"ok":true,"tokens":300000,"message_count":80}`,
		"lease":            `{"ok":true,"acquired":true}`,
		"lock":             `{"ok":true,"locked":true}`,
		"read_transcript":  `{"ok":true,"session_id":"hub-abc","watermark":80,"messages":[{"role":"user","content":"m1"}]}`,
		"apply_transcript": `{"ok":true,"active_count":2}`,
		"unlock":           `{"ok":true}`,
		"unlease":          `{"ok":true}`,
	}
	for name, tc := range map[string]struct {
		breakOp       string
		failSummarize bool
		class         string
	}{
		"read":      {"read_transcript", false, "unavailable"},
		"summarize": {"", true, "summarize_failed"},
		"apply":     {"apply_transcript", false, "apply_failed"},
	} {
		t.Run(name, func(t *testing.T) {
			dir := t.TempDir()
			replies := map[string]string{}
			for k, v := range base {
				replies[k] = v
			}
			if tc.breakOp != "" {
				replies[tc.breakOp] = `{"ok":false,"error":"internal"}`
			}
			stubRunner(t, replies)
			result, err := Sweep(context.Background(), SweepOptions{
				StateDir: dir, SessionID: "hub-abc", ResolvedModel: "m1",
				Gate: Gate{Enabled: true, Threshold: 160000, CooldownSeconds: 600},
				Now:  func() time.Time { return time.Date(2026, 2, 1, 12, 0, 0, 0, time.UTC) },
				Summarize: func(context.Context, string, []map[string]any) (string, error) {
					if tc.failSummarize {
						return "", context.DeadlineExceeded
					}
					return "x", nil
				},
			})
			if err != nil {
				t.Fatal(err)
			}
			if result.Acted || result.Refusal != tc.class {
				t.Fatalf("%s refusal: %+v", name, result)
			}
			j, _ := Open(dir)
			events, _ := j.Events("hub-abc")
			if len(events) != 1 || events[0].FailureClass != tc.class {
				t.Fatalf("%s refusal not journaled: %+v", name, events)
			}
		})
	}
}
