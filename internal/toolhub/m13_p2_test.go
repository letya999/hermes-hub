package toolhub

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/letya999/hermes-hub/internal/credstore"
	"github.com/letya999/hermes-hub/internal/identity"
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
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), "accounts.example.com") {
		t.Fatalf("public connect status=%d", rec.Code)
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

// S2.3: RFC 8628 device flow rides the same one-time link surface — GET
// renders the user code + verification URI, POST polls and completes.
func TestDeviceFlowPageCompletesOnboarding(t *testing.T) {
	store := NewStore()
	grantTestControlOperations(t, store, "alice", api.ControlOperations...)
	definition := remoteDefinition()
	definition.DefinitionID = "device-mcp"
	if err := store.RegisterDefinition(definition); err != nil {
		t.Fatal(err)
	}
	if err := putGrant(t, store, api.OperatorGrant(api.GrantDefinition, "alice", "device-mcp", "1.0.0")); err != nil {
		t.Fatal(err)
	}
	if err := putGrant(t, store, api.OperatorGrant(api.GrantCatalogDefault, "alice", "", "")); err != nil {
		t.Fatal(err)
	}
	key, _ := credstore.GenerateKey()
	secrets, serr := credstore.Open(credstore.Options{Path: filepath.Join(t.TempDir(), "dev.enc"), Key: key})
	if serr != nil {
		t.Fatal(serr)
	}
	control := &ControlPlane{Store: store, Secrets: secrets, WorkloadRoot: t.TempDir(), Now: time.Now}
	fixture := oauth.NewFixture()
	t.Cleanup(fixture.Close)
	broker := oauth.NewBroker(secrets, nil)
	broker.HTTP = fixture.Server.Client()
	broker.Providers["fixture"] = fixture.Provider()
	control.OAuth = broker

	auth := aliceAuth()
	prep, err := control.Invoke(context.Background(), auth, "prepare_source", map[string]any{"definition_id": "device-mcp", "version": "1.0.0", "request_key": "dev-1"})
	if err != nil {
		t.Fatal(err)
	}
	onboardingID := prep["onboarding_id"].(string)
	if _, err := control.Invoke(context.Background(), auth, "required_credentials", map[string]any{"onboarding_id": onboardingID}); err != nil {
		t.Fatal(err)
	}
	onboarding, err := store.onboarding(onboardingID)
	if err != nil || onboarding.FormNonce == "" {
		t.Fatal("no form nonce minted")
	}
	onboarding.OAuthProvider = "fixture"
	if err := store.PutOnboarding(onboarding); err != nil {
		t.Fatal(err)
	}
	gateway := &Gateway{Control: control}
	get, getReq := confirmLinkRequest(t, http.MethodGet, "http://127.0.0.1/device/"+onboardingID+"?t="+onboarding.FormNonce)
	gateway.serveDevice(get, getReq)
	if get.Code != http.StatusOK || !strings.Contains(get.Body.String(), "ABCD-1234") || !strings.Contains(get.Body.String(), fixture.Server.URL+"/device") {
		t.Fatalf("device page status=%d body=%s", get.Code, get.Body.String())
	}
	post, postReq := confirmLinkRequest(t, http.MethodPost, "http://127.0.0.1/device/"+onboardingID+"?t="+onboarding.FormNonce)
	postReq.PostForm = map[string][]string{"device_code": {"dev-code"}}
	gateway.serveDevice(post, postReq)
	if post.Code != http.StatusOK {
		t.Fatalf("device poll status=%d body=%s", post.Code, post.Body.String())
	}
	latest, err := store.onboarding(onboardingID)
	if err != nil || (latest.Phase != api.PhaseConfirmed && latest.Phase != api.PhaseEnabled) {
		t.Fatalf("device flow did not finish authorization: phase=%v err=%v", latest.Phase, err)
	}
}

// authorization_pending is a waiting state, not a failure — the page tells
// the user to retry; once the AS approves, the same POST completes.
func TestDeviceFlowPendingThenApproved(t *testing.T) {
	store := NewStore()
	grantTestControlOperations(t, store, "alice", api.ControlOperations...)
	definition := remoteDefinition()
	definition.DefinitionID = "device-pending-mcp"
	if err := store.RegisterDefinition(definition); err != nil {
		t.Fatal(err)
	}
	if err := putGrant(t, store, api.OperatorGrant(api.GrantDefinition, "alice", "device-pending-mcp", "1.0.0")); err != nil {
		t.Fatal(err)
	}
	if err := putGrant(t, store, api.OperatorGrant(api.GrantCatalogDefault, "alice", "", "")); err != nil {
		t.Fatal(err)
	}
	key, _ := credstore.GenerateKey()
	secrets, serr := credstore.Open(credstore.Options{Path: filepath.Join(t.TempDir(), "dp.enc"), Key: key})
	if serr != nil {
		t.Fatal(serr)
	}
	control := &ControlPlane{Store: store, Secrets: secrets, WorkloadRoot: t.TempDir(), Now: time.Now}
	fixture := oauth.NewFixture()
	fixture.DevicePending = true
	t.Cleanup(fixture.Close)
	broker := oauth.NewBroker(secrets, nil)
	broker.HTTP = fixture.Server.Client()
	broker.Providers["fixture"] = fixture.Provider()
	control.OAuth = broker

	auth := aliceAuth()
	prep, err := control.Invoke(context.Background(), auth, "prepare_source", map[string]any{"definition_id": "device-pending-mcp", "version": "1.0.0", "request_key": "dev-p1"})
	if err != nil {
		t.Fatal(err)
	}
	onboardingID := prep["onboarding_id"].(string)
	if _, err := control.Invoke(context.Background(), auth, "required_credentials", map[string]any{"onboarding_id": onboardingID}); err != nil {
		t.Fatal(err)
	}
	onboarding, err := store.onboarding(onboardingID)
	if err != nil || onboarding.FormNonce == "" {
		t.Fatal("no form nonce")
	}
	onboarding.OAuthProvider = "fixture"
	if err := store.PutOnboarding(onboarding); err != nil {
		t.Fatal(err)
	}
	gateway := &Gateway{Control: control}
	get, getReq := confirmLinkRequest(t, http.MethodGet, "http://127.0.0.1/device/"+onboardingID+"?t="+onboarding.FormNonce)
	gateway.serveDevice(get, getReq)
	if get.Code != http.StatusOK {
		t.Fatalf("device page status=%d", get.Code)
	}
	post, postReq := confirmLinkRequest(t, http.MethodPost, "http://127.0.0.1/device/"+onboardingID+"?t="+onboarding.FormNonce)
	postReq.PostForm = map[string][]string{"device_code": {"dev-code"}}
	gateway.serveDevice(post, postReq)
	if post.Code != http.StatusOK || !strings.Contains(post.Body.String(), "Жду подтверждение") {
		t.Fatalf("pending poll status=%d body=%s", post.Code, post.Body.String())
	}
	fixture.ApproveDevice("dev-code")
	// PollDevice marks a pending entry used only after a successful exchange —
	// the broker needs a fresh pending entry per poll attempt, so restart via GET.
	retry, retryReq := confirmLinkRequest(t, http.MethodPost, "http://127.0.0.1/device/"+onboardingID+"?t="+onboarding.FormNonce)
	retryReq.PostForm = map[string][]string{"device_code": {"dev-code"}}
	gateway.serveDevice(retry, retryReq)
	if retry.Code != http.StatusOK || strings.Contains(retry.Body.String(), "Жду") {
		t.Fatalf("approved poll status=%d body=%s", retry.Code, retry.Body.String())
	}
}

// The credential form advertises device flow as an alternative when the
// onboarding's OAuth provider has a device_authorization_endpoint.
func TestCredentialFormOffersDeviceFlowWhenSupported(t *testing.T) {
	store := NewStore()
	grantTestControlOperations(t, store, "alice", api.ControlOperations...)
	definition := remoteDefinition()
	definition.DefinitionID = "device-offer-mcp"
	if err := store.RegisterDefinition(definition); err != nil {
		t.Fatal(err)
	}
	if err := putGrant(t, store, api.OperatorGrant(api.GrantDefinition, "alice", "device-offer-mcp", "1.0.0")); err != nil {
		t.Fatal(err)
	}
	key, _ := credstore.GenerateKey()
	secrets, serr := credstore.Open(credstore.Options{Path: filepath.Join(t.TempDir(), "do.enc"), Key: key})
	if serr != nil {
		t.Fatal(serr)
	}
	control := &ControlPlane{Store: store, Secrets: secrets, WorkloadRoot: t.TempDir(), Now: time.Now}
	fixture := oauth.NewFixture()
	t.Cleanup(fixture.Close)
	broker := oauth.NewBroker(secrets, nil)
	broker.HTTP = fixture.Server.Client()
	broker.Providers["fixture"] = fixture.Provider()
	control.OAuth = broker

	auth := aliceAuth()
	prep, err := control.Invoke(context.Background(), auth, "prepare_source", map[string]any{"definition_id": "device-offer-mcp", "version": "1.0.0", "request_key": "dev-o1"})
	if err != nil {
		t.Fatal(err)
	}
	onboardingID := prep["onboarding_id"].(string)
	if _, err := control.Invoke(context.Background(), auth, "required_credentials", map[string]any{"onboarding_id": onboardingID}); err != nil {
		t.Fatal(err)
	}
	onboarding, err := store.onboarding(onboardingID)
	if err != nil || onboarding.FormNonce == "" {
		t.Fatal("no form nonce")
	}
	onboarding.OAuthProvider = "fixture"
	if err := store.PutOnboarding(onboarding); err != nil {
		t.Fatal(err)
	}
	gateway := &Gateway{Control: control}
	rec, req := confirmLinkRequest(t, http.MethodGet, "http://127.0.0.1/credentials/"+onboardingID+"?t="+onboarding.FormNonce)
	gateway.serveCredentials(rec, req)
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), "/device/"+onboardingID) {
		t.Fatalf("form status=%d, no device offer: %.200s", rec.Code, rec.Body.String())
	}
}

