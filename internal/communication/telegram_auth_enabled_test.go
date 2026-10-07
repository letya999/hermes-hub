//go:build telegramauth

package communication

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/gotd/td/session"
	"github.com/gotd/td/tg"
	"github.com/letya999/hermes-hub/internal/credentialbroker"
	"github.com/letya999/hermes-hub/internal/identity"
)

func TestPrepareOutcomeSendsQRWithoutFormContinuation(t *testing.T) {
	cfg := testConfig(t)
	cfg.ControlAuth = "control-token"
	cfg.FormOrigin = "http://localhost:8081"
	cfg.BrokerApprove = credentialbroker.Config{URL: "https://broker.example"}
	cfg.TelegramAuthEnabled = true
	gateway, err := New(cfg)
	if err != nil {
		t.Fatal(err)
	}
	request := prepareOutcomeRequest{OnboardingID: "onboard-qr", Phase: "awaiting-credentials", ContractID: "telegram-session", BrokerRequestID: "request-qr", Repository: "https://github.com/example/telegram-mcp"}
	body, _ := json.Marshal(request)
	req := httptest.NewRequest(http.MethodPost, "/v1/prepare-outcome", bytes.NewReader(body))
	req.Header.Set("Authorization", "Bearer control-token")
	req.Header.Set("X-Hub-Principal", "alice")
	rec := httptest.NewRecorder()
	gateway.Handler().ServeHTTP(rec, req)
	if rec.Code != http.StatusNoContent {
		t.Fatalf("prepare outcome=%d body=%s", rec.Code, rec.Body.String())
	}
	notices, err := os.ReadDir(filepath.Join(cfg.SpoolDir, "outbox", "pending"))
	if err != nil || len(notices) != 1 {
		t.Fatalf("notices=%v err=%v", notices, err)
	}
	notice, err := os.ReadFile(filepath.Join(cfg.SpoolDir, "outbox", "pending", notices[0].Name()))
	if err != nil || !bytes.Contains(notice, []byte("/telegram-auth/")) || !bytes.Contains(notice, []byte("продолжи подключение Telegram")) {
		t.Fatalf("QR notice missing: err=%v", err)
	}
	jobs, err := os.ReadDir(filepath.Join(cfg.SpoolDir, "pending"))
	if err != nil || len(jobs) != 0 {
		t.Fatalf("QR login enqueued form continuation: jobs=%v err=%v", jobs, err)
	}
}

func TestTelethonStringSession(t *testing.T) {
	key := make([]byte, 256)
	for i := range key {
		key[i] = byte(i)
	}
	value, err := telethonStringSession(&session.Data{DC: 2, Config: session.Config{DCOptions: []tg.DCOption{{ID: 2, IPAddress: "149.154.167.50", Port: 443}}}, AuthKey: key})
	if err != nil || !strings.HasPrefix(value, "1") {
		t.Fatalf("session encoding: %v", err)
	}
	raw, err := base64.URLEncoding.DecodeString(value[1:])
	if err != nil || len(raw) != 263 || raw[0] != 2 || raw[5] != 1 || raw[6] != 187 || string(raw[7:]) != string(key) {
		t.Fatal("Telethon session fields changed")
	}
	if _, err := telethonStringSession(&session.Data{DC: 2, Addr: "example.com:443", AuthKey: key}); err == nil {
		t.Fatal("DNS address accepted")
	}
}

