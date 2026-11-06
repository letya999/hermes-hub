package communication

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/letya999/hermes-hub/internal/channel"
	"github.com/letya999/hermes-hub/internal/commodel"
	"github.com/letya999/hermes-hub/internal/identity"
	"github.com/letya999/hermes-hub/internal/locale"
	"github.com/letya999/hermes-hub/internal/routines"
	"github.com/letya999/hermes-hub/internal/toolhub/api"
	"github.com/letya999/hermes-hub/internal/toolhub/automation"
)

type allowAutomationRights struct{}

func (allowAutomationRights) Decide(identity.Envelope, string, string, string, time.Time) (api.RightDecision, error) {
	return api.RightDecision{Allow: true}, nil
}

func TestAssignmentGatewayWritesOnlyToolHubOwner(t *testing.T) {
	cfg := testConfig(t)
	cfg.ControlAuth, cfg.AutomationAuth = "control-token", "control-token"
	user := cfg.Users[0]
	host, err := automation.NewHost(automation.Config{
		Root: filepath.Join(t.TempDir(), "automation"), Environment: "dev", Store: allowAutomationRights{},
		AuthForPrincipal: func(string) (identity.Envelope, error) { return user.Envelope(11), nil },
		Channels:         map[string]routines.OccurrenceChannel{"telegram_bot": channel.NewTelegram(channel.TelegramDeps{})},
		Supervisor:       &automation.HTTPSupervisorClient{URL: "http://127.0.0.1", Token: "control"},
		Communication:    &automation.HTTPCommunicationClient{URL: "http://127.0.0.1", Token: "control"},
		AuthorizeChannel: func(context.Context, string, commodel.Job) error { return nil },
		LangOf:           func(string) locale.Lang { return locale.LangRU },
	})
	if err != nil {
		t.Fatal(err)
	}
	server := httptest.NewServer(host.ControlHandler("control-token"))
	defer server.Close()
	cfg.ToolHubURL = server.URL
	g, err := New(cfg)
	if err != nil {
		t.Fatal(err)
	}
	w := destinationRequest(g, http.MethodPost, "/v1/assignments", `{"title":"check","intent":"watch this"}`)
	var created commodel.Assignment
	if w.Code != http.StatusOK || json.Unmarshal(w.Body.Bytes(), &created) != nil || created.AssignmentID == "" {
		t.Fatalf("gateway create: %d %s", w.Code, w.Body.String())
	}
	if _, err := host.LoadAssignment(context.Background(), user.Envelope(11), created.AssignmentID); err != nil {
		t.Fatalf("ToolHub owner missing assignment: %v", err)
	}
	if entries, _ := os.ReadDir(filepath.Join(cfg.SpoolDir, "assignments")); len(entries) != 0 {
		t.Fatalf("gateway spool retained an assignment writer: %d records", len(entries))
	}
	w = destinationRequest(g, http.MethodGet, "/v1/assignments", "")
	var listed []commodel.Assignment
	if w.Code != http.StatusOK || json.Unmarshal(w.Body.Bytes(), &listed) != nil || len(listed) != 1 || listed[0].AssignmentID != created.AssignmentID {
		t.Fatalf("gateway list: %d %s", w.Code, w.Body.String())
	}
}