func TestDeviceRouteRegisteredOnBothMuxes(t *testing.T) {
	store := NewStore()
	grantTestControlOperations(t, store, "alice", api.ControlOperations...)
	control := &ControlPlane{Store: store, WorkloadRoot: t.TempDir(), Now: time.Now}
	key, _ := credstore.GenerateKey()
	secrets, serr := credstore.Open(credstore.Options{Path: filepath.Join(t.TempDir(), "d.enc"), Key: key})
	if serr != nil {
		t.Fatal(serr)
	}
	control.OAuth = oauth.NewBroker(secrets, nil)
	gateway := &Gateway{Store: store, Control: control,
		Backend: backendFunc(func(context.Context, api.EffectiveBinding, api.ToolSpec, map[string]any) (api.BackendResult, error) {
			return api.BackendResult{Text: "ok"}, nil
		}),
		Tokens: map[string]identity.Envelope{strings.Repeat("t", 40): aliceAuth()}}
	handler, err := gateway.Handler()
	if err != nil {
		t.Fatal(err)
	}
	for _, target := range []http.Handler{handler, gateway.PublicMux()} {
		rec := httptest.NewRecorder()
		req := httptest.NewRequest(http.MethodGet, "http://127.0.0.1/device/onb-x?t=nope", nil)
		req.Host = "127.0.0.1"
		target.ServeHTTP(rec, req)
		// 401 proves the route reached serveDevice; a mux 404 would mean the
		// route is unregistered.
		if rec.Code != http.StatusUnauthorized {
			t.Fatalf("/device route status=%d", rec.Code)
		}
	}
}

