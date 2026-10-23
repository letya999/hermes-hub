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

	"github.com/letya999/hermes-hub/internal/commodel"
	"github.com/letya999/hermes-hub/internal/identity"
)

// CHG-0127 S0.3: confirmation and removal links ride the same notice channel
// as credential forms — all are loopback action pages the gateway may relay.
func TestPrepareOutcomeLinkPaths(t *testing.T) {
	for _, raw := range []string{
		"http://127.0.0.1:8090/credentials/onboard-abc?nonce=n1",
		"http://127.0.0.1:8090/confirm/onboard-abc?nonce=n2",
		"http://127.0.0.1:8090/remove/onboard-abc?nonce=n3",
		"http://localhost:8090/remove/onboard-abc?nonce=n4",
		"http://127.0.0.1:8090/connect/onboard-abc?state=flow-xyz",
		"http://127.0.0.1:8090/connect/onboard-abc?request=req-777",
	} {
		if !loopbackCredentialURL(raw) {
			t.Fatalf("rejected action link %q", raw)
		}
	}
	for _, raw := range []string{
		"http://127.0.0.1:8090/evil/onboard-abc?nonce=n",
		"https://attacker.example/remove/onboard-abc?nonce=n",
		"http://127.0.0.1:8090/remove/onboard-abc",
	} {
		if loopbackCredentialURL(raw) {
			t.Fatalf("accepted unsafe link %q", raw)
		}
	}
}

// CHG-0129 S2.7: an onboarding initiated on the mock channel receives its
// action link back on the mock channel — the notice follows the origin job's
// channel instead of assuming Telegram. The link itself stays plain text, so
// a channel with Caps().Actions == false delivers it unchanged.
func TestPrepareOutcomeNoticeFollowsOriginChannel(t *testing.T) {
	cfg := testConfig(t)
	cfg.ControlAuth = "control-token"
	gateway, err := New(cfg)
	if err != nil {
		t.Fatal(err)
	}
	created := time.Now().UTC()
	mapping := commodel.JobMapping{
		JobID: "job-mock-origin", PrincipalID: "alice", Trigger: "message",
		Channel: "mock", ConversationID: "mock-conv-9", DeliveryTargetID: "mock-conv-9",
		ChatID: 42, Status: "done", CreatedAt: created.Add(-time.Minute),
	}
	gateway.spool.mu.Lock()
	err = gateway.spool.writeMappingLocked(mapping)
	gateway.spool.mu.Unlock()
	if err != nil {
		t.Fatal(err)
	}
	body, _ := json.Marshal(map[string]string{
		"onboarding_id": "onboard-mock", "phase": "awaiting-credentials",
		"definition_id": "user-mcp", "repository": "https://github.com/example/mcp",
		"form_url":   "http://127.0.0.1:8090/credentials/onboard-mock?t=tok1",
		"created_at": created.Format(time.RFC3339Nano),
	})
	req := httptest.NewRequest(http.MethodPost, "/v1/prepare-outcome", bytes.NewReader(body))
	req.Header.Set("Authorization", "Bearer control-token")
	req.Header.Set("X-Hub-Principal", "alice")
	rec := httptest.NewRecorder()
	gateway.Handler().ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", rec.Code, rec.Body.String())
	}
	var result map[string]string
	if json.Unmarshal(rec.Body.Bytes(), &result) != nil || result["login_url"] != "http://127.0.0.1:8090/credentials/onboard-mock?t=tok1" {
		t.Fatalf("protected form link missing from handoff: %s", rec.Body.String())
	}
	entries, err := os.ReadDir(filepath.Join(cfg.SpoolDir, "outbox", "pending"))
	if err != nil {
		t.Fatal(err)
	}
	sawMock := false
	for _, entry := range entries {
		raw, err := os.ReadFile(filepath.Join(cfg.SpoolDir, "outbox", "pending", entry.Name()))
		if err != nil {
			t.Fatal(err)
		}
		var delivery Delivery
		if json.Unmarshal(raw, &delivery) != nil {
			t.Fatalf("outbox record %s", raw)
		}
		if delivery.Channel != "mock" || delivery.ChatID != 42 {
			continue
		}
		if !strings.Contains(delivery.Text, "http://127.0.0.1:8090/credentials/onboard-mock?t=tok1") {
			t.Fatalf("mock notice lost the action link: %q", delivery.Text)
		}
		sawMock = true
	}
	if !sawMock {
		t.Fatal("no mock-channel notice spooled")
	}
}

