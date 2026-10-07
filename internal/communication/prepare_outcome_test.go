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
	if rec := post("alice", "control-token"); rec.Code != http.StatusNoContent {
		t.Fatalf("status=%d body=%s", rec.Code, rec.Body.String())
	}
	if rec := post("alice", "control-token"); rec.Code != http.StatusNoContent {
		t.Fatalf("repeat=%d", rec.Code)
	}
	body["form_url"] = "http://127.0.0.1:8090/credentials/onboard-abc?nonce=one"
	if rec := post("alice", "control-token"); rec.Code != http.StatusNoContent {
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
	if rec := post("alice", "control-token"); rec.Code != http.StatusNoContent {
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
	if rec.Code != http.StatusNoContent {
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

	if rec := post(); rec.Code != http.StatusNoContent {
		t.Fatalf("prepare-started=%d body=%s", rec.Code, rec.Body.String())
	}
	if rec := post(); rec.Code != http.StatusNoContent {
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
	if rec := post(); rec.Code != http.StatusNoContent {
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
	if rec := post(); rec.Code != http.StatusNoContent {
		t.Fatalf("rejected=%d", rec.Code)
	}
	got = notices()
	if !contains(got, "Причина: auth.test failed") || !contains(got, "исправить и отправить ещё раз") {
		t.Fatalf("rejection notice=%v", got)
	}
	body["detail"] = "server timeout"
	if rec := post(); rec.Code != http.StatusNoContent {
		t.Fatalf("second rejection=%d", rec.Code)
	}
	got = notices()
	if !contains(got, "Причина: server timeout") || len(got) != 4 {
		t.Fatalf("distinct rejection reasons must each notify: %v", got)
	}
	if rec := post(); rec.Code != http.StatusNoContent || len(notices()) != 4 {
		t.Fatal("same rejection reason must dedup")
	}

	body["event"] = "binding-failed"
	body["detail"] = "workload admission refused"
	if rec := post(); rec.Code != http.StatusNoContent {
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
	if rec := post(); rec.Code != http.StatusNoContent {
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
