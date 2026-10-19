package toolhub

import (
	"context"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/letya999/hermes-hub/internal/credstore"
	"github.com/letya999/hermes-hub/internal/oauth"
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

// S2.2: the public listener exposes only the human-action surface — a valid
// confirm link answers even on a non-loopback Host, while control API paths
// are invisible there.
func TestPublicMuxServesOnlyHumanPaths(t *testing.T) {
	gateway, _, id := confirmLinkFixture(t, "pub-token", time.Now().Add(time.Hour))
	public := gateway.PublicMux()
	if public == nil {
		t.Fatal("no public mux")
	}
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "https://hub.example.com/confirm/"+id+"?t=pub-token", nil)
	req.Host = "hub.example.com"
	public.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), "<form") {
		t.Fatalf("public confirm status=%d", rec.Code)
	}
	blocked := httptest.NewRecorder()
	ctrlReq := httptest.NewRequest(http.MethodGet, "https://hub.example.com/v1/connectors", nil)
	ctrlReq.Host = "hub.example.com"
	public.ServeHTTP(blocked, ctrlReq)
	if blocked.Code != http.StatusNotFound {
		t.Fatalf("control path on public surface status=%d", blocked.Code)
	}
}

func TestPublicMuxServesCredentialsRemoveConnect(t *testing.T) {
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
	key, kerr := credstore.GenerateKey()
	if kerr != nil {
		t.Fatal(kerr)
	}
	secrets, serr := credstore.Open(credstore.Options{Path: filepath.Join(t.TempDir(), "s.enc"), Key: key})
	if serr != nil {
		t.Fatal(serr)
	}
	control.OAuth = oauth.NewBroker(secrets, nil)
	auth := aliceAuth()
	onboarding := api.Onboarding{
		Schema: api.SchemaVersion, OnboardingID: "onb-pub-all", PrincipalID: auth.PrincipalID, ContextID: auth.ContextID,
		RuntimeID: auth.RuntimeID, PolicyVersion: auth.PolicyVersion, Revision: 1,
		Mode: api.OnboardingCatalog, Phase: api.PhaseAwaitingCreds,
		DefinitionID: definition.DefinitionID, DefinitionVersion: definition.Version,
		FormNonce: "form-tok", FormExpires: time.Now().Add(time.Hour),
		RemoveNonce: "rm-tok", RemoveExpires: time.Now().Add(time.Hour),
		ProviderAuthorizationURL: "https://accounts.example.com/authorize?state=st-1",
	}
	if err := store.PutOnboarding(onboarding); err != nil {
		t.Fatal(err)
	}
	public := (&Gateway{Control: control}).PublicMux()
	serve := func(path string) *httptest.ResponseRecorder {
		rec := httptest.NewRecorder()
		req := httptest.NewRequest(http.MethodGet, "https://hub.example.com"+path, nil)
		req.Host = "hub.example.com"
		public.ServeHTTP(rec, req)
		return rec
	}
	if rec := serve("/credentials/onb-pub-all?t=form-tok"); rec.Code != http.StatusOK {
		t.Fatalf("public credentials status=%d", rec.Code)
	}
	if rec := serve("/remove/onb-pub-all?t=rm-tok"); rec.Code != http.StatusOK {
		t.Fatalf("public remove status=%d", rec.Code)
	}
	rec := serve("/connect/onb-pub-all?state=st-1")
	if rec.Code != http.StatusFound || rec.Header().Get("Location") != onboarding.ProviderAuthorizationURL {
		t.Fatalf("public connect status=%d loc=%q", rec.Code, rec.Header().Get("Location"))
	}
	// /oauth/callback is registered on the public surface: with no OAuth
	// broker wired the handler 404s itself, so assert the route exists via a
	// non-404 answer only when a broker is present; otherwise a bare request
	// proves routing by reaching the handler (400/403/404 are all handler
	// answers — an unregistered path would also be 404, so we assert against
	// the unambiguous /confirm shape already covered above and only verify
	// registration loosely here).
	if rec := serve("/oauth/callback?onboarding_id=missing"); rec.Code != http.StatusUnauthorized {
		t.Fatalf("public oauth callback status=%d", rec.Code)
	}
}

// S2.2: the endpoint handler exposes the public surface for the second
// listener and mints links against HUB_PUBLIC_ORIGIN when configured.
func TestEndpointHandlerExposesPublicSurface(t *testing.T) {
	store := NewStore()
	auth := aliceAuth()
	token := strings.Repeat("p", 40)
	config := EndpointConfig{Token: token, Auth: auth, Backend: backendFunc(func(context.Context, api.EffectiveBinding, api.ToolSpec, map[string]any) (api.BackendResult, error) {
		return api.BackendResult{Text: "ok"}, nil
	})}
	handler, err := NewEndpointHandler(config, store)
	if err != nil {
		t.Fatal(err)
	}
	public, ok := handler.(interface{ PublicHandler() http.Handler })
	if !ok || public.PublicHandler() == nil {
		t.Fatal("endpoint does not expose a public handler")
	}
}

func TestEndpointHandlerPublicOriginSetsFormOrigin(t *testing.T) {
	t.Setenv("HUB_PUBLIC_ORIGIN", "https://hub.example.com")
	store := NewStore()
	auth := aliceAuth()
	config := EndpointConfig{Token: strings.Repeat("p", 40), Auth: auth, Backend: backendFunc(func(context.Context, api.EffectiveBinding, api.ToolSpec, map[string]any) (api.BackendResult, error) {
		return api.BackendResult{Text: "ok"}, nil
	})}
	handler, err := NewEndpointHandler(config, store)
	if err != nil {
		t.Fatal(err)
	}
	endpoint, ok := handler.(*projectionEndpoint)
	if !ok || endpoint.gateway.Control == nil {
		t.Fatal("no control plane")
	}
	if endpoint.gateway.Control.FormOrigin != "https://hub.example.com" {
		t.Fatalf("form origin=%q", endpoint.gateway.Control.FormOrigin)
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
