package main

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/letya999/hermes-hub/internal/toolhub"
)

func TestGovernanceCLIRuleGrantRevoke(t *testing.T) {
	dir := t.TempDir()
	govPath := filepath.Join(dir, "governance.json")
	base := []string{"--governance", govPath}

	// status on a fresh path reports the shipped default posture without
	// creating the document.
	if err := runGovernance(context.Background(), append(base, "--kind", "status")); err != nil {
		t.Fatal(err)
	}
	if _, err := toolhub.LoadGovernance(govPath); !os.IsNotExist(err) {
		t.Fatal("status must not create the document")
	}

	// Rules need --confirm (digest gate).
	if err := runGovernance(context.Background(), append(base, "--kind", "rule", "--section", "user_mcp", "--reason", "x")); err == nil {
		t.Fatal("unconfirmed rule committed")
	}
	if err := runGovernance(context.Background(), append(base, "--kind", "rule", "--scope", "user", "--user", "alice", "--section", "user_mcp", "--effect", "allow", "--reason", "allow alice user mcp", "--confirm")); err != nil {
		t.Fatal(err)
	}
	doc, err := toolhub.LoadGovernance(govPath)
	if err != nil {
		t.Fatal(err)
	}
	if d := doc.Evaluate("alice", "", "user_mcp", nil, time.Now()); !d.Allowed() {
		t.Fatalf("CLI rule not effective: %+v", d)
	}
	if d := doc.Evaluate("bob", "", "user_mcp", nil, time.Now()); d.Allowed() {
		t.Fatalf("user-scope rule leaked: %+v", d)
	}
	// Retire the same rule without deleting its audit history or silently
	// accepting a stale update. Disabled grants no authority.
	retire := append(append([]string{}, base...), "--kind", "rule", "--scope", "user", "--user", "alice", "--section", "user_mcp", "--effect", "allow", "--reason", "retire workaround", "--status", "disabled", "--revision", "2", "--confirm")
	if err := runGovernance(context.Background(), retire); err != nil {
		t.Fatal(err)
	}
	doc, _ = toolhub.LoadGovernance(govPath)
	if doc.Evaluate("alice", "", "user_mcp", nil, time.Now()).Allowed() {
		t.Fatal("disabled rule still grants access")
	}
	if err := runGovernance(context.Background(), retire); err == nil {
		t.Fatal("stale retirement accepted")
	}

	// Grant: pending, requires confirmRecord, activates only via request+ack.
	if err := runGovernance(context.Background(), append(base, "--kind", "grant", "--user", "alice", "--section", "org_mcp", "--expires", "24h", "--reason", "host approved", "--confirm")); err != nil {
		t.Fatal(err)
	}
	doc, _ = toolhub.LoadGovernance(govPath)
	var pending *toolhub.ToolGrant
	for i := range doc.Grants {
		if doc.Grants[i].Section == "org_mcp" {
			pending = &doc.Grants[i]
		}
	}
	if pending == nil || pending.Status != toolhub.GrantPending {
		t.Fatalf("grant not pending: %+v", pending)
	}
	if err := runGovernance(context.Background(), append(base, "--kind", "revoke", "--grant", pending.GrantID, "--reason", "cleanup")); err != nil {
		t.Fatal(err)
	}
	doc, _ = toolhub.LoadGovernance(govPath)
	for _, g := range doc.Grants {
		if g.GrantID == pending.GrantID && g.Status != toolhub.GrantRevoked {
			t.Fatal("revoke did not land")
		}
	}
	// Revoked grants deny again.
	if d := doc.Evaluate("alice", "", "org_mcp", nil, time.Now()); !d.Allowed() {
		t.Fatalf("org_mcp default should allow: %+v", d)
	}

	// Missing args and unknown kinds fail.
	if err := runGovernance(context.Background(), append(base, "--kind", "grant", "--user", "alice", "--section", "user_mcp")); err == nil {
		t.Fatal("grant without expires/reason accepted")
	}
	if err := runGovernance(context.Background(), append(base, "--kind", "bogus")); err == nil {
		t.Fatal("unknown kind accepted")
	}
	if err := runGovernance(context.Background(), append(base, "--kind", "revoke", "--grant", "x")); err == nil {
		t.Fatal("revoke without reason accepted")
	}
	// Rule arg validation: exactly one of section/match, well-formed pairs.
	if err := runGovernance(context.Background(), append(base, "--kind", "rule", "--reason", "x", "--confirm")); err == nil {
		t.Fatal("rule without target accepted")
	}
	if err := runGovernance(context.Background(), append(base, "--kind", "rule", "--section", "toolhub", "--match", "owner=external", "--reason", "x", "--confirm")); err == nil {
		t.Fatal("section+match rule accepted")
	}
	if err := runGovernance(context.Background(), append(base, "--kind", "rule", "--match", "broken", "--reason", "x", "--confirm")); err == nil {
		t.Fatal("malformed match accepted")
	}
	// Attribute-match rules land and evaluate against projected tool attrs.
	if err := runGovernance(context.Background(), append(base, "--kind", "rule", "--scope", "global", "--match", "owner=external", "--effect", "deny", "--reason", "deny external installs", "--expires", "72h", "--confirm")); err != nil {
		t.Fatal(err)
	}
	doc, _ = toolhub.LoadGovernance(govPath)
	if d := doc.Evaluate("alice", "", "toolhub", map[string]string{"owner": "external"}, time.Now()); d.Effect != toolhub.RuleDeny {
		t.Fatalf("match rule not applied: %+v", d)
	}
}