// S2.4: the connect hop renders an interstitial page — a link to the
// provider plus a paste-the-redirect form for browsers that finished OAuth
// on another device where the loopback callback cannot load.
func TestConnectPageOffersPasteRedirectForm(t *testing.T) {
	gateway, store, _ := confirmLinkFixture(t, "unused", time.Now().Add(time.Hour))
	onboarding, err := store.onboarding("onb-link-t")
	if err != nil {
		t.Fatal(err)
	}
	onboarding.Phase = api.PhaseAwaitingOAuth
	onboarding.ProviderAuthorizationURL = "https://accounts.example.com/authorize?state=st-paste&client_id=c"
	if err := store.PutOnboarding(onboarding); err != nil {
		t.Fatal(err)
	}
	rec, req := confirmLinkRequest(t, http.MethodGet, "http://127.0.0.1/connect/onb-link-t?state=st-paste")
	gateway.serveConnect(rec, req)
	body := rec.Body.String()
	if rec.Code != http.StatusOK || !strings.Contains(body, "accounts.example.com") || !strings.Contains(body, "redirect_url") {
		t.Fatalf("connect page status=%d body=%.200s", rec.Code, body)
	}
	if rec.Header().Get("Cache-Control") != "no-store" || rec.Header().Get("Content-Security-Policy") == "" {
		t.Fatal("connect page missing security headers")
	}
}

