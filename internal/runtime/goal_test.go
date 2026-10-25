package runtime

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/letya999/hermes-hub/internal/compaction"
)

// goalBody renders a /v1/goal request from a valid execute envelope plus
// goal-specific fields.
func goalBody(t *testing.T, execReq ExecuteRequest, extra map[string]any) string {
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

// Undeclared spaces resolve the hub engine by default; set returns the
// resolved engine, the kickoff prompt (the goal text itself) and the
// default turn budget — with no sidecar call at all.
func TestGoalSetResolvesHubByDefault(t *testing.T) {
	t.Setenv("HUB_RUNTIME_AUTH", "runtime-secret")
	t.Setenv("HUB_USER_ID", "alice")
	t.Setenv("HUB_ORGANIZATION_ID", "personal")
	stateDir := t.TempDir()
	t.Setenv("HUB_STATE", stateDir)
	execReq := validExecuteRequest("alice", "personal", "telegram:1", "")
	body := goalBody(t, execReq, map[string]any{"op": "set", "text": "ship the api"})
	req := httptest.NewRequest(http.MethodPost, "/v1/goal", strings.NewReader(body))
	req.Header.Set("Authorization", "Bearer runtime-secret")
	recorder := httptest.NewRecorder()
	runtimeHandler().ServeHTTP(recorder, req)
	if recorder.Code != http.StatusOK {
		t.Fatalf("status %d body %s", recorder.Code, recorder.Body.String())
	}
	var result GoalResult
	if err := json.Unmarshal(recorder.Body.Bytes(), &result); err != nil {
		t.Fatal(err)
	}
	if result.Engine != "hub" || result.KickoffPrompt != "ship the api" || result.MaxTurns != 20 {
		t.Fatalf("result: %+v", result)
	}
}

// A declared goals.engine=native pins the engine; set dispatches to the
// upstream goal machinery through the sidecar and carries its kickoff prompt
// and configured budget back to the gateway (SPEC-0074).
func TestGoalSetResolvesDeclaredNative(t *testing.T) {
	t.Setenv("HUB_RUNTIME_AUTH", "runtime-secret")
	t.Setenv("HUB_USER_ID", "alice")
	t.Setenv("HUB_ORGANIZATION_ID", "personal")
	t.Setenv("HUB_MANAGED_CONTEXT", `{"goals_engine":"native","goals_max_turns":9}`)
	stateDir := t.TempDir()
	t.Setenv("HUB_STATE", stateDir)
	var calls []map[string]any
	old := compaction.Runner
	compaction.Runner = func(_ context.Context, payload []byte) ([]byte, error) {
		var req map[string]any
		if err := json.Unmarshal(payload, &req); err != nil {
			return nil, err
		}
		calls = append(calls, req)
		if req["op"] == "goal_dispatch" {
			out, _ := json.Marshal(map[string]any{"ok": true, "output": "⊙ Goal set (9-turn budget): ship the api", "kickoff_prompt": "ship the api"})
			return out, nil
		}
		return []byte(`{"ok":false,"error":"unsupported"}`), nil
	}
	t.Cleanup(func() { compaction.Runner = old })
	execReq := validExecuteRequest("alice", "personal", "telegram:1", "")
	body := goalBody(t, execReq, map[string]any{"op": "set", "text": "ship the api"})
	req := httptest.NewRequest(http.MethodPost, "/v1/goal", strings.NewReader(body))
	req.Header.Set("Authorization", "Bearer runtime-secret")
	recorder := httptest.NewRecorder()
	runtimeHandler().ServeHTTP(recorder, req)
	var result GoalResult
	_ = json.Unmarshal(recorder.Body.Bytes(), &result)
	if result.Engine != "native" || result.MaxTurns != 9 || result.KickoffPrompt != "ship the api" || result.Output == "" {
		t.Fatalf("result: %+v", result)
	}
	if len(calls) != 1 || calls[0]["op"] != "goal_dispatch" || calls[0]["verb"] != "set" ||
		calls[0]["text"] != "ship the api" || calls[0]["session_id"] != sessionIDFor(execReq) ||
		calls[0]["max_turns"] != float64(9) {
		t.Fatalf("sidecar calls: %+v", calls)
	}
}

// An engine outside the declared pin is a named refusal — a goal must never
// silently run under an engine the space did not grant (SPEC-0074).
func TestGoalSetDeniesUnpinnedEngine(t *testing.T) {
	t.Setenv("HUB_RUNTIME_AUTH", "runtime-secret")
	t.Setenv("HUB_USER_ID", "alice")
	t.Setenv("HUB_ORGANIZATION_ID", "personal")
	t.Setenv("HUB_MANAGED_CONTEXT", `{"goals_engine":"native"}`)
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
	body := goalBody(t, execReq, map[string]any{"op": "set", "engine": "hub", "text": "x"})
	req := httptest.NewRequest(http.MethodPost, "/v1/goal", strings.NewReader(body))
	req.Header.Set("Authorization", "Bearer runtime-secret")
	recorder := httptest.NewRecorder()
	runtimeHandler().ServeHTTP(recorder, req)
	var result GoalResult
	_ = json.Unmarshal(recorder.Body.Bytes(), &result)
	if result.FailureClass != "engine_denied" || result.Engine != "" {
		t.Fatalf("denial: %+v", result)
	}
	if called {
		t.Fatal("denied engine reached the sidecar")
	}
}

// An undeclared space admits both engines — an explicit engine request
// resolves instead of colliding with the hub default.
func TestGoalSetHonorsRequestedEngineWhenUndeclared(t *testing.T) {
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
		if req["op"] == "goal_dispatch" {
			out, _ := json.Marshal(map[string]any{"ok": true, "output": "set", "kickoff_prompt": "x"})
			return out, nil
		}
		return []byte(`{"ok":false,"error":"unsupported"}`), nil
	}
	t.Cleanup(func() { compaction.Runner = old })
	execReq := validExecuteRequest("alice", "personal", "telegram:1", "")
	body := goalBody(t, execReq, map[string]any{"op": "set", "engine": "native", "text": "x"})
	req := httptest.NewRequest(http.MethodPost, "/v1/goal", strings.NewReader(body))
	req.Header.Set("Authorization", "Bearer runtime-secret")
	recorder := httptest.NewRecorder()
	runtimeHandler().ServeHTTP(recorder, req)
	var result GoalResult
	_ = json.Unmarshal(recorder.Body.Bytes(), &result)
	if result.Engine != "native" || result.FailureClass != "" {
		t.Fatalf("result: %+v", result)
	}
}