// CHG-0129 S2.7: when the operator exposes a public origin, toolhub mints
// https links; the gateway accepts https only for the configured origin host
// and only on the approved human-action paths.
func TestPrepareOutcomePublicOriginLink(t *testing.T) {
	t.Setenv("HUB_PUBLIC_ORIGIN", "https://hub.example.com")
	for _, raw := range []string{
		"https://hub.example.com/credentials/onboard-abc?t=n1",
		"https://hub.example.com/confirm/onboard-abc?t=n2",
		"https://hub.example.com/device/onboard-abc?t=n3",
		"http://127.0.0.1:8090/device/onboard-abc?t=n4",
	} {
		if !loopbackCredentialURL(raw) {
			t.Fatalf("rejected action link %q", raw)
		}
	}
	for _, raw := range []string{
		"https://evil.example/credentials/onboard-abc?t=n1",
		"https://hub.example.com.evil.example/credentials/onboard-abc?t=n1",
		"https://hub.example.com/status/onboard-abc?t=n1",
		"https://hub.example.com/credentials/onboard-abc",
	} {
		if loopbackCredentialURL(raw) {
			t.Fatalf("accepted unsafe link %q", raw)
		}
	}
}

func TestPrepareNoticeWaitsForTelegramLogin(t *testing.T) {
	cfg := testConfig(t)
	gateway, err := New(cfg)
	if err != nil {
		t.Fatal(err)
	}
	request := prepareOutcomeRequest{OnboardingID: "onboard-qr", Phase: "awaiting-credentials", ContractID: "telegram-session"}
	if err := gateway.deliverPrepareNotice(cfg.Users[0], identity.TelegramEnvelope("alice", 11, "alice", "policy-1"), "prepare-qr", "Open QR", request, true); err != nil {
		t.Fatal(err)
	}
	notices, err := os.ReadDir(filepath.Join(cfg.SpoolDir, "outbox", "pending"))
	if err != nil || len(notices) != 1 {
		t.Fatalf("notices=%v err=%v", notices, err)
	}
	jobs, err := os.ReadDir(filepath.Join(cfg.SpoolDir, "pending"))
	if err != nil || len(jobs) != 0 {
		t.Fatalf("QR login enqueued continuation: jobs=%v err=%v", jobs, err)
	}
}

