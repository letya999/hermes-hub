package toolhub

import (
	"bytes"
	"context"
	"errors"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"net/url"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/letya999/hermes-hub/internal/credstore"
	"github.com/letya999/hermes-hub/internal/identity"
	"github.com/letya999/hermes-hub/internal/oauth"
)

func TestGoogleOAuthFormAcceptsFileOrExplicitValues(t *testing.T) {
	var body bytes.Buffer
	writer := multipart.NewWriter(&body)
	file, _ := writer.CreateFormFile("google_oauth_file", "client_secret.json")
	_, _ = file.Write([]byte(`{"installed":{"client_id":"id","client_secret":"secret"}}`))
	_ = writer.Close()
	req := httptest.NewRequest(http.MethodPost, "/", &body)
	req.Header.Set("Content-Type", writer.FormDataContentType())
	if err := req.ParseMultipartForm(64 << 10); err != nil {
		t.Fatal(err)
	}
	if value, err := googleOAuthFormValue(req); err != nil || !strings.Contains(value, `"client_id":"id"`) {
		t.Fatalf("file value=%q err=%v", value, err)
	}

	body.Reset()
	writer = multipart.NewWriter(&body)
	_ = writer.WriteField("google_oauth_client_id", "id")
	_ = writer.WriteField("google_oauth_client_secret", "secret")
	_ = writer.Close()
	manual := httptest.NewRequest(http.MethodPost, "/", &body)
	manual.Header.Set("Content-Type", writer.FormDataContentType())
	if err := manual.ParseMultipartForm(64 << 10); err != nil {
		t.Fatal(err)
	}
	if value, err := googleOAuthFormValue(manual); err != nil || !strings.Contains(value, `"client_secret":"secret"`) {
		t.Fatalf("manual value=%q err=%v", value, err)
	}

	hints := credentialFormHints([]CredentialHint{{Name: "GOOGLE_OAUTH_CREDENTIALS"}, {Name: "HOST"}, {Name: "PORT"}, {Name: "TRANSPORT"}})
	if len(hints) != 1 || hints[0].Name != "GOOGLE_OAUTH_CREDENTIALS" {
		t.Fatalf("visible hints=%+v", hints)
	}
}

