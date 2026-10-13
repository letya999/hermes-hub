package e2e

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/letya999/hermes-hub/internal/communication"
)

type transportFunc func(*http.Request) (*http.Response, error)

func (f transportFunc) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

func TestServerReminderChecksReceiptBindingAndCleanup(t *testing.T) {
	for _, mode := range []string{"telegram-server", "mock-server"} {
		for _, fault := range []string{"", "wrong-chat", "schedule-cleanup", "task-cleanup", "source-cleanup"} {
			for _, name := range []string{"reminder", "text", "voice-out", "script", "watch", "check", "digest"} {
				if (fault == "task-cleanup" && mode == "mock-server") || (fault == "source-cleanup" && name != "digest") {
					continue
				}
				config := communication.Config{SpoolDir: t.TempDir(), ControlAuth: "synthetic"}
				user := communication.User{ID: "alice", Enabled: true, TelegramIDs: []int64{11}}
				_, err := communication.NewSpool(config.SpoolDir)
				if err != nil {
					t.Fatal(err)
				}
				if err := os.MkdirAll(filepath.Join(config.SpoolDir, "mock-sent"), 0700); err != nil {
					t.Fatal(err)
				}
				deleted := false
				client := &http.Client{Transport: transportFunc(func(r *http.Request) (*http.Response, error) {
					if r.Header.Get("Authorization") != "Bearer synthetic" || r.Header.Get("X-Hub-Principal") != "alice" {
						t.Fatal("missing authenticated owner")
					}
					body := "{}"
					if r.Method == http.MethodPost && r.URL.Path == "/v1/mock/message" {
						var input struct {
							Text   string
							ChatID int64 `json:"chat_id"`
						}
						if err := json.NewDecoder(r.Body).Decode(&input); err != nil {
							t.Fatal(err)
						}
						id := "mock-" + strings.Repeat("a", 64)
						m := communication.JobMapping{JobID: id, PrincipalID: "alice", TaskID: "mock-task", SessionID: "session", RunID: "run", Status: "completed"}
						d := communication.Delivery{JobID: id, TaskID: "mock-task", ChatID: input.ChatID, Channel: "mock", Text: regexp.MustCompile(`E2E [a-f0-9]{16}`).FindString(input.Text)}
						for file, value := range map[string]any{"mappings/" + id + ".json": m, "mock-sent/job-" + id + "-response.json": map[string]any{"principal": "alice", "delivery": d}} {
							data, _ := json.Marshal(value)
							if err := os.WriteFile(filepath.Join(config.SpoolDir, file), data, 0600); err != nil {
								t.Fatal(err)
							}
						}
						body = fmt.Sprintf(`{"job_id":%q,"task_id":"mock-task"}`, id)
					} else if r.Method == http.MethodPost && r.URL.Path == "/v1/tasks" {
						body = `{ "task_id": "test-task" }`
					} else if r.Method == http.MethodPost {
						var s communication.Schedule
						if err := json.NewDecoder(r.Body).Decode(&s); err != nil {
							t.Fatal(err)
						}
						if s.TaskID == "" || !s.Enabled {
							t.Fatal("unisolated draft", s)
						}
						s.Revision = 1
						id := communication.OccurrenceID(communication.RoutineOccurrence{ScheduleID: s.ScheduleID, Revision: 1, DueAt: s.NextDue})
						marker := regexp.MustCompile(`E2E [a-f0-9]{16}`).FindString(s.Input)
						if s.JobKind == "watcher" || s.JobKind == "check" {
							if s.Probe == nil || !strings.HasPrefix(s.Probe.URL, "https://httpbin.org/uuid?e2e=") || s.Probe.Regex != `"uuid"\s*:\s*"([a-f0-9-]{36})"` || s.InitialProbeValue != nil {
								t.Fatal("wrong public deterministic fixture")
							}
							state := communication.WatchState{ScheduleID: s.ScheduleID, Value: "22222222-2222-4222-8222-222222222222", Generation: 1, LastSuccess: time.Now()}
							b, _ := json.Marshal(state)
							if err := os.WriteFile(filepath.Join(config.SpoolDir, "watchers", s.ScheduleID+".json"), b, 0600); err != nil {
								t.Fatal(err)
							}
							if s.NotifyMode == "digest" {
								b, _ := json.Marshal(s)
								return &http.Response{StatusCode: 200, Body: io.NopCloser(strings.NewReader(string(b)))}, nil
							}
						}
						d := communication.Delivery{ID: "reminder-" + id, OccurrenceID: id, ChatID: s.ChatID, TaskID: s.TaskID, Channel: s.Channel, Text: marker}
						if s.JobKind == "script" {
							d.ID = "script-" + id + "-0"
						}
						if s.JobKind == "watcher" {
							id = communication.OccurrenceID(communication.RoutineOccurrence{ScheduleID: s.ScheduleID, Revision: s.Revision, DueAt: s.NextDue.Add(time.Minute)})
							d.OccurrenceID = id
							d.ID, d.Text = "watch-"+id, marker+" now: 22222222-2222-4222-8222-222222222222"
						}
						if s.JobKind == "check" {
							d.ID = "check-" + id
							if s.Expect != "changed" {
								t.Fatal("wrong expected check")
							}
						}
						if s.JobKind == "digest" {
							if len(s.SourceIDs) != 1 || s.SourceIDs[0] != s.ScheduleID+"-source" || s.Probe != nil {
								t.Fatal("digest escaped its own source")
							}
							d.ID, d.Text = "digest-"+id, marker+" 22222222-2222-4222-8222-222222222222"
							b, _ := json.Marshal(communication.DigestState{ScheduleID: s.ScheduleID, Generations: map[string]uint64{s.SourceIDs[0]: 1}, LastDigestAt: time.Now()})
							if err := os.WriteFile(filepath.Join(config.SpoolDir, "digests", s.ScheduleID+".json"), b, 0600); err != nil {
								t.Fatal(err)
							}
						}
						if s.JobKind == "agent" {
							d.ID = "job-" + id + "-response"
							m := communication.JobMapping{JobID: id, PrincipalID: "alice", TaskID: s.TaskID, SessionID: "session", RunID: "run", Status: "completed"}
							b, _ := json.Marshal(m)
							if err := os.WriteFile(filepath.Join(config.SpoolDir, "mappings", id+".json"), b, 0600); err != nil {
								t.Fatal(err)
							}
						}
						if name == "voice-out" {
							d.Voice = marker
							d.VoiceSent = true
						}
						if fault == "wrong-chat" {
							d.ChatID = 22
						}
						data, _ := json.Marshal(d)
						file := filepath.Join(config.SpoolDir, "outbox", "done", d.ID+".json")
						if mode == "mock-server" {
							data, _ = json.Marshal(map[string]any{"principal": "alice", "delivery": d})
							file = filepath.Join(config.SpoolDir, "mock-sent", d.ID+".json")
						}
						if err := os.WriteFile(file, data, 0600); err != nil {
							t.Fatal(err)
						}
						data, _ = json.Marshal(s)
						body = string(data)
					} else if r.Method == http.MethodDelete {
						deleted = true
						if (fault == "schedule-cleanup" && strings.HasPrefix(r.URL.Path, "/v1/routines/")) || (fault == "source-cleanup" && strings.HasSuffix(r.URL.Path, "-source")) || (fault == "task-cleanup" && strings.HasPrefix(r.URL.Path, "/v1/tasks/")) {
							return &http.Response{StatusCode: 503, Body: io.NopCloser(strings.NewReader("private failure details"))}, nil
						}
					} else {
						t.Fatal("unexpected method")
					}
					return &http.Response{StatusCode: 200, Body: io.NopCloser(strings.NewReader(body))}, nil
				})}
				status, reason := serverPromptModeJobCase(context.Background(), client, config, user, name, "", "", mode, nil)
				if name == "reminder" && status == "PASS" && strings.Contains(reason, "provider") {
					t.Fatal("deterministic reminder falsely claims provider execution")
				}
				want := "PASS"
				if fault != "" {
					want = "FAIL"
				}
				if status != want || !deleted {
					t.Fatalf("got %s cleanup=%t want %s", status, deleted, want)
				}
				if strings.HasSuffix(fault, "cleanup") && !strings.Contains(reason, "cleanup failed") {
					t.Fatal("cleanup failure was hidden", fault, reason)
				}
			}
		}
	}
}

