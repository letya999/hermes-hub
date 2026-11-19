package communication

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// IAM deny paths: every rejection branch must answer with the bounded page,
// never leak whether a token almost existed.
func TestIAMLinkRejectsMalformedAndUnknownTokens(t *testing.T) {
	idp := newIAMFixtureIdP(t)
	defer idp.Close()
	g := newIAMGateway(t, idp)
	handler := g.Handler()

	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/iam/link/x", nil))
	if rec.Code != http.StatusMethodNotAllowed {
		t.Fatalf("link GET contract: %d", rec.Code)
	}
	for _, path := range []string{"/iam/link/", "/iam/link/short", "/iam/link/" + strings.Repeat("z", 64)} {
		rec = httptest.NewRecorder()
		handler.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, path, nil))
		if rec.Code != http.StatusGone {
			t.Fatalf("link %s must be gone, got %d", path, rec.Code)
		}
	}
	// A minted nonce that was already spent is gone, not re-renderable.
	token := g.iamMintNonce("telegram-1", "alice")
	if _, ok := g.iamTakeNonce(token); !ok {
		t.Fatal("minted nonce must take once")
	}
	rec = httptest.NewRecorder()
	handler.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/iam/link/"+token, nil))
	if rec.Code != http.StatusGone {
		t.Fatalf("spent nonce must be gone, got %d", rec.Code)
	}
	if _, ok := g.iamTakeNonce(token); ok {
		t.Fatal("nonce must be single-use")
	}
	if _, ok := g.iamPeekNonce("nonexistent"); ok {
		t.Fatal("peek must not mint")
	}
}

func TestIAMOIDCStartRejectsWrongMethodAndUnknownNonce(t *testing.T) {
	idp := newIAMFixtureIdP(t)
	defer idp.Close()
	g := newIAMGateway(t, idp)
	handler := g.Handler()

	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/iam/oidc/start", nil))
	if rec.Code != http.StatusMethodNotAllowed {
		t.Fatalf("start GET: %d", rec.Code)
	}
	rec = httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/iam/oidc/start", strings.NewReader(url.Values{"n": {"bogus"}}.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	handler.ServeHTTP(rec, req)
	if rec.Code != http.StatusGone {
		t.Fatalf("unknown nonce must be gone, got %d", rec.Code)
	}
}

func TestIAMOIDCCallbackRejectsWrongMethodUnknownStateAndMissingCode(t *testing.T) {
	idp := newIAMFixtureIdP(t)
	defer idp.Close()
	g := newIAMGateway(t, idp)
	handler := g.Handler()

	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/iam/oidc/callback", nil))
	if rec.Code != http.StatusMethodNotAllowed {
		t.Fatalf("callback POST: %d", rec.Code)
	}
	rec = httptest.NewRecorder()
	handler.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/iam/oidc/callback?state=unknown&code=x", nil))
	if rec.Code != http.StatusGone {
		t.Fatalf("unknown state must be gone, got %d", rec.Code)
	}
	// A live pending record without a code is denied, not crashed.
	state := g.iamPutPending(oidcPending{verifier: "v", expires: g.now().Add(time.Minute)})
	rec = httptest.NewRecorder()
	handler.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/iam/oidc/callback?state="+url.QueryEscape(state), nil))
	if rec.Code != http.StatusForbidden {
		t.Fatalf("missing code must be denied, got %d", rec.Code)
	}
}

func TestIAMPendingAndSweepExpireState(t *testing.T) {
	idp := newIAMFixtureIdP(t)
	defer idp.Close()
	g := newIAMGateway(t, idp)

	expired := time.Now().Add(-time.Hour)
	g.now = func() time.Time { return expired }
	stale := g.iamPutPending(oidcPending{verifier: "v", expires: expired.Add(-time.Minute)})
	g.iam.nonces["stale-nonce"] = linkNonce{transportID: "t", expires: expired.Add(-time.Minute)}
	g.now = time.Now
	if _, ok := g.iamTakePending(stale); ok {
		t.Fatal("expired pending must not take")
	}
	// Sweep clears both maps without a take.
	stale2 := g.iamPutPending(oidcPending{verifier: "v", expires: time.Now().Add(-time.Minute)})
	g.iam.nonces["stale-2"] = linkNonce{transportID: "t", expires: time.Now().Add(-time.Minute)}
	g.iamSweepLocked()
	if _, ok := g.iamTakePending(stale2); ok {
		t.Fatal("swept pending must not survive")
	}
	if _, ok := g.iamPeekNonce("stale-2"); ok {
		t.Fatal("swept nonce must not survive")
	}
}