func TestControlErrorPathsGrantsAndOAuthCallback(t *testing.T) {
	if _, err := (&ControlPlane{}).Invoke(context.Background(), aliceAuth(), "status", nil); !errors.Is(err, ErrInvalid) {
		t.Fatalf("nil store: %v", err)
	}
	store := NewStore()
	control := &ControlPlane{Store: store, Listen: "https://127.0.0.1:9", Now: time.Now, ConfirmationTTL: time.Minute}
	if control.origin() != "https://127.0.0.1:9" {
		t.Fatalf("origin=%s", control.origin())
	}
	control.Listen = "127.0.0.1:8090"
	if !strings.HasPrefix(control.origin(), "http://127.0.0.1:8090") {
		t.Fatalf("listen origin=%s", control.origin())
	}
	if _, err := control.Invoke(context.Background(), aliceAuth(), "unknown", nil); !errors.Is(err, ErrInvalid) {
		t.Fatalf("unknown op: %v", err)
	}
	if _, err := control.Invoke(context.Background(), aliceAuth(), "prepare_source", map[string]any{}); !errors.Is(err, ErrInvalid) {
		t.Fatalf("empty prepare: %v", err)
	}
	if _, err := ParseGitHubSource("http://github.com/x/y/commit/0123456789abcdef0123456789abcdef01234567"); !errors.Is(err, ErrInvalid) {
		t.Fatal("insecure github accepted")
	}
	if _, err := ParseGitHubSource("https://github.com/x/y"); !errors.Is(err, ErrInvalid) {
		t.Fatal("unpinned github accepted")
	}
	src, err := ParseGitHubSource("https://github.com/example/mcp/tree/0123456789abcdef0123456789abcdef01234567")
	if err != nil || src.CommitSHA == "" {
		t.Fatalf("tree url: %+v %v", src, err)
	}
	if err := store.RegisterDefinition(catalogReadDefinition()); err != nil {
		t.Fatal(err)
	}
	if _, err := control.Invoke(context.Background(), aliceAuth(), "prepare_source", map[string]any{"definition_id": "catalog-read", "version": "1.0.0"}); !errors.Is(err, ErrUnauthorized) {
		t.Fatalf("catalog without grant: %v", err)
	}
	if err := store.PutGrant(OperatorGrant(GrantCatalogDefault, "alice", "", "")); err != nil {
		t.Fatal(err)
	}
	if err := store.PutGrant(Grant{Schema: SchemaVersion, GrantID: "grant-selfalice", Kind: GrantSelfInstall, PrincipalID: "alice", IssuedBy: "operator", Status: ActiveStatus, Revision: 1}); err != nil {
		t.Fatal(err)
	}
	if _, err := control.prepareSelfInstall(context.Background(), aliceAuth(), githubCommitURL(), "no-reviewer", nil); !errors.Is(err, ErrInvalid) {
		t.Fatalf("nil reviewer: %v", err)
	}
	control.Reviewer = fixtureReviewer(userMCPDefinition())
	if _, err := control.Invoke(context.Background(), aliceAuth(), "prepare_source", map[string]any{"source": "https://evil.example/repo"}); err == nil {
		t.Fatal("non-github source accepted")
	}
	prepared, err := control.Invoke(context.Background(), aliceAuth(), "prepare_source", map[string]any{"definition_id": "catalog-read", "version": "1.0.0", "request_key": "more-1"})
	if err != nil {
		t.Fatal(err)
	}
	again, err := control.Invoke(context.Background(), aliceAuth(), "prepare_source", map[string]any{"definition_id": "catalog-read", "version": "1.0.0", "request_key": "more-1"})
	if err != nil || again["onboarding_id"] != prepared["onboarding_id"] {
		t.Fatalf("idempotent prepare=%v err=%v", again, err)
	}
	// The failed self-install above leaves a durable "failed" record that the
	// latest-onboarding selector picks; select by definition for the catalog one.
	if status, err := control.Invoke(context.Background(), aliceAuth(), "status", map[string]any{"definition_id": "catalog-read"}); err != nil || status["onboarding_id"] != prepared["onboarding_id"] {
		t.Fatalf("status selector: status=%v err=%v", status, err)
	}
	st, err := control.Invoke(context.Background(), aliceAuth(), "status", map[string]any{"definition_id": "catalog-read", "version": "1.0.0"})
	if err != nil || st["onboarding_id"] != prepared["onboarding_id"] {
		t.Fatalf("status by definition=%v err=%v", st, err)
	}
	if _, err := control.Invoke(context.Background(), aliceAuth(), "confirm", map[string]any{"onboarding_id": prepared["onboarding_id"], "nonce": "wrong"}); !errors.Is(err, ErrUnauthorized) {
		t.Fatalf("bad nonce: %v", err)
	}
	if _, err := control.Invoke(context.Background(), aliceAuth(), "confirm", map[string]any{"onboarding_id": prepared["onboarding_id"], "nonce": st["nonce"], "tools": []any{"missing-tool"}}); !errors.Is(err, ErrUnauthorized) {
		t.Fatalf("tool escalation: %v", err)
	}
	direct := catalogReadDefinition()
	direct.DefinitionID = "catalog-direct"
	if err := store.RegisterDefinition(direct); err != nil {
		t.Fatal(err)
	}
	if _, err := control.Invoke(context.Background(), aliceAuth(), "enable", map[string]any{"definition_id": "catalog-direct", "version": "1.0.0", "memory_mib": 999999}); !errors.Is(err, ErrUnauthorized) {
		t.Fatalf("enable budget: %v", err)
	}
	if _, err := control.Invoke(context.Background(), aliceAuth(), "enable", map[string]any{"definition_id": "catalog-direct", "version": "1.0.0", "timeout_seconds": 999, "max_pids": 999, "output_bytes": 1 << 30}); !errors.Is(err, ErrUnauthorized) {
		t.Fatalf("enable other budget: %v", err)
	}
	enabled, err := control.Invoke(context.Background(), aliceAuth(), "enable", map[string]any{"definition_id": "catalog-direct", "version": "1.0.0"})
	if err != nil || enabled["binding_id"] == "" {
		t.Fatalf("enable by definition=%v err=%v", enabled, err)
	}
	_ = control.publicationForCheck(catalogReadDefinition())
	if err := store.PutPublication(DefinitionPublication{DefinitionID: "catalog-read", Version: "1.0.0", Visibility: PublicationUser, OwnerPrincipalID: "alice"}); err != nil {
		t.Fatal(err)
	}
	if _, err := store.Enable(bobAuth(), "catalog-read", "1.0.0"); !errors.Is(err, ErrUnauthorized) {
		t.Fatalf("bob enabled alice user publication: %v", err)
	}
	entries, err := store.Catalog(bobAuth())
	if err != nil {
		t.Fatal(err)
	}
	for _, entry := range entries {
		if entry.Name == "catalog-read" {
			t.Fatal("bob catalog listed alice user publication")
		}
	}
	if err := (&Grant{Schema: SchemaVersion, GrantID: "grant-bad", Kind: GrantKind("nope"), PrincipalID: "alice", IssuedBy: "operator", Status: ActiveStatus, Revision: 1}).Validate(); err == nil {
		t.Fatal("invalid grant kind")
	}
	if err := (DefinitionPublication{}).Validate(); err == nil {
		t.Fatal("empty publication")
	}
	if err := (SharedCredentialPolicy{Schema: SchemaVersion, PolicyID: "policy-x", DefinitionID: "catalog-read", Locator: "loc", StoreOwner: "alice", Principals: []string{"alice"}, IssuedBy: "model", Status: ActiveStatus}).Validate(); err == nil {
		t.Fatal("model shared policy")
	}
	if err := store.PutSharedCredentialPolicy(SharedCredentialPolicy{Schema: SchemaVersion, PolicyID: "policy-ok", DefinitionID: "catalog-read", Locator: "local://shared/1", StoreOwner: "alice", Principals: []string{"alice"}, IssuedBy: "operator", Status: ActiveStatus}); err != nil {
		t.Fatal(err)
	}
	if err := store.PromoteToCatalog("missing", "1.0.0", "operator"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("promote missing: %v", err)
	}
	if err := store.PromoteToCatalog("catalog-read", "1.0.0", "model"); !errors.Is(err, ErrUnauthorized) {
		t.Fatalf("promote model: %v", err)
	}

	key, err := credstore.GenerateKey()
	if err != nil {
		t.Fatal(err)
	}
	secrets, err := credstore.Open(credstore.Options{Path: filepath.Join(t.TempDir(), "more.enc"), Key: key})
	if err != nil {
		t.Fatal(err)
	}
	control.Secrets = secrets
	if err := control.SubmitCredentials("missing", "x", nil); !errors.Is(err, ErrNotFound) {
		t.Fatalf("submit missing: %v", err)
	}
	credDef := remoteDefinition()
	credDef.DefinitionID = "oauth-mcp"
	if err := store.RegisterDefinition(credDef); err != nil {
		t.Fatal(err)
	}
	if err := store.PutGrant(OperatorGrant(GrantDefinition, "alice", "oauth-mcp", "1.0.0")); err != nil {
		t.Fatal(err)
	}
	oauthPrep, err := control.Invoke(context.Background(), aliceAuth(), "prepare_source", map[string]any{"definition_id": "oauth-mcp", "version": "1.0.0", "request_key": "oauth-1"})
	if err != nil {
		t.Fatal(err)
	}
	reqd, err := control.Invoke(context.Background(), aliceAuth(), "required_credentials", map[string]any{"onboarding_id": oauthPrep["onboarding_id"]})
	if err != nil || reqd["form_path"] == "" {
		t.Fatalf("oauth required=%v err=%v", reqd, err)
	}
	onboarding, _ := store.onboarding(oauthPrep["onboarding_id"].(string))
	if err := control.SubmitCredentials(onboarding.OnboardingID, "bad", map[string]string{"GOOGLE_TOKEN": "x"}); !errors.Is(err, ErrUnauthorized) {
		t.Fatalf("bad form nonce: %v", err)
	}

	fixture := oauth.NewFixture()
	t.Cleanup(fixture.Close)
	gateway := &Gateway{Store: store, Backend: backendFunc(func(context.Context, EffectiveBinding, ToolSpec, map[string]any) (BackendResult, error) {
		return BackendResult{Text: "ok"}, nil
	}), Tokens: map[string]identity.Envelope{aliceToken: aliceAuth()}, Control: control}
	handler, err := gateway.Handler()
	if err != nil {
		t.Fatal(err)
	}
	server := httptest.NewServer(handler)
	t.Cleanup(server.Close)
	control.FormOrigin = server.URL
	redirect := server.URL + "/oauth/callback"
	broker := oauth.NewBroker(secrets, []string{redirect})
	broker.HTTP = fixture.Server.Client()
	broker.Providers["fixture"] = fixture.Provider()
	control.OAuth = broker
	start, err := control.StartOAuth(aliceAuth(), onboarding.OnboardingID, redirect)
	if err != nil {
		t.Fatal(err)
	}
	client := &http.Client{CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	resp, err := client.Get(start.AuthorizeURL)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	parsed, _ := url.Parse(resp.Header.Get("Location"))
	if parsed == nil || parsed.Query().Get("code") == "" {
		t.Fatalf("authorize location=%s", resp.Header.Get("Location"))
	}
	if err := control.HandleOAuthCallback(aliceAuth(), onboarding.OnboardingID, parsed.Query().Get("state"), parsed.Query().Get("code"), redirect); err != nil {
		t.Fatalf("handle oauth: %v", err)
	}
	recCB := httptest.NewRecorder()
	cbReq := httptest.NewRequest(http.MethodGet, "http://127.0.0.1/oauth/callback?onboarding_id="+onboarding.OnboardingID, nil)
	cbReq.Host = "127.0.0.1"
	gateway.serveOAuthCallback(recCB, cbReq)
	if recCB.Code == http.StatusOK && strings.Contains(recCB.Body.String(), testSecret) {
		t.Fatal("oauth callback leaked secret")
	}

	badForm := httptest.NewRequest(http.MethodGet, "http://example.com/credentials/x", nil)
	badForm.Host = "example.com"
	rec := httptest.NewRecorder()
	gateway.serveCredentials(rec, badForm)
	if rec.Code != http.StatusForbidden {
		t.Fatalf("non-loopback form=%d", rec.Code)
	}
	missing := httptest.NewRequest(http.MethodPut, "http://127.0.0.1/credentials/"+onboarding.OnboardingID, nil)
	missing.Host = "127.0.0.1"
	rec = httptest.NewRecorder()
	gateway.serveCredentials(rec, missing)
	if rec.Code != http.StatusMethodNotAllowed {
		t.Fatalf("method=%d", rec.Code)
	}
	cbBad := httptest.NewRequest(http.MethodGet, "http://example.com/oauth/callback", nil)
	cbBad.Host = "example.com"
	rec = httptest.NewRecorder()
	gateway.serveOAuthCallback(rec, cbBad)
	if rec.Code != http.StatusForbidden {
		t.Fatalf("non-loopback oauth=%d", rec.Code)
	}
	cbMiss := httptest.NewRequest(http.MethodGet, "http://127.0.0.1/oauth/callback", nil)
	cbMiss.Host = "127.0.0.1"
	rec = httptest.NewRecorder()
	gateway.serveOAuthCallback(rec, cbMiss)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("missing onboarding callback=%d", rec.Code)
	}

	if _, err := control.Invoke(context.Background(), aliceAuth(), "disable", map[string]any{"onboarding_id": "missing-onboard"}); !errors.Is(err, ErrNotFound) {
		t.Fatalf("disable missing: %v", err)
	}
	if _, err := control.Invoke(context.Background(), aliceAuth(), "revoke", map[string]any{"onboarding_id": prepared["onboarding_id"]}); err != nil {
		t.Fatal(err)
	}
	if _, err := control.Invoke(context.Background(), aliceAuth(), "remove", map[string]any{"onboarding_id": prepared["onboarding_id"]}); err != nil {
		t.Fatal(err)
	}
	hints := credentialHints(ToolDefinition{Credentials: []CredentialInput{{Name: "OAUTH_TOKEN", Required: true}, {Name: "HOST", Required: true}, {Name: "OPTIONAL", Required: false}}})
	if len(hints) != 1 || hints[0].Type != "oauth" {
		t.Fatalf("hints=%v", hints)
	}
	if argInt(map[string]any{"cpu_millis": int64(3)}, "cpu_millis") != 3 || argInt(map[string]any{"cpu_millis": 4}, "cpu_millis") != 4 {
		t.Fatal("argInt types")
	}
	if argInt(nil, "cpu_millis") != 0 || argInt(map[string]any{"cpu_millis": 1.5}, "cpu_millis") != 1 || argInt(map[string]any{"cpu_millis": "x"}, "cpu_millis") != 0 {
		t.Fatal("argInt fallbacks")
	}
	if got := argStrings(map[string]any{"effects": []string{"read"}}, "effects"); len(got) != 1 {
		t.Fatalf("argStrings=%v", got)
	}
	if argStrings(nil, "effects") != nil || argStrings(map[string]any{"effects": 1}, "effects") != nil {
		t.Fatal("argStrings fallbacks")
	}
}

