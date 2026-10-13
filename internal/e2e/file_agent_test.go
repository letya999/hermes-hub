package e2e

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"testing"

	"github.com/letya999/hermes-hub/internal/communication"
	"github.com/letya999/hermes-hub/internal/identity"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

func TestFileAgentRejectsFalseClaimsAndStaleWritesAndRemovesOwnFile(t *testing.T) {
	for _, name := range []string{"file_list", "file_read", "file_write", "file_search", "document_extract", "document_create", "document_edit", "document_convert", "artifact_remove", "code_exec"} {
		for _, lie := range []bool{false, true} {
			t.Run(fmt.Sprintf("%s/lie=%t", name, lie), func(t *testing.T) {
				root := t.TempDir()
				for _, d := range []string{"mappings", "mock-sent"} {
					if err := os.MkdirAll(filepath.Join(root, d), 0700); err != nil {
						t.Fatal(err)
					}
				}
				files := map[string]string{}
				var mu sync.Mutex
				server := mcp.NewServer(&mcp.Implementation{Name: "fixture", Version: "1"}, nil)
				for _, operation := range []string{"file_write", "file_read", "artifact_remove", "file_list", "file_search"} {
					server.AddTool(&mcp.Tool{Name: operation, InputSchema: map[string]any{"type": "object"}}, func(_ context.Context, r *mcp.CallToolRequest) (*mcp.CallToolResult, error) {
						var args struct{ Path, Text, Revision string }
						if err := json.Unmarshal(r.Params.Arguments, &args); err != nil {
							t.Fatal(err)
						}
						mu.Lock()
						defer mu.Unlock()
						if (operation == "file_list" && name != "artifact_remove") || operation == "file_search" {
							t.Fatal("oracle used the operation instead of the agent")
						}
						if !strings.HasPrefix(args.Path, "artifacts/documents/e2e-") {
							t.Fatal("fixture escaped its path")
						}
						text, exists := files[args.Path]
						if operation == "file_read" && !exists {
							return &mcp.CallToolResult{IsError: true, Content: []mcp.Content{&mcp.TextContent{Text: "not found"}}}, nil
						}
						if operation == "file_list" {
							items := []map[string]any{}
							for rel := range files {
								if path.Dir(rel) == args.Path {
									items = append(items, map[string]any{"name": path.Base(rel)})
								}
							}
							return &mcp.CallToolResult{StructuredContent: map[string]any{"items": items}}, nil
						}
						rev := fmt.Sprintf("sha256:%x", sha256.Sum256([]byte(text)))
						if operation == "file_write" && args.Revision != "" && (!exists || args.Revision != rev) {
							return &mcp.CallToolResult{IsError: true, Content: []mcp.Content{&mcp.TextContent{Text: "stale revision"}}}, nil
						}
						if operation == "file_write" {
							files[args.Path] = args.Text
							text = args.Text
							rev = fmt.Sprintf("sha256:%x", sha256.Sum256([]byte(text)))
						}
						if operation == "artifact_remove" {
							if !exists {
								t.Fatal("cleanup removed unknown file")
							}
							delete(files, args.Path)
						}
						return &mcp.CallToolResult{StructuredContent: map[string]any{"text": text, "revision": rev, "removed": operation == "artifact_remove"}}, nil
					})
				}
				handler := mcp.NewStreamableHTTPHandler(func(*http.Request) *mcp.Server { return server }, &mcp.StreamableHTTPOptions{Stateless: true, JSONResponse: true})
				httpServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					if r.Header.Get("Authorization") != "Bearer synthetic-owner" {
						t.Fatal("oracle not owner bound")
					}
					handler.ServeHTTP(w, r)
				}))
				defer httpServer.Close()
				tokens := filepath.Join(root, "tokens.json")
				data, _ := json.Marshal(map[string]identity.Envelope{"synthetic-owner": {PrincipalID: "alice"}})
				if err := os.WriteFile(tokens, data, 0600); err != nil {
					t.Fatal(err)
				}
				cfg := communication.Config{ToolHubURL: httpServer.URL, ControlTokensFile: tokens, SpoolDir: root, ControlAuth: "synthetic-admin"}
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
					marker := regexp.MustCompile(`E2E [a-f0-9]{16}`).FindString(input.Text)
					if strings.HasPrefix(input.Text, "Use ") {
						mu.Lock()
						var rel, text string
						for ownPath, ownText := range files {
							rel, text = ownPath, ownText
							break
						}
						switch name {
						case "code_exec":
							if strings.Contains(input.Text, text) {
								t.Fatal("shell input content disclosed")
							}
							if !lie {
								files[path.Dir(rel)+"/exports/result.txt"] = text + " 42"
							}
						case "file_list", "file_search":
							marker += " " + path.Base(rel)
						case "file_read", "document_extract":
							if strings.Contains(input.Text, text) {
								t.Fatal("read answer disclosed")
							}
							marker += " " + text
						case "file_write", "document_edit":
							if !lie {
								files[rel] = text + " BETA"
							}
						case "document_create", "document_convert":
							if !lie {
								ownName := regexp.MustCompile(`name (e2e-[a-f0-9]{16}-output)`).FindStringSubmatch(input.Text)[1]
								ext := ".txt"
								if name == "document_convert" {
									ext = ".md"
								}
								files["artifacts/documents/"+ownName+ext] = text
							}
						case "artifact_remove":
							if !lie {
								delete(files, rel)
							}
						}
						mu.Unlock()
						if lie && (name == "file_read" || name == "file_search" || name == "file_list" || name == "document_extract") {
							marker += " false-claim"
						}
					}
					id := "mock-" + strings.Repeat("a", 64)
					m := communication.JobMapping{JobID: id, PrincipalID: "alice", TaskID: "task", SessionID: "session", RunID: "run", Status: "completed"}
					d := communication.Delivery{ID: "job-" + id + "-response", JobID: id, TaskID: "task", ChatID: input.ChatID, Channel: "mock", Text: marker}
					for file, value := range map[string]any{filepath.Join(root, "mappings", id+".json"): m, filepath.Join(root, "mock-sent", d.ID+".json"): map[string]any{"principal": "alice", "delivery": d}} {
						data, _ := json.Marshal(value)
						if err := os.WriteFile(file, data, 0600); err != nil {
							t.Fatal(err)
						}
					}
					return &http.Response{StatusCode: 200, Body: io.NopCloser(strings.NewReader(fmt.Sprintf(`{"job_id":%q,"task_id":"task"}`, id)))}, nil
				})}
				status, reason := fileAgentCase(context.Background(), client, cfg, communication.User{ID: "alice"}, "mock-server", name)
				want := "PASS"
				if lie {
					want = "FAIL"
				}
				if status != want {
					t.Fatal(status, reason)
				}
				mu.Lock()
				defer mu.Unlock()
				if status == "PASS" && len(files) != 0 {
					t.Fatal("successful test left its file")
				}
			})
		}
	}
}
