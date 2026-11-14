package toolhub

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	brokerv1 "github.com/letya999/credential-broker/api/v1"
	"github.com/letya999/hermes-hub/internal/credentialbroker"
	"github.com/letya999/hermes-hub/internal/credstore"
	"github.com/letya999/hermes-hub/internal/toolhub/api"
)

// CHG-0145: minting a credential form also arms the wizard nonce — the first
// link a user ever receives is the stable /connect/<id>?k= URL, and re-mints
// after expiry keep the same wizard nonce alive instead of stranding it.
func TestRefreshCredentialFormArmsWizard(t *testing.T) {
	store := NewStore()
	control := &ControlPlane{Store: store, WorkloadRoot: t.TempDir(), Now: time.Now}
	auth := aliceAuth()
	onboarding := api.Onboarding{
		Schema: api.SchemaVersion, OnboardingID: "onb-wiz", PrincipalID: auth.PrincipalID, ContextID: auth.ContextID,
		RuntimeID: auth.RuntimeID, PolicyVersion: auth.PolicyVersion, Revision: 1,
		Mode:  api.OnboardingCatalog,
		Stage: api.StagePending,
		Gate:  &api.Gate{Kind: api.GateCredentials, Nonce: "", Expires: time.Time{}}, DefinitionID: "fixture-def", DefinitionVersion: "1.0.0"}
	if err := store.PutOnboarding(onboarding); err != nil {
		t.Fatal(err)
	}
	if err := control.refreshCredentialForm(t.Context(), auth, &onboarding); err != nil {
		t.Fatal(err)
	}
	stored, err := store.onboarding("onb-wiz")
	if err != nil {
		t.Fatal(err)
	}
	if stored.WizardNonce == "" || stored.WizardExpires.IsZero() || !stored.WizardExpires.After(time.Now()) {
		t.Fatalf("wizard not armed: nonce=%q expires=%v", stored.WizardNonce, stored.WizardExpires)
	}
	// A re-mint (expired form) must keep the wizard nonce — the link in the
	// channel stays the one the user already has.
	stored.ArmGate(api.GateCredentials, "", time.Now().Add(-time.Minute))
	stored.Revision++
	if err := store.PutOnboarding(stored); err != nil {
		t.Fatal(err)
	}
	if err := control.refreshCredentialForm(t.Context(), auth, &stored); err != nil {
		t.Fatal(err)
	}
	latest, err := store.onboarding("onb-wiz")
	if err != nil {
		t.Fatal(err)
	}
	if latest.WizardNonce != stored.WizardNonce {
		t.Fatalf("wizard nonce rotated on re-mint: %q -> %q", stored.WizardNonce, latest.WizardNonce)
	}
	if latest.WizardExpires.Before(stored.WizardExpires) {
		t.Fatalf("wizard expiry did not renew: %v -> %v", stored.WizardExpires, latest.WizardExpires)
	}
}

// A fresh broker request arms the wizard: the channel notice that follows
// carries the stable /connect/<id>?k= link, not a bare broker hop.
func TestBrokerRequestArmsWizard(t *testing.T) {
	_, private, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	keyPath := filepath.Join(t.TempDir(), "toolhub.private")
	if err := os.WriteFile(keyPath, private, 0600); err != nil {
		t.Fatal(err)
	}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if r.Method == http.MethodPost && r.URL.Path == "/v1/requests" {
			w.WriteHeader(http.StatusCreated)
			_ = json.NewEncoder(w).Encode(brokerv1.Request{ID: "r1", Status: "pending", AuthorizationURL: "https://broker/connect/r1", ContractID: "c1", ContractRevision: 1})
			return
		}
		if r.Method == http.MethodGet && strings.HasPrefix(r.URL.Path, "/v1/requests/") {
			_ = json.NewEncoder(w).Encode(brokerv1.Request{ID: "r1", Status: "pending", AuthorizationURL: "https://broker/connect/r1", ContractID: "c1", ContractRevision: 1})
			return
		}
		http.NotFound(w, r)
	}))
	defer server.Close()

	store := NewStore()
	control := &ControlPlane{Store: store, Broker: &credentialbroker.Config{URL: server.URL, KeyFile: keyPath, KeyID: "toolhub", Issuer: "hermes-toolhub"}, Now: time.Now}
	definition := remoteDefinition()
	definition.Credentials = []api.CredentialInput{{Name: "TOKEN", Required: true}}
	definition.CredentialContractID = "c1"
	definition.CredentialContractRevision = 1
	definition.CredentialContractEnv = map[string]string{"TOKEN": "TOKEN"}
	onboarding := api.Onboarding{Schema: api.SchemaVersion, OnboardingID: "onb-wiz-broker", PrincipalID: "alice", ContextID: "alice", RuntimeID: "runtime", Mode: api.OnboardingSelfInstall, Stage: api.StagePending, Gate: &api.Gate{Kind: api.GateCredentials, Nonce: "", Expires: time.Time{}}, DefinitionID: definition.DefinitionID, DefinitionVersion: definition.Version, PolicyVersion: "policy-1"}

	if err := control.ensureBrokerRequest(context.Background(), aliceAuth(), &onboarding, definition); err != nil {
		t.Fatal(err)
	}
	if onboarding.WizardNonce == "" || onboarding.WizardExpires.IsZero() {
		t.Fatal("broker request did not arm the wizard")
	}
}

// A plain newOnboarding with required credentials mints the form nonce and
// arms the wizard at the same instant — the very first notice carries the
// stable link, not a bare /credentials/ hop.
func TestNewOnboardingArmsWizard(t *testing.T) {
	store := NewStore()
	control := &ControlPlane{Store: store, Now: time.Now}
	definition := remoteDefinition()
	definition.Credentials = []api.CredentialInput{{Name: "TOKEN", Required: true}}
	onboarding, _, err := control.newOnboarding(aliceAuth(), api.OnboardingCatalog, "wiz-arm", definition, "", "", false)
	if err != nil {
		t.Fatal(err)
	}
	if onboarding.WizardNonce == "" || onboarding.WizardExpires.IsZero() {
		t.Fatal("newOnboarding did not arm the wizard")
	}
}

