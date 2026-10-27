package toolhub

import (
	"context"
	"errors"
	"html"
	"net/http"
	"net/http/httptest"
	"net/url"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/letya999/hermes-hub/internal/credstore"
	"github.com/letya999/hermes-hub/internal/locale"
	"github.com/letya999/hermes-hub/internal/oauth"
	"github.com/letya999/hermes-hub/internal/toolhub/api"
)

// S3: every loopback page resolves its language from ?lang=, then
// Accept-Language, then the hub fallback — and every deterministic string on
// those pages renders through the shared catalog. This drives each surface
// through its real handler in a non-default language.
func TestToolHubPagesLocalized(t *testing.T) {
	key, err := credstore.GenerateKey()
	if err != nil {
		t.Fatal(err)
	}
	secrets, err := credstore.Open(credstore.Options{Path: filepath.Join(t.TempDir(), "tokens.enc"), Key: key})
	if err != nil {
		t.Fatal(err)
	}
	store := NewStore()
	fixture := oauth.NewFixture()
	fixture.DevicePending = true
	t.Cleanup(fixture.Close)
	broker := oauth.NewBroker(secrets, []string{"http://127.0.0.1/oauth/callback"})
	broker.HTTP = fixture.Server.Client()
	broker.Providers["fixture"] = fixture.Provider()
	control := &ControlPlane{Store: store, Secrets: secrets, OAuth: broker, Now: time.Now, ConfirmationTTL: time.Hour, FormOrigin: "http://127.0.0.1:8090", WorkloadRoot: t.TempDir()}
	g := &Gateway{Control: control}
	grantTestControlOperations(t, store, "alice", api.ControlOperations...)
	catalog := catalogReadDefinition()
	if err := store.RegisterDefinition(catalog); err != nil {
		t.Fatal(err)
	}
	if err := putGrant(t, store, api.OperatorGrant(api.GrantCatalogDefault, "alice", "", "")); err != nil {
		t.Fatal(err)
	}
	formDef := catalogReadDefinition()
	formDef.DefinitionID = "form-def"
	formDef.Tools = append(formDef.Tools, api.ToolSpec{Name: "mutate", Effect: api.WriteEffect})
	if err := store.RegisterDefinition(formDef); err != nil {
		t.Fatal(err)
	}
	deviceDef := catalogReadDefinition()
	deviceDef.DefinitionID = "device-def"
	if err := store.RegisterDefinition(deviceDef); err != nil {
		t.Fatal(err)
	}

	hour := time.Hour
	seed := func(o api.Onboarding) api.Onboarding {
		o.Schema = api.SchemaVersion
		if o.Mode == "" {
			o.Mode = api.OnboardingCatalog
		}
		if o.PrincipalID == "" {
			o.PrincipalID = "alice"
		}
		if o.ContextID == "" {
			o.ContextID = "alice"
		}
		if o.RuntimeID == "" {
			o.RuntimeID = "runtime"
		}
		if o.PolicyVersion == "" {
			o.PolicyVersion = "policy-1"
		}
		o.Revision = 1
		if err := store.PutOnboarding(o); err != nil {
			t.Fatal(err)
		}
		return o
	}
	// es is the assertion language; kk/ru prove the negotiation order.
	page := func(key string, args ...any) string {
		return html.EscapeString(locale.T(locale.LangES, key, args...))
	}
	assertPage := func(name string, rec *httptest.ResponseRecorder, wantLang string, pieces ...string) {
		body := rec.Body.String()
		if !strings.Contains(body, `lang="`+wantLang+`"`) {
			head := body
			if len(head) > 400 {
				head = head[:400]
			}
			t.Errorf("%s: page lang attr not %q:\n%s", name, wantLang, head)
		}
		for _, want := range pieces {
			if !strings.Contains(body, want) {
				t.Errorf("%s: missing %q", name, want)
			}
		}
	}
	get := func(handler func(http.ResponseWriter, *http.Request), target string, acceptLang string) *httptest.ResponseRecorder {
		req := httptest.NewRequest(http.MethodGet, "http://127.0.0.1"+target, nil)
		req.Host = "127.0.0.1"
		if acceptLang != "" {
			req.Header.Set("Accept-Language", acceptLang)
		}
		rec := httptest.NewRecorder()
		handler(rec, req)
		return rec
	}
	post := func(handler func(http.ResponseWriter, *http.Request), target string, form url.Values) *httptest.ResponseRecorder {
		req := httptest.NewRequest(http.MethodPost, "http://127.0.0.1"+target, strings.NewReader(form.Encode()))
		req.Host = "127.0.0.1"
		req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		rec := httptest.NewRecorder()
		handler(rec, req)
		return rec
	}

	// --- /credentials GET: the richest surface. Alternative groups + optional
	// fields + recipe provenance + OAuth device escape hatch are all seeded so
	// every branch of the form renders.
	recipe := &api.RecipeResolution{
		Source: api.ArtifactSource{Repository: "https://github.com/example/mcp", CommitSHA: "0123456789abcdef"},
		Trust:  "T1", Egress: []string{"api.example.com"},
		Connection: api.ConnectionRecipe{
			Fields: []api.ConnectionField{{
				Name: "ALT_TOKEN", Source: "server.json", Rank: "reviewed",
				TokenURL: "https://example.com/tokens", Unverified: false,
			}},
			Alternatives: []api.ConnectionAlternative{{Name: "OAuth app", URL: "https://example.com/auth"}},
		},
	}
	seed(api.Onboarding{
		OnboardingID: "onb-creds", Phase: api.PhaseAwaitingCreds,
		DefinitionID: "form-def", DefinitionVersion: "1.0.0",
		FormNonce: "fn1", FormExpires: time.Now().Add(hour),
		OAuthProvider: "fixture", Recipe: recipe,
		Required: []api.CredentialHint{
			{Name: "API_TOKEN", Type: "secret", Secret: true},
			{Name: "ALT_TOKEN", Type: "secret", Secret: true, AlternativeGroup: 1},
			{Name: "ALT_SECRET", Type: "secret", Secret: true, AlternativeGroup: 1},
			{Name: "ALT_CERT", Type: "secret", Secret: true, AlternativeGroup: 2},
		},
		Optional: []api.CredentialHint{{Name: "PROJECT_ID", Type: "string"}},
	})

	rec := get(g.serveCredentials, "/credentials/onb-creds?t=fn1&lang=es", "")
	assertPage("creds-form", rec, "es",
		page("page.connect-title"), page("page.form-intro-alt"), page("page.alt-set", "ALT TOKEN"),
		page("page.field-required"), page("page.field-optional"), page("page.opt-group"),
		page("page.opt-out"), page("page.submit"), page("page.submit-no-probe"),
		page("page.tools", "search"), page("page.tools-writes", "mutate"),
		page("page.prov-source", "server.json"), page("page.prov-rank", "reviewed"),
		page("page.prov-token"), page("page.alt-auth"), page("page.device-alt"), page("page.device-open"),
		page("page.wait-submit"), page("page.wait-usually"), page("page.wait-seconds"),
		`aria-live="polite"`,
		"/device/onb-creds?t=fn1&amp;lang=es")

	// ?lang= wins over Accept-Language; Accept-Language alone resolves second.
	rec = get(g.serveCredentials, "/credentials/onb-creds?t=fn1&lang=es", "kk")
	assertPage("creds-param-priority", rec, "es", page("page.connect-title"))
	rec = get(g.serveCredentials, "/credentials/onb-creds?t=fn1", "kk-KZ, en;q=0.5")
	assertPage("creds-accept", rec, "kk", html.EscapeString(locale.T(locale.LangKK, "page.connect-title")))
	rec = get(g.serveCredentials, "/credentials/onb-creds?t=fn1", "")
	assertPage("creds-fallback", rec, "ru", html.EscapeString(locale.T(locale.LangRU, "page.connect-title")))

	// A stale/expired form nonce POST renders the localized stale-link page.
	rec = post(g.serveCredentials, "/credentials/onb-creds?lang=es", url.Values{"nonce": {"forged"}, "API_TOKEN": {"x"}})
	assertPage("creds-stale", rec, "es", page("page.stale-title"), page("page.stale-body"))

	// Alternative-group validation lands localized on the page and on the
	// persisted record the channel notice carries.
	rec = post(g.serveCredentials, "/credentials/onb-creds?lang=es", url.Values{"nonce": {"fn1"}, "API_TOKEN": {"tok"}})
	assertPage("creds-alt-one", rec, "es", page("page.invalid-title"), page("cred.alt-one"))
	rec = post(g.serveCredentials, "/credentials/onb-creds?lang=es", url.Values{"nonce": {"fn1"}, "API_TOKEN": {"tok"}, "ALT_TOKEN": {"a"}})
	assertPage("creds-alt-partial", rec, "es", page("page.invalid-title"), page("cred.alt-partial-names", "ALT SECRET"))
	stored, err := store.onboarding("onb-creds")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(stored.Error, locale.T(locale.LangES, "cred.alt-partial")) {
		t.Errorf("persisted rejection not localized: %q", stored.Error)
	}

	// A missing plain required field is the single-field rejection.
	seed(api.Onboarding{
		OnboardingID: "onb-miss", Phase: api.PhaseAwaitingCreds,
		DefinitionID: "form-def", DefinitionVersion: "1.0.0",
		FormNonce: "fn4", FormExpires: time.Now().Add(hour),
		Required: []api.CredentialHint{{Name: "API_TOKEN", Type: "secret", Secret: true}},
	})
	rec = post(g.serveCredentials, "/credentials/onb-miss?lang=es", url.Values{"nonce": {"fn4"}})
	assertPage("creds-missing", rec, "es", page("page.invalid-title"), page("cred.missing"))
	if got := credentialFormRejection(errors.New("unrecognized credential alternative"), locale.LangES); got != locale.T(locale.LangES, "cred.alt-unknown") {
		t.Errorf("alt-unknown rejection = %q", got)
	}

	// A successful submit arms the wizard and redirects onto the stable link —
	// the redirect must carry the request's language.
	seed(api.Onboarding{
		OnboardingID: "onb-ok", Phase: api.PhaseAwaitingCreds,
		DefinitionID: "catalog-read", DefinitionVersion: "1.0.0",
		FormNonce: "fn2", FormExpires: time.Now().Add(hour),
		Required: []api.CredentialHint{{Name: "API_TOKEN", Type: "secret", Secret: true}},
	})
	rec = post(g.serveCredentials, "/credentials/onb-ok?t=fn2&lang=es", url.Values{"nonce": {"fn2"}, "API_TOKEN": {"tok"}})
	if rec.Code != http.StatusSeeOther {
		t.Fatalf("submit redirect: %d %s", rec.Code, rec.Body.String())
	}
	if loc := rec.Header().Get("Location"); !strings.Contains(loc, "/connect/onb-ok?k=") || !strings.Contains(loc, "lang=es") {
		t.Errorf("submit redirect lost lang: %q", loc)
	}

	seed(api.Onboarding{
		OnboardingID: "onb-wiz-sub", Phase: api.PhaseAwaitingCreds,
		DefinitionID: "form-def", DefinitionVersion: "1.0.0",
		FormNonce: "fn3", FormExpires: time.Now().Add(hour),
		WizardNonce: "wk3", WizardExpires: time.Now().Add(hour),
		Required: []api.CredentialHint{{Name: "API_TOKEN", Type: "secret", Secret: true}},
	})
	rec = post(g.serveConnect, "/connect/onb-wiz-sub?k=wk3&lang=es", url.Values{"nonce": {"fn3"}, "API_TOKEN": {"tok"}})
	if rec.Code != http.StatusSeeOther {
		t.Fatalf("wizard submit redirect: %d %s", rec.Code, rec.Body.String())
	}
	loc := rec.Header().Get("Location")
	if !strings.Contains(loc, "k=wk3") || !strings.Contains(loc, "lang=es") {
		t.Errorf("wizard redirect lost lang: %q", loc)
	}

	// --- /confirm: GET renders the prompt, POST the localized outcome; a
	// spent nonce is the 410 gone page.
	seed(api.Onboarding{
		OnboardingID: "onb-conf", Phase: api.PhaseAwaitingConfirm,
		DefinitionID: "catalog-read", DefinitionVersion: "1.0.0",
		ConfirmationNonce: "cn1", ConfirmationExpires: time.Now().Add(hour),
	})
	rec = get(g.serveConfirm, "/confirm/onb-conf?t=cn1&lang=es", "")
	assertPage("confirm-get", rec, "es",
		page("page.confirm-title"), page("page.confirm-h1"),
		page("page.confirm-body", "catalog-read"), page("page.confirm-btn"))
	rec = post(g.serveConfirm, "/confirm/onb-conf?t=cn1&lang=es", nil)
	assertPage("confirm-post", rec, "es", page("page.confirmed-title"), page("page.confirmed-body"))

	seed(api.Onboarding{
		OnboardingID: "onb-conf-fail", Phase: api.PhaseAwaitingConfirm,
		DefinitionID: "missing-def", DefinitionVersion: "1.0.0",
		ConfirmationNonce: "cn2", ConfirmationExpires: time.Now().Add(hour),
	})
	rec = post(g.serveConfirm, "/confirm/onb-conf-fail?t=cn2&lang=es", nil)
	assertPage("confirm-fail", rec, "es", page("page.confirm-failed-title"))

	seed(api.Onboarding{
		OnboardingID: "onb-conf-gone", Phase: api.PhaseAwaitingConfirm,
		ConfirmationNonce: "cn3", ConfirmationExpires: time.Now().Add(hour), ConfirmationUsed: true,
	})
	rec = get(g.serveConfirm, "/confirm/onb-conf-gone?t=cn3&lang=es", "")
	assertPage("confirm-gone", rec, "es", page("page.gone-title"), page("page.gone-body"))

	// --- /remove: prompt, terminal result, spent tombstone.
	seed(api.Onboarding{
		OnboardingID: "onb-rem", Phase: api.PhaseEnabled,
		DefinitionID: "catalog-read", DefinitionVersion: "1.0.0",
		RemoveNonce: "rn1", RemoveExpires: time.Now().Add(hour),
	})
	rec = get(g.serveRemove, "/remove/onb-rem?t=rn1&lang=es", "")
	assertPage("remove-get", rec, "es",
		page("page.remove-title"), page("page.remove-h1"),
		page("page.remove-body", "catalog-read"), page("page.remove-btn"))
	rec = post(g.serveRemove, "/remove/onb-rem?t=rn1&lang=es", nil)
	assertPage("remove-post", rec, "es", page("page.removed-title"), page("page.removed-body"))

	// --- /connect?k= wizard: one URL, every phase.
	seed(api.Onboarding{
		OnboardingID: "onb-wait", Phase: api.PhasePreparing,
		WizardNonce: "wk4", WizardExpires: time.Now().Add(hour),
	})
	rec = get(g.serveConnect, "/connect/onb-wait?k=wk4&lang=es", "")
	assertPage("wizard-wait", rec, "es",
		page("page.wait-title"), page("page.wait-h1"), page("page.wait-body"), `aria-live="polite"`)

	seed(api.Onboarding{
		OnboardingID: "onb-enabled", Phase: api.PhaseEnabled,
		WizardNonce: "wk5", WizardExpires: time.Now().Add(hour),
	})
	rec = get(g.serveConnect, "/connect/onb-enabled?k=wk5&lang=es", "")
	assertPage("wizard-enabled", rec, "es", page("page.enabled-title"), page("page.enabled-body"))

	seed(api.Onboarding{
		OnboardingID: "onb-failed", Phase: api.PhaseFailed,
		WizardNonce: "wk6", WizardExpires: time.Now().Add(hour),
	})
	rec = get(g.serveConnect, "/connect/onb-failed?k=wk6&lang=es", "")
	assertPage("wizard-failed", rec, "es", page("page.failed-title"), page("page.failed-body"))

	seed(api.Onboarding{
		OnboardingID: "onb-hop", Phase: api.PhaseAwaitingCreds,
		BrokerAuthorizationURL: "https://broker.example.com/connect/r1",
		WizardNonce:            "wk7", WizardExpires: time.Now().Add(hour),
	})
	rec = get(g.serveConnect, "/connect/onb-hop?k=wk7&lang=es", "")
	assertPage("wizard-broker-hop", rec, "es",
		page("page.connect-title"), page("page.broker-h1"), page("page.broker-body"), page("page.broker-btn"))

	// --- OAuth: the provider-hop interstitial under the stable link and via
	// the ?state= hop; a dead flow redirects back to the wizard with lang kept.
	seed(api.Onboarding{
		OnboardingID: "onb-oauth", Phase: api.PhaseAwaitingOAuth,
		ProviderAuthorizationURL: "https://accounts.example.com/authorize?state=flow-live&client_id=c",
		WizardNonce:              "wk8", WizardExpires: time.Now().Add(hour),
	})
	control.oauthFlows.flows = map[string]*preparedOAuthFlow{
		"flow-live": {onboardingID: "onb-oauth", url: "https://accounts.example.com/authorize?state=flow-live&client_id=c", expires: time.Now().Add(hour)},
	}
	rec = get(g.serveConnect, "/connect/onb-oauth?k=wk8&lang=es", "")
	assertPage("wizard-oauth", rec, "es",
		page("page.oauth-title"), page("page.oauth-body"), page("page.oauth-btn"),
		page("page.oauth-paste"), page("page.oauth-paste-hint"), page("page.oauth-paste-label"), page("page.oauth-paste-btn"))

	rec = get(g.serveConnect, "/connect/onb-oauth?state=flow-live&lang=es", "")
	assertPage("oauth-interstitial", rec, "es", page("page.oauth-title"), page("page.oauth-btn"))

	// A paste with a mismatched/unknown code ends on the denied page.
	rec = post(g.serveConnect, "/connect/onb-oauth?lang=es",
		url.Values{"state": {"flow-live"}, "redirect_url": {"http://127.0.0.1/oauth/callback?state=flow-live&code=forged"}})
	assertPage("oauth-denied", rec, "es", page("page.login-failed-title"), page("page.oauth-denied-body"))

	seed(api.Onboarding{
		OnboardingID: "onb-oauth-dead", Phase: api.PhaseAwaitingOAuth,
		ProviderAuthorizationURL: "https://accounts.example.com/authorize?state=flow-dead&client_id=c",
		WizardNonce:              "wk9", WizardExpires: time.Now().Add(hour),
	})
	rec = get(g.serveConnect, "/connect/onb-oauth-dead?state=flow-dead&lang=es", "")
	if rec.Code != http.StatusSeeOther {
		t.Fatalf("dead-state redirect: %d", rec.Code)
	}
	if loc := rec.Header().Get("Location"); !strings.Contains(loc, "k=wk9") || !strings.Contains(loc, "lang=es") {
		t.Errorf("dead-state redirect lost lang: %q", loc)
	}

	// --- /device: code page, pending poll, terminal result, reused-link error.
	seed(api.Onboarding{
		OnboardingID: "onb-dev", Phase: api.PhaseAwaitingCreds,
		DefinitionID: "device-def", DefinitionVersion: "1.0.0",
		OAuthProvider: "fixture",
		FormNonce:     "fd1", FormExpires: time.Now().Add(hour),
	})
	rec = get(g.serveDevice, "/device/onb-dev?t=fd1&lang=es", "")
	assertPage("device-get", rec, "es",
		page("page.device-title"), page("page.device-h1"), page("page.device-btn"), "dev-")
	if !strings.Contains(rec.Body.String(), `href="`+fixture.Server.URL+`/device"`) {
		t.Errorf("device body missing verification link: %s", rec.Body.String())
	}
	rec = post(g.serveDevice, "/device/onb-dev?t=fd1&lang=es", url.Values{"device_code": {"dev-code"}})
	assertPage("device-pending", rec, "es", page("page.device-pending-title"), page("page.device-pending-body"))
	fixture.ApproveDevice("dev-code")
	rec = post(g.serveDevice, "/device/onb-dev?t=fd1&lang=es", url.Values{"device_code": {"dev-code"}})
	assertPage("device-ok", rec, "es", page("page.done-title"))
	seed(api.Onboarding{
		OnboardingID: "onb-dev-denied", Phase: api.PhaseAwaitingCreds,
		DefinitionID: "device-def", DefinitionVersion: "1.0.0",
		OAuthProvider: "ghost",
		FormNonce:     "fd2", FormExpires: time.Now().Add(hour),
		DeviceCode: "bogus-code",
	})
	rec = post(g.serveDevice, "/device/onb-dev-denied?t=fd2&lang=es", url.Values{"device_code": {"bogus-code"}})
	assertPage("device-denied", rec, "es", page("page.login-failed-title"))

	// --- /viewer: owner link opens the localized remote-control shell.
	if err := store.RegisterDefinition(browserDefinition()); err != nil {
		t.Fatal(err)
	}
	connection := browserConnection()
	if err := store.PutConnection(connection); err != nil {
		t.Fatal(err)
	}
	binding := api.ToolBinding{
		Schema: api.SchemaVersion, PrincipalID: "alice", ContextID: "alice", RuntimeID: "runtime",
		DefinitionID: "browser", DefinitionVersion: "1.0.0",
		ConnectionID: connection.ConnectionID, ConnectionRevision: connection.Revision,
		PolicyVersion: "policy-1", WorkloadClass: api.PerUser,
		Status: api.ActiveStatus, Revision: 1, ProjectionRevision: 1,
	}
	binding.ToolBindingID = api.DeterministicBindingID(binding.PrincipalID, binding.ContextID, binding.RuntimeID, binding.DefinitionID, binding.DefinitionVersion, binding.ConnectionID, "")
	if err := store.PutBinding(binding); err != nil {
		t.Fatal(err)
	}
	nonce, _, err := store.MintViewerLink(aliceAuth(), "conn-browser", "op-locale", time.Now(), hour)
	if err != nil {
		t.Fatal(err)
	}
	rec = get(g.serveViewer, "/viewer/conn-browser?t="+nonce+"&lang=es", "")
	assertPage("viewer", rec, "es",
		"Browser viewer", page("page.viewer-hint"), page("page.viewer-open"),
		page("page.viewer-retry"), page("page.viewer-alt"), `aria-live="polite"`)

	// A spent viewer link is the gone page too.
	rec = get(g.serveViewer, "/viewer/conn-browser?t="+nonce+"&lang=es", "")
	if rec.Code != http.StatusGone {
		t.Fatalf("viewer relink: %d", rec.Code)
	}
	assertPage("viewer-gone", rec, "es", page("page.gone-title"))

	// --- admission probe: the checking status persists mid-probe and the
	// rejection text lands localized on both the page and the record.
	probeControl := &ControlPlane{Store: NewStore(), Secrets: secrets, Now: time.Now, WorkloadRoot: t.TempDir()}
	var midProbeError string
	probeControl.AdmitWithCredentials = func(_ context.Context, _ api.ToolDefinition, _ map[string]string) (api.ToolDefinition, error) {
		stored, err := probeControl.Store.onboarding("onb-admit")
		if err == nil {
			midProbeError = stored.Error
		}
		return api.ToolDefinition{}, errors.New("tools/list empty")
	}
	probeOnboarding := api.Onboarding{
		Schema: api.SchemaVersion, OnboardingID: "onb-admit",
		PrincipalID: "alice", ContextID: "alice", RuntimeID: "runtime", PolicyVersion: "policy-1",
		Mode: api.OnboardingCatalog, Revision: 1,
		Phase: api.PhaseAwaitingCreds, AdmissionPending: true,
		DefinitionID: "form-def", DefinitionVersion: "1.0.0",
		Definition: &formDef,
		FormNonce:  "fn9", FormExpires: time.Now().Add(hour),
		Required: []api.CredentialHint{{Name: "API_TOKEN", Type: "secret", Secret: true}},
	}
	if err := probeControl.Store.PutOnboarding(probeOnboarding); err != nil {
		t.Fatal(err)
	}
	pg := &Gateway{Control: probeControl}
	rec = post(pg.serveCredentials, "/credentials/onb-admit?t=fn9&lang=es",
		url.Values{"nonce": {"fn9"}, "API_TOKEN": {"tok"}})
	if midProbeError != locale.T(locale.LangES, "cred.checking") {
		t.Errorf("mid-probe status = %q, want %q", midProbeError, locale.T(locale.LangES, "cred.checking"))
	}
	storedProbe, _ := probeControl.Store.onboarding("onb-admit")
	if !strings.Contains(storedProbe.Error, locale.T(locale.LangES, "cred.failed", "")) ||
		!strings.Contains(storedProbe.Error, locale.T(locale.LangES, "cred.tools-empty")) {
		t.Errorf("probe rejection not localized: %q", storedProbe.Error)
	}
	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("probe reject status=%d", rec.Code)
	}
	// The re-rendered form carries the public probe error, page-side only.
	assertPage("probe-rerender", rec, "es", page("page.probe-title"), locale.T(locale.LangES, "cred.tools-empty"))
}
