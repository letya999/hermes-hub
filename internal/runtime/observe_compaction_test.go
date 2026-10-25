package runtime

import (
	"context"
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/letya999/hermes-hub/internal/compaction"
)

// A completed job leaves transcript evidence of in-place compactions behind;
// the journal is the only memory that surfaces them across turns. Missing
// the observation makes every later status read a lie.
func TestCompletedJobJournalsObservedCompactions(t *testing.T) {
	api := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/sessions":
			w.WriteHeader(201)
		case "/v1/runs":
			fmt.Fprint(w, `{"run_id":"run-1"}`)
		case "/v1/runs/run-1":
			fmt.Fprint(w, `{"status":"completed","output":"done"}`)
		default:
			http.NotFound(w, r)
		}
	}))
	defer api.Close()
	t.Setenv("HUB_HERMES_API_HOST", "127.0.0.1")
	t.Setenv("HUB_HERMES_API_PORT", fmt.Sprint(api.Listener.Addr().(*net.TCPAddr).Port))
	stateDir := t.TempDir()
	t.Setenv("HUB_STATE", stateDir)
	old := compaction.Runner
	compaction.Runner = func(context.Context, []byte) ([]byte, error) {
		return []byte(`{"ok":true,"lineage":["s"],"tip":"s","rotations":0,"in_place":2,"last_summary_at":1700000000}`), nil
	}
	t.Cleanup(func() { compaction.Runner = old })
	request := validExecuteRequest("alice", "personal", "telegram:1", "hello")
	sessionID := sessionIDFor(request)
	response, err := (&runtimeHTTP{}).postHermesRun(context.Background(), request,
		hermesRunBody{Input: request.Text, SessionID: sessionID}, nil)
	if err != nil || response.Status != "completed" {
		t.Fatalf("job did not complete: %v %+v", err, response)
	}
	j, err := compaction.Open(stateDir)
	if err != nil {
		t.Fatal(err)
	}
	events, err := j.Events(sessionID)
	if err != nil || len(events) != 2 {
		t.Fatalf("journal: %v %v", events, err)
	}
	for _, e := range events {
		if e.Trigger != compaction.TriggerAutoInLoop || e.SessionID != sessionID {
			t.Fatalf("observed event misattributed: %+v", e)
		}
	}
	// A failed job never records — the hook observes only what a completed
	// job's transcript proves.
	events2, _ := j.Events("other-session")
	if len(events2) != 0 {
		t.Fatalf("foreign session got rows: %v", events2)
	}
}

// An enabled hub_sweep declaration compacts an over-threshold session at the
// job boundary — the same lease-locked path a manual compact takes, armed
// notice included.
func TestCompletedJobSweepsWhenEnabled(t *testing.T) {
	api := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/sessions":
			w.WriteHeader(201)
		case "/v1/runs":
			fmt.Fprint(w, `{"run_id":"run-1"}`)
		case "/v1/runs/run-1":
			fmt.Fprint(w, `{"status":"completed","output":"done"}`)
		default:
			http.NotFound(w, r)
		}
	}))
	defer api.Close()
	t.Setenv("HUB_HERMES_API_HOST", "127.0.0.1")
	t.Setenv("HUB_HERMES_API_PORT", fmt.Sprint(api.Listener.Addr().(*net.TCPAddr).Port))
	t.Setenv("HUB_MANAGED_MODEL_URL", "http://model-relay:8318/v1")
	t.Setenv("HUB_MANAGED_MODEL_ID", "m1")
	t.Setenv("HUB_MANAGED_CONTEXT", `{"compression":{"hub_sweep":{"enabled":true,"threshold_tokens":100}}}`)
	stateDir := t.TempDir()
	t.Setenv("HUB_STATE", stateDir)
	released := make(chan struct{}, 1)
	old := compaction.Runner
	compaction.Runner = func(_ context.Context, payload []byte) ([]byte, error) {
		var req map[string]any
		if err := json.Unmarshal(payload, &req); err != nil {
			return nil, err
		}
		switch req["op"] {
		case "estimate_tokens":
			return []byte(`{"ok":true,"tokens":300000,"message_count":80}`), nil
		case "scan_compactions":
			return []byte(`{"ok":true,"lineage":["s"],"tip":"s","rotations":0,"in_place":0}`), nil
		case "lease":
			return []byte(`{"ok":true,"acquired":true}`), nil
		case "lock":
			return []byte(`{"ok":true,"locked":true}`), nil
		case "read_transcript":
			return []byte(`{"ok":true,"session_id":"x","watermark":80,"messages":[{"role":"user","content":"m1"}]}`), nil
		case "unlease":
			select {
			case released <- struct{}{}:
			default:
			}
			return []byte(`{"ok":true,"active_count":2}`), nil
		case "apply_transcript", "unlock":
			return []byte(`{"ok":true,"active_count":2}`), nil
		default:
			return []byte(`{"ok":false,"error":"internal"}`), nil
		}
	}
	t.Cleanup(func() { compaction.Runner = old })
	oldSummarize := sweepSummarize
	sweepSummarize = func(context.Context, string, []map[string]any) (string, error) { return "x", nil }
	t.Cleanup(func() { sweepSummarize = oldSummarize })
	request := validExecuteRequest("alice", "personal", "telegram:1", "hello")
	sessionID := sessionIDFor(request)
	if _, err := (&runtimeHTTP{}).postHermesRun(context.Background(), request,
		hermesRunBody{Input: request.Text, SessionID: sessionID}, nil); err != nil {
		t.Fatal(err)
	}
	j, err := compaction.Open(stateDir)
	if err != nil {
		t.Fatal(err)
	}
	var sweep *compaction.Event
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		events, _ := j.Events(sessionID)
		for i := range events {
			if events[i].Trigger == compaction.TriggerAutoSweep {
				sweep = &events[i]
			}
		}
		if sweep != nil {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	if sweep == nil || sweep.FailureClass != "" {
		t.Fatalf("no sweep commit: %+v", sweep)
	}
	if compaction.TakeNotice(stateDir, sessionID) == nil {
		t.Fatal("sweep commit armed no notice")
	}
	// The async sweep's deferred lease release can still be in flight after
	// the commit row lands — drain it before the cleanup restores Runner.
	select {
	case <-released:
	case <-time.After(3 * time.Second):
		t.Fatal("sweep lease never released")
	}
}