// The single link must outlive a coffee break: the owner returns long after
// the 10-minute step TTL, and the page re-arms the step instead of a 401.
func TestWizardOutlivesStepTTL(t *testing.T) {
	control := &ControlPlane{Store: NewStore(), Now: time.Now}
	var onboarding api.Onboarding
	control.armWizard(&onboarding)
	if onboarding.WizardExpires.Before(time.Now().Add(12 * time.Hour)) {
		t.Fatalf("wizard dies with the step TTL: %v", onboarding.WizardExpires)
	}
}

// A confirm-only onboarding (no required credentials) still gets the wizard:
// the /connect/<id>?k= link must work when the first phase is confirmation.
func TestNewOnboardingArmsWizardConfirmOnly(t *testing.T) {
	store := NewStore()
	control := &ControlPlane{Store: store, Now: time.Now}
	definition := remoteDefinition()
	definition.Credentials = []api.CredentialInput{{Name: "OPT", Required: false}}
	onboarding, _, err := control.newOnboarding(aliceAuth(), api.OnboardingCatalog, "wiz-confirm", definition, "", "", false)
	if err != nil {
		t.Fatal(err)
	}
	if !onboarding.Awaiting(api.GateConfirmSetup) {
		t.Fatalf("phase=%s", onboarding.NoticePhase())
	}
	if onboarding.WizardNonce == "" || onboarding.WizardExpires.IsZero() {
		t.Fatal("confirm-only onboarding did not arm the wizard")
	}
}

// serveConnect with ?k=<wizard nonce> renders the phase-appropriate step:
// awaiting-credentials renders the credential form under the same stable URL.
func TestConnectWizardRendersCredentialPhase(t *testing.T) {
	store := NewStore()
	control := &ControlPlane{Store: store, Now: time.Now}
	auth := aliceAuth()
	onboarding := api.Onboarding{
		Schema: api.SchemaVersion, OnboardingID: "onb-wiz-creds", PrincipalID: auth.PrincipalID, ContextID: auth.ContextID,
		RuntimeID: auth.RuntimeID, PolicyVersion: auth.PolicyVersion, Revision: 1,
		Mode:         api.OnboardingCatalog,
		Stage:        api.StagePending,
		Gate:         &api.Gate{Kind: api.GateCredentials, Nonce: "form-nonce", Expires: time.Now().Add(time.Hour)},
		DefinitionID: "fixture-def", DefinitionVersion: "1.0.0",
		WizardNonce: "wiz-key", WizardExpires: time.Now().Add(time.Hour)}
	if err := store.PutOnboarding(onboarding); err != nil {
		t.Fatal(err)
	}
	gateway := &Gateway{Control: control}
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "http://127.0.0.1/connect/onb-wiz-creds?k=wiz-key", nil)
	req.Host = "127.0.0.1"
	gateway.serveConnect(rec, req)
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), "<form") {
		t.Fatalf("wizard credential page status=%d", rec.Code)
	}
	bad := httptest.NewRecorder()
	badReq := httptest.NewRequest(http.MethodGet, "http://127.0.0.1/connect/onb-wiz-creds?k=forged", nil)
	badReq.Host = "127.0.0.1"
	gateway.serveConnect(bad, badReq)
	if bad.Code != http.StatusGone {
		t.Fatalf("forged wizard key status=%d", bad.Code)
	}
}

// An expired wizard nonce is dead: the stable link obeys its TTL like every
// other one-time capability.
func TestConnectWizardExpiredKeyRejected(t *testing.T) {
	store := NewStore()
	control := &ControlPlane{Store: store, Now: time.Now}
	auth := aliceAuth()
	onboarding := api.Onboarding{
		Schema: api.SchemaVersion, OnboardingID: "onb-wiz-exp", PrincipalID: auth.PrincipalID, ContextID: auth.ContextID,
		RuntimeID: auth.RuntimeID, PolicyVersion: auth.PolicyVersion, Revision: 1,
		Mode:        api.OnboardingCatalog,
		Stage:       api.StagePending,
		Gate:        &api.Gate{Kind: api.GateCredentials, Nonce: "f", Expires: time.Now().Add(time.Hour)},
		WizardNonce: "wiz-key", WizardExpires: time.Now().Add(-time.Minute)}
	if err := store.PutOnboarding(onboarding); err != nil {
		t.Fatal(err)
	}
	gateway := &Gateway{Control: control}
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "http://127.0.0.1/connect/onb-wiz-exp?k=wiz-key", nil)
	req.Host = "127.0.0.1"
	gateway.serveConnect(rec, req)
	if rec.Code != http.StatusGone {
		t.Fatalf("expired wizard key status=%d", rec.Code)
	}
}

// The wizard link accepts the credential POST itself: the form lives and
// submits on /connect/<id>?k=, so the user never sees a second URL. A bad
// submission reaches the same validation the /credentials/ page uses.
func TestConnectWizardAcceptsCredentialPost(t *testing.T) {
	store := NewStore()
	control := &ControlPlane{Store: store, Now: time.Now}
	auth := aliceAuth()
	onboarding := api.Onboarding{
		Schema: api.SchemaVersion, OnboardingID: "onb-wiz-post", PrincipalID: auth.PrincipalID, ContextID: auth.ContextID,
		RuntimeID: auth.RuntimeID, PolicyVersion: auth.PolicyVersion, Revision: 1,
		Mode:         api.OnboardingCatalog,
		Stage:        api.StagePending,
		Gate:         &api.Gate{Kind: api.GateCredentials, Nonce: "form-nonce", Expires: time.Now().Add(time.Hour)},
		DefinitionID: "fixture-def", DefinitionVersion: "1.0.0",
		Required:    []api.CredentialHint{{Name: "API_TOKEN", Type: "secret", Secret: true}},
		WizardNonce: "wiz-key", WizardExpires: time.Now().Add(time.Hour)}
	if err := store.PutOnboarding(onboarding); err != nil {
		t.Fatal(err)
	}
	gateway := &Gateway{Control: control}
	post := httptest.NewRequest(http.MethodPost, "http://127.0.0.1/connect/onb-wiz-post?k=wiz-key",
		strings.NewReader("nonce=form-nonce"))
	post.Host = "127.0.0.1"
	post.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	rec := httptest.NewRecorder()
	gateway.serveConnect(rec, post)
	// Required field missing → the credential pipeline's own rejection, not
	// a wizard-level 401.
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("wizard credential post status=%d body=%s", rec.Code, rec.Body.String())
	}
}

