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

	"github.com/letya999/hermes-hub/internal/toolhub"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

func TestTelegramConnectorCLIAccountBindCallRevoke(t *testing.T) {
	root := t.TempDir()
	t.Setenv("HUB_STATE", root)
	deployment := toolhub.ToolDefinition{Source: toolhub.DefinitionSource{Image: "ghcr.io/fixture/telegram-account", Digest: "sha256:" + strings.Repeat("1", 64)}, Workload: toolhub.WorkloadPolicy{ToolHiveVersion: "v0.48.0", SidecarImages: []string{"ghcr.io/fixture/sidecar@sha256:" + strings.Repeat("2", 64)}}, Execution: toolhub.ExecutionPolicy{TimeoutSeconds: 30, OutputBytes: 65536, CPUMillis: 500, MemoryMiB: 256, MaxPIDs: 32, Egress: []string{"telegram.org"}, Mounts: []toolhub.Mount{{Source: "connection-state", Target: "/run/connector"}}}, Health: toolhub.HealthProbe{Kind: "exec", Value: "/opt/telegram/.venv/bin/python", TimeoutSeconds: 5}}
	manifest := filepath.Join(t.TempDir(), "deployment.json")
	raw, _ := json.Marshal(deployment)
	if err := os.WriteFile(manifest, raw, 0600); err != nil {
		t.Fatal(err)
	}
	input := filepath.Join(t.TempDir(), "session.txt")
	if err := os.WriteFile(input, []byte("TELEGRAM_API_ID=1\nTELEGRAM_API_HASH=fixture-hash\nTELEGRAM_SESSION_STRING=fixture-session"), 0600); err != nil {
		t.Fatal(err)
	}
	accountID := "10"
	calls := 0
	server := mcp.NewServer(&mcp.Implementation{Name: "fixture-account", Version: "1.0.0"}, nil)
	for _, name := range []string{"get_account", "list_dialogs", "get_messages", "send_message", "reply_message", "delete_message"} {
		name := name
		server.AddTool(&mcp.Tool{Name: name, InputSchema: map[string]any{"type": "object"}}, func(ctx context.Context, r *mcp.CallToolRequest) (*mcp.CallToolResult, error) {
			calls++
			out := map[string]any{"account_id": accountID}
			if name != "get_account" {
				out = map[string]any{"peer_id": 7, "message_id": 42, "receipt": "10:7:42"}
			}
			if name == "delete_message" {
				out["pts"] = 123
				out["receipt"] = "10:7:42:pts:123"
			}
			return &mcp.CallToolResult{StructuredContent: out, Content: []mcp.Content{&mcp.TextContent{Text: "fixture"}}}, nil
		})
	}
	mcpHandler := mcp.NewStreamableHTTPHandler(func(*http.Request) *mcp.Server { return server }, &mcp.StreamableHTTPOptions{Stateless: true, JSONResponse: true})
	fixture := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/admit" {
			mcpHandler.ServeHTTP(w, r)
			return
		}
		var plan struct {
			WorkloadID    string                  `json:"workload_id"`
			WorkspacePath string                  `json:"workspace_path"`
			Execution     toolhub.ExecutionPolicy `json:"execution"`
		}
		if json.NewDecoder(r.Body).Decode(&plan) != nil {
			t.Error("invalid plan")
		}
		body, err := os.ReadFile(filepath.Join(plan.WorkspacePath, "credentials.env"))
		if err != nil || !strings.Contains(string(body), "fixture-session") {
			t.Error("credentials not injected before admission")
		}
		_ = json.NewEncoder(w).Encode(toolhub.AdmissionReceipt{WorkloadID: plan.WorkloadID, State: "running", Enforced: true, ImageDigest: deployment.Source.Digest, SidecarImages: deployment.Workload.SidecarImages, Execution: plan.Execution})
	}))
	defer fixture.Close()
	t.Setenv("HUB_TOOLHIVE_ADMISSION_ENDPOINT", fixture.URL+"/admit")
	registry := filepath.Join(t.TempDir(), "registry.json")
	base := []string{"--provider", "telegram", "--user", "alice", "--account", "10", "--toolhub-store", registry, "--store", filepath.Join(t.TempDir(), "store.enc"), "--key-file", filepath.Join(t.TempDir(), "key"), "--from-file", input, "--deployment-file", manifest, "--endpoint", fixture.URL + "/mcp"}
	for _, write := range []bool{false, true} {
		connection, id := "telegram-read", "telegram-account-read"
		extra := []string{}
		if write {
			connection, id = "telegram-write", "telegram-account-write"
			extra = []string{"--write"}
		}
		args := append(append([]string{}, base...), "--connection", connection)
		args = append(args, extra...)
		if err := run(context.Background(), append([]string{"connector", "connect"}, args...)); err != nil {
			t.Fatal(err)
		}
		name := "get_messages"
		if write {
			name = "send_message"
		}
		callInput := filepath.Join(t.TempDir(), "call.json")
		if err := os.WriteFile(callInput, []byte(`{"peer_id":7,"text":"fixture"}`), 0600); err != nil {
			t.Fatal(err)
		}
		if !write {
			_ = os.WriteFile(callInput, []byte(`{"peer_id":7}`), 0600)
		}
		callArgs := append(append([]string{}, args...), "--from-file", callInput, "--tool", toolhub.ProjectedToolName(id, "1.0.0", name))
		if err := run(context.Background(), append([]string{"connector", "call"}, callArgs...)); err != nil {
			t.Fatal(err)
		}
		if err := run(context.Background(), append([]string{"connector", "refresh"}, args...)); err == nil {
			t.Fatal("OAuth refresh admitted")
		}
		if err := run(context.Background(), append([]string{"connector", "revoke"}, args...)); err != nil {
			t.Fatal(err)
		}
		before := calls
		if err := run(context.Background(), append([]string{"connector", "call"}, callArgs...)); err == nil {
			t.Fatal("revoked call")
		}
		if calls != before {
			t.Fatal("revoked backend invoked")
		}
	}
	accountID = "999"
	beforeMismatch, _ := os.ReadFile(registry)
	args := append(append([]string{}, base...), "--connection", "mismatch")
	if err := run(context.Background(), append([]string{"connector", "connect"}, args...)); err == nil {
		t.Fatal("wrong account bound")
	}
	afterMismatch, _ := os.ReadFile(registry)
	if string(beforeMismatch) != string(afterMismatch) {
		t.Fatal("unverified account published before get_me")
	}
	for _, overrides := range [][]string{{"--account", "bad"}, {"--endpoint", "https://external.example/mcp"}, {"--from-file", manifest}, {"--deployment-file", input}} {
		bad := append(append([]string{}, args...), overrides...)
		if err := run(context.Background(), append([]string{"connector", "connect"}, bad...)); err == nil {
			t.Fatal("invalid connect accepted")
		}
	}
	t.Setenv("HUB_TOOLHIVE_ADMISSION_ENDPOINT", "")
	if err := run(context.Background(), append([]string{"connector", "connect"}, args...)); err == nil {
		t.Fatal("missing controller")
	}
}