func TestIAMEnrollmentIgnoresLegacySubjectMutation(t *testing.T) {
	idp := newIAMFixtureIdP(t)
	defer idp.Close()
	g := newIAMGateway(t, idp)
	g.config.Users[0].OIDCSubject = "corp-sub-1"
	token := g.iamMintNonce("telegram-55", "")
	if code := iamRunFlow(t, g, idp, token); code != http.StatusForbidden {
		t.Fatalf("legacy subject mutation authorized enrolment: %d", code)
	}
}

func TestIAMResolveTransportDeniesDisabledAndUnlinked(t *testing.T) {
	idp := newIAMFixtureIdP(t)
	defer idp.Close()
	g := newIAMGateway(t, idp)
	if _, ok := g.telegramUserFor(99, 99); ok {
		t.Fatal("unlinked transport must deny")
	}
	if err := fixtureOIDCLink(t, g, "ghost", "telegram-99", "corp-sub-1"); err == nil {
		t.Fatal("unknown human accepted")
	}
	if _, ok := g.telegramUserFor(99, 99); ok {
		t.Fatal("link to a disabled/unknown user must deny")
	}
	// An IAM-less gateway cannot resolve transports at all.
	plain, err := newTestGateway(t, baseTestConfig(t))
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := plain.telegramUserFor(77, 77); ok {
		t.Fatal("iam-disabled gateway must deny")
	}
}

func TestOIDCProviderRequiresFormOrigin(t *testing.T) {
	idp := newIAMFixtureIdP(t)
	defer idp.Close()
	g := newIAMGateway(t, idp)
	g.config.FormOrigin = ""
	if _, err := g.oidcProvider(); err == nil {
		t.Fatal("missing form origin must fail closed")
	}
}

func TestLinkAndEnrolCommandsStayHonest(t *testing.T) {
	// IAM-disabled gateways answer the disabled line, never a URL.
	plain, err := newTestGateway(t, baseTestConfig(t))
	if err != nil {
		t.Fatal(err)
	}
	route := commandRoute{channel: "telegram_bot", transportID: "telegram-1", principal: "alice"}
	if got := plain.linkCommand("en", route); !strings.Contains(got, "not") && strings.Contains(got, "http") {
		t.Fatalf("disabled iam must not mint links: %q", got)
	}
	if got := plain.unlinkCommand("en", route); strings.Contains(got, "http") {
		t.Fatalf("disabled iam must not mint links: %q", got)
	}
	if got := plain.iamEnrolHint(11); got != "" {
		t.Fatalf("disabled iam must not mint enrol hints: %q", got)
	}
	if got := plain.slackEnrolHint("T1", "U1"); got != "" {
		t.Fatalf("disabled iam must not mint slack hints: %q", got)
	}

	idp := newIAMFixtureIdP(t)
	defer idp.Close()
	g := newIAMGateway(t, idp)
	if got := g.linkCommand("en", commandRoute{channel: "telegram_bot"}); strings.Contains(got, "/iam/link/") {
		t.Fatalf("transport-less route must not mint: %q", got)
	}
	g.config.FormOrigin = ""
	if got := g.iamEnrolHint(11); got != "" {
		t.Fatalf("origin-less enrol hint must stay empty: %q", got)
	}
	if got := g.slackEnrolHint("T1", "U1"); got != "" {
		t.Fatalf("origin-less slack hint must stay empty: %q", got)
	}
	g.config.FormOrigin = "http://127.0.0.1:8081"
	if got := g.iamEnrolHint(11); !strings.Contains(got, "/iam/link/") {
		t.Fatalf("enrol hint must carry the link: %q", got)
	}
	if got := g.slackEnrolHint("T1", "U1"); !strings.Contains(got, "/iam/link/") {
		t.Fatalf("slack hint must carry the link: %q", got)
	}
	if got := g.unlinkCommand("en", route); strings.Contains(got, "dropped") {
		t.Fatalf("unlinked transport must report nothing to drop: %q", got)
	}
}

