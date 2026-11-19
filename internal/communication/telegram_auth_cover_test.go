package communication

import (
	"crypto/rand"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/gotd/td/session"
	"github.com/gotd/td/tg"
	"github.com/letya999/hermes-hub/internal/credentialbroker"
	"github.com/letya999/hermes-hub/internal/identity"
)

func testTelegramGateway(t *testing.T) *Gateway {
	t.Helper()
	state, err := newTelegramAuthState(Config{
		BrokerApprove: credentialbroker.Config{URL: "http://127.0.0.1:9"},
		FormOrigin:    "http://127.0.0.1:9",
	})
	if err != nil {
		t.Fatal(err)
	}
	g := &Gateway{
		config: Config{
			FormOrigin:    "http://127.0.0.1:9",
			BrokerApprove: credentialbroker.Config{URL: "http://127.0.0.1:9"},
			Users:         []User{{ID: "alice", Enabled: true, TelegramIDs: []int64{11}}},
		},
		telegramAuth: state,
		now:          time.Now,
	}
	provisionTestIdentity(t, &g.config)
	return g
}

func TestNewTelegramAuthStateGuards(t *testing.T) {
	if _, err := newTelegramAuthState(Config{}); err == nil {
		t.Fatal("unconfigured broker admitted")
	}
	broker := credentialbroker.Config{URL: "http://broker"}
	for _, origin := range []string{"", "https://127.0.0.1", "http://example.com", "http://127.0.0.1/path", "http://127.0.0.1?q=1"} {
		if _, err := newTelegramAuthState(Config{BrokerApprove: broker, FormOrigin: origin}); err == nil {
			t.Fatalf("origin %q admitted", origin)
		}
	}
	if _, err := newTelegramAuthState(Config{BrokerApprove: broker, FormOrigin: "http://localhost:8", TelegramAuthAPIID: "abc", TelegramAuthAPIHash: "x"}); err == nil {
		t.Fatal("bad api id admitted")
	}
	if _, err := newTelegramAuthState(Config{BrokerApprove: broker, FormOrigin: "http://localhost:8", TelegramAuthAPIID: "123", TelegramAuthAPIHash: "not-hex"}); err == nil {
		t.Fatal("bad api hash admitted")
	}
	state, err := newTelegramAuthState(Config{BrokerApprove: broker, FormOrigin: "http://localhost:8", TelegramAuthAPIID: "12345", TelegramAuthAPIHash: strings.Repeat("ab", 16)})
	if err != nil || state.apiID != 12345 {
		t.Fatalf("configured state: %v", err)
	}
}

func TestTelegramAuthInviteLifecycle(t *testing.T) {
	g := testTelegramGateway(t)
	owner := identity.TelegramEnvelope("alice", 11, "rt-alice", "policy-1")
	req := prepareOutcomeRequest{Phase: "awaiting-credentials", CredentialIntake: "telegram_qr", BrokerRequestID: "breq-01"}
	for _, bad := range []prepareOutcomeRequest{
		{Phase: "done", CredentialIntake: "telegram_qr", BrokerRequestID: "breq-01"},
		{Phase: "awaiting-credentials", CredentialIntake: "form", BrokerRequestID: "breq-01"},
		{Phase: "awaiting-credentials", CredentialIntake: "telegram_qr", BrokerRequestID: "!"},
	} {
		if got := g.telegramAuthInvite(owner, bad); got != "" {
			t.Fatalf("guard leaked invite %q", got)
		}
	}
	link := g.telegramAuthInvite(owner, req)
	if !strings.HasPrefix(link, "http://127.0.0.1:9/telegram-auth/") {
		t.Fatalf("invite: %q", link)
	}
	if again := g.telegramAuthInvite(owner, req); again != link {
		t.Fatalf("live attempt not reused: %q vs %q", again, link)
	}
	// A second principal gets a distinct attempt.
	other := g.telegramAuthInvite(identity.TelegramEnvelope("bob", 22, "rt-bob", "policy-1"), req)
	if other == "" || other == link {
		t.Fatalf("second invite: %q", other)
	}
	// Attempt cap: fill the map and confirm the invite refuses.
	g.telegramAuth.mu.Lock()
	for i := 0; i < 8; i++ {
		id := strings.Repeat(string(rune('a'+i)), 48)
		g.telegramAuth.attempts[id] = &telegramAuthAttempt{owner: identity.TelegramEnvelope("x", 1, "r", "p"), requestID: "r" + id[:2], expires: g.now().Add(time.Hour)}
	}
	g.telegramAuth.mu.Unlock()
	if got := g.telegramAuthInvite(identity.TelegramEnvelope("carol", 33, "rt-carol", "policy-1"), req); got != "" {
		t.Fatalf("cap leaked invite %q", got)
	}
}