// The same wizard link serves the confirm step: GET renders the button and
// The wizard shows the action; only the verified channel callback grants consent.
func TestConnectWizardConfirmStep(t *testing.T) {
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
		Schema: api.SchemaVersion, OnboardingID: "onb-wiz-confirm", PrincipalID: auth.PrincipalID, ContextID: auth.ContextID,
		RuntimeID: auth.RuntimeID, PolicyVersion: auth.PolicyVersion, Revision: 1,
		Mode:         api.OnboardingCatalog,
		Stage:        api.StagePending,
		Gate:         &api.Gate{Kind: api.GateConfirmSetup, Nonce: "confirm-nonce", Expires: time.Now().Add(time.Hour)},
		DefinitionID: definition.DefinitionID, DefinitionVersion: definition.Version,
		WizardNonce: "wiz-key", WizardExpires: time.Now().Add(time.Hour)}
	if err := store.PutOnboarding(onboarding); err != nil {
		t.Fatal(err)
	}
	gateway := &Gateway{Control: control}
	rec := httptest.NewRecorder()
	get := httptest.NewRequest(http.MethodGet, "http://127.0.0.1/connect/onb-wiz-confirm?k=wiz-key", nil)
	get.Host = "127.0.0.1"
	gateway.serveConnect(rec, get)
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), "в исходном чате") || strings.Contains(rec.Body.String(), "<form") {
		t.Fatalf("wizard confirm page status=%d", rec.Code)
	}
	post := httptest.NewRecorder()
	postReq := httptest.NewRequest(http.MethodPost, "http://127.0.0.1/connect/onb-wiz-confirm?k=wiz-key", nil)
	postReq.Host = "127.0.0.1"
	gateway.serveConnect(post, postReq)
	if post.Code != http.StatusForbidden {
		t.Fatalf("wizard confirm post=%d location=%q", post.Code, post.Header().Get("Location"))
	}
	if pending := mustOnboarding(t, store, onboarding.OnboardingID); pending.ConfirmationUsed || pending.BindingID != "" {
		t.Fatal("wizard bearer granted consent")
	}
	finishAsHuman(t, control, onboarding.OnboardingID)
	latest, err := store.onboarding("onb-wiz-confirm")
	if err != nil || (!latest.ConfirmationUsed && latest.Stage != api.StageLive && !latest.Awaiting(api.GateOAuth)) {
		t.Fatalf("wizard confirm did not advance: phase=%v err=%v", latest.NoticePhase(), err)
	}
}

// A wizard link still live when allow_writes arms a confirm step must run the
// writes path, not a fresh authorization — same branching serveConfirm uses.
func TestConnectWizardConfirmPendingWrites(t *testing.T) {
	store := NewStore()
	grantTestControlOperations(t, store, "alice", "confirm")
	control := &ControlPlane{Store: store, WorkloadRoot: t.TempDir(), Now: time.Now}
	auth := aliceAuth()
	onboarding := api.Onboarding{
		Schema: api.SchemaVersion, OnboardingID: "onb-wiz-writes", PrincipalID: auth.PrincipalID, ContextID: auth.ContextID,
		RuntimeID: auth.RuntimeID, PolicyVersion: auth.PolicyVersion, Revision: 1,
		Mode:  api.OnboardingCatalog,
		Stage: api.StagePending,
		Gate:  &api.Gate{Kind: api.GateConfirmSetup, Nonce: "confirm-nonce", Expires: time.Now().Add(time.Hour)}, PendingWrites: true,
		DefinitionID: "fixture-def", DefinitionVersion: "1.0.0",
		WizardNonce: "wiz-key", WizardExpires: time.Now().Add(time.Hour)}
	if err := store.PutOnboarding(onboarding); err != nil {
		t.Fatal(err)
	}
	gateway := &Gateway{Control: control}
	post := httptest.NewRecorder()
	postReq := httptest.NewRequest(http.MethodPost, "http://127.0.0.1/connect/onb-wiz-writes?k=wiz-key", nil)
	postReq.Host = "127.0.0.1"
	gateway.serveConnect(post, postReq)
	if post.Code != http.StatusForbidden {
		t.Fatalf("wizard writes confirm=%d", post.Code)
	}
	finishAsHuman(t, control, onboarding.OnboardingID)
	latest, err := store.onboarding("onb-wiz-writes")
	if err != nil || !latest.WritesEnabled || latest.Stage != api.StageLive {
		t.Fatalf("writes confirm did not apply: %+v err=%v", latest, err)
	}
}

// The wizard page in awaiting-oauth shows the provider hop under the same
// link — the external consent is the only step that leaves ToolHub.
func TestConnectWizardRendersOAuthPhase(t *testing.T) {
	store := NewStore()
	control := &ControlPlane{Store: store, Now: time.Now}
	auth := aliceAuth()
	onboarding := api.Onboarding{
		Schema: api.SchemaVersion, OnboardingID: "onb-wiz-oauth", PrincipalID: auth.PrincipalID, ContextID: auth.ContextID,
		RuntimeID: auth.RuntimeID, PolicyVersion: auth.PolicyVersion, Revision: 1,
		Mode:                     api.OnboardingRemote,
		Stage:                    api.StagePending,
		Gate:                     &api.Gate{Kind: api.GateOAuth, Nonce: "", Expires: time.Time{}},
		ProviderAuthorizationURL: "https://accounts.example.com/authorize?state=flow-wiz&client_id=c&response_type=code",
		WizardNonce:              "wiz-key", WizardExpires: time.Now().Add(time.Hour)}
	if err := store.PutOnboarding(onboarding); err != nil {
		t.Fatal(err)
	}
	// The hop renders only while its flow lives: a dead state now re-arms a
	// fresh one instead of fronting a URL that fails at the callback.
	control.oauthFlows.flows = map[string]*preparedOAuthFlow{"flow-wiz": {onboardingID: onboarding.OnboardingID, url: onboarding.ProviderAuthorizationURL, expires: time.Now().Add(time.Hour)}}
	gateway := &Gateway{Control: control}
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "http://127.0.0.1/connect/onb-wiz-oauth?k=wiz-key", nil)
	req.Host = "127.0.0.1"
	gateway.serveConnect(rec, req)
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), "accounts.example.com/authorize") {
		t.Fatalf("wizard oauth page status=%d", rec.Code)
	}
	// The page keeps polling so a paste on another device or a completed
	// consent still lands the tab on the next wizard step.
	if !strings.Contains(rec.Body.String(), `http-equiv="refresh"`) {
		t.Fatalf("wizard oauth page does not self-refresh")
	}
}

