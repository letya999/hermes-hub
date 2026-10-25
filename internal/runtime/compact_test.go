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

// compactBody renders a /v1/compact request from a valid execute envelope
// plus compact-specific fields.
func compactBody(t *testing.T, execReq ExecuteRequest, extra map[string]any) string {
	t.Helper()
	body := map[string]any{
		"identity_schema":      execReq.Schema,
		"principal_id":         execReq.PrincipalID,
		"external_identity_id": execReq.ExternalIdentityID,
		"context_id":           execReq.ContextID,
		"runtime_id":           execReq.RuntimeID,
		"conversation_id":      execReq.ConversationID,
		"delivery_target_id":   execReq.DeliveryTargetID,
		"policy_version":       execReq.PolicyVersion,
		"organization_id":      execReq.OrganizationID,
		"user_id":              execReq.UserID,
		"actor_id":             execReq.ActorID,
		"scope_id":             execReq.ScopeID,
		"channel":              execReq.Channel,
		"trigger":              execReq.Trigger,
		"idempotency_key":      execReq.IdempotencyKey,
	}
	for k, v := range extra {
		body[k] = v
	}
	encoded, err := json.Marshal(body)
	if err != nil {
		t.Fatal(err)
	}
	return string(encoded)
}

// An engine outside the space's grant is a named refusal with an audit row
// — never a silent substitution, never a session mutation.
func TestCompactEndpointDeniesUngrantedEngine(t *testing.T) {
	t.Setenv("HUB_RUNTIME_AUTH", "runtime-secret")
	t.Setenv("HUB_USER_ID", "alice")
	t.Setenv("HUB_ORGANIZATION_ID", "personal")
	t.Setenv("HUB_MANAGED_CONTEXT", `{"compact_engines":["native"]}`)
	stateDir := t.TempDir()
	t.Setenv("HUB_STATE", stateDir)
	called := false
	old := compaction.Runner
	compaction.Runner = func(context.Context, []byte) ([]byte, error) {
		called = true
		return []byte(`{"ok":true,"tokens":10,"message_count":4}`), nil
	}
	t.Cleanup(func() { compaction.Runner = old })
	execReq := validExecuteRequest("alice", "personal", "telegram:1", "")
	body := compactBody(t, execReq, map[string]any{"engine": "hub"})
	req := httptest.NewRequest(http.MethodPost, "/v1/compact", strings.NewReader(body))
	req.Header.Set("Authorization", "Bearer runtime-secret")
	recorder := httptest.NewRecorder()
	runtimeHandler().ServeHTTP(recorder, req)
	var result compaction.CompactResult
	_ = json.Unmarshal(recorder.Body.Bytes(), &result)
	if recorder.Code != http.StatusOK || result.FailureClass != "engine_denied" || result.Applied {
		t.Fatalf("denial: %d %+v", recorder.Code, result)
	}
	if called {
		t.Fatal("denied engine reached the sidecar")
	}
	j, _ := compaction.Open(stateDir)
	events, _ := j.Events(sessionIDFor(execReq))
	if len(events) != 1 || events[0].FailureClass != "engine_denied" || events[0].Trigger != compaction.TriggerManual {
		t.Fatalf("audit row: %+v", events)
	}
}

// Busy + busy_policy=queue parks the request for the next turn boundary —
// one deterministic outcome, the in-flight turn untouched.
func TestCompactEndpointQueuesWhenBusy(t *testing.T) {
	t.Setenv("HUB_RUNTIME_AUTH", "runtime-secret")
	t.Setenv("HUB_USER_ID", "alice")
	t.Setenv("HUB_ORGANIZATION_ID", "personal")
	stateDir := t.TempDir()
	t.Setenv("HUB_STATE", stateDir)
	old := compaction.Runner
	compaction.Runner = func(_ context.Context, payload []byte) ([]byte, error) {
		var req map[string]any
		if err := json.Unmarshal(payload, &req); err != nil {
			return nil, err
		}
		switch req["op"] {
		case "estimate_tokens":
			return []byte(`{"ok":true,"tokens":300000,"message_count":10}`), nil
		case "lease":
			return []byte(`{"ok":true,"acquired":false}`), nil
		default:
			return []byte(`{"ok":false,"error":"internal"}`), nil
		}
	}
	t.Cleanup(func() { compaction.Runner = old })
	execReq := validExecuteRequest("alice", "personal", "telegram:1", "")
	body := compactBody(t, execReq, map[string]any{"busy_policy": "queue", "here": 2})
	req := httptest.NewRequest(http.MethodPost, "/v1/compact", strings.NewReader(body))
	req.Header.Set("Authorization", "Bearer runtime-secret")
	recorder := httptest.NewRecorder()
	runtimeHandler().ServeHTTP(recorder, req)
	var result compaction.CompactResult
	_ = json.Unmarshal(recorder.Body.Bytes(), &result)
	if result.FailureClass != "queued" || result.Applied {
		t.Fatalf("queue outcome: %+v", result)
	}
	pending := compaction.TakePending(stateDir, sessionIDFor(execReq))
	if pending == nil || pending.Args.Here != 2 || pending.Engine != compaction.EngineHub {
		t.Fatalf("pending: %+v", pending)
	}
	// Default policy refuses instead — same busy session, no parked request.
	recorder2 := httptest.NewRecorder()
	req2 := httptest.NewRequest(http.MethodPost, "/v1/compact", strings.NewReader(compactBody(t, execReq, nil)))
	req2.Header.Set("Authorization", "Bearer runtime-secret")
	runtimeHandler().ServeHTTP(recorder2, req2)
	var result2 compaction.CompactResult
	_ = json.Unmarshal(recorder2.Body.Bytes(), &result2)
	if result2.FailureClass != compaction.RefusalLeaseHeld {
		t.Fatalf("refuse outcome: %+v", result2)
	}
	if compaction.TakePending(stateDir, sessionIDFor(execReq)) != nil {
		t.Fatal("default policy parked a request")
	}
}

