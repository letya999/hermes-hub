package e2e

import (
	"context"
	"encoding/json"
	"fmt"
	"github.com/letya999/hermes-hub/internal/communication"
	"github.com/letya999/hermes-hub/internal/identity"
	"github.com/modelcontextprotocol/go-sdk/mcp"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

func TestReadAgentComparesIndependentOwnerResult(t *testing.T) {
	for _, name := range []string{"discover", "settings_get", "service_catalog", "status", "required_credentials", "diagnostics"} {
		for _, lie := range []bool{false, true} {
			t.Run(fmt.Sprint(name, lie), func(t *testing.T) {
				root := t.TempDir()
				for _, dir := range []string{"mappings", "mock-sent"} {
					if err := os.MkdirAll(filepath.Join(root, dir), 0700); err != nil {
						t.Fatal(err)
					}
				}
				server := mcp.NewServer(&mcp.Implementation{Name: "read-fixture", Version: "1"}, nil)
				names := []string{name}
				if name == "required_credentials" {
					names = append(names, "status")
				}
				for _, operation := range names {
					server.AddTool(&mcp.Tool{Name: operation, InputSchema: map[string]any{"type": "object"}}, func(_ context.Context, r *mcp.CallToolRequest) (*mcp.CallToolResult, error) {
						if r.Params.Name == "discover" && string(r.Params.Arguments) != `{"query":"filesystem"}` {
							t.Fatal("unexpected discovery query")
						}
						if r.Params.Name == "required_credentials" && string(r.Params.Arguments) != `{"onboarding_id":"synthetic-private-id"}` {
							t.Fatal("wrong own installation")
						}
						return &mcp.CallToolResult{StructuredContent: map[string]any{"candidates": []any{1, 2, 3}, "manageable_keys": []string{"fixture-z", "fixture-a"}, "services": []any{1, 2, 3}, "secret_values_included": false, "definition_id": "remote-e2e-fixture", "phase": "revoked", "onboarding_id": "synthetic-private-id", "credentials": []any{}, "workloads": []any{1, 2, 3}}}, nil
					})
				}
				handler := mcp.NewStreamableHTTPHandler(func(r *http.Request) *mcp.Server {
					if r.Header.Get("Authorization") != "Bearer synthetic-owner" {
						t.Fatal("wrong owner")
					}
					return server
				}, &mcp.StreamableHTTPOptions{Stateless: true, JSONResponse: true})
				hs := httptest.NewServer(handler)
				defer hs.Close()
				token := filepath.Join(root, "tokens.json")
				data, _ := json.Marshal(map[string]identity.Envelope{"synthetic-owner": {PrincipalID: "alice"}})
				if err := os.WriteFile(token, data, 0600); err != nil {
					t.Fatal(err)
				}
				client := &http.Client{Transport: transportFunc(func(r *http.Request) (*http.Response, error) {
					if r.URL.Host != "127.0.0.1:8081" {
						return http.DefaultTransport.RoundTrip(r)
					}
					var input struct {
						Text   string
						ChatID int64 `json:"chat_id"`
					}
					if err := json.NewDecoder(r.Body).Decode(&input); err != nil {
						t.Fatal(err)
					}
					if strings.Contains(input.Text, "fixture-a") {
						t.Fatal("oracle disclosed expected response")
					}
					marker := regexp.MustCompile(`E2E [a-f0-9]{16}`).FindString(input.Text)
					text := marker
					if strings.Contains(input.Text, "Use ") {
						if name == "settings_get" {
							text += ` ["fixture-a","fixture-z"]`
						} else if name == "status" {
							text += " revoked synthetic-private-id"
						} else if name == "required_credentials" {
							text += " 0"
						} else {
							text += " 3"
						}
						if lie {
							text = marker + " invented"
						}
					}
					id := "mock-" + strings.Repeat("a", 64)
					mapping := communication.JobMapping{JobID: id, PrincipalID: "alice", TaskID: "task", SessionID: "session", RunID: "run", Status: "completed"}
					receipt := map[string]any{"principal": "alice", "delivery": communication.Delivery{JobID: id, TaskID: "task", ChatID: input.ChatID, Channel: "mock", Text: text}}
					for file, value := range map[string]any{"mappings/" + id + ".json": mapping, "mock-sent/job-" + id + "-response.json": receipt} {
						data, _ := json.Marshal(value)
						if err := os.WriteFile(filepath.Join(root, file), data, 0600); err != nil {
							t.Fatal(err)
						}
					}
					return &http.Response{StatusCode: 200, Body: io.NopCloser(strings.NewReader(fmt.Sprintf(`{"job_id":%q,"task_id":"task"}`, id)))}, nil
				})}
				cfg := communication.Config{SpoolDir: root, ToolHubURL: hs.URL, ControlTokensFile: token, ControlAuth: "synthetic-admin"}
				status, reason := readAgentCase(context.Background(), client, cfg, communication.User{ID: "alice"}, "mock-server", name)
				want := "PASS"
				if lie {
					want = "FAIL"
				}
				if status != want {
					t.Fatal(status, reason)
				}
			})
		}
	}
}