// An active goal pins its engine: a set that would resolve a different engine
// refuses by name before touching the upstream record (SPEC-0074).
func TestGoalSetRefusesEngineSwitch(t *testing.T) {
	t.Setenv("HUB_RUNTIME_AUTH", "runtime-secret")
	t.Setenv("HUB_USER_ID", "alice")
	t.Setenv("HUB_ORGANIZATION_ID", "personal")
	t.Setenv("HUB_MANAGED_CONTEXT", `{"goals_engine":"native"}`)
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
	body := goalBody(t, execReq, map[string]any{"op": "set", "expect_engine": "hub", "text": "x"})
	req := httptest.NewRequest(http.MethodPost, "/v1/goal", strings.NewReader(body))
	req.Header.Set("Authorization", "Bearer runtime-secret")
	recorder := httptest.NewRecorder()
	runtimeHandler().ServeHTTP(recorder, req)
	var result GoalResult
	_ = json.Unmarshal(recorder.Body.Bytes(), &result)
	if result.FailureClass != "engine_conflict" || called {
		t.Fatalf("conflict: %+v called=%v", result, called)
	}
}

// The `self` judge has no upstream equivalent: declaring it under the native
// engine refuses by name rather than silently judging with the wrong engine
// (SPEC-0074 R3).
func TestGoalSetRefusesSelfJudgeUnderNative(t *testing.T) {
	t.Setenv("HUB_RUNTIME_AUTH", "runtime-secret")
	t.Setenv("HUB_USER_ID", "alice")
	t.Setenv("HUB_ORGANIZATION_ID", "personal")
	t.Setenv("HUB_MANAGED_CONTEXT", `{"goals_engine":"native","goals_judge":"self"}`)
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
	body := goalBody(t, execReq, map[string]any{"op": "set", "text": "x"})
	req := httptest.NewRequest(http.MethodPost, "/v1/goal", strings.NewReader(body))
	req.Header.Set("Authorization", "Bearer runtime-secret")
	recorder := httptest.NewRecorder()
	runtimeHandler().ServeHTTP(recorder, req)
	var result GoalResult
	_ = json.Unmarshal(recorder.Body.Bytes(), &result)
	if result.FailureClass != "judge_denied" || called {
		t.Fatalf("refusal: %+v called=%v", result, called)
	}
}