func TestPrepareOutcomeDeliversNoticeAndContinuation(t *testing.T) {
	cfg := testConfig(t)
	cfg.ControlAuth = "control-token"
	slackState := filepath.Join(t.TempDir(), "slack", "state")
	slackWS := filepath.Join(t.TempDir(), "slack", "workspace")
	if err := os.MkdirAll(slackState, 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(slackWS, 0700); err != nil {
		t.Fatal(err)
	}
	cfg.Users = append(cfg.Users, User{ID: "carol", Enabled: true, SlackIDs: []SlackLink{{TeamID: "TTEAM", UserID: "UUSER"}}, StateDir: slackState, WorkspaceDir: slackWS})
	gateway, err := New(cfg)
	if err != nil {
		t.Fatal(err)
	}
	body := map[string]string{
		"onboarding_id": "onboard-abc",
		"phase":         "awaiting-credentials",
		"definition_id": "user-mcp",
		"repository":    "https://github.com/example/mcp",
		"detail":        "",
	}
	post := func(principal, auth string) *httptest.ResponseRecorder {
		t.Helper()
		raw, _ := json.Marshal(body)
		req := httptest.NewRequest(http.MethodPost, "/v1/prepare-outcome", bytes.NewReader(raw))
		if auth != "" {
			req.Header.Set("Authorization", "Bearer "+auth)
		}
		req.Header.Set("X-Hub-Principal", principal)
		rec := httptest.NewRecorder()
		gateway.Handler().ServeHTTP(rec, req)
		return rec
	}
	if rec := post("alice", ""); rec.Code != http.StatusUnauthorized {
		t.Fatalf("unauthorized=%d", rec.Code)
	}
	if rec := post("carol", "control-token"); rec.Code != http.StatusBadRequest {
		t.Fatalf("slack-only=%d body=%s", rec.Code, rec.Body.String())
	}
	if rec := post("alice", "control-token"); rec.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", rec.Code, rec.Body.String())
	}
	if rec := post("alice", "control-token"); rec.Code != http.StatusOK {
		t.Fatalf("repeat=%d", rec.Code)
	}
	body["form_url"] = "http://127.0.0.1:8090/credentials/onboard-abc?nonce=one"
	if rec := post("alice", "control-token"); rec.Code != http.StatusOK {
		t.Fatalf("fresh form=%d body=%s", rec.Code, rec.Body.String())
	}
	pending, err := os.ReadDir(filepath.Join(cfg.SpoolDir, "pending"))
	if err != nil || len(pending) != 2 {
		t.Fatalf("jobs=%v err=%v", pending, err)
	}
	outbox, err := os.ReadDir(filepath.Join(cfg.SpoolDir, "outbox", "pending"))
	if err != nil || len(outbox) != 2 {
		t.Fatalf("notices=%v err=%v", outbox, err)
	}
	jobRaw, err := os.ReadFile(filepath.Join(cfg.SpoolDir, "pending", pending[0].Name()))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(jobRaw), `"trigger":"prepare"`) || !strings.Contains(string(jobRaw), "mcp__toolhub__status") || !strings.Contains(string(jobRaw), "config.yaml") {
		t.Fatalf("continuation=%s", jobRaw)
	}
	noticeRaw, err := os.ReadFile(filepath.Join(cfg.SpoolDir, "outbox", "pending", outbox[0].Name()))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(noticeRaw), "Токен в чат отправлять не нужно") {
		t.Fatalf("notice=%s", noticeRaw)
	}
	body["phase"] = "failed"
	body["detail"] = "tools list empty"
	if rec := post("alice", "control-token"); rec.Code != http.StatusOK {
		t.Fatalf("failed outcome=%d body=%s", rec.Code, rec.Body.String())
	}
	failedNotice := false
	failedEntries, err := os.ReadDir(filepath.Join(cfg.SpoolDir, "outbox", "pending"))
	if err != nil {
		t.Fatal(err)
	}
	for _, entry := range failedEntries {
		raw, err := os.ReadFile(filepath.Join(cfg.SpoolDir, "outbox", "pending", entry.Name()))
		if err != nil {
			t.Fatal(err)
		}
		if strings.Contains(string(raw), "Причина: tools list empty") {
			failedNotice = true
		}
	}
	if !failedNotice {
		t.Fatal("failed prepare notice omitted the reason")
	}
	body["definition_id"] = "Bad"
	if rec := post("alice", "control-token"); rec.Code != http.StatusBadRequest {
		t.Fatalf("invalid definition=%d", rec.Code)
	}
	body["definition_id"] = "user-mcp"
	body["credential_intake"] = strings.Repeat("x", 41)
	if rec := post("alice", "control-token"); rec.Code != http.StatusBadRequest {
		t.Fatalf("oversized credential_intake=%d", rec.Code)
	}
	delete(body, "credential_intake")
	get := httptest.NewRequest(http.MethodGet, "/v1/prepare-outcome", nil)
	get.Header.Set("Authorization", "Bearer control-token")
	get.Header.Set("X-Hub-Principal", "alice")
	got := httptest.NewRecorder()
	gateway.Handler().ServeHTTP(got, get)
	if got.Code != http.StatusMethodNotAllowed {
		t.Fatalf("get=%d", got.Code)
	}
	body["phase"] = "preparing"
	raw, _ := json.Marshal(body)
	req := httptest.NewRequest(http.MethodPost, "/v1/prepare-outcome", bytes.NewReader(raw))
	req.Header.Set("Authorization", "Bearer control-token")
	req.Header.Set("X-Hub-Principal", "alice")
	rec := httptest.NewRecorder()
	gateway.Handler().ServeHTTP(rec, req)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("preparing phase accepted: %d", rec.Code)
	}
	if errNoPrepareChannel.Error() == "" {
		t.Fatal("missing channel error")
	}
	body["onboarding_id"] = "onboard-form"
	body["phase"] = "awaiting-credentials"
	body["detail"] = ""
	body["form_url"] = "http://127.0.0.1:8090/credentials/onboard-form?nonce=abc"
	raw, _ = json.Marshal(body)
	req = httptest.NewRequest(http.MethodPost, "/v1/prepare-outcome", bytes.NewReader(raw))
	req.Header.Set("Authorization", "Bearer control-token")
	req.Header.Set("X-Hub-Principal", "alice")
	rec = httptest.NewRecorder()
	gateway.Handler().ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("form notice=%d body=%s", rec.Code, rec.Body.String())
	}
	sawLink, sawHold := false, false
	for _, dir := range []string{filepath.Join(cfg.SpoolDir, "outbox", "pending"), filepath.Join(cfg.SpoolDir, "pending")} {
		entries, err := os.ReadDir(dir)
		if err != nil {
			t.Fatal(err)
		}
		for _, entry := range entries {
			fileRaw, err := os.ReadFile(filepath.Join(dir, entry.Name()))
			if err != nil {
				t.Fatal(err)
			}
			text := string(fileRaw)
			if strings.Contains(text, "Откройте её на этом компьютере: http://127.0.0.1:8090/credentials/onboard-form?nonce=abc") {
				sawLink = true
			}
			if strings.Contains(text, "уже отправлена ссылка") && !strings.Contains(text, "отправь пользователю form_url") {
				sawHold = true
			}
		}
	}
	if !sawLink || !sawHold {
		t.Fatalf("bot notice link=%v hold=%v", sawLink, sawHold)
	}
	body["form_url"] = "https://evil.example/credentials/onboard-form?nonce=abc"
	raw, _ = json.Marshal(body)
	req = httptest.NewRequest(http.MethodPost, "/v1/prepare-outcome", bytes.NewReader(raw))
	req.Header.Set("Authorization", "Bearer control-token")
	req.Header.Set("X-Hub-Principal", "alice")
	rec = httptest.NewRecorder()
	gateway.Handler().ServeHTTP(rec, req)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("public form url accepted: %d", rec.Code)
	}
}

