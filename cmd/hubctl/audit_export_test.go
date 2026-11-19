package main

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"

	"github.com/letya999/hermes-hub/internal/audit"
	"github.com/letya999/hermes-hub/shared/locale"
)

func TestAuditExportAtLeastOnce(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "spaces", "alice")
	ledgerPath := filepath.Join(dir, "runtime", "toolhub", "audit.jsonl")
	ledger, err := audit.Open(ledgerPath)
	if err != nil {
		t.Fatal(err)
	}
	first := audit.NewEvent("tool-call", "alice", "succeeded")
	second := audit.NewEvent("introspection", "alice", "admitted")
	for _, event := range []audit.Event{first, second} {
		if err := ledger.Append(event); err != nil {
			t.Fatal(err)
		}
	}

	requests := 0
	seen := []string{}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests++
		if got := r.Header.Get("Authorization"); got != "Bearer sink-token" {
			t.Errorf("missing bearer: %q", got)
		}
		var body auditExportEnvelope
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Errorf("bad export body: %v", err)
		}
		for _, event := range body.Events {
			seen = append(seen, event.EventID)
			if event.Kind == "audit-export" {
				t.Errorf("control event exported")
			}
		}
		if requests == 1 {
			w.WriteHeader(http.StatusBadGateway)
			return
		}
		w.WriteHeader(http.StatusNoContent)
	}))
	defer server.Close()

	t.Setenv("HUB_TEST_SINK_TOKEN", "sink-token")
	args := []string{"--dir", dir, "--webhook", server.URL, "--token-env", "HUB_TEST_SINK_TOKEN", "--once"}
	if err := runAuditExport(context.Background(), locale.LangEN, args); err == nil {
		t.Fatal("first sink failure must surface")
	}
	if err := runAuditExport(context.Background(), locale.LangEN, args); err != nil {
		t.Fatalf("retry export failed: %v", err)
	}
	want := []string{first.EventID, second.EventID, first.EventID, second.EventID}
	if len(seen) != len(want) {
		t.Fatalf("seen %v, want %v", seen, want)
	}
	for i := range want {
		if seen[i] != want[i] {
			t.Fatalf("order changed: seen %v, want %v", seen, want)
		}
	}

	after, err := audit.LoadExportCursor(audit.ExportCursorPath(ledgerPath), server.URL, nil)
	if err != nil || after != 2 {
		t.Fatalf("cursor = %d, %v", after, err)
	}
	events, err := ledger.List("alice")
	if err != nil {
		t.Fatal(err)
	}
	auditExports := 0
	for _, event := range events {
		if event.Kind == "audit-export" {
			auditExports++
		}
	}
	if auditExports != 1 {
		t.Fatalf("want one local degraded transition, got %d", auditExports)
	}
}

func TestAuditExportConfigurationGuards(t *testing.T) {
	dir := t.TempDir()
	if _, err := auditExportConfiguration(dir, "prod", "http://sink.invalid/ingest", "", "", true, 0); err == nil {
		t.Fatal("external HTTP destination accepted")
	}
	if _, err := auditExportConfiguration(dir, "prod", "https://sink.invalid/ingest", "", "", true, 0); err == nil {
		t.Fatal("external HTTPS destination without token accepted")
	}
	if _, err := auditExportConfiguration(dir, "prod", "http://127.0.0.1:9/ingest", "", "audit-export", true, 0); err == nil {
		t.Fatal("recursive audit-export kind accepted")
	}
}