// Control verbs ride the same dispatch: pause/resume/clear/status forward to
// the upstream dispatcher with the session bound server-side (SPEC-0074).
func TestGoalVerbDispatchesNative(t *testing.T) {
	t.Setenv("HUB_RUNTIME_AUTH", "runtime-secret")
	t.Setenv("HUB_USER_ID", "alice")
	t.Setenv("HUB_ORGANIZATION_ID", "personal")
	t.Setenv("HUB_MANAGED_CONTEXT", `{"goals_engine":"native"}`)
	stateDir := t.TempDir()
	t.Setenv("HUB_STATE", stateDir)
	var calls []map[string]any
	old := compaction.Runner
	compaction.Runner = func(_ context.Context, payload []byte) ([]byte, error) {
		var req map[string]any
		if err := json.Unmarshal(payload, &req); err != nil {
			return nil, err
		}
		calls = append(calls, req)
		out, _ := json.Marshal(map[string]any{"ok": true, "output": "paused", "continuation_prompt": "go on",
			"status": "paused", "turns_used": 3, "max_turns": 5})
		return out, nil
	}
	t.Cleanup(func() { compaction.Runner = old })
	execReq := validExecuteRequest("alice", "personal", "telegram:1", "")
	for _, verb := range []string{"pause", "resume", "clear", "status"} {
		body := goalBody(t, execReq, map[string]any{"op": verb, "engine": "native"})
		req := httptest.NewRequest(http.MethodPost, "/v1/goal", strings.NewReader(body))
		req.Header.Set("Authorization", "Bearer runtime-secret")
		recorder := httptest.NewRecorder()
		runtimeHandler().ServeHTTP(recorder, req)
		var result GoalResult
		_ = json.Unmarshal(recorder.Body.Bytes(), &result)
		if result.Output != "paused" || result.FailureClass != "" ||
			result.Status != "paused" || result.TurnsUsed != 3 || result.MaxTurns != 5 {
			t.Fatalf("%s: %+v", verb, result)
		}
	}
	if len(calls) != 4 || calls[0]["verb"] != "pause" || calls[3]["verb"] != "status" {
		t.Fatalf("verbs: %+v", calls)
	}
}

// Hub evaluate on an undeclared space resolves the main model and returns the
// strict verdict plus the evidence excerpt — the owner notification carries
// proof, not a bare claim (SPEC-0074 R3).
func TestGoalEvaluateHubMainJudge(t *testing.T) {
	t.Setenv("HUB_RUNTIME_AUTH", "runtime-secret")
	t.Setenv("HUB_USER_ID", "alice")
	t.Setenv("HUB_ORGANIZATION_ID", "personal")
	t.Setenv("HUB_MANAGED_MODEL_ID", "main-model")
	stateDir := t.TempDir()
	t.Setenv("HUB_STATE", stateDir)
	var model string
	var relayAuth string
	relay := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		relayAuth = r.Header.Get("Authorization")
		var req map[string]any
		_ = json.NewDecoder(r.Body).Decode(&req)
		model, _ = req["model"].(string)
		_ = json.NewEncoder(w).Encode(map[string]any{"choices": []map[string]any{
			{"message": map[string]any{"content": `{"verdict":"done","reason":"tests pass","evidence":"go test ./... ok"}`}},
		}})
	}))
	defer relay.Close()
	t.Setenv("HUB_MANAGED_MODEL_URL", relay.URL)
	t.Setenv("OPENAI_API_KEY", "relay-key")
	execReq := validExecuteRequest("alice", "personal", "telegram:1", "")
	body := goalBody(t, execReq, map[string]any{"op": "evaluate", "engine": "hub", "response": "all tests pass now"})
	req := httptest.NewRequest(http.MethodPost, "/v1/goal", strings.NewReader(body))
	req.Header.Set("Authorization", "Bearer runtime-secret")
	recorder := httptest.NewRecorder()
	runtimeHandler().ServeHTTP(recorder, req)
	var result GoalResult
	_ = json.Unmarshal(recorder.Body.Bytes(), &result)
	if result.Verdict != "done" || result.Evidence != "go test ./... ok" || result.Judge != "main" {
		t.Fatalf("result: %+v", result)
	}
	if model != "main-model" {
		t.Fatalf("judge model: %q", model)
	}
	if relayAuth != "Bearer relay-key" {
		t.Fatalf("judge relay bearer missing: %q", relayAuth)
	}
}