// S2.4 happy path: the user pasted the final (dead on their other device)
// redirect URL into the connect page — the hub validates the state embedded
// in it against the onboarding's stored authorization URL and completes the
// prepared OAuth flow exactly like the live /oauth/callback would.
func TestConnectPasteCompletesOAuth(t *testing.T) {
	entry, matched, err := api.PreparedForRepository("https://github.com/nspady/google-calendar-mcp", "")
	if err != nil || !matched || entry.OAuth == nil {
		t.Fatal("reviewed OAuth entry missing", err)
	}
	d := statefulContainerDefinition()
	d.DefinitionID, d.Version = entry.ID, "1.0.0"
	d.Source.Repository, d.Source.CommitSHA = entry.Source.Repository, entry.Source.CommitSHA
	d.Credentials = []api.CredentialInput{{Name: entry.OAuth.ClientInput, Required: true}}
	d.CredentialContractID, d.CredentialContractRevision = entry.ContractID, entry.ContractRevision
	d.CredentialContractEnv = map[string]string{entry.OAuth.ClientInput: entry.OAuth.ClientInput}
	store := NewStore()
	if err := store.RegisterDefinition(d); err != nil {
		t.Fatal(err)
	}
	auth := aliceAuth()
	ref := api.CredentialReference{Schema: api.SchemaVersion, CredentialRefID: "cred_paste", ConnectionID: "conn_paste", Revision: 1, Backend: "credential-broker", Locator: "credential_paste", Keys: []string{entry.OAuth.ClientInput}, BrokerGrantID: "grant_paste", BrokerContractID: entry.ContractID, BrokerContractRevision: entry.ContractRevision, BrokerEnv: d.CredentialContractEnv, Status: api.ActiveStatus}
	if err := store.PutCredentialReference(ref); err != nil {
		t.Fatal(err)
	}
	if err := store.PutConnection(api.Connection{Schema: api.SchemaVersion, ConnectionID: ref.ConnectionID, Owner: api.OwnerRef{Type: api.PrincipalOwner, ID: auth.PrincipalID}, DefinitionID: d.DefinitionID, CredentialRefID: ref.CredentialRefID, Revision: 1, Status: api.ActiveStatus}); err != nil {
		t.Fatal(err)
	}
	on := api.Onboarding{Schema: api.SchemaVersion, OnboardingID: "onb_paste", PrincipalID: auth.PrincipalID, ContextID: auth.ContextID, RuntimeID: auth.RuntimeID, PolicyVersion: auth.PolicyVersion, Mode: api.OnboardingCatalog, Phase: api.PhaseAwaitingConfirm, DefinitionID: d.DefinitionID, DefinitionVersion: d.Version, Locator: ref.Locator, ConfirmationNonce: "nonce", ConfirmationExpires: time.Now().Add(time.Hour), Revision: 1, CreatedAt: time.Now()}
	if err := store.PutOnboarding(on); err != nil {
		t.Fatal(err)
	}
	secrets, err := credstore.Open(credstore.Options{Path: filepath.Join(t.TempDir(), "p.enc"), Key: bytes.Repeat([]byte("k"), 32)})
	if err != nil {
		t.Fatal(err)
	}
	root := t.TempDir()
	clientFile := filepath.Join(root, "client.json")
	stateDir := filepath.Join(root, "state")
	if err := os.Mkdir(stateDir, 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(clientFile, []byte(`{"installed":{"client_id":"fixture.apps.googleusercontent.com","client_secret":"fixture-secret","redirect_uris":["http://localhost"]}}`), 0600); err != nil {
		t.Fatal(err)
	}
	c := &ControlPlane{Store: store, Secrets: secrets, FormOrigin: "http://127.0.0.1:8090", Injector: func(_ context.Context, _ api.EffectiveBinding) (api.CredentialInjection, error) {
		return api.CredentialInjection{Environment: map[string]string{entry.OAuth.ClientInput: "/run/mcp-secrets/google/client.json"}, Mounts: []api.Mount{{Source: clientFile, Target: "/run/mcp-secrets/google/client.json", ReadOnly: true}, {Source: stateDir, Target: entry.StateTarget}}, Checkpoint: func() error { return nil }}, nil
	}}
	c.Ready = func(_ context.Context, _ api.EffectiveBinding) error {
		if _, err := os.Stat(filepath.Join(stateDir, entry.OAuth.TokenFile)); err != nil {
			return api.ErrUnauthorized
		}
		return nil
	}
	body, err := c.confirm(t.Context(), auth, map[string]any{"onboarding_id": on.OnboardingID, "nonce": on.ConfirmationNonce})
	if err != nil || body["phase"] != api.PhaseAwaitingOAuth {
		t.Fatalf("handoff phase=%v err=%v", body["phase"], err)
	}
	authURL, _ := url.Parse(providerAuthURLOf(t, store, on.OnboardingID))
	state := authURL.Query().Get("state")
	provider := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/token" || r.FormValue("code_verifier") == "" {
			t.Error("invalid exchange")
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"access_token":"a","refresh_token":"r","expires_in":3600,"token_type":"Bearer","scope":"https://www.googleapis.com/auth/calendar.readonly"}`))
	}))
	defer provider.Close()
	c.oauthFlows.mu.Lock()
	c.oauthFlows.flows[state].broker.HTTP = provider.Client()
	c.oauthFlows.flows[state].broker.Providers["recipe"] = oauth.Provider{Name: "recipe", AuthorizeURL: provider.URL + "/authorize", TokenURL: provider.URL + "/token"}
	c.oauthFlows.mu.Unlock()

	gateway := &Gateway{Control: c}
	rec, req := confirmLinkRequest(t, http.MethodPost, "http://127.0.0.1/connect/"+on.OnboardingID)
	req.PostForm = map[string][]string{
		"state":        {state},
		"redirect_url": {"http://127.0.0.1:8090/oauth/callback?state=" + url.QueryEscape(state) + "&code=paste-code"},
	}
	gateway.serveConnect(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("paste status=%d body=%s", rec.Code, rec.Body.String())
	}
	latest, err := store.onboarding(on.OnboardingID)
	if err != nil || latest.Phase != api.PhaseEnabled {
		t.Fatalf("paste did not complete: phase=%v err=%v", latest.Phase, err)
	}
}

// S2.4 fail-closed: a pasted redirect whose state does not match the
// onboarding's stored authorization URL is rejected before any exchange.
func TestConnectPasteRejectsForeignState(t *testing.T) {
	gateway, store, _ := confirmLinkFixture(t, "unused", time.Now().Add(time.Hour))
	onboarding, err := store.onboarding("onb-link-t")
	if err != nil {
		t.Fatal(err)
	}
	onboarding.Phase = api.PhaseAwaitingOAuth
	onboarding.ProviderAuthorizationURL = "https://accounts.example.com/authorize?state=st-paste&client_id=c"
	if err := store.PutOnboarding(onboarding); err != nil {
		t.Fatal(err)
	}
	rec, req := confirmLinkRequest(t, http.MethodPost, "http://127.0.0.1/connect/onb-link-t")
	req.PostForm = map[string][]string{
		"state":        {"st-paste"},
		"redirect_url": {"http://127.0.0.1/oauth/callback?state=other-onboarding&code=x"},
	}
	gateway.serveConnect(rec, req)
	if rec.Code == http.StatusOK {
		t.Fatalf("foreign-state paste accepted: %d", rec.Code)
	}
	wrongBearer, wrongReq := confirmLinkRequest(t, http.MethodPost, "http://127.0.0.1/connect/onb-link-t")
	wrongReq.PostForm = map[string][]string{
		"state":        {"st-other"},
		"redirect_url": {"http://127.0.0.1/oauth/callback?state=st-paste&code=x"},
	}
	gateway.serveConnect(wrongBearer, wrongReq)
	if wrongBearer.Code != http.StatusUnauthorized {
		t.Fatalf("wrong posted state status=%d", wrongBearer.Code)
	}
	noCode, noCodeReq := confirmLinkRequest(t, http.MethodPost, "http://127.0.0.1/connect/onb-link-t")
	noCodeReq.PostForm = map[string][]string{
		"state":        {"st-paste"},
		"redirect_url": {"http://127.0.0.1/oauth/callback?state=st-paste"},
	}
	gateway.serveConnect(noCode, noCodeReq)
	if noCode.Code != http.StatusBadRequest {
		t.Fatalf("code-less paste status=%d", noCode.Code)
	}
}

// S2.5: the credential form renders the resolved recipe — trust tier,
// egress hosts, source@commit — and per-field provenance (source+rank) with
// the recipe's token_url as a "get the token" link.
func TestCredentialFormRendersRecipeProvenance(t *testing.T) {
	store := NewStore()
	grantTestControlOperations(t, store, "alice", api.ControlOperations...)
	definition := catalogReadDefinition()
	definition.Tools = append(definition.Tools, api.ToolSpec{Name: "delete_forecast", Effect: api.WriteEffect})
	if err := store.RegisterDefinition(definition); err != nil {
		t.Fatal(err)
	}
	if err := putGrant(t, store, api.OperatorGrant(api.GrantCatalogDefault, "alice", "", "")); err != nil {
		t.Fatal(err)
	}
	control := &ControlPlane{Store: store, WorkloadRoot: t.TempDir(), Now: time.Now}
	auth := aliceAuth()
	resolution := api.RecipeResolution{
		Source:   api.ArtifactSource{Repository: "https://github.com/acme/weather", CommitSHA: strings.Repeat("1", 40)},
		Launch:   api.LaunchRecipe{Artifact: "ghcr.io/acme/weather@sha256:" + strings.Repeat("2", 64), Transport: api.ContainerMCP},
		Evidence: []api.RecipeEvidence{{Source: "server.json", Detail: "declared env contract"}},
		State:    "ready",
		Connection: api.ConnectionRecipe{Fields: []api.ConnectionField{
			{Name: "ACME_API_KEY", Required: true, Secret: true, Delivery: "env", Source: "server.json", Rank: api.RankR3, TokenURL: "https://acme.example/settings/tokens"},
			{Name: "ACME_INSECURE", Required: false, Secret: false, Delivery: "env", Source: "server.json", TokenURL: "http://acme.example/not-https"},
			{Name: "ACME_REGION", Required: false, Secret: false, Delivery: "env", Source: ".env.example", Unverified: true},
		}},
		Egress: []string{"api.acme.example"},
	}
	resolution.Trust = api.TrustT1
	digest, err := store.PutRecipe(resolution)
	if err != nil {
		t.Fatal(err)
	}
	onboarding := api.Onboarding{
		Schema: api.SchemaVersion, OnboardingID: "onb-prov", PrincipalID: auth.PrincipalID, ContextID: auth.ContextID,
		RuntimeID: auth.RuntimeID, PolicyVersion: auth.PolicyVersion, Revision: 1,
		Mode: api.OnboardingCatalog, Phase: api.PhaseAwaitingCreds,
		DefinitionID: definition.DefinitionID, DefinitionVersion: definition.Version,
		Required: []api.CredentialHint{
			{Name: "ACME_API_KEY", Secret: true, Delivery: "env", Source: "server.json"},
			{Name: "ACME_INSECURE", Delivery: "env", Source: "server.json"},
			{Name: "ACME_REGION", Delivery: "env", Source: ".env.example"},
		},
		FormNonce: "prov-tok", FormExpires: time.Now().Add(time.Hour),
		RecipeDigest: digest, Recipe: &resolution,
	}
	if err := store.PutOnboarding(onboarding); err != nil {
		t.Fatal(err)
	}
	gateway := &Gateway{Control: control}
	rec, req := confirmLinkRequest(t, http.MethodGet, "http://127.0.0.1/credentials/onb-prov?t=prov-tok")
	gateway.serveCredentials(rec, req)
	body := rec.Body.String()
	if rec.Code != http.StatusOK {
		t.Fatalf("form status=%d", rec.Code)
	}
	for _, want := range []string{"T1", "api.acme.example", "acme/weather", "server.json", "https://acme.example/settings/tokens", "suggested"} {
		if !strings.Contains(body, want) {
			t.Fatalf("form missing %q: %.400s", want, body)
		}
	}
	if strings.Contains(body, `href="http://`) || strings.Contains(body, "not-https") {
		t.Fatal("non-https token_url rendered as a link")
	}
	// Write tools are listed with an explicit disabled-by-default marker so
	// the human sees the connector can mutate before saving credentials.
	if !strings.Contains(body, "delete_forecast") || !strings.Contains(body, "по умолчанию") {
		t.Fatalf("write tool not listed as disabled-by-default: %.400s", body)
	}
}