func TestAcceptanceWatchRejectsFailedOrForeignState(t *testing.T) {
	root := t.TempDir()
	if err := os.Mkdir(filepath.Join(root, "watchers"), 0700); err != nil {
		t.Fatal(err)
	}
	valid := communication.WatchState{ScheduleID: "own", Value: "22222222-2222-4222-8222-222222222222", Generation: 1, LastSuccess: time.Now()}
	for _, failure := range []string{"foreign", "invalid-value", "wrong-generation", "probe-error", "no-success", "baseline-only"} {
		state := valid
		switch failure {
		case "foreign":
			state.ScheduleID = "other"
		case "invalid-value":
			state.Value = "invented"
		case "wrong-generation":
			state.Generation = 2
		case "probe-error":
			state.LastError = "failed"
		case "no-success":
			state.LastSuccess = time.Time{}
		case "baseline-only":
			state.Generation = 0
		}
		data, _ := json.Marshal(state)
		if err := os.WriteFile(filepath.Join(root, "watchers", "own.json"), data, 0600); err != nil {
			t.Fatal(err)
		}
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Millisecond)
		_, err := waitAcceptanceWatch(ctx, root, "own", true)
		cancel()
		if err == nil {
			t.Fatal("invalid probe accepted", failure)
		}
	}
}