// A declared judge_model pins the aux judge — the auxiliary model, not the
// main one, carries the judge call.
func TestGoalEvaluateHubAuxJudge(t *testing.T) {
	t.Setenv("HUB_RUNTIME_AUTH", "runtime-secret")
	t.Setenv("HUB_USER_ID", "alice")
	t.Setenv("HUB_ORGANIZATION_ID", "personal")
	t.Setenv("HUB_MANAGED_MODEL_ID", "main-model")
	t.Setenv("HUB_MANAGED_CONTEXT", `{"goals_judge":"aux","judge_model":"judge-model"}`)
	stateDir := t.TempDir()
	t.Setenv("HUB_STATE", stateDir)
	var model string
	relay := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req map[string]any
		_ = json.NewDecoder(r.Body).Decode(&req)
		model, _ = req["model"].(string)
		_ = json.NewEncoder(w).Encode(map[string]any{"choices": []map[string]any{
			{"message": map[string]any{"content": `{"verdict":"continue","reason":"still working","evidence":""}`}},
		}})
	}))
	defer relay.Close()
	t.Setenv("HUB_MANAGED_MODEL_URL", relay.URL)
	execReq := validExecuteRequest("alice", "personal", "telegram:1", "")
	body := goalBody(t, execReq, map[string]any{"op": "evaluate", "engine": "hub", "response": "progress made"})
	req := httptest.NewRequest(http.MethodPost, "/v1/goal", strings.NewReader(body))
	req.Header.Set("Authorization", "Bearer runtime-secret")
	recorder := httptest.NewRecorder()
	runtimeHandler().ServeHTTP(recorder, req)
	var result GoalResult
	_ = json.Unmarshal(recorder.Body.Bytes(), &result)
	if result.Verdict != "continue" || result.Judge != "aux" || model != "judge-model" {
		t.Fatalf("result: %+v model=%q", result, model)
	}
}

// The self judge reads the agent's in-band GOAL_STATUS: marker — surfaced as
// judge=self, no model call (SPEC-0074 R3).
func TestGoalEvaluateHubSelfJudge(t *testing.T) {
	t.Setenv("HUB_RUNTIME_AUTH", "runtime-secret")
	t.Setenv("HUB_USER_ID", "alice")
	t.Setenv("HUB_ORGANIZATION_ID", "personal")
	t.Setenv("HUB_MANAGED_MODEL_ID", "main-model")
	t.Setenv("HUB_MANAGED_CONTEXT", `{"goals_judge":"self"}`)
	stateDir := t.TempDir()
	t.Setenv("HUB_STATE", stateDir)
	relayHit := false
	relay := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		relayHit = true
	}))
	defer relay.Close()
	t.Setenv("HUB_MANAGED_MODEL_URL", relay.URL)
	execReq := validExecuteRequest("alice", "personal", "telegram:1", "")
	body := goalBody(t, execReq, map[string]any{
		"op": "evaluate", "engine": "hub",
		"response": "Finished the migration.\nGOAL_STATUS: done — tests green",
	})
	req := httptest.NewRequest(http.MethodPost, "/v1/goal", strings.NewReader(body))
	req.Header.Set("Authorization", "Bearer runtime-secret")
	recorder := httptest.NewRecorder()
	runtimeHandler().ServeHTTP(recorder, req)
	var result GoalResult
	_ = json.Unmarshal(recorder.Body.Bytes(), &result)
	if result.Verdict != "done" || result.Judge != "self" || relayHit {
		t.Fatalf("result: %+v relayHit=%v", result, relayHit)
	}
}

