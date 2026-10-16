package communication

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// A prepare started from a group forum topic must report back to that topic.
// The callback carries only the principal, so the gateway correlates the
// onboarding with the still-active message job that initiated it; without the
// correlation every notice and the continuation leak into the owner's DM.
func TestPrepareOutcomeRoutesToOriginatingTopic(t *testing.T) {
	cfg := testConfig(t)
	cfg.ControlAuth = "control-token"
	gateway, err := New(cfg)
	if err != nil {
		t.Fatal(err)
	}
	user := cfg.Users[0]
	task, _, err := gateway.spool.CreateTask(user, -100, 66, "germi", false)
	if err != nil {
		t.Fatal(err)
	}
	envelope := user.envelope(11)
	envelope.ConversationID = task.ConversationID
	envelope.DeliveryTargetID = "telegram--100"
	job := Job{Envelope: envelope, ID: "telegram-group-1", OrganizationID: "personal", UserID: user.ID, ActorID: user.ID, ScopeID: "user:" + user.ID, Channel: "telegram_bot", Trigger: "message", IdempotencyKey: "telegram:g1", ChatID: -100, TaskID: task.TaskID, TopicID: 66, Text: "install notion mcp", CreatedAt: time.Now().UTC()}
	if _, err := gateway.spool.Enqueue(job); err != nil {
		t.Fatal(err)
	}
	body, _ := json.Marshal(map[string]string{
		"onboarding_id": "onboard-group",
		"phase":         "awaiting-credentials",
		"definition_id": "notion-mcp",
		"repository":    "https://github.com/makenotion/notion-mcp-server",
		"form_url":      "http://127.0.0.1:8090/credentials/onboard-group?nonce=abc",
	})
	req := httptest.NewRequest(http.MethodPost, "/v1/prepare-outcome", bytes.NewReader(body))
	req.Header.Set("Authorization", "Bearer control-token")
	req.Header.Set("X-Hub-Principal", "alice")
	rec := httptest.NewRecorder()
	gateway.Handler().ServeHTTP(rec, req)
	if rec.Code != http.StatusNoContent {
		t.Fatalf("status=%d body=%s", rec.Code, rec.Body.String())
	}
	entries, err := os.ReadDir(filepath.Join(cfg.SpoolDir, "outbox", "pending"))
	if err != nil || len(entries) != 1 {
		t.Fatalf("notices=%v err=%v", entries, err)
	}
	notice, err := os.ReadFile(filepath.Join(cfg.SpoolDir, "outbox", "pending", entries[0].Name()))
	if err != nil {
		t.Fatal(err)
	}
	var delivery Delivery
	if err := json.Unmarshal(notice, &delivery); err != nil {
		t.Fatal(err)
	}
	if delivery.ChatID != -100 || delivery.TopicID != 66 {
		t.Fatalf("notice leaked to DM: chat=%d topic=%d", delivery.ChatID, delivery.TopicID)
	}
	jobs, err := os.ReadDir(filepath.Join(cfg.SpoolDir, "pending"))
	if err != nil {
		t.Fatal(err)
	}
	var raw []byte
	for _, jobFile := range jobs {
		if strings.HasPrefix(jobFile.Name(), "prepare-") {
			raw, err = os.ReadFile(filepath.Join(cfg.SpoolDir, "pending", jobFile.Name()))
			if err != nil {
				t.Fatal(err)
			}
		}
	}
	if raw == nil {
		t.Fatalf("continuation job missing among %v", jobs)
	}
	var continuation Job
	if err := json.Unmarshal(raw, &continuation); err != nil {
		t.Fatal(err)
	}
	if continuation.Envelope.ConversationID != task.ConversationID || continuation.ChatID != -100 || continuation.TopicID != 66 {
		t.Fatalf("continuation leaked: conv=%q chat=%d topic=%d", continuation.Envelope.ConversationID, continuation.ChatID, continuation.TopicID)
	}
	if strings.Contains(string(raw), `"trigger":"message"`) {
		t.Fatal("continuation must keep trigger=prepare")
	}
	// The origin job ends long before the build settles; a later outcome must
	// still route to the topic instead of falling back to the DM default.
	for {
		claimed, err := gateway.spool.ClaimJob()
		if err != nil || claimed == nil {
			t.Fatalf("origin job never claimable: %v %v", claimed, err)
		}
		if claimed.ID == job.ID {
			break
		}
		if err := gateway.spool.CompleteJob(claimed.ID); err != nil {
			t.Fatal(err)
		}
	}
	if err := gateway.spool.RecordOutcome(job.ID, RunOutcome{JobID: job.ID, Status: "completed", LastEvent: "run.completed", Text: "ok"}); err != nil {
		t.Fatal(err)
	}
	if err := gateway.spool.CompleteJob(job.ID); err != nil {
		t.Fatal(err)
	}
	body, _ = json.Marshal(map[string]any{
		"onboarding_id": "onboard-group",
		"phase":         "enabled",
		"definition_id": "notion-mcp",
		"repository":    "https://github.com/makenotion/notion-mcp-server",
		"tools":         7,
	})
	req = httptest.NewRequest(http.MethodPost, "/v1/prepare-outcome", bytes.NewReader(body))
	req.Header.Set("Authorization", "Bearer control-token")
	req.Header.Set("X-Hub-Principal", "alice")
	rec = httptest.NewRecorder()
	gateway.Handler().ServeHTTP(rec, req)
	if rec.Code != http.StatusNoContent {
		t.Fatalf("settled status=%d body=%s", rec.Code, rec.Body.String())
	}
	entries, err = os.ReadDir(filepath.Join(cfg.SpoolDir, "outbox", "pending"))
	if err != nil {
		t.Fatal(err)
	}
	leaked, routed := false, false
	for _, entry := range entries {
		fileRaw, err := os.ReadFile(filepath.Join(cfg.SpoolDir, "outbox", "pending", entry.Name()))
		if err != nil {
			t.Fatal(err)
		}
		var d Delivery
		if err := json.Unmarshal(fileRaw, &d); err != nil {
			t.Fatal(err)
		}
		if d.ChatID == 11 {
			leaked = true
		}
		if d.ChatID == -100 && d.TopicID == 66 {
			routed = true
		}
	}
	if leaked || !routed {
		t.Fatalf("post-completion outcome misrouted: leaked=%v routed=%v", leaked, routed)
	}
}
