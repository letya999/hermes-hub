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
		DefinitionID: "def-1", SourceURL: "https://github.com/a/b", Error: "boom"}

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
