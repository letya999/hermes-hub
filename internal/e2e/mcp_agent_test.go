package e2e

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"github.com/letya999/hermes-hub/internal/toolhub/api"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"testing"

	"github.com/letya999/hermes-hub/internal/communication"
	"github.com/letya999/hermes-hub/internal/identity"
	"github.com/modelcontextprotocol/go-sdk/jsonrpc"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

func TestRevocationDoesNotConfuseTransportFailureWithDenial(t *testing.T) {
	if !revocationDenied(nil, &jsonrpc.Error{Code: jsonrpc.CodeInvalidParams, Message: `unknown tool "fixture"`}, "fixture") {
		t.Fatal("removed tool denial rejected")
	}
	for _, err := range []error{errors.New("connection refused"), &jsonrpc.Error{Code: jsonrpc.CodeInvalidParams, Message: "invalid arguments"}, &jsonrpc.Error{Code: jsonrpc.CodeInternalError, Message: `unknown tool "fixture"`}} {
		if revocationDenied(nil, err, "fixture") {
			t.Fatal("unrelated failure counted as revocation")
		}
	}
	if revocationDenied(&mcp.CallToolResult{}, nil, "fixture") || revocationDenied(nil, nil, "fixture") {
		t.Fatal("successful or absent result counted as denial")
	}
}