func TestControlEnableMaterializeRevokeAndGrantValidation(t *testing.T) {
	store := NewStore()
	definition := catalogReadDefinition()
	if err := store.RegisterDefinition(definition); err != nil {
		t.Fatal(err)
	}
	if err := store.PutGrant(OperatorGrant(GrantCatalogDefault, "alice", "", "")); err != nil {
		t.Fatal(err)
	}
	key, err := credstore.GenerateKey()
	if err != nil {
		t.Fatal(err)
	}
	secrets, err := credstore.Open(credstore.Options{Path: filepath.Join(t.TempDir(), "enable.enc"), Key: key})
	if err != nil {
		t.Fatal(err)
	}
	control := &ControlPlane{Store: store, Secrets: secrets, WorkloadRoot: t.TempDir(), Now: time.Now, ConfirmationTTL: time.Minute}
	auth := aliceAuth()
	prepared, err := control.Invoke(context.Background(), auth, "prepare_source", map[string]any{"definition_id": "catalog-read", "version": "1.0.0", "request_key": "enable-1"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := control.Invoke(context.Background(), auth, "enable", map[string]any{"onboarding_id": prepared["onboarding_id"]}); !errors.Is(err, ErrUnauthorized) {
		t.Fatalf("enable before confirm: %v", err)
	}
	if _, err := control.Invoke(context.Background(), auth, "enable", map[string]any{"definition_id": "missing-def"}); !errors.Is(err, ErrNotFound) {
		t.Fatalf("enable missing definition id only: %v", err)
	}
	if _, err := control.Invoke(context.Background(), auth, "enable", map[string]any{"definition_id": "missing-def", "version": "1.0.0"}); !errors.Is(err, ErrNotFound) {
		t.Fatalf("enable unknown definition: %v", err)
	}
	if _, err := control.Invoke(context.Background(), bobAuth(), "enable", map[string]any{"definition_id": "catalog-read", "version": "1.0.0"}); !errors.Is(err, ErrUnauthorized) {
		t.Fatalf("bob enable catalog without grant: %v", err)
	}

	status, err := control.Invoke(context.Background(), auth, "status", map[string]any{"onboarding_id": prepared["onboarding_id"]})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := control.Invoke(context.Background(), auth, "confirm", map[string]any{"onboarding_id": prepared["onboarding_id"], "nonce": status["nonce"]}); err != nil {
		t.Fatal(err)
	}
	onboarding, err := store.onboarding(prepared["onboarding_id"].(string))
	if err != nil || onboarding.BindingID == "" {
		t.Fatalf("confirmed onboarding=%+v err=%v", onboarding, err)
	}
	onboarding.BindingID = ""
	if err := store.PutOnboarding(onboarding); err != nil {
		t.Fatal(err)
	}
	enabled, err := control.Invoke(context.Background(), auth, "enable", map[string]any{"onboarding_id": onboarding.OnboardingID})
	if err != nil || enabled["binding_id"] == "" {
		t.Fatalf("enable rematerialize=%v err=%v", enabled, err)
	}

	orphan := Onboarding{
		Schema: SchemaVersion, OnboardingID: "onboard-orphan", PrincipalID: auth.PrincipalID, ContextID: auth.ContextID,
		RuntimeID: auth.RuntimeID, PolicyVersion: auth.PolicyVersion, Mode: OnboardingCatalog, DefinitionID: "no-such-def",
		DefinitionVersion: "1.0.0", Phase: PhaseConfirmed, Revision: 1, CreatedAt: time.Now().UTC(),
	}
	if err := store.PutOnboarding(orphan); err != nil {
		t.Fatal(err)
	}
	if _, err := control.Invoke(context.Background(), auth, "enable", map[string]any{"onboarding_id": "onboard-orphan"}); !errors.Is(err, ErrNotFound) {
		t.Fatalf("enable missing definitionOf: %v", err)
	}
	if _, err := control.Invoke(context.Background(), auth, "disable", map[string]any{"onboarding_id": "onboard-orphan"}); !errors.Is(err, ErrNotFound) {
		t.Fatalf("disable without binding: %v", err)
	}

	credDef := catalogReadDefinition()
	credDef.DefinitionID = "secret-mcp"
	credDef.Credentials = []CredentialInput{{Name: "TOKEN", Required: true}}
	if err := store.RegisterDefinition(credDef); err != nil {
		t.Fatal(err)
	}
	if err := store.PutGrant(OperatorGrant(GrantDefinition, "alice", "secret-mcp", "1.0.0")); err != nil {
		t.Fatal(err)
	}
	credPrep, err := control.Invoke(context.Background(), auth, "prepare_source", map[string]any{"definition_id": "secret-mcp", "version": "1.0.0", "request_key": "secret-1"})
	if err != nil {
		t.Fatal(err)
	}
	credOn, err := store.onboarding(credPrep["onboarding_id"].(string))
	if err != nil {
		t.Fatal(err)
	}
	if err := control.SubmitCredentials(credOn.OnboardingID, credOn.FormNonce, map[string]string{"TOKEN": "rotate-secret"}); err != nil {
		t.Fatal(err)
	}
	credStatus, err := control.Invoke(context.Background(), auth, "status", map[string]any{"onboarding_id": credOn.OnboardingID})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := control.Invoke(context.Background(), auth, "confirm", map[string]any{"onboarding_id": credOn.OnboardingID, "nonce": credStatus["nonce"]}); err != nil {
		t.Fatal(err)
	}
	if _, err := control.Invoke(context.Background(), auth, "enable", map[string]any{"onboarding_id": credOn.OnboardingID}); err != nil {
		t.Fatal(err)
	}
	if _, err := control.Invoke(context.Background(), auth, "revoke", map[string]any{"onboarding_id": credOn.OnboardingID}); err != nil {
		t.Fatal(err)
	}
	if _, err := control.StartOAuth(auth, credOn.OnboardingID, "http://127.0.0.1/callback"); !errors.Is(err, ErrInvalid) {
		t.Fatalf("nil oauth start: %v", err)
	}
	if err := control.HandleOAuthCallback(auth, credOn.OnboardingID, "state", "code", "http://127.0.0.1/callback"); !errors.Is(err, ErrInvalid) {
		t.Fatalf("nil oauth callback: %v", err)
	}

	if err := (Grant{Schema: SchemaVersion, GrantID: "grant-id", PrincipalID: "alice", IssuedBy: "operator", Status: ActiveStatus, Revision: 1}).Validate(); err == nil {
		t.Fatal("missing kind accepted")
	}
	if err := (Grant{Schema: 0, GrantID: "grant-id", Kind: GrantSelfInstall, PrincipalID: "alice", IssuedBy: "operator", Status: ActiveStatus, Revision: 1}).Validate(); err == nil {
		t.Fatal("bad schema accepted")
	}
	if err := (Grant{Schema: SchemaVersion, GrantID: "grant-id", Kind: GrantSelfInstall, PrincipalID: "alice", IssuedBy: "hermes", Status: ActiveStatus, Revision: 1}).Validate(); err == nil {
		t.Fatal("hermes issuer accepted")
	}
	if err := (Grant{Schema: SchemaVersion, GrantID: "grant-id", Kind: GrantSelfInstall, PrincipalID: "alice", IssuedBy: "operator", Status: Status("nope"), Revision: 1}).Validate(); err == nil {
		t.Fatal("bad grant status accepted")
	}
	if err := (Grant{Schema: SchemaVersion, GrantID: "grant-id", Kind: GrantCatalogDefault, PrincipalID: "alice", IssuedBy: "operator", Status: ActiveStatus, Revision: 1, DefinitionID: "catalog-read"}).Validate(); err == nil {
		t.Fatal("catalog grant with definition accepted")
	}
	if err := (Grant{Schema: SchemaVersion, GrantID: "grant-id", Kind: GrantDefinition, PrincipalID: "alice", IssuedBy: "operator", Status: ActiveStatus, Revision: 1, DefinitionID: "catalog-read"}).Validate(); err == nil {
		t.Fatal("definition grant without version accepted")
	}
	if err := (Grant{Schema: SchemaVersion, GrantID: "grant-id", Kind: GrantSharedCredential, PrincipalID: "alice", IssuedBy: "operator", Status: ActiveStatus, Revision: 1}).Validate(); err == nil {
		t.Fatal("shared-credential grant without definition accepted")
	}
	sharedGrant := OperatorGrant(GrantSharedCredential, "alice", "catalog-read", "")
	if err := sharedGrant.Validate(); err != nil {
		t.Fatal(err)
	}
	first := OperatorGrant(GrantSelfInstall, "dave", "", "")
	if err := store.PutGrant(first); err != nil {
		t.Fatal(err)
	}
	conflict := first
	conflict.Status = DisabledStatus
	if err := store.PutGrant(conflict); !errors.Is(err, ErrConflict) {
		t.Fatalf("grant conflict: %v", err)
	}
	if err := store.PutPublication(DefinitionPublication{DefinitionID: "missing-pub", Version: "1.0.0", Visibility: PublicationCatalog}); !errors.Is(err, ErrNotFound) {
		t.Fatalf("publication missing definition: %v", err)
	}
	if err := (Onboarding{Schema: SchemaVersion, OnboardingID: "onboard-x", PrincipalID: "alice", ContextID: "alice", RuntimeID: "runtime", PolicyVersion: "policy-1", Mode: "nope", Phase: PhaseReview, Revision: 1}).Validate(); err == nil {
		t.Fatal("bad onboarding mode accepted")
	}
	if err := (Onboarding{Schema: SchemaVersion, OnboardingID: "onboard-x", PrincipalID: "alice", ContextID: "alice", RuntimeID: "runtime", PolicyVersion: "policy-1", Mode: OnboardingCatalog, Phase: "nope", Revision: 1}).Validate(); err == nil {
		t.Fatal("bad onboarding phase accepted")
	}
	if err := (Onboarding{Schema: SchemaVersion, OnboardingID: "onboard-x", PrincipalID: "alice", ContextID: "alice", RuntimeID: "runtime", PolicyVersion: "policy-1", Mode: OnboardingCatalog, Phase: PhaseReview, Revision: 1, DefinitionID: "BAD"}).Validate(); err == nil {
		t.Fatal("bad onboarding definition accepted")
	}
	if err := (Onboarding{Schema: SchemaVersion, OnboardingID: "onboard-x", PrincipalID: "alice", ContextID: "alice", RuntimeID: "runtime", PolicyVersion: "policy-1", Mode: OnboardingCatalog, Phase: PhaseReview, Revision: 1, DefinitionVersion: "v"}).Validate(); err == nil {
		t.Fatal("bad onboarding version accepted")
	}
	if err := (SharedCredentialPolicy{Schema: 0, PolicyID: "policy-x", DefinitionID: "catalog-read", Locator: "local://x", StoreOwner: "alice", Principals: []string{"alice"}, IssuedBy: "operator", Status: ActiveStatus}).Validate(); err == nil {
		t.Fatal("bad shared policy schema accepted")
	}
	if err := (SharedCredentialPolicy{Schema: SchemaVersion, PolicyID: "policy-x", DefinitionID: "catalog-read", Locator: "", StoreOwner: "alice", Principals: []string{"alice"}, IssuedBy: "operator", Status: ActiveStatus}).Validate(); err == nil {
		t.Fatal("empty shared locator accepted")
	}
	if err := (SharedCredentialPolicy{Schema: SchemaVersion, PolicyID: "policy-x", DefinitionID: "catalog-read", Locator: "local://x", StoreOwner: "alice", Principals: []string{"alice"}, IssuedBy: "operator", Status: Status("nope")}).Validate(); err == nil {
		t.Fatal("bad shared status accepted")
	}
	if err := (SharedCredentialPolicy{Schema: SchemaVersion, PolicyID: "policy-x", DefinitionID: "catalog-read", Locator: "local://x", StoreOwner: "alice", Principals: nil, IssuedBy: "operator", Status: ActiveStatus}).Validate(); err == nil {
		t.Fatal("empty shared principals accepted")
	}
	if err := (SharedCredentialPolicy{Schema: SchemaVersion, PolicyID: "policy-x", DefinitionID: "catalog-read", Locator: "local://x", StoreOwner: "alice", Principals: []string{"alice", "alice"}, IssuedBy: "operator", Status: ActiveStatus}).Validate(); err == nil {
		t.Fatal("duplicate shared principal accepted")
	}
	if err := (DefinitionPublication{DefinitionID: "catalog-read", Version: "1.0.0", Visibility: PublicationUser}).Validate(); err == nil {
		t.Fatal("user publication without owner accepted")
	}
}

func TestMaterializeBindingRotatesExistingOwnerConnection(t *testing.T) {
	store := NewStore()
	v1 := remoteDefinition()
	v2 := v1
	v2.Version = "2.0.0"
	if err := store.RegisterDefinition(v1); err != nil {
		t.Fatal(err)
	}
	if err := store.RegisterDefinition(v2); err != nil {
		t.Fatal(err)
	}
	old := CredentialReference{Schema: SchemaVersion, CredentialRefID: CredentialReferenceID("conn-existing", 1), ConnectionID: "conn-existing", Revision: 1, Backend: credstore.BackendLocal, Locator: "old-locator", Keys: []string{"GOOGLE_TOKEN"}, Status: ActiveStatus}
	if err := store.PutCredentialReference(old); err != nil {
		t.Fatal(err)
	}
	if err := store.PutConnection(Connection{Schema: SchemaVersion, ConnectionID: "conn-existing", Owner: OwnerRef{Type: PrincipalOwner, ID: "alice"}, DefinitionID: v1.DefinitionID, CredentialRefID: old.CredentialRefID, Revision: 1, Status: ActiveStatus}); err != nil {
		t.Fatal(err)
	}
	control := &ControlPlane{Store: store, Ready: func(context.Context, EffectiveBinding) error { return nil }}
	binding, err := control.materializeBinding(context.Background(), aliceAuth(), Onboarding{OnboardingID: "onboard-new", Locator: "new-locator", Required: []CredentialHint{{Name: "GOOGLE_TOKEN"}}}, v2)
	if err != nil {
		t.Fatal(err)
	}
	if binding.DefinitionVersion != v2.Version || binding.ConnectionID != "conn-existing" || binding.CredentialRevision != 2 {
		t.Fatalf("binding did not reuse rotated connection: %+v", binding)
	}
	store.mu.RLock()
	defer store.mu.RUnlock()
	if len(store.connections) != 1 || store.connections["conn-existing"].Revision != 2 || store.credentials[old.CredentialRefID].Status != RevokedStatus || store.credentials[binding.CredentialRefID].Locator != "new-locator" {
		t.Fatalf("credential rotation state is inconsistent")
	}
}

func (c *ControlPlane) publicationForCheck(definition ToolDefinition) DefinitionPublication {
	if c == nil || c.Store == nil {
		return DefinitionPublication{}
	}
	return c.Store.publicationFor(definition)
}

func TestStatusBodyExposesNextAction(t *testing.T) {
	control := &ControlPlane{Store: NewStore(), Listen: "127.0.0.1:0"}
	confirm := control.statusBody(Onboarding{OnboardingID: "onboard-1", Phase: PhaseAwaitingConfirm, ConfirmationNonce: "nonce-1"}, false)
	if confirm["next_action"] != "confirm" || confirm["nonce"] != "nonce-1" || confirm["instructions"] == "" {
		t.Fatalf("awaiting-confirm status lacks next step: %v", confirm)
	}
	enable := control.statusBody(Onboarding{OnboardingID: "onboard-1", Phase: PhaseConfirmed}, false)
	if enable["next_action"] != "enable" {
		t.Fatalf("confirmed status lacks enable hint: %v", enable)
	}
	creds := control.statusBody(Onboarding{OnboardingID: "onboard-1", Phase: PhaseAwaitingCreds}, false)
	if creds["next_action"] != "submit_credentials" {
		t.Fatalf("awaiting-credentials status lacks submit hint: %v", creds)
	}
}

func TestPrepareSelfInstallRegistersPreparingThenFailedRecord(t *testing.T) {
	store := NewStore()
	if err := store.PutGrant(Grant{Schema: SchemaVersion, GrantID: "grant-selfalice", Kind: GrantSelfInstall, PrincipalID: "alice", IssuedBy: "operator", Status: ActiveStatus, Revision: 1}); err != nil {
		t.Fatal(err)
	}
	release := make(chan struct{})
	control := &ControlPlane{Store: store, WorkloadRoot: t.TempDir(), Now: time.Now, ConfirmationTTL: 10 * time.Minute,
		Reviewer: func(context.Context, ArtifactSource, *RecipeCandidate) (SourceReview, error) {
			<-release
			return SourceReview{}, errors.New("build blew up")
		}}
	done := make(chan error, 1)
	go func() {
		_, err := control.prepareSelfInstall(context.Background(), aliceAuth(), githubCommitURL(), "req-wake", nil)
		done <- err
	}()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if existing, ok := store.FindOnboardingByKey(aliceAuth(), "req-wake"); ok {
			if existing.Phase != PhasePreparing {
				t.Fatalf("mid-flight phase=%s", existing.Phase)
			}
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	if _, ok := store.FindOnboardingByKey(aliceAuth(), "req-wake"); !ok {
		t.Fatal("preparing record not visible during review/build")
	}
	close(release)
	if err := <-done; err == nil {
		t.Fatal("failed review accepted")
	}
	existing, ok := store.FindOnboardingByKey(aliceAuth(), "req-wake")
	if !ok || existing.Phase != PhaseFailed {
		t.Fatalf("failed prepare left phase=%v ok=%v", existing.Phase, ok)
	}
}