// QueueStatus ages the oldest outbox-pending record and counts the dirs.
func TestSpoolQueueStatusCountsAndAges(t *testing.T) {
	root := filepath.Join(t.TempDir(), "spool")
	s, err := NewSpool(root)
	if err != nil {
		t.Fatal(err)
	}
	writeFile := func(rel string, age time.Duration) {
		t.Helper()
		path := filepath.Join(root, rel)
		if err := os.WriteFile(path, []byte("{}"), 0600); err != nil {
			t.Fatal(err)
		}
		old := time.Now().Add(-age)
		if err := os.Chtimes(path, old, old); err != nil {
			t.Fatal(err)
		}
	}
	writeFile(filepath.Join("pending", "j1.json"), 0)
	writeFile(filepath.Join("failed", "j2.json"), 0)
	writeFile(filepath.Join("outbox", "pending", "d1.json"), 90*time.Second)
	writeFile(filepath.Join("outbox", "pending", "d2.json"), 10*time.Second)
	writeFile(filepath.Join("outbox", "failed", "d3.json"), 0)

	now := time.Now()
	queue, outbox := s.QueueStatus(now)
	if queue.Pending != 1 || queue.Failed != 1 || outbox.Pending != 2 || outbox.Failed != 1 {
		t.Fatalf("counts wrong: %+v %+v", queue, outbox)
	}
	if outbox.OldestPendingSeconds < 80 {
		t.Fatalf("oldest age not projected: %+v", outbox)
	}
}

// statusView must degrade to unavailable while ToolHub is down and project
// routine aggregates when it answers.
func TestGatewayStatusViewDegradesAndAggregates(t *testing.T) {
	g, err := newTestGateway(t, baseTestConfig(t))
	if err != nil {
		t.Fatal(err)
	}
	caller := g.config.Users[0].Envelope(11)

	view := g.statusView(context.Background(), caller)
	if view.Routines.State != "unavailable" {
		t.Fatalf("toolhub-down routines must degrade: %+v", view.Routines)
	}

	past := time.Now().Add(-time.Hour).UTC()
	future := time.Now().Add(time.Hour).UTC()
	automation := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]any{"result": []map[string]any{
			{"schedule_id": "s1", "next_due": past},
			{"schedule_id": "s2", "next_due": future},
			{"schedule_id": "s3", "paused": true, "next_due": past},
			{"schedule_id": "s4"},
		}})
	}))
	defer automation.Close()
	g.config.ToolHubURL = automation.URL
	g.config.AutomationAuth = "token"

	view = g.statusView(context.Background(), caller)
	if view.Routines.State != "ok" || view.Routines.Total != 4 || view.Routines.Enabled != 3 || view.Routines.Overdue != 1 {
		t.Fatalf("routines aggregation wrong: %+v", view.Routines)
	}
	if view.Routines.NextDue == nil || !view.Routines.NextDue.Equal(past) {
		t.Fatalf("next due must be the earliest enabled: %+v", view.Routines.NextDue)
	}
}

func TestMetricsAndStatusHTTPContract(t *testing.T) {
	config := baseTestConfig(t)
	config.ControlAuth = "ctl-secret"
	g, err := newTestGateway(t, config)
	if err != nil {
		t.Fatal(err)
	}
	handler := g.Handler()

	for _, path := range []string{"/v1/metrics", "/v1/status"} {
		rec := httptest.NewRecorder()
		handler.ServeHTTP(rec, httptest.NewRequest(http.MethodPost, path, nil))
		if rec.Code != http.StatusMethodNotAllowed {
			t.Fatalf("%s POST: %d", path, rec.Code)
		}
		rec = httptest.NewRecorder()
		handler.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, path, nil))
		if rec.Code != http.StatusUnauthorized {
			t.Fatalf("%s unauthenticated: %d", path, rec.Code)
		}
	}
	req := httptest.NewRequest(http.MethodGet, "/v1/metrics", nil)
	req.Header.Set("Authorization", "Bearer ctl-secret")
	req.Header.Set("X-Hub-Principal", "alice")
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), "hub_gateway_queue") {
		t.Fatalf("metrics = %d", rec.Code)
	}
	req = httptest.NewRequest(http.MethodGet, "/v1/status", nil)
	req.Header.Set("Authorization", "Bearer ctl-secret")
	req.Header.Set("X-Hub-Principal", "alice")
	rec = httptest.NewRecorder()
	handler.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), "\"queue\"") {
		t.Fatalf("status = %d %s", rec.Code, rec.Body.String())
	}
}