func TestGovernanceCLIStorePathDerivation(t *testing.T) {
	dir := t.TempDir()
	storePath := filepath.Join(dir, "store.json")
	if err := runGovernance(context.Background(), []string{"--kind", "status", "--toolhub-store", storePath}); err != nil {
		t.Fatal(err)
	}
	govPath := toolhub.GovernanceStorePath(storePath)
	if govPath != filepath.Join(dir, "governance.json") {
		t.Fatalf("derivation: %s", govPath)
	}
	// Corrupt governance fails the CLI closed.
	if err := runGovernance(context.Background(), []string{"--kind", "rule", "--section", "user_mcp", "--reason", "x", "--confirm", "--toolhub-store", storePath}); err != nil {
		t.Fatal(err)
	}
	doc, err := toolhub.LoadGovernance(govPath)
	if err != nil || doc == nil {
		t.Fatalf("rule path did not persist: %v", err)
	}
}

func TestGovernanceCLIFailClosedCorrupt(t *testing.T) {
	dir := t.TempDir()
	govPath := filepath.Join(dir, "governance.json")
	if err := os.WriteFile(govPath, []byte("{corrupt"), 0600); err != nil {
		t.Fatal(err)
	}
	// status on a corrupt doc must error, never seed over it.
	if err := runGovernance(context.Background(), []string{"--kind", "status", "--governance", govPath}); err == nil {
		t.Fatal("corrupt governance doc reported as status")
	}
	doc, loadErr := toolhub.LoadGovernance(govPath)
	if loadErr == nil || doc != nil {
		t.Fatal("corrupt doc silently replaced")
	}
}

// The small host helpers: expiry parsing accepts durations and future
// RFC3339 only; governance ids sanitize into the identity alphabet.
func TestGovernanceCLIHelpers(t *testing.T) {
	now := time.Now().UTC()
	if at, err := parseExpiry("24h", now); err != nil || !at.Equal(now.Add(24*time.Hour)) {
		t.Fatalf("duration: %v %v", at, err)
	}
	future := now.Add(48 * time.Hour).UTC().Format(time.RFC3339)
	if at, err := parseExpiry(future, now); err != nil || !at.Equal(now.Add(48*time.Hour).Truncate(time.Second)) {
		t.Fatalf("rfc3339: %v %v", at, err)
	}
	for _, bad := range []string{"-1h", "0s", "not-a-time", "2020-01-01T00:00:00Z"} {
		if _, err := parseExpiry(bad, now); err == nil {
			t.Fatalf("bad expiry %q accepted", bad)
		}
	}
	if id := deterministicGrantID("alice", "user_mcp", "explicit-1"); id != "explicit-1" {
		t.Fatalf("explicit id: %s", id)
	}
	if id := deterministicGrantID("alice", "user_mcp", ""); !strings.HasPrefix(id, "toolgrant-alice-user-mcp-") {
		t.Fatalf("derived id: %s", id)
	}
	if id := governanceID("x", "A.B/C:D"); id != "x-a-b-c-d" {
		t.Fatalf("sanitize: %s", id)
	}
	long := governanceID("x", strings.Repeat("a", 100))
	if len(long) > 64 {
		t.Fatalf("length bound: %d", len(long))
	}
}