// CHG-0127 made the channel notice the only path carrying one-time action
// links to the human — tool results no longer include them. Notices for
// awaiting-confirm and awaiting-oauth must render the /confirm/ and /connect/
// URLs, and a remove preview keeps the prior phase yet still owes the
// /remove/ link; dropping any of them deadlocks the onboarding.
func TestPrepareOutcomeActionLinks(t *testing.T) {
	cfg := testConfig(t)
	cfg.ControlAuth = "control-token"
	gateway, err := New(cfg)
	if err != nil {
		t.Fatal(err)
	}
	body := map[string]string{
		"onboarding_id": "onboard-act",
		"definition_id": "user-mcp",
		"repository":    "https://github.com/example/mcp",
	}
	post := func() *httptest.ResponseRecorder {
		t.Helper()
		raw, _ := json.Marshal(body)
		req := httptest.NewRequest(http.MethodPost, "/v1/prepare-outcome", bytes.NewReader(raw))
		req.Header.Set("Authorization", "Bearer control-token")
		req.Header.Set("X-Hub-Principal", "alice")
		rec := httptest.NewRecorder()
		gateway.Handler().ServeHTTP(rec, req)
		return rec
	}
	notices := func() []string {
		t.Helper()
		entries, err := os.ReadDir(filepath.Join(cfg.SpoolDir, "outbox", "pending"))
		if err != nil {
			return nil
		}
		var out []string
		for _, entry := range entries {
			raw, err := os.ReadFile(filepath.Join(cfg.SpoolDir, "outbox", "pending", entry.Name()))
			if err != nil {
				t.Fatal(err)
			}
			out = append(out, string(raw))
		}
		return out
	}
	contains := func(needle string) bool {
		for _, item := range notices() {
			if strings.Contains(item, needle) {
				return true
			}
		}
		return false
	}

	body["phase"] = "awaiting-confirm"
	body["form_url"] = "http://127.0.0.1:8090/confirm/onboard-act?t=conf-1"
	if rec := post(); rec.Code != http.StatusOK {
		t.Fatalf("awaiting-confirm=%d body=%s", rec.Code, rec.Body.String())
	}
	if !contains("http://127.0.0.1:8090/confirm/onboard-act?t=conf-1") {
		t.Fatalf("confirm notice lost the link: %v", notices())
	}

	body["phase"] = "awaiting-oauth"
	body["form_url"] = "http://127.0.0.1:8090/connect/onboard-act?state=flow-1"
	if rec := post(); rec.Code != http.StatusOK {
		t.Fatalf("awaiting-oauth=%d body=%s", rec.Code, rec.Body.String())
	}
	if !contains("http://127.0.0.1:8090/connect/onboard-act?state=flow-1") {
		t.Fatalf("oauth notice lost the link: %v", notices())
	}

	// The remove preview posts the still-current phase — enabled here — while
	// form_url carries the armed /remove/ link.
	jobsBefore, err := os.ReadDir(filepath.Join(cfg.SpoolDir, "pending"))
	if err != nil {
		jobsBefore = nil
	}
	body["phase"] = "enabled"
	body["form_url"] = "http://127.0.0.1:8090/remove/onboard-act?t=rm-1"
	if rec := post(); rec.Code != http.StatusOK {
		t.Fatalf("remove preview=%d body=%s", rec.Code, rec.Body.String())
	}
	if !contains("http://127.0.0.1:8090/remove/onboard-act?t=rm-1") {
		t.Fatalf("remove notice lost the link: %v", notices())
	}
	if contains("подключён") {
		t.Fatalf("remove preview rendered as an enable notice: %v", notices())
	}
	// The human click is the continuation — no agent job may resume while the
	// destructive link is pending.
	jobsAfter, err := os.ReadDir(filepath.Join(cfg.SpoolDir, "pending"))
	if err != nil {
		jobsAfter = nil
	}
	if len(jobsAfter) != len(jobsBefore) {
		t.Fatalf("remove preview enqueued a continuation job: before=%d after=%d", len(jobsBefore), len(jobsAfter))
	}
}

