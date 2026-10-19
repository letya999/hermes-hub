package toolhub

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/letya999/hermes-hub/internal/toolhub/api"
)

// confirmLinkFixture arms an awaiting-confirm onboarding with a known nonce
// and returns the gateway ready to serve its pages.
func confirmLinkFixture(t *testing.T, nonce string, expires time.Time) (*Gateway, *Store, string) {
	t.Helper()
	store := NewStore()
	grantTestControlOperations(t, store, "alice", api.ControlOperations...)
	definition := catalogReadDefinition()
	if err := store.RegisterDefinition(definition); err != nil {
		t.Fatal(err)
	}
	if err := putGrant(t, store, api.OperatorGrant(api.GrantCatalogDefault, "alice", "", "")); err != nil {
		t.Fatal(err)
	}
	control := &ControlPlane{Store: store, WorkloadRoot: t.TempDir(), Now: time.Now}
	auth := aliceAuth()
	onboarding := api.Onboarding{
		Schema: api.SchemaVersion, OnboardingID: "onb-link-t", PrincipalID: auth.PrincipalID, ContextID: auth.ContextID,
		RuntimeID: auth.RuntimeID, PolicyVersion: auth.PolicyVersion, Revision: 1,
		Mode: api.OnboardingCatalog, Phase: api.PhaseAwaitingConfirm,
		DefinitionID: definition.DefinitionID, DefinitionVersion: definition.Version,
		ConfirmationNonce: nonce, ConfirmationExpires: expires,
	}
	if err := store.PutOnboarding(onboarding); err != nil {
		t.Fatal(err)
	}
	return &Gateway{Control: control}, store, "onb-link-t"
}

func confirmLinkRequest(t *testing.T, method, url string) (*httptest.ResponseRecorder, *http.Request) {
	t.Helper()
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(method, url, nil)
	req.Host = "127.0.0.1"
	return rec, req
}

func TestConfirmLinkAcceptsTParam(t *testing.T) {
	gateway, _, id := confirmLinkFixture(t, "one-time-token", time.Now().Add(time.Hour))
	rec, req := confirmLinkRequest(t, http.MethodGet, "http://127.0.0.1/confirm/"+id+"?t=one-time-token")
	gateway.serveConfirm(rec, req)
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), "<form") {
		t.Fatalf("t-param link status=%d", rec.Code)
	}
}

func TestConfirmLinkReplayReturns410(t *testing.T) {
	gateway, store, id := confirmLinkFixture(t, "used-token", time.Now().Add(time.Hour))
	rec, req := confirmLinkRequest(t, http.MethodPost, "http://127.0.0.1/confirm/"+id+"?t=used-token")
	gateway.serveConfirm(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("first use status=%d body=%s", rec.Code, rec.Body.String())
	}
	if got, err := store.onboarding(id); err != nil || (got.Phase != api.PhaseConfirmed && got.Phase != api.PhaseEnabled) {
		t.Fatalf("phase=%v err=%v", got.Phase, err)
	}
	replay, replayReq := confirmLinkRequest(t, http.MethodGet, "http://127.0.0.1/confirm/"+id+"?t=used-token")
	gateway.serveConfirm(replay, replayReq)
	if replay.Code != http.StatusGone || !strings.Contains(strings.ToLower(replay.Body.String()), "fresh") {
		t.Fatalf("replay status=%d body=%s", replay.Code, replay.Body.String())
	}
}

func TestConfirmLinkExpiredReturns410(t *testing.T) {
	gateway, _, id := confirmLinkFixture(t, "stale-token", time.Now().Add(-time.Minute))
	rec, req := confirmLinkRequest(t, http.MethodGet, "http://127.0.0.1/confirm/"+id+"?t=stale-token")
	gateway.serveConfirm(rec, req)
	if rec.Code != http.StatusGone || !strings.Contains(strings.ToLower(rec.Body.String()), "fresh") {
		t.Fatalf("expired status=%d body=%s", rec.Code, rec.Body.String())
	}
}

func TestConfirmLinkCrossOperationRejected(t *testing.T) {
	gateway, store, id := confirmLinkFixture(t, "token-a", time.Now().Add(time.Hour))
	onboarding, err := store.onboarding(id)
	if err != nil {
		t.Fatal(err)
	}
	onboarding.OnboardingID = "onb-link-b"
	onboarding.ConfirmationNonce = "token-b"
	if err := store.PutOnboarding(onboarding); err != nil {
		t.Fatal(err)
	}
	rec, req := confirmLinkRequest(t, http.MethodGet, "http://127.0.0.1/confirm/onb-link-b?t=token-a")
	gateway.serveConfirm(rec, req)
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("cross-operation token status=%d", rec.Code)
	}
}

func TestConfirmPostWithoutTokenRejected(t *testing.T) {
	gateway, _, id := confirmLinkFixture(t, "csrf-guard", time.Now().Add(time.Hour))
	rec, req := confirmLinkRequest(t, http.MethodPost, "http://127.0.0.1/confirm/"+id)
	gateway.serveConfirm(rec, req)
	if rec.Code != http.StatusUnauthorized && rec.Code != http.StatusForbidden {
		t.Fatalf("tokenless POST status=%d", rec.Code)
	}
}

func TestRemoveLinkReplayReturns410(t *testing.T) {
	store := NewStore()
	grantTestControlOperations(t, store, "alice", api.ControlOperations...)
	definition := catalogReadDefinition()
	if err := store.RegisterDefinition(definition); err != nil {
		t.Fatal(err)
	}
	if err := putGrant(t, store, api.OperatorGrant(api.GrantCatalogDefault, "alice", "", "")); err != nil {
		t.Fatal(err)
	}
	control := &ControlPlane{Store: store, WorkloadRoot: t.TempDir(), Now: time.Now}
	auth := aliceAuth()
	onboarding := api.Onboarding{
		Schema: api.SchemaVersion, OnboardingID: "onb-remove-replay", PrincipalID: auth.PrincipalID, ContextID: auth.ContextID,
		RuntimeID: auth.RuntimeID, PolicyVersion: auth.PolicyVersion, Revision: 1,
		Mode: api.OnboardingCatalog, Phase: api.PhaseEnabled,
		DefinitionID: definition.DefinitionID, DefinitionVersion: definition.Version,
	}
	if err := store.PutOnboarding(onboarding); err != nil {
		t.Fatal(err)
	}
	if _, err := control.Invoke(context.Background(), auth, "remove", map[string]any{"onboarding_id": "onb-remove-replay"}); err != nil {
		t.Fatal(err)
	}
	armed, err := store.onboarding("onb-remove-replay")
	if err != nil || armed.RemoveNonce == "" {
		t.Fatal("preview did not arm the nonce")
	}
	gateway := &Gateway{Control: control}
	rec, req := confirmLinkRequest(t, http.MethodPost, "http://127.0.0.1/remove/onb-remove-replay?t="+armed.RemoveNonce)
	gateway.serveRemove(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("first use status=%d body=%s", rec.Code, rec.Body.String())
	}
	replay, replayReq := confirmLinkRequest(t, http.MethodGet, "http://127.0.0.1/remove/onb-remove-replay?t="+armed.RemoveNonce)
	gateway.serveRemove(replay, replayReq)
	if replay.Code != http.StatusGone || !strings.Contains(strings.ToLower(replay.Body.String()), "fresh") {
		t.Fatalf("replay status=%d body=%s", replay.Code, replay.Body.String())
	}
}