// countingSecrets wraps the real backend so the residue check can assert the
// ciphertext path was never reached on a failed probe.
type countingSecrets struct {
	credstore.Backend
	puts int32
}

func (s *countingSecrets) Put(locator, owner string, values map[string]string) error {
	s.puts++
	return s.Backend.Put(locator, owner, values)
}

// S2.6: a failed read-only probe persists nothing — no ciphertext, no
// locator, no connection — and re-renders the form carrying the entered
// values and the public probe error, so the owner can correct in place.
func TestCredentialSubmitProbeFailureLeavesNoResidue(t *testing.T) {
	store := NewStore()
	grantTestControlOperations(t, store, "alice", api.ControlOperations...)
	definition := remoteDefinition()
	if err := store.RegisterDefinition(definition); err != nil {
		t.Fatal(err)
	}
	if err := putGrant(t, store, api.OperatorGrant(api.GrantCatalogDefault, "alice", "", "")); err != nil {
		t.Fatal(err)
	}
	key, err := credstore.GenerateKey()
	if err != nil {
		t.Fatal(err)
	}
	secrets, err := credstore.Open(credstore.Options{Path: filepath.Join(t.TempDir(), "s26.enc"), Key: key})
	if err != nil {
		t.Fatal(err)
	}
	spy := &countingSecrets{Backend: secrets}
	control := &ControlPlane{Store: store, Secrets: spy, WorkloadRoot: t.TempDir(), Now: time.Now,
		AdmitWithCredentials: func(context.Context, api.ToolDefinition, map[string]string) (api.ToolDefinition, error) {
			return api.ToolDefinition{}, errors.New("tools/list: 401 invalid token")
		}}
	auth := aliceAuth()
	onboarding := api.Onboarding{
		Schema: api.SchemaVersion, OnboardingID: "onb-s26", PrincipalID: auth.PrincipalID, ContextID: auth.ContextID,
		RuntimeID: auth.RuntimeID, PolicyVersion: auth.PolicyVersion, Revision: 1,
		Mode: api.OnboardingCatalog, Phase: api.PhaseAwaitingCreds,
		DefinitionID: definition.DefinitionID, DefinitionVersion: definition.Version,
		Required:  []api.CredentialHint{{Name: "GOOGLE_TOKEN", Secret: true, Delivery: "env"}},
		FormNonce: "s26-tok", FormExpires: time.Now().Add(time.Hour),
	}
	if err := store.PutOnboarding(onboarding); err != nil {
		t.Fatal(err)
	}
	gateway := &Gateway{Control: control}
	rec, req := confirmLinkRequest(t, http.MethodPost, "http://127.0.0.1/credentials/onb-s26?t=s26-tok")
	req.PostForm = url.Values{"GOOGLE_TOKEN": {"wrong-token"}, "nonce": {"s26-tok"}}
	gateway.serveCredentials(rec, req)
	body := rec.Body.String()
	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("probe failure status=%d", rec.Code)
	}
	// The form comes back with the entered value page-side and the probe's
	// public error; the model channel sees neither.
	if !strings.Contains(body, `name="GOOGLE_TOKEN"`) || !strings.Contains(body, "wrong-token") || !strings.Contains(body, "401") {
		t.Fatalf("form not re-rendered with values+error: %.400s", body)
	}
	if spy.puts != 0 {
		t.Fatalf("ciphertext persisted despite failed probe: %d puts", spy.puts)
	}
	stored, err := store.onboarding("onb-s26")
	if err != nil {
		t.Fatal(err)
	}
	if stored.Locator != "" || stored.Phase != api.PhaseAwaitingCreds || stored.BindingID != "" {
		t.Fatalf("residue after probe failure: locator=%q phase=%s binding=%q", stored.Locator, stored.Phase, stored.BindingID)
	}
}

