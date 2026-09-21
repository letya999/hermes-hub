package toolhub

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/letya999/hermes-hub/internal/credstore"
	"github.com/letya999/hermes-hub/internal/identity"
	"github.com/letya999/hermes-hub/internal/oauth"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

const testSecret = "scan-secret-ORANGE-NINE-7721"

func TestProtectedCredentialElicitationAndOAuth(t *testing.T) {
	key, err := credstore.GenerateKey()
	if err != nil {
		t.Fatal(err)
	}
	secrets, err := credstore.Open(credstore.Options{Path: filepath.Join(t.TempDir(), "store.enc"), Key: key})
	if err != nil {
		t.Fatal(err)
	}
	store := NewStore()
	if err := store.RegisterDefinition(remoteDefinition()); err != nil {
		t.Fatal(err)
	}
	auth := aliceAuth()
	if err := store.PutGrant(OperatorGrant(GrantDefinition, "alice", "google-work", "1.0.0")); err != nil {
		t.Fatal(err)
	}
	var calls atomic.Int32
	var lastArgs map[string]any
	control := &ControlPlane{Store: store, Secrets: secrets, WorkloadRoot: t.TempDir(), Now: time.Now, ConfirmationTTL: time.Minute}
	gateway := &Gateway{
		Store:  store,
		Tokens: map[string]identity.Envelope{aliceToken: auth, bobToken: bobAuth()},
		Backend: backendFunc(func(_ context.Context, _ EffectiveBinding, _ ToolSpec, args map[string]any) (BackendResult, error) {
			calls.Add(1)
			lastArgs = args
			return BackendResult{Text: "ok"}, nil
		}),
		Control: control,
		Injector: func(_ context.Context, effective EffectiveBinding) (CredentialInjection, error) {
			env, err := DecryptAuthorized(secrets, effective)
			if err != nil {
				return CredentialInjection{}, err
			}
			return CredentialInjection{Environment: env, Cleanup: func() error { return nil }}, nil
		},
	}
	handler, err := gateway.Handler()
	if err != nil {
		t.Fatal(err)
	}
	server := httptestNew(t, handler)
	control.FormOrigin = server.URL
	alice := mcpConnect(t, server.URL, aliceToken)
	prepared, err := callControl(t, alice, "prepare_source", map[string]any{"definition_id": "google-work", "version": "1.0.0", "request_key": "cred-1"})
	if err != nil {
		t.Fatal(err)
	}
	required, err := callControl(t, alice, "required_credentials", map[string]any{"onboarding_id": prepared["onboarding_id"]})
	if err != nil {
		t.Fatal(err)
	}
	requiredJSON, _ := json.Marshal(required)
	if strings.Contains(string(requiredJSON), testSecret) || strings.Contains(string(requiredJSON), "local://") {
		t.Fatalf("required_credentials leaked secret: %s", requiredJSON)
	}
	hints, _ := required["credentials"].([]any)
	if len(hints) != 1 {
		t.Fatalf("hints=%v", required["credentials"])
	}
	hint, _ := hints[0].(map[string]any)
	if hint["name"] != "GOOGLE_TOKEN" || hint["type"] != "secret" {
		t.Fatalf("hint=%v", hint)
	}
	if _, err := callControl(t, alice, "confirm", map[string]any{"onboarding_id": prepared["onboarding_id"], "GOOGLE_TOKEN": testSecret}); err == nil {
		t.Fatal("chat KEY=value credential argument accepted")
	}
	formPath, _ := required["form_path"].(string)
	if formPath == "" {
		t.Fatal("missing form_path")
	}
	get, err := http.Get(server.URL + formPath)
	if err != nil {
		t.Fatal(err)
	}
	page, _ := io.ReadAll(get.Body)
	get.Body.Close()
	if get.StatusCode != 200 || strings.Contains(string(page), testSecret) {
		t.Fatalf("form get=%d body=%s", get.StatusCode, page)
	}
	form := url.Values{}
	form.Set("nonce", nonceFromPath(formPath))
	form.Set("GOOGLE_TOKEN", testSecret)
	post, err := http.Post(server.URL+formPath, "application/x-www-form-urlencoded", strings.NewReader(form.Encode()))
	if err != nil {
		t.Fatal(err)
	}
	posted, _ := io.ReadAll(post.Body)
	post.Body.Close()
	if post.StatusCode != 200 || strings.Contains(string(posted), testSecret) {
		t.Fatalf("form post=%d body=%s", post.StatusCode, posted)
	}
	info, err := secrets.Info(mustOnboarding(t, store, prepared["onboarding_id"].(string)).Locator, "alice")
	if err != nil || info.Status != credstore.StatusActive {
		t.Fatalf("ciphertext info=%+v err=%v", info, err)
	}
	status, err := callControl(t, alice, "status", map[string]any{"onboarding_id": prepared["onboarding_id"]})
	if err != nil {
		t.Fatal(err)
	}
	if status["phase"] != PhaseEnabled {
		t.Fatalf("credential form did not complete authorization: %v", status)
	}
	if err := control.SubmitCredentials(prepared["onboarding_id"].(string), nonceFromPath(formPath), map[string]string{"GOOGLE_TOKEN": testSecret}); err == nil {
		t.Fatal("one-time credential form replay accepted")
	}
	status, err = callControl(t, alice, "status", map[string]any{"onboarding_id": prepared["onboarding_id"]})
	if err != nil || status["phase"] != PhaseEnabled {
		t.Fatalf("confirmed onboarding was not enabled: status=%v err=%v", status, err)
	}
	projected, _ := status["projected_tools"].([]any)
	if len(projected) == 0 || projected[0] != ProjectedToolName("google-work", "1.0.0", "search") {
		t.Fatalf("enabled status omitted exact projected tools: %v", status)
	}
	if err := control.finishAuthorization(context.Background(), "missing-onboarding"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("missing onboarding finalization error=%v", err)
	}
	badAuth := aliceAuth()
	badAuth.PolicyVersion = ""
	if _, err := control.Invoke(context.Background(), badAuth, "status", map[string]any{}); !errors.Is(err, ErrUnauthorized) {
		t.Fatalf("invalid control identity accepted: %v", err)
	}
	name := ProjectedToolName("google-work", "1.0.0", "search")
	if _, err := alice.CallTool(context.Background(), &mcp.CallToolParams{Name: name, Arguments: map[string]any{"query": "ok"}}); err != nil {
		t.Fatal(err)
	}
	if lastArgs["GOOGLE_TOKEN"] != nil {
		t.Fatal("secret reached tool arguments")
	}
	scan := strings.Join([]string{string(requiredJSON), string(posted), string(page)}, "\n")
	if strings.Contains(scan, testSecret) {
		t.Fatal("test secret appeared in HTTP/control surfaces")
	}
	expired := control
	expired.Now = func() time.Time { return time.Now().UTC().Add(2 * time.Hour) }
	if _, err := expired.Invoke(context.Background(), auth, "confirm", map[string]any{"onboarding_id": prepared["onboarding_id"], "nonce": status["nonce"]}); !errors.Is(err, ErrUnauthorized) {
		t.Fatalf("expired confirmation: %v", err)
	}
	if _, err := callControl(t, alice, "confirm", map[string]any{"onboarding_id": prepared["onboarding_id"], "nonce": status["nonce"]}); err == nil {
		t.Fatal("replayed nonce accepted")
	}

	fixture := oauth.NewFixture()
	t.Cleanup(fixture.Close)
	broker := oauth.NewBroker(secrets, []string{server.URL + "/oauth/callback?onboarding_id=" + prepared["onboarding_id"].(string)})
	broker.HTTP = fixture.Server.Client()
	broker.Providers["fixture"] = fixture.Provider()
	control.OAuth = broker
	redirect := server.URL + "/oauth/callback?onboarding_id=" + prepared["onboarding_id"].(string)
	start, err := control.StartOAuth(auth, prepared["onboarding_id"].(string), redirect)
	if err != nil {
		t.Fatal(err)
	}
	if start.State == "" || !strings.Contains(start.AuthorizeURL, "code_challenge") {
		t.Fatalf("oauth start=%+v", start)
	}
	client := &http.Client{CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	resp, err := client.Get(start.AuthorizeURL)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	location := resp.Header.Get("Location")
	if location == "" {
		t.Fatal("oauth authorize missing redirect")
	}
	cb, err := client.Get(location)
	if err != nil {
		t.Fatal(err)
	}
	cbBody, _ := io.ReadAll(cb.Body)
	cb.Body.Close()
	if strings.Contains(string(cbBody), testSecret) {
		t.Fatal("oauth callback leaked test secret")
	}
}

func TestExpiredCredentialFormIsRenewedOnResume(t *testing.T) {
	store := NewStore()
	if err := store.RegisterDefinition(remoteDefinition()); err != nil {
		t.Fatal(err)
	}
	if err := store.PutGrant(OperatorGrant(GrantDefinition, "alice", "google-work", "1.0.0")); err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC()
	control := &ControlPlane{Store: store, Now: func() time.Time { return now }, ConfirmationTTL: time.Minute}
	auth := aliceAuth()
	args := map[string]any{"definition_id": "google-work", "version": "1.0.0", "request_key": "expired-form"}
	prepared, err := control.Invoke(context.Background(), auth, "prepare_source", args)
	if err != nil {
		t.Fatal(err)
	}
	id := prepared["onboarding_id"].(string)
	first := mustOnboarding(t, store, id)
	oldNonce := first.FormNonce
	now = now.Add(2 * time.Minute)
	if _, err := control.Invoke(context.Background(), auth, "prepare_source", args); err != nil {
		t.Fatal(err)
	}
	resumed := mustOnboarding(t, store, id)
	if resumed.FormNonce == oldNonce || !now.Before(resumed.FormExpires) {
		t.Fatalf("expired form was not renewed: old=%q new=%q expires=%s", oldNonce, resumed.FormNonce, resumed.FormExpires)
	}
	now = now.Add(2 * time.Minute)
	required, err := control.Invoke(context.Background(), auth, "required_credentials", map[string]any{"onboarding_id": id})
	if err != nil {
		t.Fatal(err)
	}
	formURL, _ := required["form_url"].(string)
	if formURL == "" || nonceFromPath(formURL) == resumed.FormNonce || !strings.Contains(formURL, id) {
		t.Fatalf("expired required_credentials form was not renewed: %q", formURL)
	}
}

func TestExpiredConfirmationRejectsUnusedNonce(t *testing.T) {
	store := NewStore()
	if err := store.RegisterDefinition(catalogReadDefinition()); err != nil {
		t.Fatal(err)
	}
	if err := store.PutGrant(OperatorGrant(GrantCatalogDefault, "alice", "", "")); err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC()
	control := &ControlPlane{Store: store, Now: func() time.Time { return now }, ConfirmationTTL: time.Minute}
	auth := aliceAuth()
	prepared, err := control.Invoke(context.Background(), auth, "prepare_source", map[string]any{"definition_id": "catalog-read", "version": "1.0.0", "request_key": "expire-1"})
	if err != nil {
		t.Fatal(err)
	}
	status, err := control.Invoke(context.Background(), auth, "status", map[string]any{"onboarding_id": prepared["onboarding_id"]})
	if err != nil {
		t.Fatal(err)
	}
	nonce, _ := status["nonce"].(string)
	if nonce == "" {
		t.Fatal("missing confirmation nonce")
	}
	onboarding, err := store.onboarding(prepared["onboarding_id"].(string))
	if err != nil || onboarding.ConfirmationUsed {
		t.Fatalf("nonce already used before expiry check: %+v err=%v", onboarding, err)
	}
	control.Now = func() time.Time { return now.Add(2 * time.Hour) }
	if _, err := control.Invoke(context.Background(), auth, "confirm", map[string]any{"onboarding_id": prepared["onboarding_id"], "nonce": nonce}); !errors.Is(err, ErrUnauthorized) || !strings.Contains(err.Error(), "expired") {
		t.Fatalf("unused expired nonce: %v", err)
	}
	onboarding, err = store.onboarding(prepared["onboarding_id"].(string))
	if err != nil || onboarding.ConfirmationUsed {
		t.Fatalf("expiry marked nonce used: %+v err=%v", onboarding, err)
	}
	resumed, err := control.Invoke(context.Background(), auth, "status", map[string]any{"onboarding_id": prepared["onboarding_id"]})
	if err != nil || resumed["nonce"] == "" || resumed["nonce"] == nonce {
		t.Fatalf("expired confirmation was not renewed: status=%v err=%v", resumed, err)
	}
}

func httptestNew(t *testing.T, handler http.Handler) *httptest.Server {
	t.Helper()
	server := httptest.NewServer(handler)
	t.Cleanup(server.Close)
	return server
}

func mcpConnect(t *testing.T, base, token string) *mcp.ClientSession {
	t.Helper()
	client := mcp.NewClient(&mcp.Implementation{Name: "elicitation-test", Version: "1"}, nil)
	session, err := client.Connect(context.Background(), &mcp.StreamableClientTransport{Endpoint: base + DefaultEndpointPath, HTTPClient: &http.Client{Transport: testBearerTransport{base: http.DefaultTransport, token: token}}}, nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = session.Close() })
	return session
}