func localReq(t *testing.T, method, target string, form url.Values, cookies ...*http.Cookie) *http.Request {
	t.Helper()
	var r *http.Request
	if form != nil {
		r = httptest.NewRequest(method, "http://localhost"+target, strings.NewReader(form.Encode()))
		r.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	} else {
		r = httptest.NewRequest(method, "http://localhost"+target, nil)
	}
	r.Host = "localhost"
	for _, c := range cookies {
		r.AddCookie(c)
	}
	return r
}

func TestServeTelegramAuthFlow(t *testing.T) {
	g := testTelegramGateway(t)
	owner := identity.TelegramEnvelope("alice", 11, "rt-alice", "policy-1")
	link := g.telegramAuthInvite(owner, prepareOutcomeRequest{Phase: "awaiting-credentials", CredentialIntake: "telegram_qr", BrokerRequestID: "breq-02"})
	if link == "" {
		t.Fatal("no invite")
	}
	id := link[strings.LastIndex(link, "/")+1:]
	path := "/telegram-auth/" + id

	// Unknown attempt and bad method both reject.
	rec := httptest.NewRecorder()
	g.serveTelegramAuth(rec, localReq(t, http.MethodGet, "/telegram-auth/"+strings.Repeat("z", 48), nil))
	if rec.Code != http.StatusNotFound {
		t.Fatalf("unknown id: %d", rec.Code)
	}

	// GET serves the claim page; the claim POST enforces the CSRF token.
	rec = httptest.NewRecorder()
	g.serveTelegramAuth(rec, localReq(t, http.MethodGet, path, nil))
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), "claim") {
		t.Fatalf("claim page: %d", rec.Code)
	}
	rec = httptest.NewRecorder()
	g.serveTelegramAuth(rec, localReq(t, http.MethodPost, path+"/claim", url.Values{"csrf": {"wrong"}}))
	if rec.Code != http.StatusForbidden {
		t.Fatalf("bad csrf: %d", rec.Code)
	}
	g.telegramAuth.mu.Lock()
	csrf := g.telegramAuth.attempts[id].csrf
	g.telegramAuth.mu.Unlock()
	rec = httptest.NewRecorder()
	g.serveTelegramAuth(rec, localReq(t, http.MethodPost, path+"/claim", url.Values{"csrf": {csrf}}))
	if rec.Code != http.StatusSeeOther {
		t.Fatalf("claim: %d", rec.Code)
	}
	var browser *http.Cookie
	for _, c := range rec.Result().Cookies() {
		if c.Name == "tg_auth_browser" {
			browser = c
		}
	}
	if browser == nil {
		t.Fatal("browser cookie not set")
	}

	// The status page requires the claimed browser cookie.
	rec = httptest.NewRecorder()
	g.serveTelegramAuth(rec, localReq(t, http.MethodGet, path, nil))
	if rec.Code != http.StatusForbidden {
		t.Fatalf("page without cookie: %d", rec.Code)
	}
	rec = httptest.NewRecorder()
	g.serveTelegramAuth(rec, localReq(t, http.MethodGet, path, nil, browser))
	if rec.Code != http.StatusOK {
		t.Fatalf("page: %d", rec.Code)
	}
	// QR asset is a 404 until login produces an image.
	rec = httptest.NewRecorder()
	g.serveTelegramAuth(rec, localReq(t, http.MethodGet, path+"/qr.png", nil, browser))
	if rec.Code != http.StatusNotFound {
		t.Fatalf("empty qr: %d", rec.Code)
	}
	// Forms re-check CSRF and dispatch each action.
	rec = httptest.NewRecorder()
	g.serveTelegramAuth(rec, localReq(t, http.MethodPost, path+"/start", url.Values{"api_id": {"bad"}}, browser))
	if rec.Code != http.StatusForbidden {
		t.Fatalf("form without csrf: %d", rec.Code)
	}
	rec = httptest.NewRecorder()
	g.serveTelegramAuth(rec, localReq(t, http.MethodPost, path+"/start", url.Values{"csrf": {csrf}, "api_id": {"bad"}, "api_hash": {"x"}}, browser))
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("invalid api creds: %d", rec.Code)
	}
	rec = httptest.NewRecorder()
	g.serveTelegramAuth(rec, localReq(t, http.MethodPost, path+"/password", url.Values{"csrf": {csrf}, "password": {""}}, browser))
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("empty password: %d", rec.Code)
	}
	rec = httptest.NewRecorder()
	g.serveTelegramAuth(rec, localReq(t, http.MethodPost, path+"/password", url.Values{"csrf": {csrf}, "password": {"pw"}}, browser))
	if rec.Code != http.StatusConflict {
		t.Fatalf("password out of phase: %d", rec.Code)
	}
	rec = httptest.NewRecorder()
	g.serveTelegramAuth(rec, localReq(t, http.MethodPost, path+"/save", url.Values{"csrf": {csrf}}, browser))
	if rec.Code != http.StatusConflict {
		t.Fatalf("save out of phase: %d", rec.Code)
	}
	rec = httptest.NewRecorder()
	g.serveTelegramAuth(rec, localReq(t, http.MethodPost, path+"/cancel", url.Values{"csrf": {csrf}}, browser))
	if rec.Code != http.StatusSeeOther {
		t.Fatalf("cancel: %d", rec.Code)
	}
	rec = httptest.NewRecorder()
	g.serveTelegramAuth(rec, localReq(t, http.MethodPost, path+"/bogus", url.Values{"csrf": {csrf}}, browser))
	if rec.Code != http.StatusNotFound {
		t.Fatalf("unknown action: %d", rec.Code)
	}
	rec = httptest.NewRecorder()
	g.serveTelegramAuth(rec, localReq(t, http.MethodGet, path+"/extra/depth", nil, browser))
	if rec.Code != http.StatusNotFound {
		t.Fatalf("deep path: %d", rec.Code)
	}
}

