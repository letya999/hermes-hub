package communication

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/letya999/hermes-hub/internal/toolhub/api"
)

func TestHumanConfirmationNoticeRequiresServiceBearer(t *testing.T) {
	cfg := testConfig(t)
	cfg.Supervised = true
	cfg.RuntimeURL = "http://127.0.0.1:1"
	cfg.ControlAuth = "synthetic-owner-runtime-bearer"
	cfg.RuntimeAuth = cfg.ControlAuth
	cfg.ControlPrincipal = cfg.Users[0].ID
	cfg.AutomationAuth = "synthetic-confirmation-service-bearer"
	g, err := New(cfg)
	if err != nil {
		t.Fatal(err)
	}
	actor := cfg.Users[0].Envelope(11)
	proof := api.HumanConfirmation{OnboardingID: "memprop-service", Actor: actor, Nonce: "synthetic-confirmation-nonce", Revision: 1, ActionDigest: strings.Repeat("a", 64), ExpiresAt: time.Now().Add(time.Minute)}
	body, err := json.Marshal(map[string]any{"onboarding_id": proof.OnboardingID, "phase": "awaiting-confirm", "confirmation": proof})
	if err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		name, token string
		allow       bool
		origin      string
	}{
		{"runtime", cfg.ControlAuth, false, ""}, {"wrong", "unrelated-bearer", false, ""}, {"missing", "", false, ""}, {"browser origin", cfg.AutomationAuth, false, "https://untrusted.example"}, {"service", cfg.AutomationAuth, true, ""},
	} {
		r := httptest.NewRequest(http.MethodPost, "/v1/prepare-outcome", bytes.NewReader(body))
		r.Header.Set("Authorization", "Bearer "+tc.token)
		r.Header.Set("X-Hub-Principal", actor.PrincipalID)
		r.Header.Set("Origin", tc.origin)
		w := httptest.NewRecorder()
		g.Handler().ServeHTTP(w, r)
		if (w.Code == http.StatusOK) != tc.allow {
			t.Errorf("%s confirmation admission status=%d", tc.name, w.Code)
		}
	}
}

func TestHumanConfirmationCallbackPreservesManagedBinding(t *testing.T) {
	testHumanConfirmationBinding(t, false)
}

func TestHumanConfirmationCallbackUsesServiceBearer(t *testing.T) {
	testHumanConfirmationBinding(t, true)
}

func testHumanConfirmationBinding(t *testing.T, service bool) {
	t.Helper()
	cfg := testConfig(t)
	cfg.ControlAuth = "synthetic-legacy-service-bearer"
	expectedToken := cfg.ControlAuth
	if service {
		cfg.ControlAuth = "synthetic-owner-runtime-bearer"
		cfg.AutomationAuth = "synthetic-confirmation-service-bearer"
		expectedToken = cfg.AutomationAuth
	}
	caller := cfg.Users[0].Envelope(11)
	actor := caller
	actor.CapabilityProfile, actor.Environment, actor.Generation, actor.Organization = "alice-default", "prod", 7, "fixture"
	proof := api.HumanConfirmation{OnboardingID: "memprop-binding", Actor: actor, Nonce: "synthetic-confirmation-nonce", Revision: 1, ActionDigest: strings.Repeat("a", 64), ExpiresAt: time.Now().Add(time.Minute)}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var got api.HumanConfirmation
		if r.URL.Path != "/v1/confirmations" || r.Header.Get("Authorization") != "Bearer "+expectedToken || json.NewDecoder(r.Body).Decode(&got) != nil || !got.Actor.Equal(actor) {
			w.WriteHeader(http.StatusForbidden)
			return
		}
		w.WriteHeader(http.StatusOK)
	}))
	t.Cleanup(server.Close)
	cfg.ToolHubURL = server.URL
	g, err := New(cfg)
	if err != nil {
		t.Fatal(err)
	}
	button, err := g.spool.humanConfirmationButton(proof, "telegram_bot", 11)
	if err != nil {
		t.Fatal(err)
	}
	if err := g.confirmHumanCallback(context.Background(), caller, "telegram_bot", 11, button.Data); err != nil {
		t.Fatal("verified callback lost its issued managed binding")
	}
}
