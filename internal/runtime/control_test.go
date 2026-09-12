package runtime

import (
	"encoding/json"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestControlBindsApprovalAndRejectsExpiredDecision(t *testing.T) {
	for key, value := range map[string]string{"HUB_RUNTIME_AUTH": "secret", "HUB_USER_ID": "alice", "HUB_ORGANIZATION_ID": "personal", "HUB_RUNTIME_ID": "alice", "HUB_POLICY_VERSION": "policy-1", "HUB_RUNTIME_GENERATION": "gen-1"} {
		t.Setenv(key, value)
	}
	request := validExecuteRequest("alice", "personal", "control", "unused")
	request.JobID = "job-control"
	control := RunControl{RunReference: RunReference{ExecuteRequest: request, RunID: "original", SessionID: sessionIDFor(request), RuntimeGeneration: "gen-1"}, Action: "approve", RequestID: "approval-1", Choice: "once", Deadline: time.Now().Add(time.Minute)}
	mutations := 0
	api := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodGet {
			_ = json.NewEncoder(w).Encode(map[string]any{"status": "waiting_for_approval", "session_id": control.SessionID, "approval": map[string]any{"request_id": "approval-1", "choices": []string{"once", "deny"}}})
			return
		}
		if r.URL.Path != "/v1/runs/original/approval" {
			t.Errorf("unexpected mutation %s", r.URL.Path)
		}
		var body map[string]string
		_ = json.NewDecoder(r.Body).Decode(&body)
		if len(body) != 2 || body["request_id"] != "approval-1" || body["choice"] != "once" {
			t.Errorf("body=%v", body)
		}
		mutations++
	}))
	defer api.Close()
	host, port, _ := net.SplitHostPort(api.Listener.Addr().String())
	t.Setenv("HUB_HERMES_API_HOST", host)
	t.Setenv("HUB_HERMES_API_PORT", port)
	call := func(c RunControl) int {
		req := httptest.NewRequest(http.MethodPost, "/v1/control", strings.NewReader(mustJSON(t, c)))
		req.Header.Set("Authorization", "Bearer secret")
		rec := httptest.NewRecorder()
		runtimeHandler().ServeHTTP(rec, req)
		return rec.Code
	}
	for _, field := range []string{"expired", "request", "generation", "choice", "session"} {
		bad := control
		switch field {
		case "expired":
			bad.Deadline = time.Now().Add(-time.Minute)
		case "request":
			bad.RequestID = "other"
		case "generation":
			bad.RuntimeGeneration = "old"
		case "choice":
			bad.Choice = "always"
		case "session":
			bad.SessionID = "other"
		}
		if code := call(bad); code != http.StatusConflict {
			t.Fatalf("%s: %d", field, code)
		}
	}
	if mutations != 0 {
		t.Fatal("invalid decision mutated Hermes")
	}
	readOnly := control
	readOnly.Reconcile = true
	readOnly.Deadline = time.Now().Add(-time.Minute)
	if code := call(readOnly); code != http.StatusOK || mutations != 0 {
		t.Fatalf("reconciliation mutated Hermes: code=%d mutations=%d", code, mutations)
	}
	if code := call(control); code != http.StatusOK || mutations != 1 {
		t.Fatalf("code=%d mutations=%d", code, mutations)
	}
}

func TestCancellationPreservesTerminalResultAndConfirmsStop(t *testing.T) {
	for key, value := range map[string]string{"HUB_RUNTIME_AUTH": "secret", "HUB_USER_ID": "alice", "HUB_ORGANIZATION_ID": "personal", "HUB_RUNTIME_ID": "alice", "HUB_POLICY_VERSION": "policy-1", "HUB_RUNTIME_GENERATION": "gen-1"} {
		t.Setenv(key, value)
	}
	request := validExecuteRequest("alice", "personal", "cancel", "unused")
	request.JobID = "cancel-job"
	control := RunControl{RunReference: RunReference{ExecuteRequest: request, RunID: "original", SessionID: sessionIDFor(request), RuntimeGeneration: "gen-1"}, Action: "cancel"}
	state := "completed"
	stops := 0
	api := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodPost {
			stops++
			state = "cancelled"
			_, _ = w.Write([]byte(`{"status":"stopping"}`))
			return
		}
		_ = json.NewEncoder(w).Encode(map[string]string{"status": state, "session_id": control.SessionID, "output": "saved answer"})
	}))
	defer api.Close()
	host, port, _ := net.SplitHostPort(api.Listener.Addr().String())
	t.Setenv("HUB_HERMES_API_HOST", host)
	t.Setenv("HUB_HERMES_API_PORT", port)
	call := func() ExecuteResponse {
		req := httptest.NewRequest(http.MethodPost, "/v1/control", strings.NewReader(mustJSON(t, control)))
		req.Header.Set("Authorization", "Bearer secret")
		rec := httptest.NewRecorder()
		runtimeHandler().ServeHTTP(rec, req)
		var response ExecuteResponse
		if rec.Code != 200 || json.Unmarshal(rec.Body.Bytes(), &response) != nil {
			t.Fatalf("response=%d %s", rec.Code, rec.Body.String())
		}
		return response
	}
	if response := call(); response.Status != "completed" || response.Text != "saved answer" || stops != 0 {
		t.Fatalf("terminal result=%+v stops=%d", response, stops)
	}
	state = "running"
	if response := call(); response.Status != "cancelled" || stops != 1 {
		t.Fatalf("stop=%+v stops=%d", response, stops)
	}
	if response := call(); response.Status != "cancelled" || stops != 1 {
		t.Fatalf("repeat=%+v stops=%d", response, stops)
	}
}