// S2.6: the admission submit already probes; the pre-persist check must not
// re-probe the same values a second time.
func TestCredentialSubmitAdmissionProbesOnce(t *testing.T) {
	store := NewStore()
	grantTestControlOperations(t, store, "alice", api.ControlOperations...)
	key, err := credstore.GenerateKey()
	if err != nil {
		t.Fatal(err)
	}
	secrets, err := credstore.Open(credstore.Options{Path: filepath.Join(t.TempDir(), "s26b.enc"), Key: key})
	if err != nil {
		t.Fatal(err)
	}
	definition := remoteDefinition()
	probes := 0
	control := &ControlPlane{Store: store, Secrets: secrets, WorkloadRoot: t.TempDir(), Now: time.Now,
		AdmitWithCredentials: func(context.Context, api.ToolDefinition, map[string]string) (api.ToolDefinition, error) {
			probes++
			return definition, nil
		}}
	auth := aliceAuth()
	draft := definition
	onboarding := api.Onboarding{
		Schema: api.SchemaVersion, OnboardingID: "onb-s26b", PrincipalID: auth.PrincipalID, ContextID: auth.ContextID,
		RuntimeID: auth.RuntimeID, PolicyVersion: auth.PolicyVersion, Revision: 1,
		Mode: api.OnboardingRemote, Phase: api.PhaseAwaitingCreds, AdmissionPending: true,
		DefinitionID: definition.DefinitionID, DefinitionVersion: definition.Version,
		Required:  []api.CredentialHint{{Name: "GOOGLE_TOKEN", Secret: true, Delivery: "env"}},
		FormNonce: "s26b-tok", FormExpires: time.Now().Add(time.Hour),
		Definition: &draft,
	}
	if err := store.PutOnboarding(onboarding); err != nil {
		t.Fatal(err)
	}
	if err := control.SubmitCredentials("onb-s26b", "s26b-tok", map[string]string{"GOOGLE_TOKEN": "ok"}); err != nil {
		t.Fatal(err)
	}
	if probes != 1 {
		t.Fatalf("admission submit probed %d times, want 1", probes)
	}
}