// The browser viewer link arrives with its own fields — connection_id and
// viewer_url — and must reach the owner's channel like any other action
// link; the old handler rejected the shape outright and the link vanished.
func TestPrepareOutcomeViewerLink(t *testing.T) {
	cfg := testConfig(t)
	cfg.ControlAuth = "control-token"
	gateway, err := New(cfg)
	if err != nil {
		t.Fatal(err)
	}
	post := func(body map[string]string) *httptest.ResponseRecorder {
		t.Helper()
		raw, _ := json.Marshal(body)
		req := httptest.NewRequest(http.MethodPost, "/v1/prepare-outcome", bytes.NewReader(raw))
		req.Header.Set("Authorization", "Bearer control-token")
		req.Header.Set("X-Hub-Principal", "alice")
		rec := httptest.NewRecorder()
		gateway.Handler().ServeHTTP(rec, req)
		return rec
	}
	if rec := post(map[string]string{"connection_id": "conn-1", "viewer_url": "http://127.0.0.1:8090/viewer/conn-1?t=view-1"}); rec.Code != http.StatusOK {
		t.Fatalf("viewer link=%d body=%s", rec.Code, rec.Body.String())
	}
	entries, err := os.ReadDir(filepath.Join(cfg.SpoolDir, "outbox", "pending"))
	if err != nil || len(entries) == 0 {
		t.Fatalf("viewer notice not spooled: %v %v", entries, err)
	}
	sawLink := false
	for _, entry := range entries {
		raw, err := os.ReadFile(filepath.Join(cfg.SpoolDir, "outbox", "pending", entry.Name()))
		if err != nil {
			t.Fatal(err)
		}
		if strings.Contains(string(raw), "http://127.0.0.1:8090/viewer/conn-1?t=view-1") {
			sawLink = true
		}
	}
	if !sawLink {
		t.Fatal("viewer notice lost the link")
	}
	if jobs, _ := os.ReadDir(filepath.Join(cfg.SpoolDir, "pending")); len(jobs) != 0 {
		t.Fatalf("viewer link enqueued a continuation job: %d", len(jobs))
	}
	if rec := post(map[string]string{"connection_id": "conn-1", "viewer_url": "http://127.0.0.1:8090/confirm/conn-1?t=x"}); rec.Code != http.StatusBadRequest {
		t.Fatalf("non-viewer url=%d", rec.Code)
	}
	if rec := post(map[string]string{"connection_id": "conn-1", "viewer_url": "https://evil.example/viewer/conn-1?t=x"}); rec.Code != http.StatusBadRequest {
		t.Fatalf("foreign host=%d", rec.Code)
	}
	if rec := post(map[string]string{"connection_id": "conn-1", "viewer_url": "http://127.0.0.1:8090/viewer/conn-1"}); rec.Code != http.StatusBadRequest {
		t.Fatalf("tokenless viewer=%d", rec.Code)
	}
}