// Marker verdicts: blocked stops with its reason, a missing or malformed
// marker counts toward the parse-failure streak (SPEC-0074 R3).
func TestGoalEvaluateSelfMarkerVerdicts(t *testing.T) {
	t.Setenv("HUB_RUNTIME_AUTH", "runtime-secret")
	t.Setenv("HUB_USER_ID", "alice")
	t.Setenv("HUB_ORGANIZATION_ID", "personal")
	t.Setenv("HUB_MANAGED_MODEL_ID", "main-model")
	t.Setenv("HUB_MANAGED_CONTEXT", `{"goals_judge":"self"}`)
	t.Setenv("HUB_STATE", t.TempDir())
	evaluate := func(response string) GoalResult {
		execReq := validExecuteRequest("alice", "personal", "telegram:1", "")
		body := goalBody(t, execReq, map[string]any{"op": "evaluate", "engine": "hub", "response": response})
		req := httptest.NewRequest(http.MethodPost, "/v1/goal", strings.NewReader(body))
		req.Header.Set("Authorization", "Bearer runtime-secret")
		recorder := httptest.NewRecorder()
		runtimeHandler().ServeHTTP(recorder, req)
		var result GoalResult
		_ = json.Unmarshal(recorder.Body.Bytes(), &result)
		return result
	}
	blocked := evaluate("Cannot proceed.\nGOAL_STATUS: blocked needs credentials")
	if blocked.Verdict != "blocked" || blocked.Reason != "needs credentials" {
		t.Fatalf("blocked: %+v", blocked)
	}
	missing := evaluate("still working, no marker yet")
	if missing.Verdict != "continue" || !missing.ParseFailure {
		t.Fatalf("missing: %+v", missing)
	}
}

// Native evaluate forwards the completed turn to upstream's goal machinery
// through the sidecar — verdict and turn counter come back bounded.
func TestGoalEvaluateNativeSidecar(t *testing.T) {
	t.Setenv("HUB_RUNTIME_AUTH", "runtime-secret")
	t.Setenv("HUB_USER_ID", "alice")
	t.Setenv("HUB_ORGANIZATION_ID", "personal")
	t.Setenv("HUB_MANAGED_CONTEXT", `{"goals_engine":"native"}`)
	t.Setenv("HUB_STATE", t.TempDir())
	var op map[string]any
	restore := compaction.Runner
	compaction.Runner = func(ctx context.Context, payload []byte) ([]byte, error) {
		_ = json.Unmarshal(payload, &op)
		return []byte(`{"ok":true,"verdict":"continue","reason":"upstream says keep going","turns_used":4,"max_turns":20,"should_continue":true,"continuation_prompt":"Next step: write the report."}`), nil
	}
	defer func() { compaction.Runner = restore }()
	execReq := validExecuteRequest("alice", "personal", "telegram:1", "")
	body := goalBody(t, execReq, map[string]any{"op": "evaluate", "engine": "native", "response": "progress"})
	req := httptest.NewRequest(http.MethodPost, "/v1/goal", strings.NewReader(body))
	req.Header.Set("Authorization", "Bearer runtime-secret")
	recorder := httptest.NewRecorder()
	runtimeHandler().ServeHTTP(recorder, req)
	var result GoalResult
	_ = json.Unmarshal(recorder.Body.Bytes(), &result)
	if op["op"] != "goal_evaluate" {
		t.Fatalf("op: %v", op["op"])
	}
	if result.Verdict != "continue" || result.TurnsUsed != 4 || !result.ShouldContinue ||
		result.ContinuationPrompt != "Next step: write the report." || result.Engine != "native" {
		t.Fatalf("result: %+v", result)
	}
}