func nonceFromPath(path string) string {
	u, _ := url.Parse(path)
	return u.Query().Get("nonce")
}

func mustOnboarding(t *testing.T, store *Store, id string) Onboarding {
	t.Helper()
	onboarding, err := store.onboarding(id)
	if err != nil {
		t.Fatal(err)
	}
	return onboarding
}

func TestRotateRevokeCutsOpenSession(t *testing.T) {
	key, err := credstore.GenerateKey()
	if err != nil {
		t.Fatal(err)
	}
	secrets, err := credstore.Open(credstore.Options{Path: filepath.Join(t.TempDir(), "c.enc"), Key: key})
	if err != nil {
		t.Fatal(err)
	}
	store, auth, binding := seededStore(t)
	if err := store.PutWorkloadInstance(runningWorkload(t, binding)); err != nil {
		t.Fatal(err)
	}
	stops := &stopRecorder{}
	store.Stopper = stops
	var calls atomic.Int32
	control := &ControlPlane{Store: store, Secrets: secrets, WorkloadRoot: t.TempDir()}
	if err := store.PutGrant(OperatorGrant(GrantCatalogDefault, "alice", "", "")); err != nil {
		t.Fatal(err)
	}
	onboarding := Onboarding{Schema: SchemaVersion, OnboardingID: "onboard-rotate1", PrincipalID: auth.PrincipalID, ContextID: auth.ContextID, RuntimeID: auth.RuntimeID, PolicyVersion: auth.PolicyVersion, Mode: OnboardingCatalog, DefinitionID: "google-work", DefinitionVersion: "1.0.0", Phase: PhaseEnabled, BindingID: binding.ToolBindingID, ConnectionID: binding.ConnectionID, Locator: "local://alice/google/1", Revision: 1, CreatedAt: time.Now().UTC()}
	if err := store.PutOnboarding(onboarding); err != nil {
		t.Fatal(err)
	}
	gateway := &Gateway{
		Store: store, Control: control, Tokens: map[string]identity.Envelope{aliceToken: auth},
		Backend: backendFunc(func(context.Context, EffectiveBinding, ToolSpec, map[string]any) (BackendResult, error) {
			calls.Add(1)
			return BackendResult{Text: "ok"}, nil
		}),
	}
	handler, err := gateway.Handler()
	if err != nil {
		t.Fatal(err)
	}
	server := httptestNew(t, handler)
	alice := mcpConnect(t, server.URL, aliceToken)
	name := ProjectedToolName("google-work", "1.0.0", "search")
	if _, err := alice.CallTool(context.Background(), &mcp.CallToolParams{Name: name, Arguments: map[string]any{"query": "ok"}}); err != nil {
		t.Fatal(err)
	}
	if _, err := control.Rotate(auth, map[string]any{"onboarding_id": onboarding.OnboardingID}); err != nil {
		t.Fatal(err)
	}
	if _, err := alice.CallTool(context.Background(), &mcp.CallToolParams{Name: name, Arguments: map[string]any{"query": "after"}}); err == nil {
		t.Fatal("open session survived rotate")
	}
	if calls.Load() != 1 {
		t.Fatalf("backend after rotate calls=%d", calls.Load())
	}
	if len(stops.ids) == 0 {
		t.Fatal("rotate did not stop workload")
	}
}
