//go:build telegramauth

package communication

import (
	"context"
	"encoding/base64"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/gotd/td/session"
	"github.com/gotd/td/tg"
	"github.com/letya999/hermes-hub/internal/credentialbroker"
	"github.com/letya999/hermes-hub/internal/identity"
)

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
	foreign := httptest.NewRequest(http.MethodPost, link+"/claim", nil)
	foreign.Header.Set("Origin", "https://example.com")
	rejectedClaim := httptest.NewRecorder()
	g.Handler().ServeHTTP(rejectedClaim, foreign)
	if rejectedClaim.Code != http.StatusForbidden {
		t.Fatalf("foreign claim: %d", rejectedClaim.Code)
	}
	claim := httptest.NewRequest(http.MethodPost, link+"/claim", nil)
	claim.Header.Set("Origin", cfg.FormOrigin)
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
