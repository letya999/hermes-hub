package e2e

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/letya999/hermes-hub/internal/communication"
)

func TestRoutineAgentRequiresIndependentStateAndCleansOnlyOwnFixture(t *testing.T) {
	for _, name := range []string{"routine_create", "routine_list", "routine_edit", "routine_snooze", "routine_pause", "routine_delete"} {
		for _, lie := range []bool{false, true} {
			t.Run(name+map[bool]string{false: "/correct", true: "/false-claim"}[lie], func(t *testing.T) {
				root := t.TempDir()
				for _, dir := range []string{"mappings", "mock-sent"} {
					if err := os.MkdirAll(filepath.Join(root, dir), 0700); err != nil {
						t.Fatal(err)
					}
				}
				cfg := communication.Config{SpoolDir: root, ControlAuth: "synthetic"}
				var fixture *communication.Schedule
				var fixtureID string
				client := &http.Client{Transport: transportFunc(func(r *http.Request) (*http.Response, error) {
					if r.Header.Get("X-Hub-Principal") != "alice" {
						t.Fatal("wrong owner")
					}
					body := any(map[string]string{})
					switch {
					case r.Method == http.MethodPost && r.URL.Path == "/v1/routines":
						var s communication.Schedule
						if err := json.NewDecoder(r.Body).Decode(&s); err != nil {
							t.Fatal(err)
						}
						if s.Expression != "once:2099-01-01T00:00:00Z" || s.JobKind != "reminder" {
							t.Fatal("unsafe fixture")
						}
						fixtureID, s.UserID, s.Enabled = s.ScheduleID, "alice", true
						fixture = &s
					case r.Method == http.MethodGet && r.URL.Path == "/v1/routines":
						body = []communication.Schedule{}
						if fixture != nil {
							body = []communication.Schedule{*fixture}
						}
					case r.Method == http.MethodDelete:
						if fixture == nil || r.URL.Path != "/v1/routines/"+fixtureID {
							t.Fatal("deleted another fixture")
						}
						fixture = nil
					case r.URL.Path == "/v1/mock/message":
						var input struct {
							ChatID int64  `json:"chat_id"`
							Text   string `json:"text"`
						}
						if err := json.NewDecoder(r.Body).Decode(&input); err != nil {
							t.Fatal(err)
						}
						marker := regexp.MustCompile(`E2E [a-f0-9]{16}`).FindString(input.Text)
						if name == "routine_create" {
							fixtureID = regexp.MustCompile(`e2e-routine-[a-f0-9]{16}`).FindString(input.Text)
							if !lie {
								fixture = &communication.Schedule{ScheduleID: fixtureID, UserID: "alice", JobKind: "reminder", Timezone: "UTC", Expression: "once:2099-01-01T00:00:00Z", Input: regexp.MustCompile(`E2E_PRIVATE_[a-f0-9]{16}`).FindString(input.Text), Enabled: true}
							}
						} else if name == "routine_list" {
							if strings.Contains(input.Text, fixture.Input) {
								t.Fatal("answer disclosed in prompt")
							}
							if lie {
								marker += " guessed"
							} else {
								marker += " " + fixture.Input
							}
						} else if !lie {
							switch name {
							case "routine_edit":
								fixture.Expression, fixture.Input, fixture.Revision = "once:2099-02-01T00:00:00Z", marker, 2
							case "routine_snooze":
								fixture.SnoozedUntil = time.Date(2099, 3, 1, 0, 0, 0, 0, time.UTC)
							case "routine_pause":
								fixture.Paused = true
							case "routine_delete":
								fixture = nil
							}
						}
						id := "mock-" + strings.Repeat("a", 64)
						m := communication.JobMapping{JobID: id, PrincipalID: "alice", TaskID: "task", SessionID: "session", RunID: "run", Status: "completed"}
						d := communication.Delivery{ID: "job-" + id + "-response", JobID: id, TaskID: "task", ChatID: input.ChatID, Channel: "mock", Text: marker}
						for path, value := range map[string]any{filepath.Join(root, "mappings", id+".json"): m, filepath.Join(root, "mock-sent", d.ID+".json"): map[string]any{"principal": "alice", "delivery": d}} {
							data, _ := json.Marshal(value)
							if err := os.WriteFile(path, data, 0600); err != nil {
								t.Fatal(err)
							}
						}
						body = map[string]string{"job_id": id, "task_id": "task"}
					default:
						t.Fatal("unexpected request", r.Method, r.URL.Path)
					}
					data, _ := json.Marshal(body)
					return &http.Response{StatusCode: 200, Body: io.NopCloser(strings.NewReader(string(data)))}, nil
				})}
				status, reason := routineAgentCase(context.Background(), client, cfg, communication.User{ID: "alice"}, "mock-server", name)
				want := "PASS"
				if lie {
					want = "FAIL"
				}
				if status != want {
					t.Fatal(status, reason)
				}
				if status == "PASS" && fixture != nil {
					t.Fatal("successful test left fixture")
				}
			})
		}
	}
}
