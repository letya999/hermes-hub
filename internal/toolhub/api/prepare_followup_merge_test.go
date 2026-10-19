package api

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/letya999/hermes-hub/internal/identity"
)

func TestPostPrepareNoticeContract(t *testing.T) {
	var got map[string]any
	var authHeader, principalHeader string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/prepare-outcome" {
			w.WriteHeader(404)
			return
		}
		authHeader = r.Header.Get("Authorization")
		principalHeader = r.Header.Get("X-Hub-Principal")
		json.NewDecoder(r.Body).Decode(&got)
		w.WriteHeader(200)
	}))
	defer server.Close()

	auth := identity.Envelope{PrincipalID: "p1"}
	onboarding := Onboarding{OnboardingID: "onb-1", Phase: PhaseAwaitingCreds, FormNonce: "n/1",
		DefinitionID: "def-1", SourceURL: "https://github.com/a/b", Error: "boom",
		CreatedAt: time.Date(2026, 10, 17, 10, 55, 2, 0, time.UTC)}

	// no control URL configured: a complete no-op
	t.Setenv("HUB_COMMUNICATION_CONTROL_URL", "")
	DeliverPrepareOutcome(context.Background(), auth, onboarding)
	if got != nil {
		t.Fatal("posted without control URL")
	}
	// no auth token anywhere: still a no-op
	t.Setenv("HUB_COMMUNICATION_CONTROL_URL", server.URL)
	t.Setenv("HUB_COMMUNICATION_AUTH", "")
	t.Setenv("HUB_RUNTIME_SUPERVISOR_URL", "")
	t.Setenv("HUB_RUNTIME_AUTH", "")
	DeliverPrepareOutcome(context.Background(), auth, onboarding)
	if got != nil {
		t.Fatal("posted without token")
	}
	// configured: posts phase, repository, and the credential form URL
	t.Setenv("HUB_COMMUNICATION_AUTH", "tok")
	t.Setenv("HUB_TOOLHUB_LISTEN", "127.0.0.1:9999")
	DeliverPrepareOutcome(context.Background(), auth, onboarding)
	if got == nil || got["onboarding_id"] != "onb-1" || got["phase"] != PhaseAwaitingCreds {
		t.Fatalf("payload: %v", got)
	}
	if got["created_at"] != "2026-10-17T10:55:02Z" {
		t.Fatalf("created_at missing from payload: %v", got)
	}
	if authHeader != "Bearer tok" || principalHeader != "p1" {
		t.Fatalf("headers: %q %q", authHeader, principalHeader)
	}
	form, _ := got["form_url"].(string)
	if form == "" || !json.Valid([]byte(`"`+form+`"`)) {
		t.Fatalf("form url: %q", form)
	}
	// interim events dedupe separately and carry the event key
	got = nil
	DeliverPrepareEvent(context.Background(), auth, onboarding, "probe-start")
	if got == nil || got["event"] != "probe-start" {
		t.Fatalf("event payload: %v", got)
	}
	// settled phases carry no form URL
	got = nil
	settled := onboarding
	settled.Phase = PhaseEnabled
	settled.ConfirmationExpires = time.Now()
	PostPrepareNotice(context.Background(), auth, settled, "", 3)
	if _, ok := got["form_url"]; ok && got["form_url"] != "" {
		t.Fatalf("settled form url: %v", got["form_url"])
	}
	if got["tools"] != float64(3) {
		t.Fatalf("tools count: %v", got)
	}
	// unreachable base is a logged no-op, not a panic
	t.Setenv("HUB_COMMUNICATION_CONTROL_URL", "http://127.0.0.1:1")
	DeliverPrepareOutcome(context.Background(), auth, onboarding)
}

func TestPostPrepareNoticeCarriesCredentialIntake(t *testing.T) {
	var got map[string]any
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		json.NewDecoder(r.Body).Decode(&got)
		w.WriteHeader(200)
	}))
	defer server.Close()
	t.Setenv("HUB_COMMUNICATION_CONTROL_URL", server.URL)
	t.Setenv("HUB_COMMUNICATION_AUTH", "tok")
	t.Setenv("HUB_TOOLHUB_LISTEN", "127.0.0.1:9999")
	auth := identity.Envelope{PrincipalID: "p1"}

	// The reviewed telegram-session contract declares QR intake; the gateway
	// needs it in the payload instead of pattern-matching the contract id.
	onboarding := Onboarding{OnboardingID: "onb-tg", Phase: PhaseAwaitingCreds, BrokerContractID: "telegram-session", BrokerContractRevision: 2, BrokerRequestID: "req-1"}
	PostPrepareNotice(context.Background(), auth, onboarding, "", 0)
	if got["credential_intake"] != "telegram_qr" {
		t.Fatalf("intake missing from payload: %v", got)
	}
	// A contract without an intake declaration sends no key at all.
	got = nil
	onboarding.BrokerContractID = "github-pat"
	PostPrepareNotice(context.Background(), auth, onboarding, "", 0)
	if _, ok := got["credential_intake"]; ok {
		t.Fatalf("unexpected intake for plain contract: %v", got)
	}
}

// CHG-0127 S0.3: external action links (OAuth authorize, broker connect) ride
// the same notice channel as loopback links — via the /connect/ hop so the
// provider URL and its state stay off the wire text.
func TestCredentialNoticeURLActionLinks(t *testing.T) {
	t.Setenv("HUB_TOOLHUB_LISTEN", "127.0.0.1:9999")
	oauth := Onboarding{OnboardingID: "onb-oa", Phase: PhaseAwaitingOAuth,
		ProviderAuthorizationURL: "https://accounts.example.com/authorize?state=flow-xyz&client_id=c"}
	if got := credentialNoticeURL(oauth); got != "http://127.0.0.1:9999/connect/onb-oa?state=flow-xyz" {
		t.Fatalf("oauth action link=%q", got)
	}
	broker := Onboarding{OnboardingID: "onb-br", Phase: PhaseAwaitingCreds,
		BrokerRequestID: "req-1", BrokerAuthorizationURL: "https://broker.example.com/connect/req-1"}
	if got := credentialNoticeURL(broker); got != "http://127.0.0.1:9999/connect/onb-br?request=req-1" {
		t.Fatalf("broker action link=%q", got)
	}
}

// CHG-0129 S2.2: HUB_PUBLIC_ORIGIN is the opt-in operator-owned surface —
// minted links carry it (https-only) instead of the loopback listen origin.
func TestCredentialNoticeURLPublicOrigin(t *testing.T) {
	t.Setenv("HUB_TOOLHUB_LISTEN", "127.0.0.1:9999")
	t.Setenv("HUB_PUBLIC_ORIGIN", "https://hub.example.com")
	onboarding := Onboarding{OnboardingID: "onb-pub", Phase: PhaseAwaitingConfirm,
		ConfirmationNonce: "tok-1", ConfirmationExpires: time.Now().Add(time.Hour)}
	if got := credentialNoticeURL(onboarding); got != "https://hub.example.com/confirm/onb-pub?t=tok-1" {
		t.Fatalf("public link=%q", got)
	}
	// http public origins are not link targets: loopback stays.
	t.Setenv("HUB_PUBLIC_ORIGIN", "http://hub.example.com")
	if got := credentialNoticeURL(onboarding); got != "http://127.0.0.1:9999/confirm/onb-pub?t=tok-1" {
		t.Fatalf("http origin rejected, got %q", got)
	}
}