// S2.8 property: no agent-facing result — a control-op body or its error —
// may carry human-link material. Every secret-bearing onboarding field is
// seeded with a unique sentinel across every phase; any sentinel found in
// any op's marshalled output is a leak.
func TestControlOpsCarryNoLinkMaterial(t *testing.T) {
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
	var sentinels []string
	mark := func(name string) string {
		value := "SENT-" + name + "-9f27c1"
		sentinels = append(sentinels, value)
		return value
	}
	fill := func(o *api.Onboarding) {
		o.FormNonce = mark("form")
		o.FormExpires = time.Now().Add(time.Hour)
		o.ConfirmationNonce = mark("confirm")
		o.ConfirmationExpires = time.Now().Add(time.Hour)
		o.RemoveNonce = mark("remove")
		o.RemoveExpires = time.Now().Add(time.Hour)
		o.Locator = mark("locator")
		o.CredentialOwner = mark("owner")
		o.IdempotencyKey = mark("idem")
		o.BrokerAuthorizationURL = "https://broker.example/auth?code=" + mark("burl")
		o.ProviderAuthorizationURL = "https://as.example/authorize?state=" + mark("oauthstate")
		o.WizardNonce = mark("wizard")
		o.WizardExpires = time.Now().Add(time.Hour)
		o.DeviceCode = mark("devcode")
		o.DeviceUserCode = mark("usercode")
		o.DeviceVerification = "https://verify.example/" + mark("device")
	}
	phases := []string{api.PhasePreparing, api.PhaseReview, api.PhaseAwaitingCreds, api.PhaseAwaitingConfirm, api.PhaseAwaitingOAuth, api.PhaseConfirmed, api.PhaseEnabled, api.PhaseDisabled, api.PhaseRevoked, api.PhaseFailed}
	ids := map[string]string{}
	for _, phase := range phases {
		id := "onb-prop-" + strings.ReplaceAll(phase, "-", "")
		onboarding := api.Onboarding{
			Schema: api.SchemaVersion, OnboardingID: id, PrincipalID: auth.PrincipalID, ContextID: auth.ContextID,
			RuntimeID: auth.RuntimeID, PolicyVersion: auth.PolicyVersion, Revision: 1,
			Mode: api.OnboardingCatalog, Phase: phase,
			DefinitionID: definition.DefinitionID, DefinitionVersion: definition.Version,
		}
		fill(&onboarding)
		if err := store.PutOnboarding(onboarding); err != nil {
			t.Fatalf("%s: %v", phase, err)
		}
		ids[phase] = id
	}
	// Every read-shaped control op over every phase; mutations that mint link
	// material (remove preview) run too — their output must not echo it.
	ops := []struct {
		name string
		args func(id string) map[string]any
	}{
		{"status", func(id string) map[string]any { return map[string]any{"onboarding_id": id} }},
		{"required_credentials", func(id string) map[string]any { return map[string]any{"onboarding_id": id} }},
		{"diagnostics", func(id string) map[string]any { return map[string]any{"onboarding_id": id} }},
		{"remove", func(id string) map[string]any { return map[string]any{"onboarding_id": id} }},
	}
	for _, phase := range phases {
		for _, op := range ops {
			body, err := control.Invoke(context.Background(), auth, op.name, op.args(ids[phase]))
			encoded, _ := json.Marshal(body)
			output := string(encoded)
			if err != nil {
				output += "|" + err.Error()
			}
			for _, sentinel := range sentinels {
				if strings.Contains(output, sentinel) {
					t.Fatalf("%s on %s leaked %q: %.400s", op.name, phase, sentinel, output)
				}
			}
			if strings.Contains(output, "?t=") || strings.Contains(output, "?k=") {
				t.Fatalf("%s on %s emitted a link token: %.400s", op.name, phase, output)
			}
		}
	}
	// confirm consumes the nonce as input — the result must not echo it back.
	stored, err := store.onboarding(ids[api.PhaseAwaitingConfirm])
	if err != nil {
		t.Fatal(err)
	}
	body, err := control.Invoke(context.Background(), auth, "confirm", map[string]any{"onboarding_id": stored.OnboardingID, "nonce": stored.ConfirmationNonce})
	encoded, _ := json.Marshal(body)
	output := string(encoded)
	if err != nil {
		output += "|" + err.Error()
	}
	for _, sentinel := range sentinels {
		if strings.Contains(output, sentinel) {
			t.Fatalf("confirm leaked %q: %.400s", sentinel, output)
		}
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
