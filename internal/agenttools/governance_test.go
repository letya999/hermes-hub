package agenttools

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/letya999/hermes-hub/internal/toolhub"
)

// The rendered snapshot is the executor's third-plane fence: it re-checks
// host policy at dispatch so a mid-session denial cuts even an admitted
// binding. Nil policy (unmanaged runtime) stays permissive.
func TestToolPolicyDispatchDeny(t *testing.T) {
	v := fixture(t)
	write(t, v, "docs/a.txt", "body")

	if _, err := v.ExecCall(context.Background(), "file_read", map[string]any{"path": "docs/a.txt"}, nil); err != nil {
		t.Fatalf("nil policy must not deny: %v", err)
	}
	v.ToolPolicy = &toolhub.ToolPolicySnapshot{Schema: 1, Principal: "me", Deny: []string{"files"}}
	if _, err := v.ExecCall(context.Background(), "file_read", map[string]any{"path": "docs/a.txt"}, nil); err == nil || !strings.Contains(err.Error(), "denied by tool governance") {
		t.Fatalf("denied family admitted: %v", err)
	}
	if _, err := v.ExecCall(context.Background(), "service_catalog", map[string]any{}, nil); err != nil {
		t.Fatalf("unrelated family denied: %v", err)
	}
	v.ToolPolicy.DenyAll = true
	if _, err := v.ExecCall(context.Background(), "service_catalog", map[string]any{}, nil); err == nil {
		t.Fatal("deny_all admitted a call")
	}
	// cap:read refuses write-effect tools of the family, keeps reads.
	v.ToolPolicy = &toolhub.ToolPolicySnapshot{Schema: 1, Principal: "me", ReadOnly: []string{"files"}}
	if _, err := v.ExecCall(context.Background(), "file_write", map[string]any{"path": "docs/b.txt", "text": "x"}, nil); err == nil || !strings.Contains(err.Error(), "read-only") {
		t.Fatalf("capped family admitted a write: %v", err)
	}
	if _, err := v.ExecCall(context.Background(), "file_read", map[string]any{"path": "docs/a.txt"}, nil); err != nil {
		t.Fatalf("capped family denied a read: %v", err)
	}
}

func TestToolPolicyLoadFailsClosed(t *testing.T) {
	dir := t.TempDir()
	policyPath := filepath.Join(dir, "tool-policy.json")
	if got, err := loadToolPolicy(); err != nil || got != nil {
		t.Fatalf("unset env must not load: %v", err)
	}
	t.Setenv("HUB_TOOL_POLICY", policyPath)
	if _, err := loadToolPolicy(); err == nil {
		t.Fatal("missing snapshot loaded")
	}
	if err := os.WriteFile(policyPath, []byte("{corrupt"), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := loadToolPolicy(); err == nil {
		t.Fatal("corrupt snapshot loaded")
	}
	// The pin being set but unreadable must stop the executor entirely.
	if _, err := OpenRoots(dir, "", ""); err == nil {
		t.Fatal("corrupt policy opened an executor")
	}
	body, _ := json.Marshal(toolhub.ToolPolicySnapshot{Schema: 1, Principal: "me", Deny: []string{"ssh"}})
	if err := os.WriteFile(policyPath, body, 0600); err != nil {
		t.Fatal(err)
	}
	v, err := OpenRoots(dir, "", "")
	if err != nil {
		t.Fatal(err)
	}
	defer v.Close()
	if v.ToolPolicy == nil || !v.ToolPolicy.Denied("ssh") {
		t.Fatal("policy not loaded into Tools")
	}
}