func TestMCPAgentRequiresIndependentInstallationAndRevocation(t *testing.T) {
	for _, cli := range []bool{false, true} {
		for _, fault := range []string{"", "installation", "invocation", "disable"} {
			t.Run(fmt.Sprint(cli, fault), func(t *testing.T) {
				root := t.TempDir()
				for _, dir := range []string{"mappings", "mock-sent"} {
					if err := os.MkdirAll(filepath.Join(root, dir), 0700); err != nil {
						t.Fatal(err)
					}
				}
				var mu sync.Mutex
				name := ""
				revoked := false
				phase := api.PhaseEnabled
				calls := 0
				server := mcp.NewServer(&mcp.Implementation{Name: "fixture", Version: "1"}, nil)
				for _, operation := range []string{"prepare_source", "status", "confirm", "enable", "disable", "revoke", "remove"} {
					server.AddTool(&mcp.Tool{Name: operation, InputSchema: map[string]any{"type": "object"}}, func(_ context.Context, r *mcp.CallToolRequest) (*mcp.CallToolResult, error) {
						mu.Lock()
						defer mu.Unlock()
						if operation == "prepare_source" || operation == "confirm" || operation == "enable" || operation == "disable" || operation == "remove" {
							t.Fatal("oracle manufactured agent installation")
						}
						if operation == "revoke" {
							var args map[string]any
							if err := json.Unmarshal(r.Params.Arguments, &args); err != nil {
								t.Fatal(err)
							}
							if args["onboarding_id"] != "own-onboarding" || name == "" {
								t.Fatal("foreign installation cleanup")
							}
							revoked = true
						}
						return &mcp.CallToolResult{StructuredContent: map[string]any{"definition_id": name, "onboarding_id": "own-onboarding", "phase": phase, "repository": "https://mcp.deepwiki.com/mcp"}}, nil
					})
				}
				handler := mcp.NewStreamableHTTPHandler(func(*http.Request) *mcp.Server { return server }, &mcp.StreamableHTTPOptions{Stateless: true, JSONResponse: true})
				hs := httptest.NewServer(handler)
				defer hs.Close()
				tokens := filepath.Join(root, "tokens.json")
				data, _ := json.Marshal(map[string]identity.Envelope{"synthetic-owner": {PrincipalID: "alice"}})
				if err := os.WriteFile(tokens, data, 0600); err != nil {
					t.Fatal(err)
				}
				cfg := communication.Config{SpoolDir: root, ToolHubURL: hs.URL, ControlTokensFile: tokens, ControlAuth: "synthetic-admin"}
				client := &http.Client{Transport: transportFunc(func(r *http.Request) (*http.Response, error) {
					if r.URL.Host != "127.0.0.1:8081" {
						if r.Header.Get("Authorization") != "Bearer synthetic-owner" {
							t.Fatal("foreign token")
						}
						return http.DefaultTransport.RoundTrip(r)
					}
					var input struct {
						Text   string
						ChatID int64 `json:"chat_id"`
					}
					if err := json.NewDecoder(r.Body).Decode(&input); err != nil {
						t.Fatal(err)
					}
					if fault != "installation" && strings.Contains(input.Text, "prepare_source") {
						mu.Lock()
						name = regexp.MustCompile(`(?:remote-e2e-dw-|cli-e2e-)[a-f0-9]{16}`).FindString(input.Text)
						tool := "read_wiki_structure"
						if cli {
							tool = "version"
							if !strings.Contains(input.Text, "ruff==0.11.13") || !strings.Contains(input.Text, `"workspace_scope":"none"`) {
								t.Fatal("unsafe CLI fixture")
							}
						}
						projected := api.ProjectedToolName(name, "1.0.0", tool)
						mu.Unlock()
						server.AddTool(&mcp.Tool{Name: projected, InputSchema: map[string]any{"type": "object"}}, func(_ context.Context, r *mcp.CallToolRequest) (*mcp.CallToolResult, error) {
							mu.Lock()
							defer mu.Unlock()
							calls++
							if !cli && !strings.Contains(string(r.Params.Arguments), "golang/go") {
								t.Fatal("private data sent")
							}
							text := "Available pages for golang/go:\n\n- 1 Overview\n  - 1.1 Subpage\n- 2 Architecture"
							if cli {
								text = "ruff 0.11.13"
							}
							return &mcp.CallToolResult{IsError: revoked || phase != api.PhaseEnabled, Content: []mcp.Content{&mcp.TextContent{Text: text}}}, nil
						})
					}
					if strings.HasPrefix(input.Text, "Call ToolHub ") {
						if !strings.Contains(input.Text, "onboarding_id own-onboarding") {
							t.Fatal("foreign lifecycle target")
						}
						mu.Lock()
						for op, value := range map[string]string{"disable": api.PhaseDisabled, "enable": api.PhaseEnabled, "revoke": api.PhaseRevoked, "remove": api.PhaseRemoved} {
							if strings.HasPrefix(input.Text, "Call ToolHub "+op+" ") && !(fault == "disable" && op == "disable") {
								phase = value
							}
						}
						mu.Unlock()
					}
					id := "mock-" + strings.Repeat("a", 64)
					marker := regexp.MustCompile(`E2E [a-f0-9]{16}`).FindString(input.Text)
					if strings.Contains(input.Text, "Use the installed tool") {
						if !strings.Contains(input.Text, api.ProjectedToolName(name, "1.0.0", "read_wiki_structure")) && !strings.Contains(input.Text, api.ProjectedToolName(name, "1.0.0", "version")) {
							t.Fatal("invocation prompt lost exact installation")
						}
						marker += " Overview"
						if cli {
							marker = strings.TrimSuffix(marker, " Overview") + " ruff 0.11.13"
						}
						if fault == "invocation" {
							marker = "incorrect result"
						}
					}
					m := communication.JobMapping{JobID: id, PrincipalID: "alice", TaskID: "task", SessionID: "session", RunID: "run", Status: "completed"}
					d := communication.Delivery{JobID: id, TaskID: "task", ChatID: input.ChatID, Channel: "mock", Text: marker}
					for file, value := range map[string]any{"mappings/" + id + ".json": m, "mock-sent/job-" + id + "-response.json": map[string]any{"principal": "alice", "delivery": d}} {
						data, _ := json.Marshal(value)
						if err := os.WriteFile(filepath.Join(root, file), data, 0600); err != nil {
							t.Fatal(err)
						}
					}
					return &http.Response{StatusCode: 200, Body: io.NopCloser(strings.NewReader(fmt.Sprintf(`{"job_id":%q,"task_id":"task"}`, id)))}, nil
				})}
				status, reason := installationAgentCase(context.Background(), client, cfg, communication.User{ID: "alice"}, "mock-server", cli)
				want := "PASS"
				if fault != "" {
					want = "FAIL"
				}
				if status != want {
					t.Fatal(status, reason)
				}
				mu.Lock()
				defer mu.Unlock()
				if fault == "" && (phase != api.PhaseRemoved || calls != 5) {
					t.Fatal("revocation not exercised")
				}
			})
		}
	}
}

func TestInstallationAnswerRejectsUnverifiableResults(t *testing.T) {
	for _, output := range []string{"", "success", `[]`, `[""]`, `{"titles":["Overview"]}`} {
		result := &mcp.CallToolResult{Content: []mcp.Content{&mcp.TextContent{Text: output}}}
		if _, err := installationAnswer(result, false); err == nil {
			t.Fatal("unverifiable public result accepted", output)
		}
		if _, err := installationAnswer(result, true); err == nil {
			t.Fatal("wrong CLI version accepted", output)
		}
	}
}
