//go:build live

package main

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/letya999/hermes-hub/internal/audit"
)

// TestLiveUserDisable drives runUserDisable against the running dev stack.
// Enable with -tags live plus env:
//
//	LIVE_SUP_AUTH, LIVE_GW_AUTH, LIVE_TH_AUTH: real control bearers
//	LIVE_SPACE: writable dir ending spaces/<principal>; a published fixture
//	space is fabricated there and reused by the audit-export checks.
func TestLiveUserDisable(t *testing.T) {
	supAuth := os.Getenv("LIVE_SUP_AUTH")
	gwAuth := os.Getenv("LIVE_GW_AUTH")
	thAuth := os.Getenv("LIVE_TH_AUTH")
	spaceDir := os.Getenv("LIVE_SPACE")
	if supAuth == "" || gwAuth == "" || thAuth == "" || spaceDir == "" {
		t.Skip("live acceptance env not configured")
	}
	principal := filepath.Base(filepath.Clean(spaceDir))
	writeManagedSpaceDocs(t, spaceDir, principal, nil)
	var out strings.Builder
	opts := userDisableOptions{
		Dir: spaceDir, Environment: "dev",
		SupervisorURL: "http://127.0.0.1:8876", GatewayURL: "http://127.0.0.1:8081", ToolHubURL: "http://127.0.0.1:8090",
		SupervisorAuth: supAuth, GatewayAuth: gwAuth, ToolHubAuth: thAuth,
		Timeout:   15 * time.Second,
		AuditPath: filepath.Join(spaceDir, "runtime", "toolhub", "audit.jsonl"),
	}
	if err := runUserDisable(context.Background(), &out, opts); err != nil {
		t.Fatalf("live disable: %v\n%s", err, out.String())
	}
	t.Logf("\n%s", out.String())
}

// TestLiveIntrospectionDeny exercises the introspection gate on a fabricated
// group-scope space: no explicit flag means deny, and the deny lands in the
// same audit ledger the exporter drains.
func TestLiveIntrospectionDeny(t *testing.T) {
	spaceDir := os.Getenv("LIVE_SPACE_GROUP")
	if spaceDir == "" {
		t.Skip("live acceptance env not configured")
	}
	principal := filepath.Base(filepath.Clean(spaceDir))
	writeManagedSpaceDocs(t, spaceDir, principal, map[string]string{
		"stack": "kind: stack\nidentity: {user: " + principal + ", infra: false, kind: group}\nservices:\n" +
			"  hermes-runtime: {mode: supervised}\n" +
			"  model-relay: {mode: per-space}\n" +
			"  toolhub-relay: {mode: per-space}\n" +
			"  toolhub: {mode: shared}\n" +
			"  credential-broker: {mode: shared}\n",
	})
	if _, err := runtimeIntrospectionGate(spaceDir, "dev", "exec"); err == nil {
		t.Fatal("group scope without introspection flag was allowed")
	}
	body, err := os.ReadFile(filepath.Join(spaceDir, "runtime", "toolhub", "audit.jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	var event audit.Event
	for _, line := range strings.Split(strings.TrimSpace(string(body)), "\n") {
		if err := json.Unmarshal([]byte(line), &event); err != nil {
			t.Fatalf("audit line: %v", err)
		}
	}
	if event.Kind != "introspection" || event.Action != "exec" || event.Authorization != "deny" || event.Outcome != "denied" || event.PrincipalID != principal {
		t.Fatalf("deny event missing: %+v", event)
	}
}
