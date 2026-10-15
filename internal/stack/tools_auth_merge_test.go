package stack

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// validateTools edge branches beyond the malformed-entry table: the native
// boundary, managed meet sealing, mcp-raw server resolution and toggle
// conflicts. Runtime/media auth files are provisioned once, then reused.
func TestValidateToolsEdgeBranchesAndAuthFiles(t *testing.T) {
	base := Settings{Schema: 2, User: "alice", Timezone: "UTC"}
	check := func(tools map[string]ToolEntry, want string) {
		s := base
		s.Tools = tools
		err := s.validateTools()
		if want == "" {
			if err != nil {
				t.Fatalf("valid tools rejected: %v (%+v)", err, tools)
			}
			return
		}
		if err == nil || !strings.Contains(err.Error(), want) {
			t.Fatalf("want %q, got %v (%+v)", want, err, tools)
		}
	}
	check(map[string]ToolEntry{"terminal": {Via: ToolViaToolHub}, "code_execution": {Via: ToolViaNative}}, "conflicts with native code_execution")
	check(map[string]ToolEntry{"file": {Via: ToolViaToolHub, Access: "execute"}}, "access must be ro or rw")
	check(map[string]ToolEntry{"file": {Via: ToolViaNative, Access: ToolAccessRO}}, "access ro cannot apply to native")
	check(map[string]ToolEntry{"file": {Via: ToolViaNative, Only: []string{"read"}}}, "native grants are whole-toolset")
	check(map[string]ToolEntry{"web": {Via: ToolViaMCPRaw, Server: "missing"}}, "needs a server defined")
	check(map[string]ToolEntry{"ssh": {Via: ToolViaToolHub, Access: ToolAccessRO, Tools: map[string]bool{"write": true}}}, "conflicts")
	check(map[string]ToolEntry{"ssh": {Via: ToolViaToolHub, Tools: map[string]bool{"nonexistent": true}}}, "unknown toggle")
	check(map[string]ToolEntry{"file": {Via: ToolViaToolHub, Except: []string{"Bad Name"}}}, "invalid tool name")
	check(map[string]ToolEntry{"file": {Via: ToolViaToolHub, Limits: map[string]int{"bad key": 1}}}, "invalid limit")
	check(map[string]ToolEntry{"file": {Via: ToolViaToolHub}}, "")
	check(map[string]ToolEntry{"github": {Via: ToolViaMCP}}, "")

	managed := base
	managed.CapabilityMode = "managed"
	managed.Tools = map[string]ToolEntry{"meet": {Via: ToolViaNative}}
	if err := managed.validateTools(); err == nil || !strings.Contains(err.Error(), "meet") {
		t.Fatalf("managed meet not sealed: %v", err)
	}

	raw := base
	raw.MCP = map[string]MCPServer{"custom": {Command: "srv"}, "playwright": {Command: "pw"}}
	raw.Tools = map[string]ToolEntry{"web": {Via: ToolViaMCPRaw, Server: "custom", Access: ToolAccessRO}}
	if err := raw.validateTools(); err == nil || !strings.Contains(err.Error(), "reviewed mutation table") {
		t.Fatalf("ro mcp-raw without except accepted: %v", err)
	}
	raw.Tools = map[string]ToolEntry{"web": {Via: ToolViaMCPRaw, Server: "custom", Access: ToolAccessRO, Except: []string{"write_thing"}}}
	if err := raw.validateTools(); err != nil {
		t.Fatalf("ro mcp-raw with except rejected: %v", err)
	}
	raw.Tools = map[string]ToolEntry{"web": {Via: ToolViaMCPRaw, Server: "playwright", Access: ToolAccessRO}}
	if err := raw.validateTools(); err != nil {
		t.Fatalf("ro mcp-raw on reviewed server rejected: %v", err)
	}

	ingress := base
	ingress.Ingress = []string{"irc"}
	if err := ingress.validateTools(); err == nil || !strings.Contains(err.Error(), "unknown ingress channel") {
		t.Fatalf("unknown ingress accepted: %v", err)
	}

	dir := t.TempDir()
	authPath := filepath.Join(dir, "runtime.auth")
	if err := ensureRuntimeAuth(authPath); err != nil {
		t.Fatal(err)
	}
	body, err := os.ReadFile(authPath)
	if err != nil || !strings.HasPrefix(string(body), "HUB_RUNTIME_AUTH=") {
		t.Fatalf("runtime auth not provisioned: %v %q", err, body)
	}
	if err := ensureRuntimeAuth(authPath); err != nil {
		t.Fatalf("existing runtime auth rejected: %v", err)
	}
	if err := ensureRuntimeAuth(dir); err == nil {
		t.Fatal("directory accepted as runtime auth")
	}
	mediaPath := filepath.Join(dir, "media.auth")
	if err := ensureMediaAuth(mediaPath); err != nil {
		t.Fatal(err)
	}
	mbody, err := os.ReadFile(mediaPath)
	if err != nil || !strings.Contains(string(mbody), "HUB_MEDIA_AUTH=") || !strings.Contains(string(mbody), "HUB_STT_AUTH=") {
		t.Fatalf("media auth not provisioned: %v %q", err, mbody)
	}
	if err := ensureMediaAuth(mediaPath); err != nil {
		t.Fatalf("existing media auth rejected: %v", err)
	}
}