func TestPrepareOutcomeProgressEvents(t *testing.T) {
	cfg := testConfig(t)
	cfg.ControlAuth = "control-token"
	gateway, err := New(cfg)
	if err != nil {
		t.Fatal(err)
	}
	body := map[string]any{
		"onboarding_id": "onboard-evt",
		"phase":         "preparing",
		"definition_id": "user-mcp",
		"repository":    "https://github.com/example/mcp",
		"event":         "prepare-started",
	}
	post := func() *httptest.ResponseRecorder {
		t.Helper()
		raw, _ := json.Marshal(body)
		req := httptest.NewRequest(http.MethodPost, "/v1/prepare-outcome", bytes.NewReader(raw))
		req.Header.Set("Authorization", "Bearer control-token")
		req.Header.Set("X-Hub-Principal", "alice")
		rec := httptest.NewRecorder()
		gateway.Handler().ServeHTTP(rec, req)
		return rec
	}
	notices := func() []string {
		t.Helper()
		entries, err := os.ReadDir(filepath.Join(cfg.SpoolDir, "outbox", "pending"))
		if err != nil {
			return nil
		}
		var out []string
		for _, entry := range entries {
			raw, err := os.ReadFile(filepath.Join(cfg.SpoolDir, "outbox", "pending", entry.Name()))
			if err != nil {
				t.Fatal(err)
			}
			out = append(out, string(raw))
		}
		return out
	}
	jobs := func() int {
		t.Helper()
		entries, err := os.ReadDir(filepath.Join(cfg.SpoolDir, "pending"))
		if err != nil {
			return 0
		}
		return len(entries)
	}

	contains := func(hay []string, needle string) bool {
		for _, item := range hay {
			if strings.Contains(item, needle) {
				return true
			}
		}
		return false
	}

	if rec := post(); rec.Code != http.StatusOK {
		t.Fatalf("prepare-started=%d body=%s", rec.Code, rec.Body.String())
	}
	if rec := post(); rec.Code != http.StatusOK {
		t.Fatalf("repeat=%d", rec.Code)
	}
	got := notices()
	if len(got) != 1 || !contains(got, "скачиваю, собираю образ") {
		t.Fatalf("start notices=%v", got)
	}
	if jobs() != 0 {
		t.Fatal("interim event enqueued a continuation job")
	}

	body["phase"] = "awaiting-credentials"
	body["event"] = "credentials-check"
	if rec := post(); rec.Code != http.StatusOK {
		t.Fatalf("credentials-check=%d", rec.Code)
	}
	got = notices()
	if len(got) != 2 || !contains(got, "Данные из формы приняты") {
		t.Fatalf("check notices=%v", got)
	}
	if jobs() != 0 {
		t.Fatal("credentials-check enqueued a continuation job")
	}

	body["event"] = "credentials-rejected"
	body["detail"] = "auth.test failed"
	if rec := post(); rec.Code != http.StatusOK {
		t.Fatalf("rejected=%d", rec.Code)
	}
	got = notices()
	if !contains(got, "Причина: auth.test failed") || !contains(got, "исправить и отправить ещё раз") {
		t.Fatalf("rejection notice=%v", got)
	}
	body["detail"] = "server timeout"
	if rec := post(); rec.Code != http.StatusOK {
		t.Fatalf("second rejection=%d", rec.Code)
	}
	got = notices()
	if !contains(got, "Причина: server timeout") || len(got) != 4 {
		t.Fatalf("distinct rejection reasons must each notify: %v", got)
	}
	if rec := post(); rec.Code != http.StatusOK || len(notices()) != 4 {
		t.Fatal("same rejection reason must dedup")
	}

	body["event"] = "binding-failed"
	body["detail"] = "workload admission refused"
	if rec := post(); rec.Code != http.StatusOK {
		t.Fatalf("binding-failed=%d", rec.Code)
	}
	if got = notices(); !contains(got, "повторное подтверждение пройдёт без формы") {
		t.Fatalf("binding-failed notice=%v", got)
	}

	body["event"] = "bogus-event"
	if rec := post(); rec.Code != http.StatusBadRequest {
		t.Fatalf("unknown event=%d", rec.Code)
	}
	body["event"] = "prepare-started"
	body["phase"] = ""
	if rec := post(); rec.Code != http.StatusBadRequest {
		t.Fatalf("event without phase=%d", rec.Code)
	}
	body["phase"] = "enabled"
	body["event"] = ""
	body["detail"] = ""
	body["tools"] = 15
	if rec := post(); rec.Code != http.StatusOK {
		t.Fatalf("enabled=%d", rec.Code)
	}
	got = notices()
	if !contains(got, "подключён: 15 инструментов") {
		t.Fatalf("enabled notice missing tool count: %v", got)
	}
	if jobs() != 1 {
		t.Fatal("settled enabled outcome must still enqueue the continuation job")
	}
	body["tools"] = 600
	if rec := post(); rec.Code != http.StatusBadRequest {
		t.Fatalf("tools bound=%d", rec.Code)
	}
}