// Confirmation rotation renews the wizard TTL too: a resend keeps the same
// stable link alive instead of expiring mid-flow.
func TestRefreshConfirmationRenewsWizard(t *testing.T) {
	store := NewStore()
	control := &ControlPlane{Store: store, Now: time.Now}
	auth := aliceAuth()
	onboarding := api.Onboarding{
		Schema: api.SchemaVersion, OnboardingID: "onb-wiz-confirm-renew", PrincipalID: auth.PrincipalID, ContextID: auth.ContextID,
		RuntimeID: auth.RuntimeID, PolicyVersion: auth.PolicyVersion, Revision: 1,
		Mode:         api.OnboardingCatalog,
		Stage:        api.StagePending,
		Gate:         &api.Gate{Kind: api.GateConfirmSetup, Nonce: "old", Expires: time.Now().Add(-time.Minute)},
		DefinitionID: "fixture-def", DefinitionVersion: "1.0.0",
		WizardNonce: "wiz-key", WizardExpires: time.Now().Add(-time.Minute)}
	if err := store.PutOnboarding(onboarding); err != nil {
		t.Fatal(err)
	}
	if err := control.refreshConfirmation(t.Context(), auth, &onboarding); err != nil {
		t.Fatal(err)
	}
	stored, err := store.onboarding("onb-wiz-confirm-renew")
	if err != nil {
		t.Fatal(err)
	}
	if stored.WizardNonce != "wiz-key" || !stored.WizardExpires.After(time.Now()) {
		t.Fatalf("wizard not renewed on confirmation refresh: %+v", stored)
	}
}

// Credential submission is itself the consent: the form gate clears and the
// record advances straight to bind/live — no second confirm click, no wizard
// re-arm for a step that no longer exists.
func TestSubmitCredentialsRenewsWizard(t *testing.T) {
	secrets, err := credstore.Open(credstore.Options{Path: filepath.Join(t.TempDir(), "tokens.enc"), Key: bytes.Repeat([]byte("k"), 32)})
	if err != nil {
		t.Fatal(err)
	}
	store := NewStore()
	control := &ControlPlane{Store: store, Secrets: secrets, Now: time.Now}
	auth := aliceAuth()
	onboarding := api.Onboarding{
		Schema: api.SchemaVersion, OnboardingID: "onb-wiz-submit", PrincipalID: auth.PrincipalID, ContextID: auth.ContextID,
		RuntimeID: auth.RuntimeID, PolicyVersion: auth.PolicyVersion, Revision: 1,
		Mode:         api.OnboardingCatalog,
		Stage:        api.StagePending,
		Gate:         &api.Gate{Kind: api.GateCredentials, Nonce: "form-nonce", Expires: time.Now().Add(time.Hour)},
		DefinitionID: "fixture-def", DefinitionVersion: "1.0.0",
		Required:    []api.CredentialHint{{Name: "API_TOKEN", Type: "secret", Secret: true}},
		WizardNonce: "wiz-key", WizardExpires: time.Now().Add(time.Minute)}
	if err := store.PutOnboarding(onboarding); err != nil {
		t.Fatal(err)
	}
	if err := control.SubmitCredentials("onb-wiz-submit", "form-nonce", map[string]string{"API_TOKEN": "tok"}); err != nil {
		t.Fatal(err)
	}
	stored, err := store.onboarding("onb-wiz-submit")
	// This fixture carries no stored definition, so the bind leg cannot run —
	// what must hold is that the credential gate cleared and the secret
	// persisted, with no second confirm step armed.
	if err != nil || stored.Locator == "" || stored.Awaiting(api.GateCredentials) || stored.Awaiting(api.GateConfirmSetup) {
		t.Fatalf("submit left a redundant human step armed: %+v err=%v", stored, err)
	}
}

// A broker request that reports ready moves the onboarding to confirm and
// renews the wizard — the same stable link now shows the confirm step.
func TestBrokerReadyTransitionRenewsWizard(t *testing.T) {
	_, private, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	keyPath := filepath.Join(t.TempDir(), "toolhub.private")
	if err := os.WriteFile(keyPath, private, 0600); err != nil {
		t.Fatal(err)
	}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if r.Method == http.MethodGet && r.URL.Path == "/v1/requests/r1" {
			_ = json.NewEncoder(w).Encode(brokerv1.Request{ID: "r1", Status: "ready", CredentialID: "credential_1", ContractID: "c1", ContractRevision: 1})
			return
		}
		http.NotFound(w, r)
	}))
	defer server.Close()

	store := NewStore()
	control := &ControlPlane{Store: store, Broker: &credentialbroker.Config{URL: server.URL, KeyFile: keyPath, KeyID: "toolhub", Issuer: "hermes-toolhub"}, Now: time.Now}
	auth := aliceAuth()
	onboarding := api.Onboarding{
		Schema: api.SchemaVersion, OnboardingID: "onb-wiz-broker-ready", PrincipalID: auth.PrincipalID, ContextID: auth.ContextID,
		RuntimeID: auth.RuntimeID, PolicyVersion: auth.PolicyVersion, Revision: 1,
		Mode:         api.OnboardingSelfInstall,
		Stage:        api.StagePending,
		Gate:         &api.Gate{Kind: api.GateCredentials, Nonce: "", Expires: time.Time{}},
		DefinitionID: "fixture-def", DefinitionVersion: "1.0.0",
		BrokerRequestID: "r1",
		WizardNonce:     "wiz-key", WizardExpires: time.Now().Add(time.Minute)}
	if err := store.PutOnboarding(onboarding); err != nil {
		t.Fatal(err)
	}
	if err := control.refreshBrokerRequest(context.Background(), auth, &onboarding); err != nil {
		t.Fatal(err)
	}
	// The submitted credential is the consent: the credential gate clears and
	// the record advances to pending→live setup instead of arming a second
	// confirm click. (Completion itself is best-effort here — this fixture
	// carries no stored definition — but the transition must persist.)
	if onboarding.BrokerCredentialID != "credential_1" || onboarding.Awaiting(api.GateCredentials) || onboarding.Awaiting(api.GateConfirmSetup) {
		t.Fatalf("credential ready did not clear the credential gate: %+v", onboarding)
	}
}

