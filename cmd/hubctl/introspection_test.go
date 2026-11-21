package main

import (
	"context"
	"path/filepath"
	"testing"

	"github.com/letya999/hermes-hub/internal/audit"
)

func TestRuntimeIntrospectionGateAuditsDeniedCall(t *testing.T) {
	root := t.TempDir()
	dir := filepath.Join(root, "spaces", "alice")
	if _, err := runtimeIntrospectionGate(context.Background(), dir, "dev", "exec"); err == nil {
		t.Fatal("unpublished space passed the introspection gate")
	}

	ledger, err := audit.Open(filepath.Join(dir, "runtime", "toolhub", "audit.jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	events, err := ledger.List("alice")
	if err != nil {
		t.Fatal(err)
	}
	if len(events) != 1 {
		t.Fatalf("want one introspection event, got %d", len(events))
	}
	event := events[0]
	if event.Kind != "introspection" || event.Action != "exec" || event.Authorization != "deny" || event.Outcome != "denied" || event.Reason != "config" {
		t.Fatalf("bad introspection event: %+v", event)
	}
	if event.Name == "" || event.Name == "operator:" {
		t.Fatalf("operator actor missing: %+v", event)
	}
	if event.Receipt != "" {
		t.Fatalf("introspection audit leaked a receipt: %+v", event)
	}
}