// An awaiting-confirm notice carrying the stable wizard link must keep the
// URL in its text: the notice now edits the shared message in place, so a
// link-free body would strip the only actionable URL the user has.
func TestPrepareOutcomeWizardConfirmKeepsLink(t *testing.T) {
	cfg := testConfig(t)
	cfg.ControlAuth = "control-token"
	gateway, err := New(cfg)
	if err != nil {
		t.Fatal(err)
	}
	wizard := "http://127.0.0.1:8090/connect/onboard-wzc?k=0123456789abcdef0123456789abcdef"
	raw, _ := json.Marshal(map[string]string{
		"onboarding_id": "onboard-wzc", "phase": "awaiting-confirm",
		"definition_id": "user-mcp", "repository": "https://github.com/example/mcp",
		"form_url": wizard,
	})
	req := httptest.NewRequest(http.MethodPost, "/v1/prepare-outcome", bytes.NewReader(raw))
	req.Header.Set("Authorization", "Bearer control-token")
	req.Header.Set("X-Hub-Principal", "alice")
	rec := httptest.NewRecorder()
	gateway.Handler().ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", rec.Code, rec.Body.String())
	}
	entries, err := os.ReadDir(filepath.Join(cfg.SpoolDir, "outbox", "pending"))
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, entry := range entries {
		body, _ := os.ReadFile(filepath.Join(cfg.SpoolDir, "outbox", "pending", entry.Name()))
		if strings.Contains(string(body), wizard) {
			found = true
		}
	}
	if !found {
		t.Fatal("awaiting-confirm notice with a wizard link lost the URL")
	}
}