// The broker admission path (probe + definition admit) lands on
// awaiting-confirm — the wizard TTL renews there too, so a slow broker form
// cannot strand the link between steps.
func TestBrokerAdmissionRenewsWizard(t *testing.T) {
	keyPath := filepath.Join(t.TempDir(), "toolhub.private")
	_, private, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(keyPath, private, 0600); err != nil {
		t.Fatal(err)
	}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch {
		case r.Method == http.MethodGet && r.URL.Path == "/v1/requests/r1":
			_ = json.NewEncoder(w).Encode(brokerv1.Request{ID: "r1", Status: "ready", CredentialID: "credential_1", ContractID: "c1", ContractRevision: 1})
		case r.Method == http.MethodPost && r.URL.Path == "/v1/credentials/credential_1/grants":
			_ = json.NewEncoder(w).Encode(brokerv1.Grant{ID: "grant_temp", CredentialID: "credential_1", Active: true})
		case r.Method == http.MethodPost && r.URL.Path == "/v1/grants/grant_temp/revoke":
			_ = json.NewEncoder(w).Encode(map[string]any{})
		case r.Method == http.MethodPost && r.URL.Path == "/v1/leases":
			_ = json.NewEncoder(w).Encode(brokerv1.Lease{ID: "lease_1", GrantID: "grant_temp", CredentialID: "credential_1", Revision: 1})
		case r.Method == http.MethodPost && r.URL.Path == "/v1/runtime/leases/lease_1/materialize":
			_ = json.NewEncoder(w).Encode(brokerv1.Materialized{LeaseID: "lease_1", Env: map[string]string{"GOOGLE_TOKEN": "x"}})
		case r.Method == http.MethodPost && r.URL.Path == "/v1/runtime/leases/lease_1/release":
			_ = json.NewEncoder(w).Encode(map[string]any{})
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()

	auth := aliceAuth()
	definition := remoteDefinition()
	definition.CredentialContractID = "c1"
	definition.CredentialContractRevision = 1
	definition.CredentialContractEnv = map[string]string{"GOOGLE_TOKEN": "GOOGLE_TOKEN"}
	definition.Credentials = []api.CredentialInput{{Name: "GOOGLE_TOKEN", Required: true}}
	store := NewStore()
	if err := store.RegisterDefinition(definition); err != nil {
		t.Fatal(err)
	}
	cfg := credentialbroker.Config{URL: server.URL, KeyFile: keyPath, KeyID: "toolhub", Issuer: "hermes-toolhub"}
	control := &ControlPlane{Store: store, Broker: &cfg, BrokerRuntime: &cfg, Now: time.Now}
	control.AdmitWithCredentials = func(_ context.Context, d api.ToolDefinition, _ map[string]string) (api.ToolDefinition, error) {
		return d, nil
	}
	onboarding := api.Onboarding{
		Schema: api.SchemaVersion, OnboardingID: "onb-wiz-admit", PrincipalID: auth.PrincipalID, ContextID: auth.ContextID,
		RuntimeID: auth.RuntimeID, PolicyVersion: auth.PolicyVersion, Revision: 1,
		Mode:  api.OnboardingCatalog,
		Stage: api.StagePending,
		Gate:  &api.Gate{Kind: api.GateCredentials, Nonce: "", Expires: time.Time{}}, AdmissionPending: true,
		DefinitionID: definition.DefinitionID, DefinitionVersion: definition.Version,
		BrokerRequestID: "r1",
		WizardNonce:     "wiz-key", WizardExpires: time.Now().Add(time.Minute)}
	if err := store.PutOnboarding(onboarding); err != nil {
		t.Fatal(err)
	}
	if err := control.refreshBrokerRequest(context.Background(), auth, &onboarding); err != nil {
		t.Fatal(err)
	}
	// The submitted credential is the consent: admission proves it and the
	// record binds straight through to live — no second confirm click, no
	// wizard re-arm.
	if onboarding.Stage != api.StageLive {
		t.Fatalf("phase=%s", onboarding.NoticePhase())
	}
}

// An existing active broker credential short-circuits straight to live:
// the install request itself is the consent, so no wizard or confirm arms.
func TestBrokerCredentialReuseArmsWizard(t *testing.T) {
	_, private, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	keyPath := filepath.Join(t.TempDir(), "toolhub.private")
	if err := os.WriteFile(keyPath, private, 0600); err != nil {
		t.Fatal(err)
	}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if r.Method == http.MethodGet && strings.HasPrefix(r.URL.Path, "/v1/credentials/") {
			_ = json.NewEncoder(w).Encode(brokerv1.Credential{ID: "credential_1", Status: "active"})
			return
		}
		http.NotFound(w, r)
	}))
	defer server.Close()

	auth := aliceAuth()
	definition := remoteDefinition()
	definition.CredentialContractID = "c1"
	definition.CredentialContractRevision = 1
	definition.CredentialContractEnv = map[string]string{"GOOGLE_TOKEN": "GOOGLE_TOKEN"}
	definition.Credentials = []api.CredentialInput{{Name: "GOOGLE_TOKEN", Required: true}}
	store := NewStore()
	if err := store.RegisterDefinition(definition); err != nil {
		t.Fatal(err)
	}
	ref := api.CredentialReference{Schema: api.SchemaVersion, CredentialRefID: "cref", ConnectionID: "conn-1", Revision: 1, Backend: "credential-broker", Locator: "credential_1", Keys: []string{"GOOGLE_TOKEN"}, BrokerContractID: "c1", BrokerContractRevision: 1, Status: api.ActiveStatus}
	if err := store.PutCredentialReference(ref); err != nil {
		t.Fatal(err)
	}
	if err := store.PutConnection(api.Connection{Schema: api.SchemaVersion, ConnectionID: "conn-1", Owner: api.OwnerRef{Type: api.PrincipalOwner, ID: auth.PrincipalID}, DefinitionID: definition.DefinitionID, CredentialRefID: ref.CredentialRefID, Revision: 1, Status: api.ActiveStatus}); err != nil {
		t.Fatal(err)
	}
	control := &ControlPlane{Store: store, Broker: &credentialbroker.Config{URL: server.URL, KeyFile: keyPath, KeyID: "toolhub", Issuer: "hermes-toolhub"}, Now: time.Now}
	onboarding := api.Onboarding{
		Schema: api.SchemaVersion, OnboardingID: "onb-wiz-reuse", PrincipalID: auth.PrincipalID, ContextID: auth.ContextID,
		RuntimeID: auth.RuntimeID, PolicyVersion: auth.PolicyVersion, Revision: 1,
		Mode:         api.OnboardingCatalog,
		Stage:        api.StagePending,
		Gate:         &api.Gate{Kind: api.GateCredentials, Nonce: "", Expires: time.Time{}},
		DefinitionID: definition.DefinitionID, DefinitionVersion: definition.Version}
	if err := store.PutOnboarding(onboarding); err != nil {
		t.Fatal(err)
	}
	// Reuse attempts the bind inline; the mock grants no delivery endpoints,
	// so the completion reports its own failure — the record must not be
	// left hanging on a second confirm click either way.
	_ = control.ensureBrokerRequest(context.Background(), auth, &onboarding, definition)
	if onboarding.Awaiting(api.GateConfirmSetup) || onboarding.Awaiting(api.GateCredentials) {
		t.Fatalf("credential reuse armed a redundant human step: %+v", onboarding)
	}
}

