package main

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/letya999/hermes-hub/internal/audit"
)

func TestUserDisablePropagatesInOrderAndAuditsStages(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "spaces", "alice")
	writeManagedSpaceDocs(t, dir, "alice", nil)
	expected := []string{"ingress", "jobs", "runs", "toolhub", "connectors", "cron", "oauth", "delivery"}
	var calls []string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost || r.Header.Get("Origin") != "" || r.Header.Get("X-Hub-Principal") != "alice" {
			t.Errorf("bad request: method=%s origin=%q principal=%q", r.Method, r.Header.Get("Origin"), r.Header.Get("X-Hub-Principal"))
		}
		token := strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer ")
		var body struct {
			PrincipalID      string `json:"principal_id"`
			ContextID        string `json:"context_id"`
			Stage            string `json:"stage"`
			DeferContainment bool   `json:"defer_containment"`
		}
		if json.NewDecoder(r.Body).Decode(&body) != nil || body.PrincipalID != "alice" || body.ContextID != "alice" {
			t.Errorf("bad revocation body: %+v", body)
			return
		}
		stage := body.Stage
		switch r.URL.Path {
		case "/v1/admin/holds":
			if token != "supervisor-token" {
				t.Errorf("wrong supervisor bearer")
			}
			if body.DeferContainment {
				stage = "ingress"
			} else {
				stage = "runs"
			}
		case "/v1/admin/revocations":
			if token != "service-token" && token != "toolhub-token" {
				t.Errorf("wrong service bearer")
			}
		default:
			t.Errorf("unexpected path %s", r.URL.Path)
		}
		calls = append(calls, stage)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"stage":"` + stage + `"}`))
	}))
	defer server.Close()
	opts := userDisableOptions{Dir: dir, Environment: "dev", SupervisorURL: server.URL, GatewayURL: server.URL, ToolHubURL: server.URL, SupervisorAuth: "supervisor-token", GatewayAuth: "service-token", ToolHubAuth: "toolhub-token", AuditPath: filepath.Join(t.TempDir(), "audit.jsonl")}
	var out strings.Builder
	if err := runUserDisable(context.Background(), &out, opts); err != nil {
		t.Fatal(err)
	}
	if len(calls) != len(expected) {
		t.Fatalf("calls=%v", calls)
	}
	for i := range expected {
		if calls[i] != expected[i] {
			t.Fatalf("call %d=%s, calls=%v", i, calls[i], calls)
		}
	}
	data, err := os.ReadFile(opts.AuditPath)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(data), "token") || strings.Contains(string(data), server.URL) {
		t.Fatalf("audit leaked control material: %s", data)
	}
	lines := strings.Split(strings.TrimSpace(string(data)), "\n")
	if len(lines) != 18 {
		t.Fatalf("audit events=%d\n%s", len(lines), data)
	}
	correlationID := ""
	for i, line := range lines {
		var event audit.Event
		if json.Unmarshal([]byte(line), &event) != nil || event.Kind != "principal-revocation" || event.PrincipalID != "alice" || event.ContextID != "alice" || event.Environment != "dev" {
			t.Fatalf("event %d: %s", i, line)
		}
		wantStage := "complete"
		if i < 16 {
			wantStage = expected[i/2]
		}
		if event.Action != wantStage {
			t.Fatalf("event %d action=%s want=%s", i, event.Action, wantStage)
		}
		if correlationID == "" {
			correlationID = event.CorrelationID
		}
		if event.CorrelationID != correlationID || event.OperationID != correlationID+":"+event.Action {
			t.Fatalf("event %d correlation mismatch: %+v", i, event)
		}
		if i%2 == 0 && event.Outcome != "admitted" || i%2 == 1 && event.Outcome != "succeeded" {
			t.Fatalf("event %d outcome=%s", i, event.Outcome)
		}
	}
	if !strings.Contains(out.String(), "ingress→jobs→runs→ToolHub→connectors→cron→OAuth→delivery") {
		t.Fatalf("missing order summary: %q", out.String())
	}
}

func TestUserDisableStopsOnFirstFailedStage(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "spaces", "alice")
	writeManagedSpaceDocs(t, dir, "alice", nil)
	var calls []string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body struct {
			Stage            string `json:"stage"`
			DeferContainment bool   `json:"defer_containment"`
		}
		_ = json.NewDecoder(r.Body).Decode(&body)
		stage := body.Stage
		if r.URL.Path == "/v1/admin/holds" {
			if body.DeferContainment {
				stage = "ingress"
			} else {
				stage = "runs"
			}
		}
		calls = append(calls, stage)
		if stage == "toolhub" {
			http.Error(w, "broken", http.StatusBadGateway)
			return
		}
		_, _ = w.Write([]byte(`{}`))
	}))
	defer server.Close()
	opts := userDisableOptions{Dir: dir, Environment: "dev", SupervisorURL: server.URL, GatewayURL: server.URL, ToolHubURL: server.URL, SupervisorAuth: "supervisor-token", GatewayAuth: "service-token", ToolHubAuth: "toolhub-token", AuditPath: filepath.Join(t.TempDir(), "audit.jsonl")}
	err := runUserDisable(context.Background(), &strings.Builder{}, opts)
	if err == nil || !strings.Contains(err.Error(), "toolhub") {
		t.Fatalf("expected stage failure, got %v", err)
	}
	if got := strings.Join(calls, ","); got != "ingress,jobs,runs,toolhub" {
		t.Fatalf("propagated after failure: %s", got)
	}
	data, readErr := os.ReadFile(opts.AuditPath)
	if readErr != nil {
		t.Fatal(readErr)
	}
	if !strings.Contains(string(data), `"action":"toolhub","authorization":"allow","execution":"failed"`) || strings.Contains(string(data), `"action":"delivery"`) {
		t.Fatalf("bad failure audit: %s", data)
	}
}