func TestTelegramAuthPageRendersAllStatuses(t *testing.T) {
	g := testTelegramGateway(t)
	attempt := &telegramAuthAttempt{owner: identity.TelegramEnvelope("alice", 11, "rt-alice", "policy-1"), csrf: "csrf-token", expires: time.Now().Add(time.Hour)}
	for _, status := range []string{"new", "connecting", "qr", "password", "ready", "checking-password", "saving", "done", "failed", "weird"} {
		attempt.mu.Lock()
		attempt.status = status
		attempt.accountName = "Alice"
		attempt.accountID = 42
		attempt.mu.Unlock()
		rec := httptest.NewRecorder()
		g.telegramAuthPage(rec, strings.Repeat("a", 48), attempt)
		if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), "tg") && rec.Body.Len() < 100 {
			t.Fatalf("status %s page: %d", status, rec.Code)
		}
	}
}

func TestTelethonStringSessionPacking(t *testing.T) {
	if _, err := telethonStringSession(nil); err == nil {
		t.Fatal("nil data admitted")
	}
	key := make([]byte, 256)
	if _, err := rand.Read(key); err != nil {
		t.Fatal(err)
	}
	for _, bad := range []*session.Data{
		{DC: 0, AuthKey: key},
		{DC: 300, AuthKey: key},
		{DC: 2, AuthKey: key[:10]},
		{DC: 2, AuthKey: key, Config: session.Config{TestMode: true}},
		{DC: 2, AuthKey: key, Addr: "not-an-addr"},
		{DC: 2, AuthKey: key, Addr: "example.com:443"},
		{DC: 2, AuthKey: key, Addr: "127.0.0.1:0"},
		{DC: 2, AuthKey: key, Addr: "127.0.0.1:99999"},
	} {
		if _, err := telethonStringSession(bad); err == nil {
			t.Fatalf("bad session admitted: %+v", bad)
		}
	}
	packed, err := telethonStringSession(&session.Data{DC: 2, AuthKey: key, Addr: "149.154.167.51:443"})
	if err != nil || !strings.HasPrefix(packed, "1") {
		t.Fatalf("pack: %q %v", packed, err)
	}
	// DCOptions fallback resolves the address when Addr is empty.
	packed2, err := telethonStringSession(&session.Data{DC: 2, AuthKey: key, Config: session.Config{DCOptions: []tg.DCOption{
		{ID: 1, IPAddress: "10.0.0.1", Port: 80},
		{ID: 2, IPAddress: "not-an-ip", Port: 443},
		{ID: 2, IPAddress: "149.154.167.51", Port: 443, Ipv6: true},
		{ID: 2, IPAddress: "149.154.167.51", Port: 443},
	}}})
	if err != nil || packed2 != packed {
		t.Fatalf("dc-options pack mismatch")
	}
}

func TestTelegramAuthAttemptExpiry(t *testing.T) {
	g := testTelegramGateway(t)
	attempt := &telegramAuthAttempt{owner: identity.TelegramEnvelope("alice", 11, "rt-alice", "policy-1"), requestID: "r", expires: g.now().Add(-time.Second), status: "new", password: make(chan string, 1)}
	id := strings.Repeat("b", 48)
	g.telegramAuth.attempts[id] = attempt
	if g.authAttempt(id) != nil {
		t.Fatal("expired attempt served")
	}
	attempt.expires = g.now().Add(time.Hour)
	if g.authAttempt(id) != attempt {
		t.Fatal("live attempt not served")
	}
	if g.authAttempt("short") != nil || g.authAttempt(strings.Repeat("c", 48)) != nil {
		t.Fatal("bad id served")
	}
	g.expireTelegramAuth(id, attempt)
	if g.authAttempt(id) != nil {
		t.Fatal("expired attempt still tracked")
	}
	attempt.mu.Lock()
	failed := attempt.status == "failed"
	attempt.mu.Unlock()
	if !failed {
		t.Fatal("attempt not marked failed")
	}
}
