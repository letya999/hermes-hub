package e2e

import (
	"context"
	"encoding/json"
	"fmt"
	"github.com/letya999/hermes-hub/internal/communication"
	"github.com/letya999/hermes-hub/internal/identity"
	hubruntime "github.com/letya999/hermes-hub/internal/runtime"
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

func TestSettingsAgentVerifiesChangeAndRestoresOverride(t *testing.T) {
	for _, present := range []bool{false, true} {
		for _, outcome := range []string{"ok", "lie", "interrupted"} {
			lie := outcome == "lie"
			t.Run(fmt.Sprint(present, outcome), func(t *testing.T) {
				root := t.TempDir()
				for _, dir := range []string{"mappings", "mock-sent"} {
					if err := os.MkdirAll(filepath.Join(root, dir), 0700); err != nil {
						t.Fatal(err)
					}
				}
				overridden, current := present, present
				restores := 0
				server := mcp.NewServer(&mcp.Implementation{Name: "fixture", Version: "1"}, nil)
				for _, name := range []string{"settings_get", "settings_set"} {
					name := name
					mcp.AddTool(server, &mcp.Tool{Name: name}, func(ctx context.Context, r *mcp.CallToolRequest, args map[string]any) (*mcp.CallToolResult, any, error) {
						if args["key"] != "display.long_running_notifications" {
							t.Fatal("wrong setting", args)
						}
						if name == "settings_get" {
							return nil, map[string]any{"overridden": overridden, "override": current, "effective": map[string]any{"display.long_running_notifications": current}}, nil
						}
						if (present && args["value"] != "true") || (!present && args["value"] != "") {
							t.Fatal("oracle performed agent change", args)
						}
						restores++
						overridden, current = present, present
						return nil, map[string]any{"restart_scheduled": true}, nil
					})
				}
				hs := httptest.NewServer(mcp.NewStreamableHTTPHandler(func(r *http.Request) *mcp.Server {
					if r.Header.Get("Authorization") != "Bearer synthetic-owner" {
						t.Fatal("wrong owner")
					}
					return server
				}, &mcp.StreamableHTTPOptions{Stateless: true, JSONResponse: true}))
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
					marker := regexp.MustCompile(`E2E [a-f0-9]{16}`).FindString(input.Text)
					if strings.Contains(input.Text, "Use settings_set") && !lie {
						overridden, current = true, !present
					}
					if outcome == "interrupted" && strings.Contains(input.Text, "Use settings_set") {
						marker = "Operation interrupted"
					}
					id := "mock-" + strings.Repeat("a", 64)
					mapping := communication.JobMapping{JobID: id, PrincipalID: "alice", TaskID: "task", SessionID: "session", RunID: "run", Status: "completed"}
					receipt := map[string]any{"principal": "alice", "delivery": communication.Delivery{JobID: id, TaskID: "task", ChatID: input.ChatID, Channel: "mock", Text: marker}}
					for file, value := range map[string]any{"mappings/" + id + ".json": mapping, "mock-sent/job-" + id + "-response.json": receipt} {
						data, _ := json.Marshal(value)
						if err := os.WriteFile(filepath.Join(root, file), data, 0600); err != nil {
							t.Fatal(err)
						}
					}
					return &http.Response{StatusCode: 200, Body: io.NopCloser(strings.NewReader(fmt.Sprintf(`{"job_id":%q,"task_id":"task"}`, id)))}, nil
				})}
				cfg := communication.Config{SpoolDir: root, ToolHubURL: hs.URL, ControlTokensFile: token, ControlAuth: "synthetic-admin"}
				status, reason := settingsAgentCase(context.Background(), client, cfg, communication.User{ID: "alice"}, "mock-server")
				if outcome == "interrupted" {
					if status != "FAIL" || restores != 1 || overridden != present || current != present {
						t.Fatal(status, reason, restores)
					}
				} else if lie {
					if status != "FAIL" || restores != 0 {
						t.Fatal(status, reason, restores)
					}
				} else if status != "PASS" || restores != 1 || overridden != present || current != present {
					t.Fatal(status, reason, restores)
				}
			})
		}
	}
}

func TestSettingsRestartUsesCompletedOwnerAndFailsClosed(t *testing.T) {
	root := t.TempDir()
	if err := os.Mkdir(filepath.Join(root, "done"), 0700); err != nil {
		t.Fatal(err)
	}
	id := "mock-" + strings.Repeat("b", 64)
	job := communication.Job{ID: id, Envelope: identity.Envelope{PrincipalID: "alice"}}
	data, _ := json.Marshal(job)
	if err := os.WriteFile(filepath.Join(root, "done", id+".json"), data, 0600); err != nil {
		t.Fatal(err)
	}
	for _, status := range []int{200, 503} {
		calls := 0
		client := &http.Client{Transport: transportFunc(func(r *http.Request) (*http.Response, error) {
			calls++
			var got hubruntime.ExecuteRequest
			if json.NewDecoder(r.Body).Decode(&got) != nil || got.Envelope.PrincipalID != "alice" || got.JobID != id || r.Method != http.MethodPost || r.URL.Path != "/v1/restart" || r.Header.Get("Authorization") != "Bearer synthetic-runtime" {
				t.Fatal("wrong restart ownership or contract")
			}
			return &http.Response{StatusCode: status, Body: io.NopCloser(strings.NewReader(`{"applied":true}`))}, nil
		})}
		cfg := communication.Config{SpoolDir: root, RuntimeURL: "http://supervisor", RuntimeAuth: "synthetic-runtime"}
		if err := waitSettingsRestart(context.Background(), client, cfg, "alice", id); (err == nil) != (status == 200) {
			t.Fatal(status, err)
		}
		for _, input := range []struct{ owner, id string }{{"bob", id}, {"alice", "../escape"}, {"alice", "mock-" + strings.Repeat("c", 64)}} {
			if err := waitSettingsRestart(context.Background(), client, cfg, input.owner, input.id); err == nil {
				t.Fatal("invalid owner/job accepted")
			}
		}
		if calls != 1 {
			t.Fatal("invalid identity reached restart endpoint", calls)
		}
	}
}
