package toolhub

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/letya999/hermes-hub/internal/toolhub/api"
)

// A resend request on status for an awaiting-confirm onboarding must post a
// link-resent event so the gateway drops a FRESH message carrying the confirm
// link — the standing prepare notice only edits the old chat message, which a
// user who lost the link never sees. The nonce never reaches the result.
func TestStatusResendConfirmationPostsFreshLink(t *testing.T) {
	var events []string
	noticer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var payload map[string]any
		_ = json.NewDecoder(r.Body).Decode(&payload)
		events = append(events, fmt.Sprint(payload["event"]))
		w.WriteHeader(200)
	}))
	defer noticer.Close()
	t.Setenv("HUB_COMMUNICATION_CONTROL_URL", noticer.URL)
	t.Setenv("HUB_COMMUNICATION_AUTH", "tok")
	t.Setenv("HUB_TOOLHUB_LISTEN", "127.0.0.1:9999")
	store := NewStore()
	auth := aliceAuth()
	on := api.Onboarding{Schema: api.SchemaVersion, OnboardingID: "onboard_confirm4", PrincipalID: auth.PrincipalID, ContextID: auth.ContextID, RuntimeID: auth.RuntimeID, PolicyVersion: auth.PolicyVersion, Mode: api.OnboardingCatalog, Phase: api.PhaseAwaitingConfirm, DefinitionID: "stateful-mcp", DefinitionVersion: "2.1.0", Revision: 1, CreatedAt: time.Now()}
	if err := store.PutOnboarding(on); err != nil {
		t.Fatal(err)
	}
	c := &ControlPlane{Store: store, FormOrigin: "http://127.0.0.1:8090"}
	body, err := c.status(context.Background(), auth, map[string]any{"onboarding_id": on.OnboardingID, "resend": true})
	if err != nil {
		t.Fatal(err)
	}
	resent := false
	for _, event := range events {
		if event == "link-resent" {
			resent = true
		}
	}
	if !resent {
		t.Fatalf("no link-resent notice posted; events=%v", events)
	}
	stored, err := store.onboarding(on.OnboardingID)
	if err != nil {
		t.Fatal(err)
	}
	if stored.ConfirmationNonce == "" {
		t.Fatal("status left the onboarding without an armed confirmation nonce")
	}
	raw, _ := json.Marshal(body)
	if strings.Contains(string(raw), stored.ConfirmationNonce) {
		t.Fatalf("confirmation nonce leaked into the tool result: %s", raw)
	}
}