// The next turn after a compaction carries the bounded marker: the agent
// learns the transcript folded, nothing else.
func TestAdmitInjectsCompactionNotice(t *testing.T) {
	var runBody map[string]any
	api := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/sessions":
			w.WriteHeader(201)
		case "/v1/runs":
			_ = json.NewDecoder(r.Body).Decode(&runBody)
			fmt.Fprint(w, `{"run_id":"run-1"}`)
		case "/v1/runs/run-1":
			fmt.Fprint(w, `{"status":"completed","output":"done"}`)
		default:
			http.NotFound(w, r)
		}
	}))
	defer api.Close()
	t.Setenv("HUB_HERMES_API_HOST", "127.0.0.1")
	t.Setenv("HUB_HERMES_API_PORT", fmt.Sprint(api.Listener.Addr().(*net.TCPAddr).Port))
	stateDir := t.TempDir()
	t.Setenv("HUB_STATE", stateDir)
	old := compaction.Runner
	compaction.Runner = func(context.Context, []byte) ([]byte, error) {
		return []byte(`{"ok":true,"lineage":[],"rotations":0,"in_place":0}`), nil
	}
	t.Cleanup(func() { compaction.Runner = old })
	request := validExecuteRequest("alice", "personal", "telegram:1", "hello")
	sessionID := sessionIDFor(request)
	j, err := compaction.Open(stateDir)
	if err != nil {
		t.Fatal(err)
	}
	_ = j.Append(compaction.Event{At: time.Date(2026, 2, 1, 12, 0, 0, 0, time.UTC),
		SessionID: sessionID, Trigger: compaction.TriggerAutoSweep,
		Engine: compaction.EngineHub, Method: compaction.MethodArchiveCompct})
	if _, err := (&runtimeHTTP{}).admitHermesRun(context.Background(), request, nil); err != nil {
		t.Fatal(err)
	}
	input, _ := runBody["input"].(string)
	if !strings.Contains(input, "[hub:") || !strings.Contains(input, "auto_sweep") {
		t.Fatalf("notice missing from next turn: %q", input)
	}
	if strings.Contains(input, "summary text") {
		t.Fatal("notice leaked content")
	}
	// Consumed once: a second admit carries no marker.
	if _, err := (&runtimeHTTP{}).admitHermesRun(context.Background(), request, nil); err != nil {
		t.Fatal(err)
	}
	input2, _ := runBody["input"].(string)
	if strings.Contains(input2, "[hub:") {
		t.Fatalf("notice fired twice: %q", input2)
	}
}

// A native engine declaration keeps compaction upstream-only: the hub sweep
// may act only where the grant admits the hub engine.
func TestSweepHonorsEngineGrant(t *testing.T) {
	stateDir := t.TempDir()
	t.Setenv("HUB_STATE", stateDir)
	t.Setenv("HUB_MANAGED_MODEL_ID", "m1")
	t.Setenv("HUB_MANAGED_CONTEXT", `{"compact_engine":"native","compression":{"hub_sweep":{"enabled":true,"threshold_tokens":100}}}`)
	called := false
	old := compaction.Runner
	compaction.Runner = func(context.Context, []byte) ([]byte, error) {
		called = true
		return []byte(`{"ok":true,"tokens":300000,"message_count":80}`), nil
	}
	t.Cleanup(func() { compaction.Runner = old })
	(&runtimeHTTP{}).sweepAfterJob(context.Background(), "hub-x")
	if called {
		t.Fatal("sweep ran under native engine grant")
	}
	// An engines grant that omits "hub" closes the same path.
	t.Setenv("HUB_MANAGED_CONTEXT", `{"compact_engines":["native"],"compression":{"hub_sweep":{"enabled":true,"threshold_tokens":100}}}`)
	called = false
	(&runtimeHTTP{}).sweepAfterJob(context.Background(), "hub-x")
	if called {
		t.Fatal("sweep ran outside the engines grant")
	}
}
