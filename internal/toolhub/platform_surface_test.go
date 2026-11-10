package toolhub

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/letya999/hermes-hub/internal/identity"
	"github.com/letya999/hermes-hub/internal/toolhub/api"
	"github.com/modelcontextprotocol/go-sdk/mcp"
	"net/http/httptest"
)

// M31.8: the model-visible connector surface carries the five catalog r5
// names — legacy verb tools (connector_enable/disable/remove/reauth/
// allow_writes) and invoke never appear on the model surface.
func TestConnectorSurfaceR5Names(t *testing.T) {
	s, auth, _, profile := managedStore(t)
	profile.Revision++
	profile.Connectors = api.ConnectorsPersonal
	if err := putProfile(t, s, profile); err != nil {
		t.Fatal(err)
	}
	g := &Gateway{Store: s, Tokens: map[string]identity.Envelope{aliceToken: auth},
		Control: &ControlPlane{Store: s, Now: time.Now},
		Backend: backendFunc(func(context.Context, api.EffectiveBinding, api.ToolSpec, map[string]any) (api.BackendResult, error) {
			return api.BackendResult{Text: "ok"}, nil
		})}
	attachGatewayAudit(t, g)
	handler, err := g.Handler()
	if err != nil {
		t.Fatal(err)
	}
	server := httptest.NewServer(handler)
	t.Cleanup(server.Close)
	client := mcpConnect(t, server.URL, aliceToken)
	listed, err := client.ListTools(t.Context(), nil)
	if err != nil {
		t.Fatal(err)
	}
	got := map[string]bool{}
	for _, tool := range listed.Tools {
		got[tool.Name] = true
	}
	for _, name := range []string{"connector_list", "connector_search", "connector_status", "connector_install", "connector_edit"} {
		if !got[name] {
			t.Fatalf("r5 connector surface missing %q: %v", name, got)
		}
	}
	for _, legacy := range []string{"connector_enable", "connector_disable", "connector_remove", "connector_reauth", "connector_allow_writes", "invoke", "tools_list", "system_guide"} {
		if got[legacy] {
			t.Fatalf("legacy name %q leaked onto the model surface", legacy)
		}
	}
	for _, platform := range []string{"platform_guide", "tool_search", "tool_call", "platform_status"} {
		if !got[platform] {
			t.Fatalf("platform layer missing %q: %v", platform, got)
		}
	}
	// connector_edit multiplexes every lifecycle verb — each action resolves
	// the facade and reports a real outcome, not "unknown connector tool".
	// A dispatched call returns a real outcome — a domain error such as
	// "connector not found" proves dispatch; only "unknown connector tool"
	// means the r5 name never reached the facade.
	callText := func(t *testing.T, name string, args map[string]any) string {
		t.Helper()
		res, err := client.CallTool(t.Context(), &mcp.CallToolParams{Name: name, Arguments: args})
		if err != nil {
			return err.Error()
		}
		text := ""
		for _, content := range res.Content {
			if tc, ok := content.(*mcp.TextContent); ok {
				text += tc.Text
			}
		}
		return text
	}
	for _, action := range []string{"enable", "disable", "allow_writes", "reauth", "remove"} {
		if text := callText(t, "connector_edit", map[string]any{"name": "nonexistent", "action": action}); strings.Contains(text, "unknown connector tool") {
			t.Fatalf("connector_edit action %q not multiplexed: %s", action, text)
		}
	}
	// The catalog argument is `name`; the facade resolves it like `target`.
	if text := callText(t, "connector_status", map[string]any{"name": "nonexistent"}); strings.Contains(text, "unknown connector tool") {
		t.Fatalf("connector_status name normalization: %s", text)
	}
	// platform_status answers bounded allowlisted diagnostics — never env,
	// auth files or credential keys (AGENTS diagnostic allowlist rule).
	status := callText(t, "platform_status", map[string]any{})
	for _, field := range []string{"tools_admitted", "tools_visible", "service"} {
		if !strings.Contains(status, field) {
			t.Fatalf("platform_status missing %q: %s", field, status)
		}
	}
	for _, forbidden := range []string{"HUB_", "token", "secret", "password", "credential"} {
		if strings.Contains(strings.ToLower(status), strings.ToLower(forbidden)) {
			t.Fatalf("platform_status leaked %q: %s", forbidden, status)
		}
	}
}