// The wizard page in awaiting-oauth accepts the paste-the-redirect fallback
// POST on the same URL — a wrong state is rejected exactly like the ?state=
// connect link rejects it.
func TestConnectWizardOAuthPastePost(t *testing.T) {
	store := NewStore()
	control := &ControlPlane{Store: store, Now: time.Now}
	auth := aliceAuth()
	onboarding := api.Onboarding{
		Schema: api.SchemaVersion, OnboardingID: "onb-wiz-oauth-post", PrincipalID: auth.PrincipalID, ContextID: auth.ContextID,
		RuntimeID: auth.RuntimeID, PolicyVersion: auth.PolicyVersion, Revision: 1,
		Mode:                     api.OnboardingRemote,
		Stage:                    api.StagePending,
		Gate:                     &api.Gate{Kind: api.GateOAuth, Nonce: "", Expires: time.Time{}},
		ProviderAuthorizationURL: "https://accounts.example.com/authorize?state=flow-wiz&client_id=c",
		WizardNonce:              "wiz-key", WizardExpires: time.Now().Add(time.Hour)}
	if err := store.PutOnboarding(onboarding); err != nil {
		t.Fatal(err)
	}
	gateway := &Gateway{Control: control}
	post := httptest.NewRequest(http.MethodPost, "http://127.0.0.1/connect/onb-wiz-oauth-post?k=wiz-key",
		strings.NewReader("state=flow-wiz&redirect_url="+url.QueryEscape("http://127.0.0.1/oauth/callback?state=other&code=x")))
	post.Host = "127.0.0.1"
	post.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	rec := httptest.NewRecorder()
	gateway.serveConnect(rec, post)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("wizard oauth paste status=%d body=%s", rec.Code, rec.Body.String())
	}
}

// A broker-mediated onboarding renders the broker hop inside the wizard page
// and the page itself advances the phase: a ready request flips the render to
// the confirm step on the same URL — no second link needed.
func TestConnectWizardBrokerPagePollsRequest(t *testing.T) {
	keyPath := filepath.Join(t.TempDir(), "toolhub.private")
	_, private, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(keyPath, private, 0600); err != nil {
		t.Fatal(err)
	}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if r.Method == http.MethodGet && r.URL.Path == "/v1/requests/r1" {
			_ = json.NewEncoder(w).Encode(brokerv1.Request{ID: "r1", Status: "ready", CredentialID: "credential_1", ContractID: "c1", ContractRevision: 1})
			return
		}
		http.NotFound(w, r)
	}))
	defer server.Close()

	store := NewStore()
	control := &ControlPlane{Store: store, Broker: &credentialbroker.Config{URL: server.URL, KeyFile: keyPath, KeyID: "toolhub", Issuer: "hermes-toolhub"}, Now: time.Now}
	auth := aliceAuth()
	onboarding := api.Onboarding{
		Schema: api.SchemaVersion, OnboardingID: "onb-wiz-broker-poll", PrincipalID: auth.PrincipalID, ContextID: auth.ContextID,
		RuntimeID: auth.RuntimeID, PolicyVersion: auth.PolicyVersion, Revision: 1,
		Mode:         api.OnboardingSelfInstall,
		Stage:        api.StagePending,
		Gate:         &api.Gate{Kind: api.GateCredentials, Nonce: "", Expires: time.Time{}},
		DefinitionID: "fixture-def", DefinitionVersion: "1.0.0",
		BrokerRequestID: "r1", BrokerAuthorizationURL: "https://broker.example/connect/r1",
		WizardNonce: "wiz-key", WizardExpires: time.Now().Add(time.Hour)}
	if err := store.PutOnboarding(onboarding); err != nil {
		t.Fatal(err)
	}
	gateway := &Gateway{Control: control}
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "http://127.0.0.1/connect/onb-wiz-broker-poll?k=wiz-key", nil)
	req.Host = "127.0.0.1"
	gateway.serveConnect(rec, req)
	// The request was already ready — the credential gate clears; the page
	// renders whatever the advanced record now shows (no confirm step).
	if rec.Code != http.StatusOK {
		t.Fatalf("broker-ready wizard page status=%d body=%.300s", rec.Code, rec.Body.String())
	}
	stored, err := store.onboarding("onb-wiz-broker-poll")
	if err != nil || stored.BrokerCredentialID == "" || stored.Awaiting(api.GateCredentials) || stored.Awaiting(api.GateConfirmSetup) {
		t.Fatalf("page did not advance the phase: %+v err=%v", stored, err)
	}
}