func TestRoutineTransportRejectsFailuresWithoutLeakingResponse(t *testing.T) {
	for _, code := range []int{401, 403, 500} {
		client := &http.Client{Transport: transportFunc(func(*http.Request) (*http.Response, error) {
			return &http.Response{StatusCode: code, Body: io.NopCloser(strings.NewReader("private response"))}, nil
		})}
		err := routineRequest(context.Background(), client, communication.Config{}, "alice", "POST", "", nil, nil)
		if err == nil || strings.Contains(err.Error(), "private") || !strings.Contains(err.Error(), fmt.Sprint(code)) {
			t.Fatal("unsafe failure", err)
		}
	}
}

func TestServerRequiresExplicitPrincipalBeforeAnySend(t *testing.T) {
	root := t.TempDir()
	state, workspace := filepath.Join(root, "state"), filepath.Join(root, "workspace")
	if err := os.MkdirAll(state, 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(workspace, 0700); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(root, "communication.yaml")
	content := "organization_id: personal\nusers:\n  - id: alice\n    enabled: true\n    telegram_ids: [11]\n    state_dir: " + state + "\n    workspace_dir: " + workspace + "\nspool_dir: " + filepath.Join(root, "spool") + "\n"
	if err := os.WriteFile(path, []byte(content), 0600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("HUB_COMMUNICATION_CONFIG", path)
	t.Setenv("HUB_COMMUNICATION_AUTH", "synthetic")
	for _, principal := range []string{"", "bob"} {
		if _, err := Server(context.Background(), nil, principal, io.Discard); err == nil {
			t.Fatal("implicit/foreign principal accepted")
		}
	}
	report, err := Server(context.Background(), []Case{{ID: "E2E-001", Surface: "hub", Name: "file_read"}}, "alice", io.Discard)
	if err != nil || len(report.Results) != 1 || report.Results[0].Status != "BLOCKED" {
		t.Fatal("unsupported live oracle passed", report, err)
	}
}

func TestServerStopsAfterFailureBeforeAnotherAdmission(t *testing.T) {
	root := t.TempDir()
	state := filepath.Join(root, "state")
	workspace := filepath.Join(root, "workspace")
	for _, dir := range []string{state, workspace} {
		if err := os.MkdirAll(dir, 0700); err != nil {
			t.Fatal(err)
		}
	}
	path := filepath.Join(root, "communication.yaml")
	content := "organization_id: personal\nusers:\n  - id: alice\n    enabled: true\n    telegram_ids: [11]\n    state_dir: " + state + "\n    workspace_dir: " + workspace + "\nspool_dir: " + filepath.Join(root, "spool") + "\n"
	if err := os.WriteFile(path, []byte(content), 0600); err != nil {
		t.Fatal(err)
	}
	api := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { http.Error(w, "fixture unavailable", 500) }))
	defer api.Close()
	t.Setenv("HUB_COMMUNICATION_CONFIG", path)
	t.Setenv("HUB_COMMUNICATION_AUTH", "synthetic")
	t.Setenv("HUB_TTS_URL", api.URL)
	t.Setenv("HUB_STT_URL", api.URL)
	report, err := Server(context.Background(), []Case{{ID: "E2E-065", Surface: "channel", Name: "voice-in"}, {ID: "E2E-064", Surface: "channel", Name: "text"}}, "alice", io.Discard)
	if err != nil || len(report.Results) != 2 || report.Results[0].Status != "FAIL" || report.Results[1].Status != "BLOCKED" || !strings.Contains(report.Results[1].Reason, "no next job submitted") {
		t.Fatal(report, err)
	}
}
