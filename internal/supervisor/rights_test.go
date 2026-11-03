package supervisor

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
)

// The runtime-facing PEP calls the space's ToolHub decide endpoint before
// acting; a denied right surfaces as 403 without touching the runtime
// (SPEC-0092 R2.2). Unconfigured pattern keeps the legacy posture.
func TestRightsGateDecides(t *testing.T) {
	var seen string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		seen = r.URL.Path
		if r.Header.Get("Authorization") != "Bearer control-secret" {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		var req struct {
			PrincipalID string `json:"principal_id"`
			Capability  string `json:"capability"`
			Operation   string `json:"operation"`
		}
		_ = json.NewDecoder(r.Body).Decode(&req)
		allow := req.Operation == "restart_self" && req.PrincipalID == "alice"
		_ = json.NewEncoder(w).Encode(map[string]any{"allow": allow})
	}))
	defer server.Close()
	t.Setenv("HUB_TOOLHUB_URL_PATTERN", server.URL+"/"+`%s`)
	m := &Manager{cfg: Config{RuntimeAuth: "control-secret", Environment: "dev"}}

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/v1/restart-request", nil)
	if !m.rightsGate(rec, req, "alice", "agents", "restart_self", "runtime") {
		t.Fatal("allowed right must pass the gate")
	}
	if seen == "" {
		t.Fatal("decide endpoint must have been called")
	}
	rec = httptest.NewRecorder()
	if m.rightsGate(rec, req, "alice", "agents", "monitor_self", "runtime") {
		t.Fatal("denied right must block")
	}
	if rec.Code != http.StatusForbidden {
		t.Fatalf("denied right must surface 403: %d", rec.Code)
	}
	// PDP outage fails closed.
	seen = ""
	server.Close()
	rec = httptest.NewRecorder()
	if m.rightsGate(rec, req, "alice", "agents", "restart_self", "runtime") {
		t.Fatal("decide outage must deny")
	}
}

func TestRightsGateLegacyPosture(t *testing.T) {
	t.Setenv("HUB_TOOLHUB_URL_PATTERN", "")
	m := &Manager{cfg: Config{RuntimeAuth: "x"}}
	if !m.rightsGate(httptest.NewRecorder(), httptest.NewRequest(http.MethodPost, "/", nil), "alice", "agents", "restart_self", "runtime") {
		t.Fatal("unconfigured pattern keeps the legacy posture")
	}
}