// While the broker request is still pending, the wizard page offers the
// broker hop itself and self-refreshes — the credential form never carries
// fields the broker owns.
func TestConnectWizardBrokerPagePending(t *testing.T) {
	keyPath := filepath.Join(t.TempDir(), "toolhub.private")
	_, private, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(keyPath, private, 0600); err != nil {
		t.Fatal(err)
	}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if r.Method == http.MethodGet && r.URL.Path == "/v1/requests/r1" {
			_ = json.NewEncoder(w).Encode(brokerv1.Request{ID: "r1", Status: "pending", AuthorizationURL: "https://broker.example/connect/r1"})
			return
		}
		http.NotFound(w, r)
	}))
	defer server.Close()

	store := NewStore()
	control := &ControlPlane{Store: store, Broker: &credentialbroker.Config{URL: server.URL, KeyFile: keyPath, KeyID: "toolhub", Issuer: "hermes-toolhub"}, Now: time.Now}
	auth := aliceAuth()
	onboarding := api.Onboarding{
		Schema: api.SchemaVersion, OnboardingID: "onb-wiz-broker-pend", PrincipalID: auth.PrincipalID, ContextID: auth.ContextID,
		RuntimeID: auth.RuntimeID, PolicyVersion: auth.PolicyVersion, Revision: 1,
		Mode:         api.OnboardingSelfInstall,
		Stage:        api.StagePending,
		Gate:         &api.Gate{Kind: api.GateCredentials, Nonce: "", Expires: time.Time{}},
		DefinitionID: "fixture-def", DefinitionVersion: "1.0.0",
		BrokerRequestID: "r1", BrokerAuthorizationURL: "https://broker.example/connect/r1",
		WizardNonce: "wiz-key", WizardExpires: time.Now().Add(time.Hour)}
	if err := store.PutOnboarding(onboarding); err != nil {
		t.Fatal(err)
	}
	gateway := &Gateway{Control: control}
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "http://127.0.0.1/connect/onb-wiz-broker-pend?k=wiz-key", nil)
	req.Host = "127.0.0.1"
	gateway.serveConnect(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("status=%d", rec.Code)
	}
	body := rec.Body.String()
	if !strings.Contains(body, "broker.example/connect/r1") {
		t.Fatalf("pending page lacks the broker hop: %.300s", body)
	}
	if !strings.Contains(body, `http-equiv="refresh"`) {
		t.Fatalf("pending page does not self-refresh: %.300s", body)
	}
}

// The wizard link is sent while the backend is still building; those phases
// render an honest waiting page that self-refreshes into the next step.
func TestConnectWizardWaitingPhases(t *testing.T) {
	store := NewStore()
	control := &ControlPlane{Store: store, Now: time.Now}
	auth := aliceAuth()
	for _, phase := range []string{api.PhasePreparing, "review", api.PhaseConfirmed} {
		onboarding := api.Onboarding{
			Schema: api.SchemaVersion, OnboardingID: "onb-wiz-wait-" + phase, PrincipalID: auth.PrincipalID, ContextID: auth.ContextID,
			RuntimeID: auth.RuntimeID, PolicyVersion: auth.PolicyVersion, Revision: 1,
			Mode:         api.OnboardingSelfInstall,
			DefinitionID: "fixture-def", DefinitionVersion: "1.0.0",
			WizardNonce: "wiz-key", WizardExpires: time.Now().Add(time.Hour),
		}
		applyOnboardingPhase(&onboarding, phase)
		if err := store.PutOnboarding(onboarding); err != nil {
			t.Fatal(err)
		}
		gateway := &Gateway{Control: control}
		rec := httptest.NewRecorder()
		req := httptest.NewRequest(http.MethodGet, "http://127.0.0.1/connect/onb-wiz-wait-"+phase+"?k=wiz-key", nil)
		req.Host = "127.0.0.1"
		gateway.serveConnect(rec, req)
		if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), `http-equiv="refresh"`) {
			t.Fatalf("phase %s: status=%d body=%.200s", phase, rec.Code, rec.Body.String())
		}
	}
}

// A finished onboarding renders the terminal state on the same URL — success
// for enabled, the stored error for failed — so the page never strands the
// user on 401 after the flow completes.
func TestConnectWizardTerminalPhases(t *testing.T) {
	store := NewStore()
	control := &ControlPlane{Store: store, Now: time.Now}
	auth := aliceAuth()
	cases := map[string]string{api.PhaseEnabled: "подключён", api.PhaseFailed: "ошибка"}
	for phase, want := range cases {
		onboarding := api.Onboarding{
			Schema: api.SchemaVersion, OnboardingID: "onb-wiz-term-" + phase, PrincipalID: auth.PrincipalID, ContextID: auth.ContextID,
			RuntimeID: auth.RuntimeID, PolicyVersion: auth.PolicyVersion, Revision: 1,
			Mode:         api.OnboardingSelfInstall,
			DefinitionID: "fixture-def", DefinitionVersion: "1.0.0",
			WizardNonce: "wiz-key", WizardExpires: time.Now().Add(time.Hour),
		}
		applyOnboardingPhase(&onboarding, phase)
		if err := store.PutOnboarding(onboarding); err != nil {
			t.Fatal(err)
		}
		gateway := &Gateway{Control: control}
		rec := httptest.NewRecorder()
		req := httptest.NewRequest(http.MethodGet, "http://127.0.0.1/connect/onb-wiz-term-"+phase+"?k=wiz-key", nil)
		req.Host = "127.0.0.1"
		gateway.serveConnect(rec, req)
		if rec.Code != http.StatusOK || !strings.Contains(strings.ToLower(rec.Body.String()), want) {
			t.Fatalf("phase %s: status=%d body=%.200s", phase, rec.Code, rec.Body.String())
		}
	}
}