// prepareContinuation must treat a wizard link as the already-sent link — the
// generic fallback would tell the agent to fetch and forward a form_url that
// never reaches the model-facing body.
func TestPrepareContinuationWizardConfirm(t *testing.T) {
	request := prepareOutcomeRequest{OnboardingID: "onboard-wzc", Phase: "awaiting-confirm",
		Repository: "https://github.com/example/mcp",
		FormURL:    "http://127.0.0.1:8090/connect/onboard-wzc?k=0123456789abcdef0123456789abcdef"}
	text := prepareContinuation(request)
	if !strings.Contains(text, "уже отправлена") || strings.Contains(text, "required_credentials") {
		t.Fatalf("continuation did not recognize the wizard link: %s", text)
	}
}

// Issue #104 live defect: connector_remove armed on a disabled onboarding
// posts phase "disabled" with a /remove/ action link. The phase allowlist only
// knew install-flow phases, so the confirmation notice died with a 400 before
// the remove text was ever built — the owner never got the link in chat.
func TestPrepareOutcomeRemoveOnDisabledDeliversLink(t *testing.T) {
	cfg := testConfig(t)
	cfg.ControlAuth = "control-token"
	gateway, err := New(cfg)
	if err != nil {
		t.Fatal(err)
	}
	created := time.Now().UTC()
	mapping := commodel.JobMapping{
		JobID: "job-remove-origin", PrincipalID: "alice", Trigger: "message",
		Channel: "mock", ConversationID: "mock-conv-7", DeliveryTargetID: "mock-conv-7",
		ChatID: 42, Status: "done", CreatedAt: created.Add(-time.Minute),
	}
	gateway.spool.mu.Lock()
	err = gateway.spool.writeMappingLocked(mapping)
	gateway.spool.mu.Unlock()
	if err != nil {
		t.Fatal(err)
	}
	for _, phase := range []string{"disabled", "revoked", "cleanup_incomplete"} {
		body, _ := json.Marshal(map[string]string{
			"onboarding_id": "onboard-rmv-" + phase, "phase": phase,
			"definition_id": "google-calendar-mcp", "repository": "github.com/example/calendar",
			"form_url":   "http://127.0.0.1:8090/remove/onboard-rmv-" + phase + "?t=nonce-" + phase,
			"created_at": created.Format(time.RFC3339Nano),
		})
		req := httptest.NewRequest(http.MethodPost, "/v1/prepare-outcome", bytes.NewReader(body))
		req.Header.Set("Authorization", "Bearer control-token")
		req.Header.Set("X-Hub-Principal", "alice")
		rec := httptest.NewRecorder()
		gateway.Handler().ServeHTTP(rec, req)
		if rec.Code != http.StatusOK {
			t.Fatalf("phase %s: status=%d body=%s", phase, rec.Code, rec.Body.String())
		}
	}
	entries, err := os.ReadDir(filepath.Join(cfg.SpoolDir, "outbox", "pending"))
	if err != nil {
		t.Fatal(err)
	}
	sawRemove := 0
	for _, entry := range entries {
		raw, err := os.ReadFile(filepath.Join(cfg.SpoolDir, "outbox", "pending", entry.Name()))
		if err != nil {
			t.Fatal(err)
		}
		var delivery Delivery
		if json.Unmarshal(raw, &delivery) != nil {
			t.Fatalf("outbox record %s", raw)
		}
		if strings.Contains(delivery.Text, "ждёт подтверждения") && strings.Contains(delivery.Text, "/remove/") {
			sawRemove++
		}
	}
	if sawRemove != 3 {
		t.Fatalf("remove confirmation notices spooled=%d, want 3", sawRemove)
	}
}