// M31.8 S3: platform_bootstrap lists pending first-run steps, records
// action=done step=X idempotently and reports done once every step is closed.
func TestPlatformBootstrapProgress(t *testing.T) {
	s, auth, _, profile := managedStore(t)
	profile.Revision++
	if err := putProfile(t, s, profile); err != nil {
		t.Fatal(err)
	}
	t.Setenv("HUB_STATE", t.TempDir())
	g := &Gateway{Store: s, Tokens: map[string]identity.Envelope{aliceToken: auth},
		Control: &ControlPlane{Store: s, Now: time.Now},
		Backend: backendFunc(func(context.Context, api.EffectiveBinding, api.ToolSpec, map[string]any) (api.BackendResult, error) {
			return api.BackendResult{Text: "ok"}, nil
		})}
	attachGatewayAudit(t, g)
	handler, err := g.Handler()
	if err != nil {
		t.Fatal(err)
	}
	server := httptest.NewServer(handler)
	t.Cleanup(server.Close)
	client := mcpConnect(t, server.URL, aliceToken)
	text := func(args map[string]any) string {
		res, err := client.CallTool(t.Context(), &mcp.CallToolParams{Name: "platform_bootstrap", Arguments: args})
		if err != nil {
			return err.Error()
		}
		out := ""
		for _, c := range res.Content {
			if tc, ok := c.(*mcp.TextContent); ok {
				out += tc.Text
			}
		}
		return out
	}
	first := text(map[string]any{})
	if strings.Contains(first, "\"done\":true") || !strings.Contains(first, "steps") {
		t.Fatalf("fresh space bootstrap should list pending steps: %s", first)
	}
	if done := text(map[string]any{"action": "done", "step": "workspace"}); !strings.Contains(done, "workspace") {
		t.Fatalf("done action not recorded: %s", done)
	}
	// Idempotent: marking the remaining steps converges to done.
	for _, step := range []string{"profile", "connector", "workspace"} {
		text(map[string]any{"action": "done", "step": step})
	}
	if last := text(map[string]any{}); !strings.Contains(last, "\"done\":true") {
		t.Fatalf("bootstrap did not converge to done: %s", last)
	}
	if again := text(map[string]any{"action": "done", "step": "workspace"}); !strings.Contains(again, "\"done\":true") {
		t.Fatalf("bootstrap done state is not idempotent: %s", again)
	}
}

