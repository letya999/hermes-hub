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

	"github.com/letya999/hermes-hub/internal/identity"
)

// A link-resent outcome is the explicit "send me the link again" moment: it
// must pass validation, render the /connect/ hop, and land as a NEW message
// at the bottom of chat — editing the standing status message would bury the
// link mid-history, which is exactly what the user asked to escape.
func TestLinkResentPostsFreshMessage(t *testing.T) {
	cfg := testConfig(t)
	cfg.ControlAuth = "control-token"
	gateway, err := New(cfg)
	if err != nil {
		t.Fatal(err)
	}
	post := func(payload map[string]string) *httptest.ResponseRecorder {
		raw, _ := json.Marshal(payload)
		req := httptest.NewRequest(http.MethodPost, "/v1/prepare-outcome", bytes.NewReader(raw))
		req.Header.Set("Authorization", "Bearer control-token")
		req.Header.Set("X-Hub-Principal", "alice")
		rec := httptest.NewRecorder()
		gateway.Handler().ServeHTTP(rec, req)
		return rec
	}
	// A standing status message exists: the initial oauth notice claimed it.
	first := post(map[string]string{
		"onboarding_id": "onboard-rsd",
		"phase":         "awaiting-oauth",
		"definition_id": "user-mcp",
		"repository":    "https://github.com/nspady/google-calendar-mcp",
		"form_url":      "http://127.0.0.1:8090/connect/onboard-rsd?state=flow-1",
	})
	if first.Code != http.StatusOK {
		t.Fatalf("initial notice=%d body=%s", first.Code, first.Body.String())
	}
	pending := filepath.Join(cfg.SpoolDir, "outbox", "pending")
	markSent := func() {
		entries, err := os.ReadDir(pending)
		if err != nil {
			return
		}
		for _, entry := range entries {
			src := filepath.Join(pending, entry.Name())
			raw, err := os.ReadFile(src)
			if err != nil {
				continue
			}
			var d Delivery
			if json.Unmarshal(raw, &d) != nil {
				continue
			}
			d.TelegramMessageID = 777
			body, _ := json.Marshal(d)
			_ = os.WriteFile(filepath.Join(cfg.SpoolDir, "outbox", "done", entry.Name()), body, 0600)
			_ = os.Remove(src)
		}
	}
	if err := os.MkdirAll(filepath.Join(cfg.SpoolDir, "outbox", "done"), 0700); err != nil {
		t.Fatal(err)
	}
	markSent()
	t.Run("new action link stays visible", func(t *testing.T) {
		confirm := post(map[string]string{
			"onboarding_id": "onboard-rsd",
			"phase":         "awaiting-confirm",
			"definition_id": "user-mcp",
			"repository":    "https://github.com/nspady/google-calendar-mcp",
			"form_url":      "http://127.0.0.1:8090/confirm/onboard-rsd?t=confirm-1",
		})
		if confirm.Code != http.StatusOK {
			t.Fatalf("confirmation notice=%d body=%s", confirm.Code, confirm.Body.String())
		}
		entries, err := os.ReadDir(pending)
		if err != nil {
			t.Fatal(err)
		}
		found := false
		for _, entry := range entries {
			raw, err := os.ReadFile(filepath.Join(pending, entry.Name()))
			if err != nil {
				t.Fatal(err)
			}
			var delivery Delivery
			if json.Unmarshal(raw, &delivery) != nil || !strings.Contains(delivery.Text, "confirm-1") {
				continue
			}
			found = true
			if delivery.EditMessageID != 0 {
				t.Fatalf("new confirmation link edited old message %d", delivery.EditMessageID)
			}
		}
		if !found {
			t.Fatal("new confirmation link was not enqueued")
		}
	})
	// The resend carries the same phase but a rotated link — it must arrive as
	// a fresh message, not an edit of the buried status message.
	second := post(map[string]string{
		"onboarding_id": "onboard-rsd",
		"phase":         "awaiting-oauth",
		"definition_id": "user-mcp",
		"repository":    "https://github.com/nspady/google-calendar-mcp",
		"event":         "link-resent",
		"form_url":      "http://127.0.0.1:8090/connect/onboard-rsd?state=flow-2",
	})
	if second.Code != http.StatusOK {
		t.Fatalf("resend notice=%d body=%s", second.Code, second.Body.String())
	}
	entries, err := os.ReadDir(pending)
	if err != nil {
		t.Fatal(err)
	}
	var fresh *Delivery
	for _, entry := range entries {
		raw, _ := os.ReadFile(filepath.Join(pending, entry.Name()))
		var d Delivery
		if json.Unmarshal(raw, &d) == nil && strings.Contains(d.Text, "flow-2") {
			delivery := d
			fresh = &delivery
		}
	}
	if fresh == nil {
		t.Fatal("no resend delivery enqueued")
	}
	if fresh.EditMessageID != 0 {
		t.Fatalf("resend edited the buried status message %d instead of posting fresh", fresh.EditMessageID)
	}
}

// The stable wizard link keeps one outcome key across resends while the
// continuation job payload legitimately drifts (policy version, bound task,
// reworded copy after a redeploy). Reusing the link-scoped key as the job's
// idempotency key made every later notice hit "key already used with
// different payload" → 400 — observed live on onboard-443a7a19.
func TestPrepareContinuationSurvivesPayloadDrift(t *testing.T) {
	cfg := testConfig(t)
	cfg.ControlAuth = "control-token"
	gateway, err := New(cfg)
	if err != nil {
		t.Fatal(err)
	}
	caller := identity.TelegramEnvelope("alice", 11, "alice", "policy-1")
	request := prepareOutcomeRequest{
		OnboardingID: "onboard-drift", Phase: "awaiting-oauth",
		Repository: "https://github.com/taylorwilsdon/google_workspace_mcp",
		FormURL:    "http://127.0.0.1:8090/connect/onboard-drift?k=wizard-key",
	}
	key := prepareOutcomeKey(request, "")
	deliver := func(c identity.Envelope) error {
		return gateway.deliverPrepareNotice(cfg.Users[0], c, key, "Войдите у провайдера", request, false)
	}
	if err := deliver(caller); err != nil {
		t.Fatal(err)
	}
	if err := deliver(caller); err != nil {
		t.Fatalf("identical resend must dedupe, not fail: %v", err)
	}
	caller.PolicyVersion = "policy-2"
	if err := deliver(caller); err != nil {
		t.Fatalf("drifted continuation rejected: %v", err)
	}
	jobs, err := os.ReadDir(filepath.Join(cfg.SpoolDir, "pending"))
	if err != nil {
		t.Fatal(err)
	}
	continuations := 0
	for _, entry := range jobs {
		raw, err := os.ReadFile(filepath.Join(cfg.SpoolDir, "pending", entry.Name()))
		if err != nil {
			continue
		}
		var job Job
		if json.Unmarshal(raw, &job) == nil && strings.HasPrefix(job.ID, key) {
			continuations++
		}
	}
	if continuations != 2 {
		t.Fatalf("expected deduped first job plus drifted resend job, got %d", continuations)
	}
}