func TestRoutineGatewayWritesOnlyToolHubOwner(t *testing.T) {
	cfg := testConfig(t)
	cfg.ControlAuth, cfg.AutomationAuth = "control-token", "control-token"
	user := cfg.Users[0]
	clock := time.Now().UTC()
	host, err := automation.NewHost(automation.Config{
		Root: filepath.Join(t.TempDir(), "automation"), Environment: "dev", Store: allowAutomationRights{},
		Now:              func() time.Time { return clock },
		AuthForPrincipal: func(string) (identity.Envelope, error) { return user.Envelope(11), nil },
		Channels:         map[string]routines.OccurrenceChannel{"telegram_bot": channel.NewTelegram(channel.TelegramDeps{})},
		Supervisor:       &automation.HTTPSupervisorClient{URL: "http://127.0.0.1", Token: "control"},
		Communication:    &automation.HTTPCommunicationClient{URL: "http://127.0.0.1", Token: "control"},
		AuthorizeChannel: func(context.Context, string, commodel.Job) error { return nil },
		LangOf:           func(string) locale.Lang { return locale.LangRU },
		ToolCall: func(context.Context, string, *commodel.WatchProbe) ([]byte, error) {
			return []byte(`{"event":true,"id":"m1","text":"hello","chat_id":7}`), nil
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	server := httptest.NewServer(host.ControlHandler("control-token"))
	defer server.Close()
	cfg.ToolHubURL = server.URL
	g, err := New(cfg)
	if err != nil {
		t.Fatal(err)
	}
	w := destinationRequest(g, http.MethodPost, "/v1/routines", `{"schedule_id":"rt-bridge","chat_id":11,"timezone":"UTC","expression":"once:2099-01-01T10:00:00Z","job_kind":"reminder","input":"fixture"}`)
	var created commodel.Schedule
	if w.Code != http.StatusOK || json.Unmarshal(w.Body.Bytes(), &created) != nil || created.ScheduleID != "rt-bridge" {
		t.Fatalf("gateway create: %d %s", w.Code, w.Body.String())
	}
	if entries, _ := os.ReadDir(filepath.Join(cfg.SpoolDir, "schedules")); len(entries) != 0 {
		t.Fatalf("gateway spool retained a schedule writer: %d records", len(entries))
	}
	w = destinationRequest(g, http.MethodGet, "/v1/routines", "")
	var listed []commodel.Schedule
	if w.Code != http.StatusOK || json.Unmarshal(w.Body.Bytes(), &listed) != nil || len(listed) != 1 || listed[0].ScheduleID != created.ScheduleID {
		t.Fatalf("gateway list: %d %s", w.Code, w.Body.String())
	}
	w = destinationRequest(g, http.MethodGet, "/v1/routines/rt-bridge", "")
	var card RoutineCard
	if w.Code != http.StatusOK || json.Unmarshal(w.Body.Bytes(), &card) != nil || card.Schedule.ScheduleID != created.ScheduleID {
		t.Fatalf("gateway card: %d %s", w.Code, w.Body.String())
	}
	w = destinationRequest(g, http.MethodGet, "/v1/routines/rt-bridge/preview?limit=1", "")
	var preview RoutinePreview
	if w.Code != http.StatusOK || json.Unmarshal(w.Body.Bytes(), &preview) != nil || preview.State != "active" || len(preview.Times) != 1 {
		t.Fatalf("gateway preview: %d %s", w.Code, w.Body.String())
	}
	clock = created.NextDue.Add(time.Second)
	if err := host.Tick(context.Background(), clock); err != nil {
		t.Fatal(err)
	}
	w = destinationRequest(g, http.MethodGet, "/v1/routines/rt-bridge/history", "")
	var history RoutineHistoryPage
	if w.Code != http.StatusOK || json.Unmarshal(w.Body.Bytes(), &history) != nil || len(history.Items) != 1 || history.Items[0].ID == "" {
		t.Fatalf("gateway history: %d %s", w.Code, w.Body.String())
	}
	w = destinationRequest(g, http.MethodGet, "/v1/routines/rt-bridge", "")
	if w.Code != http.StatusOK || json.Unmarshal(w.Body.Bytes(), &card) != nil || card.Schedule.LastOccurrenceState == "" || card.Schedule.LastDeliveryState != history.Items[0].DeliveryState {
		t.Fatalf("gateway reminder card lost outcome: %d %s", w.Code, w.Body.String())
	}
	w = destinationRequest(g, http.MethodPost, "/v1/routines/rt-bridge", `{"input":"changed","expected_revision":1}`)
	var edited commodel.Schedule
	if w.Code != http.StatusOK || json.Unmarshal(w.Body.Bytes(), &edited) != nil || edited.Input != "changed" || edited.Revision <= created.Revision {
		t.Fatalf("gateway edit: %d %s", w.Code, w.Body.String())
	}
	w = destinationRequest(g, http.MethodDelete, "/v1/routines/rt-bridge", `{"expected_revision":2}`)
	if w.Code != http.StatusOK {
		t.Fatalf("gateway delete: %d %s", w.Code, w.Body.String())
	}
	w = destinationRequest(g, http.MethodGet, "/v1/routines/rt-bridge", "")
	if w.Code != http.StatusOK || json.Unmarshal(w.Body.Bytes(), &card) != nil || card.State != "deleted" {
		t.Fatalf("retired card: %d %s", w.Code, w.Body.String())
	}
	w = destinationRequest(g, http.MethodGet, "/v1/routines/rt-bridge/preview?limit=1", "")
	if w.Code != http.StatusOK || json.Unmarshal(w.Body.Bytes(), &preview) != nil || preview.State != "deleted" || len(preview.Times) != 0 {
		t.Fatalf("retired preview: %d %s", w.Code, w.Body.String())
	}
	w = destinationRequest(g, http.MethodPost, "/v1/routines", `{"schedule_id":"watch-bridge","chat_id":11,"timezone":"UTC","expression":"0 10 * * *","job_kind":"event","delivery":"stream","sink":"agent","input":"watch","probe":{"kind":"mcp_tool","tool":"events.wait"}}`)
	if w.Code != http.StatusOK {
		t.Fatalf("watch create: %d %s", w.Code, w.Body.String())
	}
	w = destinationRequest(g, http.MethodPost, "/v1/routines/watch-bridge/sample", `{}`)
	var sampled struct {
		ScheduleID string `json:"schedule_id"`
		Sources    []struct {
			Events []struct {
				Raw string `json:"raw"`
			} `json:"events"`
		} `json:"sources"`
	}
	if w.Code != http.StatusOK || json.Unmarshal(w.Body.Bytes(), &sampled) != nil || sampled.ScheduleID != "watch-bridge" || len(sampled.Sources) != 1 || len(sampled.Sources[0].Events) != 1 {
		t.Fatalf("watch sample bridge: %d %s", w.Code, w.Body.String())
	}
	if entries, _ := os.ReadDir(filepath.Join(cfg.SpoolDir, "schedules")); len(entries) != 0 {
		t.Fatalf("sample used old automation state: %d records", len(entries))
	}
}

func TestGatewayRefusesLegacyAutomationBeforeSpoolRecovery(t *testing.T) {
	cfg := testConfig(t)
	path := filepath.Join(cfg.SpoolDir, "schedules", "old.json")
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	body := []byte(`{"schedule_id":"old","input":"keep"}`)
	if err := os.WriteFile(path, body, 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := New(cfg); err == nil {
		t.Fatal("gateway accepted a populated old automation layout")
	}
	got, err := os.ReadFile(path)
	if err != nil || string(got) != string(body) {
		t.Fatalf("legacy record changed on refusal: %v", err)
	}
}