// M31.8 S4: platform_usage honestly answers unavailable with no usage source,
// and joins meter counters with models.yaml limits when both are mounted.
func TestPlatformUsageReport(t *testing.T) {
	s, auth, _, profile := managedStore(t)
	profile.Revision++
	if err := putProfile(t, s, profile); err != nil {
		t.Fatal(err)
	}
	t.Setenv("HUB_MODEL_USAGE_FILE", "")
	t.Setenv("HUB_MODEL_REGISTRY_FILE", "")
	g := &Gateway{Store: s, Tokens: map[string]identity.Envelope{aliceToken: auth},
		Control: &ControlPlane{Store: s, Now: time.Now},
		Backend: backendFunc(func(context.Context, api.EffectiveBinding, api.ToolSpec, map[string]any) (api.BackendResult, error) {
			return api.BackendResult{Text: "ok"}, nil
		})}
	attachGatewayAudit(t, g)
	handler, err := g.Handler()
	if err != nil {
		t.Fatal(err)
	}
	server := httptest.NewServer(handler)
	t.Cleanup(server.Close)
	client := mcpConnect(t, server.URL, aliceToken)
	call := func(args map[string]any) string {
		res, err := client.CallTool(t.Context(), &mcp.CallToolParams{Name: "platform_usage", Arguments: args})
		if err != nil {
			return err.Error()
		}
		out := ""
		for _, c := range res.Content {
			if tc, ok := c.(*mcp.TextContent); ok {
				out += tc.Text
			}
		}
		return out
	}
	if empty := call(map[string]any{}); !strings.Contains(empty, "unavailable") {
		t.Fatalf("platform_usage without a mounted meter must answer unavailable: %s", empty)
	}
	dir := t.TempDir()
	usagePath := filepath.Join(dir, "usage.json")
	registryPath := filepath.Join(dir, "models.yaml")
	if err := os.WriteFile(usagePath, []byte(`{"antigravity/gemini|shared|tokens|1d|1000":{"until":9999999999,"count":42}}`), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(registryPath, []byte("providers: {}\nmodels:\n  antigravity/gemini:\n    ladder:\n      - provider: antigravity\n        limits:\n          - {of: tokens, amount: 100, per: 1d}\n"), 0600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("HUB_MODEL_USAGE_FILE", usagePath)
	t.Setenv("HUB_MODEL_REGISTRY_FILE", registryPath)
	report := call(map[string]any{})
	for _, field := range []string{"antigravity/gemini", "tokens", "\"limit\":100", "\"used\":42"} {
		if !strings.Contains(report, field) {
			t.Fatalf("platform_usage missing %q: %s", field, report)
		}
	}
	if filtered := call(map[string]any{"period": "1w"}); strings.Contains(filtered, "tokens") {
		t.Fatalf("period filter leaked non-matching windows: %s", filtered)
	}
}

// The shared ToolHub serves every space: usage and registry resolve under
// per-principal roots, never one flat path — a sibling's meter is not yours.
func TestPlatformUsagePerPrincipalRoots(t *testing.T) {
	s, auth, _, profile := managedStore(t)
	profile.Revision++
	if err := putProfile(t, s, profile); err != nil {
		t.Fatal(err)
	}
	root := t.TempDir()
	principalRoot := filepath.Join(root, "alice")
	if err := os.MkdirAll(principalRoot, 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(principalRoot, "model-select.json.usage.json"), []byte(`{"m|shared|requests|5h|10":{"until":9999999999,"count":7}}`), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(principalRoot, "models.yaml"), []byte("models:\n  m:\n    ladder:\n      - limits:\n          - {of: requests, amount: 10, per: 5h}\n"), 0600); err != nil {
		t.Fatal(err)
	}
	// A sibling's files must never leak into alice's report.
	other := filepath.Join(root, "mallory")
	if err := os.MkdirAll(other, 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(other, "models.yaml"), []byte("models:\n  sibling-only:\n    ladder:\n      - limits:\n          - {of: tokens, amount: 1, per: 1d}\n"), 0600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("HUB_MODEL_USAGE_FILE", "")
	t.Setenv("HUB_MODEL_REGISTRY_FILE", "")
	t.Setenv("HUB_MODEL_USAGE_BY", root)
	t.Setenv("HUB_MODEL_REGISTRY_BY", root)
	g := &Gateway{Store: s, Tokens: map[string]identity.Envelope{aliceToken: auth},
		Control: &ControlPlane{Store: s, Now: time.Now},
		Backend: backendFunc(func(context.Context, api.EffectiveBinding, api.ToolSpec, map[string]any) (api.BackendResult, error) {
			return api.BackendResult{Text: "ok"}, nil
		})}
	attachGatewayAudit(t, g)
	handler, err := g.Handler()
	if err != nil {
		t.Fatal(err)
	}
	server := httptest.NewServer(handler)
	t.Cleanup(server.Close)
	client := mcpConnect(t, server.URL, aliceToken)
	res, err := client.CallTool(t.Context(), &mcp.CallToolParams{Name: "platform_usage", Arguments: map[string]any{}})
	if err != nil {
		t.Fatal(err)
	}
	out := ""
	for _, c := range res.Content {
		if tc, ok := c.(*mcp.TextContent); ok {
			out += tc.Text
		}
	}
	for _, field := range []string{"\"used\":7", "\"limit\":10", "\"available\":true"} {
		if !strings.Contains(out, field) {
			t.Fatalf("per-principal usage missing %q: %s", field, out)
		}
	}
	if strings.Contains(out, "sibling-only") {
		t.Fatalf("per-principal usage leaked a sibling registry: %s", out)
	}
	// A meter file that does not exist yet means no reservations — the
	// registry limits still report with used:0, never "unavailable".
	if err := os.Remove(filepath.Join(principalRoot, "model-select.json.usage.json")); err != nil {
		t.Fatal(err)
	}
	res, err = client.CallTool(t.Context(), &mcp.CallToolParams{Name: "platform_usage", Arguments: map[string]any{}})
	if err != nil {
		t.Fatal(err)
	}
	out = ""
	for _, c := range res.Content {
		if tc, ok := c.(*mcp.TextContent); ok {
			out += tc.Text
		}
	}
	for _, field := range []string{"\"available\":true", "\"used\":0", "\"limit\":10"} {
		if !strings.Contains(out, field) {
			t.Fatalf("absent meter must report zero usage, got %q missing: %s", field, out)
		}
	}
}