// A queued compact fires at the next turn boundary: the post-job hook runs
// the parked request as a manual compact — once, then it's gone.
func TestPendingCompactRunsAfterJob(t *testing.T) {
	t.Setenv("HUB_RUNTIME_AUTH", "runtime-secret")
	t.Setenv("HUB_USER_ID", "alice")
	t.Setenv("HUB_ORGANIZATION_ID", "personal")
	t.Setenv("HUB_MANAGED_MODEL_ID", "m1")
	stateDir := t.TempDir()
	t.Setenv("HUB_STATE", stateDir)
	execReq := validExecuteRequest("alice", "personal", "telegram:1", "hello")
	sessionID := sessionIDFor(execReq)
	if err := compaction.QueueCompact(stateDir, sessionID, compaction.PendingCompact{
		Args: compaction.CompactArgs{Here: 1}, Engine: compaction.EngineHub, At: time.Now().UTC(),
	}); err != nil {
		t.Fatal(err)
	}
	released := make(chan struct{}, 1)
	old := compaction.Runner
	compaction.Runner = func(_ context.Context, payload []byte) ([]byte, error) {
		var req map[string]any
		if err := json.Unmarshal(payload, &req); err != nil {
			return nil, err
		}
		switch req["op"] {
		case "estimate_tokens":
			return []byte(`{"ok":true,"tokens":300000,"message_count":10}`), nil
		case "scan_compactions":
			return []byte(`{"ok":true,"lineage":[],"rotations":0,"in_place":0}`), nil
		case "lease":
			return []byte(`{"ok":true,"acquired":true}`), nil
		case "lock":
			return []byte(`{"ok":true,"locked":true}`), nil
		case "read_transcript":
			msgs := []map[string]any{{"role": "user", "content": strings.Repeat("u", 800000)}}
			for i := 1; i <= 12; i++ {
				role := "user"
				if i%2 == 0 {
					role = "assistant"
				}
				msgs = append(msgs, map[string]any{"role": role, "content": "m"})
			}
			out, _ := json.Marshal(map[string]any{"ok": true, "session_id": "s", "watermark": 13, "messages": msgs})
			return out, nil
		case "unlease":
			select {
			case released <- struct{}{}:
			default:
			}
			return []byte(`{"ok":true}`), nil
		case "apply_transcript", "unlock":
			return []byte(`{"ok":true,"active_count":3}`), nil
		default:
			return []byte(`{"ok":false,"error":"internal"}`), nil
		}
	}
	t.Cleanup(func() { compaction.Runner = old })
	oldSummarize := compactSummarize
	compactSummarize = func(context.Context, string, []map[string]any, string) (string, error) { return "s", nil }
	t.Cleanup(func() { compactSummarize = oldSummarize })
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
	if _, err := (&runtimeHTTP{}).postHermesRun(context.Background(), execReq,
		hermesRunBody{Input: execReq.Text, SessionID: sessionID}, nil); err != nil {
		t.Fatal(err)
	}
	j, _ := compaction.Open(stateDir)
	var manual *compaction.Event
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		events, _ := j.Events(sessionID)
		for i := range events {
			if events[i].Trigger == compaction.TriggerManual {
				manual = &events[i]
			}
		}
		if manual != nil {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	if manual == nil || manual.FailureClass != "" {
		t.Fatalf("pending compact never ran: %+v", manual)
	}
	if compaction.TakePending(stateDir, sessionID) != nil {
		t.Fatal("pending survived its run")
	}
	// The async compact's deferred lease release can still be in flight —
	// drain it before the cleanup restores Runner.
	select {
	case <-released:
	case <-time.After(3 * time.Second):
		t.Fatal("compact lease never released")
	}
}

// Out-of-range args fail before any sidecar call — upstream clamps `here`
// at 100, and a policy word outside refuse|queue is a bad request.
func TestCompactEndpointRejectsBadArgs(t *testing.T) {
	t.Setenv("HUB_RUNTIME_AUTH", "runtime-secret")
	t.Setenv("HUB_USER_ID", "alice")
	t.Setenv("HUB_ORGANIZATION_ID", "personal")
	stateDir := t.TempDir()
	t.Setenv("HUB_STATE", stateDir)
	called := false
	old := compaction.Runner
	compaction.Runner = func(context.Context, []byte) ([]byte, error) {
		called = true
		return []byte(`{"ok":true}`), nil
	}
	t.Cleanup(func() { compaction.Runner = old })
	execReq := validExecuteRequest("alice", "personal", "telegram:1", "")
	for _, extra := range []map[string]any{
		{"here": -1}, {"here": 101}, {"busy_policy": "explode"}, {"focus": strings.Repeat("f", 600)},
	} {
		req := httptest.NewRequest(http.MethodPost, "/v1/compact", strings.NewReader(compactBody(t, execReq, extra)))
		req.Header.Set("Authorization", "Bearer runtime-secret")
		recorder := httptest.NewRecorder()
		runtimeHandler().ServeHTTP(recorder, req)
		if recorder.Code != http.StatusBadRequest {
			t.Fatalf("args %v: status %d", extra, recorder.Code)
		}
	}
	if called {
		t.Fatal("bad args reached the sidecar")
	}
}

// The channel command lands here: the runtime resolves the caller's stable
// session, runs the manual pipeline and returns only the allowlisted fields.
func TestCompactEndpointRunsManualPipeline(t *testing.T) {
	t.Setenv("HUB_RUNTIME_AUTH", "runtime-secret")
	t.Setenv("HUB_USER_ID", "alice")
	t.Setenv("HUB_ORGANIZATION_ID", "personal")
	t.Setenv("HUB_MANAGED_MODEL_ID", "m1")
	stateDir := t.TempDir()
	t.Setenv("HUB_STATE", stateDir)
	var sessions []string
	old := compaction.Runner
	compaction.Runner = func(_ context.Context, payload []byte) ([]byte, error) {
		var req map[string]any
		if err := json.Unmarshal(payload, &req); err != nil {
			return nil, err
		}
		if id, ok := req["session_id"].(string); ok {
			sessions = append(sessions, id)
		}
		switch req["op"] {
		case "estimate_tokens":
			return []byte(`{"ok":true,"tokens":300000,"message_count":10}`), nil
		case "lease":
			return []byte(`{"ok":true,"acquired":true}`), nil
		case "lock":
			return []byte(`{"ok":true,"locked":true}`), nil
		case "read_transcript":
			msgs := []map[string]any{
				{"role": "user", "content": strings.Repeat("u", 800000)},
				{"role": "assistant", "content": "a1"},
			}
			for i := 2; i <= 12; i++ {
				role := "user"
				if i%2 == 1 {
					role = "assistant"
				}
				msgs = append(msgs, map[string]any{"role": role, "content": "m"})
			}
			out, _ := json.Marshal(map[string]any{"ok": true, "session_id": "s", "watermark": 12, "messages": msgs})
			return out, nil
		case "apply_transcript":
			return []byte(`{"ok":true,"active_count":3}`), nil
		case "unlock", "unlease":
			return []byte(`{"ok":true}`), nil
		default:
			return []byte(`{"ok":false,"error":"internal"}`), nil
		}
	}
	t.Cleanup(func() { compaction.Runner = old })
	oldSummarize := compactSummarize
	compactSummarize = func(context.Context, string, []map[string]any, string) (string, error) { return "summary text", nil }
	t.Cleanup(func() { compactSummarize = oldSummarize })
	execReq := validExecuteRequest("alice", "personal", "telegram:1", "")
	body := compactBody(t, execReq, nil)
	req := httptest.NewRequest(http.MethodPost, "/v1/compact", strings.NewReader(string(body)))
	req.Header.Set("Authorization", "Bearer runtime-secret")
	recorder := httptest.NewRecorder()
	runtimeHandler().ServeHTTP(recorder, req)
	if recorder.Code != http.StatusOK {
		t.Fatalf("status %d body %s", recorder.Code, recorder.Body.String())
	}
	var result compaction.CompactResult
	if err := json.Unmarshal(recorder.Body.Bytes(), &result); err != nil {
		t.Fatal(err)
	}
	if !result.Applied || result.Method == "" || result.FailureClass != "" {
		t.Fatalf("result: %+v", result)
	}
	want := sessionIDFor(execReq)
	for _, s := range sessions {
		if s != want {
			t.Fatalf("compact touched session %q, want %q", s, want)
		}
	}
	j, err := compaction.Open(stateDir)
	if err != nil {
		t.Fatal(err)
	}
	events, _ := j.Events(want)
	if len(events) != 1 || events[0].Trigger != compaction.TriggerManual {
		t.Fatalf("journal: %+v", events)
	}
}