func TestTelegramAuthInviteAndBrowserBinding(t *testing.T) {
	cfg := Config{FormOrigin: "http://localhost:8081", BrokerApprove: credentialbroker.Config{URL: "https://broker.example"}}
	state, err := newTelegramAuthState(cfg)
	if err != nil {
		t.Fatal(err)
	}
	g := &Gateway{config: cfg, now: time.Now, telegramAuth: state}
	owner := identity.TelegramEnvelope("alice", 11, "alice", "policy-1")
	req := prepareOutcomeRequest{Phase: "awaiting-credentials", ContractID: "telegram-session", BrokerRequestID: "request_1"}
	link := g.telegramAuthInvite(owner, req)
	if !strings.HasPrefix(link, "http://localhost:8081/telegram-auth/") {
		t.Fatalf("invite: %q", link)
	}
	if g.telegramAuthInvite(owner, prepareOutcomeRequest{Phase: "awaiting-credentials", ContractID: "github-pat", BrokerRequestID: "request_1"}) != "" {
		t.Fatal("non-Telegram invite")
	}
	otherContext := owner
	otherContext.ContextID = "other"
	if next := g.telegramAuthInvite(otherContext, req); next == "" || next == link {
		t.Fatal("invitation reused across contexts")
	}
	path := strings.TrimPrefix(link, "http://localhost:8081")
	request := httptest.NewRequest(http.MethodGet, link, nil)
	first := httptest.NewRecorder()
	g.Handler().ServeHTTP(first, request)
	if first.Code != http.StatusOK || !strings.Contains(first.Body.String(), "Начать") || len(first.Result().Cookies()) != 0 {
		t.Fatalf("preview claimed invite: %d", first.Code)
	}
	csrf := g.authAttempt(strings.TrimPrefix(path, "/telegram-auth/")).csrf
	if !strings.Contains(first.Body.String(), `name="csrf" value="`+csrf+`"`) {
		t.Fatal("claim form has no CSRF token")
	}
	foreign := httptest.NewRequest(http.MethodPost, link+"/claim", nil)
	foreign.Header.Set("Origin", "https://example.com")
	rejectedClaim := httptest.NewRecorder()
	g.Handler().ServeHTTP(rejectedClaim, foreign)
	if rejectedClaim.Code != http.StatusForbidden {
		t.Fatalf("foreign claim: %d", rejectedClaim.Code)
	}
	claim := httptest.NewRequest(http.MethodPost, link+"/claim", strings.NewReader("csrf="+csrf))
	claim.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	claim.Header.Set("Origin", "null")
	claim.Header.Set("Sec-Fetch-Site", "cross-site")
	claimed := httptest.NewRecorder()
	g.Handler().ServeHTTP(claimed, claim)
	if claimed.Code != http.StatusSeeOther || len(claimed.Result().Cookies()) != 1 {
		t.Fatal("missing browser binding")
	}
	second := httptest.NewRecorder()
	g.Handler().ServeHTTP(second, httptest.NewRequest(http.MethodGet, link, nil))
	if second.Code != http.StatusForbidden {
		t.Fatalf("unbound browser: %d", second.Code)
	}
	bound := httptest.NewRequest(http.MethodGet, link, nil)
	bound.AddCookie(claimed.Result().Cookies()[0])
	page := httptest.NewRecorder()
	g.Handler().ServeHTTP(page, bound)
	if page.Code != http.StatusOK || !strings.Contains(page.Body.String(), "API hash") {
		t.Fatalf("bound browser: %d", page.Code)
	}
	post := httptest.NewRequest(http.MethodPost, path+"/start", strings.NewReader("api_id=123&api_hash=00000000000000000000000000000000&csrf=wrong"))
	post.Host = "localhost:8081"
	post.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	post.AddCookie(claimed.Result().Cookies()[0])
	rejected := httptest.NewRecorder()
	g.Handler().ServeHTTP(rejected, post)
	if rejected.Code != http.StatusForbidden {
		t.Fatalf("bad CSRF: %d", rejected.Code)
	}
	id := strings.TrimPrefix(path, "/telegram-auth/")
	ctx, cancel := context.WithCancel(context.Background())
	attempt := g.authAttempt(id)
	attempt.mu.Lock()
	attempt.status, attempt.accountID, attempt.accountName = "ready", 42, `<script>alert(1)</script>`
	attempt.cancel, attempt.session, attempt.apiHash, attempt.qr = cancel, "session-secret", "hash-secret", []byte("qr-secret")
	attempt.mu.Unlock()
	confirmed := httptest.NewRequest(http.MethodGet, link, nil)
	confirmed.AddCookie(claimed.Result().Cookies()[0])
	confirmation := httptest.NewRecorder()
	g.Handler().ServeHTTP(confirmation, confirmed)
	if strings.Contains(confirmation.Body.String(), "<script>") || !strings.Contains(confirmation.Body.String(), "ID 42") {
		t.Fatal("account identity was not escaped")
	}
	g.expireTelegramAuth(id, attempt)
	if g.authAttempt(id) != nil || ctx.Err() == nil {
		t.Fatal("expired login remains active")
	}
	attempt.mu.Lock()
	defer attempt.mu.Unlock()
	if attempt.session != "" || attempt.apiHash != "" || attempt.accountName != "" || len(attempt.qr) != 0 || attempt.status != "failed" {
		t.Fatal("expired login retains credentials")
	}
}
