package main

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/letya999/hermes-hub/internal/supervisor"
)

func TestCollectStatusMergesBothPlanes(t *testing.T) {
	sup := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer tok" {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		_ = json.NewEncoder(w).Encode(supervisor.StatusView{
			GeneratedAt: time.Now(),
			Runtimes: []supervisor.RuntimeStatus{{
				PrincipalID: "alice", ContextID: "ctx", Phase: "degraded", Desired: true,
				Conditions: []supervisor.Condition{
					{Name: "ready", OK: false, Reason: "readiness_unavailable"},
					{Name: "ownership_verified", OK: true, Reason: "verified"},
					{Name: "connectors", OK: true, Reason: "healthy"},
					{Name: "crash_budget", OK: true, Reason: "within_budget"},
				},
			}},
			Jobs: supervisor.JobSummary{Total: 2, Unconfirmed: 1, ByPhase: map[string]int{"running": 1, "failed": 1}},
		})
	}))
	defer sup.Close()
	gw := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]any{
			"generated_at": time.Now(),
			"queue":        map[string]int{"pending": 2, "running": 0, "failed": 1},
			"outbox":       map[string]int{"pending": 3, "sending": 0, "failed": 0, "oldest_pending_seconds": 42},
			"routines":     map[string]any{"state": "ok", "total": 4, "enabled": 3, "overdue": 1},
		})
	}))
	defer gw.Close()

	status := collectStatus(t.Context(), statusOptions{
		SupervisorURL: sup.URL, GatewayURL: gw.URL, Token: "tok", Client: sup.Client(),
	})
	if status.Supervisor == nil || status.Gateway == nil {
		t.Fatalf("missing section: %+v", status)
	}
	if status.Supervisor.Runtimes[0].Phase != "degraded" {
		t.Fatalf("phase=%q", status.Supervisor.Runtimes[0].Phase)
	}
	if status.Gateway.Queue.Pending != 2 || status.Gateway.Routines.Overdue != 1 {
		t.Fatalf("gateway=%+v", status.Gateway)
	}

	var out strings.Builder
	renderStatus(&out, status)
	text := out.String()
	for _, want := range []string{"degraded", "ready=readiness_unavailable", "pending=2", "overdue=1", "unconfirmed=1"} {
		if !strings.Contains(text, want) {
			t.Fatalf("render missing %q:\n%s", want, text)
		}
	}
}

func TestCollectStatusUnreachablePlane(t *testing.T) {
	status := collectStatus(t.Context(), statusOptions{
		SupervisorURL: "http://127.0.0.1:1", GatewayURL: "http://127.0.0.1:1",
		Timeout: 500 * time.Millisecond,
	})
	if status.Supervisor != nil || status.Gateway != nil {
		t.Fatal("unreachable endpoints must not fabricate sections")
	}
	if len(status.Errors) != 2 {
		t.Fatalf("errors=%v", status.Errors)
	}
	var out strings.Builder
	renderStatus(&out, status)
	if !strings.Contains(out.String(), "unreachable") {
		t.Fatalf("render should mark unreachable planes:\n%s", out.String())
	}
}