// A successful credential submit through the wizard lands back on the same
// stable link — the reload renders whatever phase follows (confirm/oauth/
// waiting) instead of a dead-end result page.
func TestConnectWizardCredentialPostRedirectsToWizard(t *testing.T) {
	store := NewStore()
	key, err := credstore.GenerateKey()
	if err != nil {
		t.Fatal(err)
	}
	secrets, err := credstore.Open(credstore.Options{Path: filepath.Join(t.TempDir(), "form.enc"), Key: key})
	if err != nil {
		t.Fatal(err)
	}
	control := &ControlPlane{Store: store, Secrets: secrets, Now: time.Now, ConfirmationTTL: time.Hour, FormOrigin: "http://127.0.0.1:8090"}
	onboarding := api.Onboarding{
		Schema: api.SchemaVersion, OnboardingID: "onb-wiz-credredir", PrincipalID: "alice", ContextID: "alice", RuntimeID: "runtime", PolicyVersion: "policy-1",
		Mode:  api.OnboardingSelfInstall,
		Stage: api.StagePending,
		Gate:  &api.Gate{Kind: api.GateCredentials, Nonce: api.RandomNonce(), Expires: time.Now().Add(time.Hour)}, SourceURL: "https://github.com/zereight/gitlab-mcp",
		DefinitionID: "gitlab", DefinitionVersion: "1.0.0", Revision: 1, CreatedAt: time.Now(),
		WizardNonce:   "wiz-key",
		WizardExpires: time.Now().Add(time.Hour),
		Required:      []api.CredentialHint{{Name: "GITLAB_PERSONAL_ACCESS_TOKEN", Secret: true, Delivery: "env"}}}
	if err := store.PutOnboarding(onboarding); err != nil {
		t.Fatal(err)
	}
	definition := userMCPDefinition()
	definition.DefinitionID = "gitlab"
	definition.Version = "1.0.0"
	definition.Credentials = []api.CredentialInput{{Name: "GITLAB_PERSONAL_ACCESS_TOKEN", Required: true}}
	if err := store.RegisterDefinition(definition); err != nil {
		t.Fatal(err)
	}
	gateway := &Gateway{Control: control}
	form := url.Values{"nonce": {onboarding.GateNonce(api.GateCredentials)}, "GITLAB_PERSONAL_ACCESS_TOKEN": {"glpat-fixture"}}
	post := httptest.NewRequest(http.MethodPost, "http://127.0.0.1/connect/onb-wiz-credredir?k=wiz-key", strings.NewReader(form.Encode()))
	post.Host = "127.0.0.1"
	post.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	rec := httptest.NewRecorder()
	gateway.serveConnect(rec, post)
	if rec.Code != http.StatusSeeOther || !strings.Contains(rec.Header().Get("Location"), "/connect/onb-wiz-credredir?k=") {
		t.Fatalf("credential submit did not return to the wizard link: %d %q %.200s", rec.Code, rec.Header().Get("Location"), rec.Body.String())
	}
}

// Reusing a live OAuth flow on a status poll must renew the wizard TTL too:
// otherwise the stable link dies while the provider flow is still valid and
// the channel would need a fresh message.
func TestPreparedOAuthReuseRenewsWizard(t *testing.T) {
	store := NewStore()
	key, kerr := credstore.GenerateKey()
	if kerr != nil {
		t.Fatal(kerr)
	}
	secrets, serr := credstore.Open(credstore.Options{Path: filepath.Join(t.TempDir(), "wiz-renew.enc"), Key: key})
	if serr != nil {
		t.Fatal(serr)
	}
	control := &ControlPlane{Store: store, Secrets: secrets, Now: time.Now,
		Injector: func(context.Context, api.EffectiveBinding) (api.CredentialInjection, error) {
			return api.CredentialInjection{}, nil
		}}
	auth := aliceAuth()
	onboarding := api.Onboarding{
		Schema: api.SchemaVersion, OnboardingID: "onb-wiz-oauth-renew", PrincipalID: auth.PrincipalID, ContextID: auth.ContextID,
		RuntimeID: auth.RuntimeID, PolicyVersion: auth.PolicyVersion, Revision: 1,
		Mode:         api.OnboardingCatalog,
		Stage:        api.StagePending,
		Gate:         &api.Gate{Kind: api.GateOAuth, Nonce: "", Expires: time.Time{}},
		DefinitionID: "fixture-def", DefinitionVersion: "1.0.0",
		ProviderAuthorizationURL: "https://accounts.example.com/authorize?state=flow-renew",
		WizardNonce:              "wiz-key", WizardExpires: time.Now().Add(-time.Minute)}
	if err := store.PutOnboarding(onboarding); err != nil {
		t.Fatal(err)
	}
	definition := statefulContainerDefinition()
	definition.DefinitionID, definition.Version = "fixture-def", "1.0.0"
	definition.OAuth = &api.OAuthRecipe{AuthorizeURL: "https://accounts.example.com/authorize", TokenURL: "https://accounts.example.com/token", Scopes: []string{"scope"}, ClientInput: "CLIENT_JSON", ClientSections: []string{"installed"}, StateTarget: "/state", TokenFile: "token.json", TokenTemplate: map[string]any{"normal": map[string]any{"access_token": "$oauth.ACCESS_TOKEN"}}, ProbeTool: "probe"}
	if err := store.RegisterDefinition(definition); err != nil {
		t.Fatal(err)
	}
	control.oauthFlows.mu.Lock()
	control.oauthFlows.flows = map[string]*preparedOAuthFlow{"flow-renew": {onboardingID: "onb-wiz-oauth-renew", url: onboarding.ProviderAuthorizationURL, expires: time.Now().Add(time.Hour)}}
	control.oauthFlows.mu.Unlock()
	if _, err := control.ensurePreparedOAuth(context.Background(), auth, onboarding, definition, false); err != nil {
		t.Fatal(err)
	}
	stored, err := store.onboarding("onb-wiz-oauth-renew")
	if err != nil || stored.WizardNonce != "wiz-key" || !time.Now().Before(stored.WizardExpires) {
		t.Fatalf("wizard ttl not renewed on reuse: %+v err=%v", stored, err)
	}
}
