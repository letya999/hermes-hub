package toolhub

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/letya999/hermes-hub/internal/identity"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

const (
	aliceToken = "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	bobToken   = "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"
)

func aliceAuth() identity.Envelope {
	return identity.TelegramEnvelope("alice", 7, "runtime", "policy-1")
}

func TestControlToolContractDocumentsResumeSelector(t *testing.T) {
	description, schema := controlToolContract("status")
	if !strings.Contains(description, "definition_id") {
		t.Fatalf("status description does not explain resume selector: %q", description)
	}
	properties, ok := schema["properties"].(map[string]any)
	if !ok || properties["onboarding_id"] == nil || properties["definition_id"] == nil {
		t.Fatalf("status schema missing selectors: %#v", schema)
	}
}

func TestControlToolContractRoutesInstallBeforeLifecycle(t *testing.T) {
	prepare, _ := controlToolContract("prepare_source")
	if !strings.Contains(prepare, "call this first") || !strings.Contains(prepare, "GitHub repository URL") {
		t.Fatalf("prepare_source does not own explicit install routing: %q", prepare)
	}
	for _, op := range []string{"disable", "revoke", "remove"} {
		description, _ := controlToolContract(op)
		if !strings.Contains(description, "only when the user's current message explicitly requests") {
			t.Fatalf("%s permits inferred lifecycle calls: %q", op, description)
		}
	}
}

func bobAuth() identity.Envelope {
	return identity.TelegramEnvelope("bob", 8, "runtime-bob", "policy-1")
}

func catalogReadDefinition() ToolDefinition {
	definition := remoteDefinition()
	definition.DefinitionID = "catalog-read"
	definition.Credentials = nil
	definition.Tools = []ToolSpec{{Name: "search", Effect: ReadEffect}}
	return definition
}

func userMCPDefinition() ToolDefinition {
	definition := catalogReadDefinition()
	definition.DefinitionID = "user-mcp"
	return definition
}

func fixtureReviewer(definition ToolDefinition) SourceReviewer {
	return func(context.Context, ArtifactSource) (SourceReview, error) {
		return SourceReview{Definition: definition, Permissions: toolNames(definition), Effects: effectNames(definition), ReviewDigest: "sha256:review"}, nil
	}
}

func githubCommitURL() string {
	return "https://github.com/example/mcp/commit/0123456789abcdef0123456789abcdef01234567"
}

func TestControlResolvesRepositoryURLBeforeReview(t *testing.T) {
	definition := userMCPDefinition()
	fix := newControlFixture(t, func(_ context.Context, source ArtifactSource) (SourceReview, error) {
		if source.Repository != "https://github.com/example/mcp" || source.CommitSHA != "0123456789abcdef0123456789abcdef01234567" {
			t.Fatalf("review received mutable source: %+v", source)
		}
		return SourceReview{Definition: definition, Permissions: toolNames(definition), Effects: effectNames(definition), ReviewDigest: "sha256:review"}, nil
	})
	fix.control.SourceResolver = func(_ context.Context, raw string) (ArtifactSource, error) {
		if raw != "https://github.com/example/mcp" {
			t.Fatalf("resolver input=%q", raw)
		}
		return ArtifactSource{Repository: raw, CommitSHA: "0123456789abcdef0123456789abcdef01234567"}, nil
	}
	if err := fix.store.PutGrant(OperatorGrant(GrantSelfInstall, "alice", "", "")); err != nil {
		t.Fatal(err)
	}
	prepared, err := callControl(t, fix.session(t, aliceToken), "prepare_source", map[string]any{"source": "https://github.com/example/mcp", "request_key": "repo-url"})
	if err != nil {
		t.Fatal(err)
	}
	onboarding := mustOnboarding(t, fix.store, prepared["onboarding_id"].(string))
	if onboarding.SourceURL != "https://github.com/example/mcp" || onboarding.CommitSHA != "0123456789abcdef0123456789abcdef01234567" {
		t.Fatalf("mutable source persisted: %+v", onboarding)
	}
}

func TestSelfInstallReusesReviewedCommitOnRetry(t *testing.T) {
	definition := statefulContainerDefinition()
	definition.DefinitionID = "mcp"
	definition.Version = "0.0.2"
	definition.Source.Repository = "https://github.com/example/mcp"
	definition.Source.CommitSHA = "0123456789abcdef0123456789abcdef01234567"
	fix := newControlFixture(t, func(context.Context, ArtifactSource) (SourceReview, error) {
		t.Fatal("retry rebuilt an immutable source instead of reusing it")
		return SourceReview{}, nil
	})
	if err := fix.store.RegisterDefinition(definition); err != nil {
		t.Fatal(err)
	}
	if err := fix.store.PutPublication(DefinitionPublication{DefinitionID: "mcp", Version: "0.0.2", Visibility: PublicationUser, OwnerPrincipalID: "alice"}); err != nil {
		t.Fatal(err)
	}
	if err := fix.store.PutGrant(OperatorGrant(GrantSelfInstall, "alice", "", "")); err != nil {
		t.Fatal(err)
	}
	prepared, err := fix.control.Invoke(context.Background(), aliceAuth(), "prepare_source", map[string]any{
		"source": githubCommitURL(), "request_key": "retry-reviewed",
	})
	if err != nil {
		t.Fatal(err)
	}
	onboarding := mustOnboarding(t, fix.store, prepared["onboarding_id"].(string))
	if onboarding.DefinitionID != "mcp" || onboarding.CommitSHA != definition.Source.CommitSHA || onboarding.Phase != PhaseAwaitingCreds {
		t.Fatalf("reused onboarding=%+v", onboarding)
	}
}

type controlFixture struct {
	store   *Store
	control *ControlPlane
	gateway *Gateway
	server  *httptest.Server
	calls   *atomic.Int32
	audit   []map[string]string
}

func newControlFixture(t *testing.T, reviewer SourceReviewer) *controlFixture {
	t.Helper()
	store := NewStore()
	var calls atomic.Int32
	fix := &controlFixture{store: store, calls: &calls}
	control := &ControlPlane{Store: store, Reviewer: reviewer, WorkloadRoot: t.TempDir(), Now: time.Now, ConfirmationTTL: 10 * time.Minute}
	gateway := &Gateway{
		Store:  store,
		Tokens: map[string]identity.Envelope{aliceToken: aliceAuth(), bobToken: bobAuth()},
		Backend: backendFunc(func(context.Context, EffectiveBinding, ToolSpec, map[string]any) (BackendResult, error) {
			calls.Add(1)
			return BackendResult{Text: "ok"}, nil
		}),
		Control: control,
		Audit: func(_ string, fields map[string]string) {
			copied := map[string]string{}
			for k, v := range fields {
				copied[k] = v
			}
			fix.audit = append(fix.audit, copied)
		},
	}
	handler, err := gateway.Handler()
	if err != nil {
		t.Fatal(err)
	}
	server := httptest.NewServer(handler)
	t.Cleanup(server.Close)
	control.FormOrigin = server.URL
	fix.control, fix.gateway, fix.server = control, gateway, server
	return fix
}

func (f *controlFixture) session(t *testing.T, token string) *mcp.ClientSession {
	t.Helper()
	client := mcp.NewClient(&mcp.Implementation{Name: "control-test", Version: "1"}, nil)
	session, err := client.Connect(context.Background(), &mcp.StreamableClientTransport{Endpoint: f.server.URL + DefaultEndpointPath, HTTPClient: &http.Client{Transport: testBearerTransport{base: http.DefaultTransport, token: token}}}, nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = session.Close() })
	return session
}

func callControl(t *testing.T, session *mcp.ClientSession, op string, args map[string]any) (map[string]any, error) {
	t.Helper()
	result, err := session.CallTool(context.Background(), &mcp.CallToolParams{Name: op, Arguments: args})
	if err != nil {
		return nil, err
	}
	if result.IsError {
		return nil, errors.New("control tool error")
	}
	if body, ok := result.StructuredContent.(map[string]any); ok {
		return body, nil
	}
	for _, content := range result.Content {
		text, ok := content.(*mcp.TextContent)
		if !ok {
			continue
		}
		var body map[string]any
		if json.Unmarshal([]byte(text.Text), &body) == nil {
			return body, nil
		}
	}
	return nil, errors.New("missing control status body")
}

func TestControlMCPOperationsAndIsolation(t *testing.T) {
	fix := newControlFixture(t, fixtureReviewer(userMCPDefinition()))
	if err := fix.store.RegisterDefinition(catalogReadDefinition()); err != nil {
		t.Fatal(err)
	}
	if err := fix.store.PutGrant(OperatorGrant(GrantCatalogDefault, "alice", "", "")); err != nil {
		t.Fatal(err)
	}
	if err := fix.store.PutGrant(OperatorGrant(GrantSelfInstall, "alice", "", "")); err != nil {
		t.Fatal(err)
	}
	alice := fix.session(t, aliceToken)
	bob := fix.session(t, bobToken)
	listed, err := alice.ListTools(context.Background(), nil)
	if err != nil {
		t.Fatal(err)
	}
	have := map[string]bool{}
	for _, tool := range listed.Tools {
		have[tool.Name] = true
	}
	for _, op := range ControlOperations {
		if !have[op] {
			t.Fatalf("missing control operation %s", op)
		}
	}
	if !have["invoke"] {
		t.Fatal("missing stable projected-tool invoke operation")
	}
	prepared, err := callControl(t, alice, "prepare_source", map[string]any{"definition_id": "catalog-read", "version": "1.0.0", "request_key": "cat-1"})
	if err != nil {
		t.Fatal(err)
	}
	if prepared["phase"] != PhaseAwaitingConfirm || prepared["binding_id"] != "" {
		t.Fatalf("prepare catalog=%v", prepared)
	}
	if _, err := callControl(t, alice, "prepare_source", map[string]any{"user_id": "bob", "definition_id": "catalog-read", "version": "1.0.0"}); err == nil {
		t.Fatal("forged user_id accepted")
	}
	for _, args := range []map[string]any{
		{"owner_id": "bob", "definition_id": "catalog-read", "version": "1.0.0"},
		{"locator": "local://stolen", "definition_id": "catalog-read", "version": "1.0.0"},
		{"backend": "http://evil.example/mcp", "definition_id": "catalog-read", "version": "1.0.0"},
		{"policy": "open", "definition_id": "catalog-read", "version": "1.0.0"},
	} {
		if _, err := callControl(t, alice, "prepare_source", args); err == nil {
			t.Fatalf("forged arguments accepted: %v", args)
		}
	}
	status, err := callControl(t, alice, "status", map[string]any{"onboarding_id": prepared["onboarding_id"]})
	if err != nil || status["phase"] != PhaseAwaitingConfirm {
		t.Fatalf("status=%v err=%v", status, err)
	}
	creds, err := callControl(t, alice, "required_credentials", map[string]any{"onboarding_id": prepared["onboarding_id"]})
	if err != nil {
		t.Fatal(err)
	}
	encoded, _ := json.Marshal(creds)
	if strings.Contains(string(encoded), "local://") || strings.Contains(strings.ToLower(string(encoded)), "token=") {
		t.Fatalf("required_credentials leaked locator/secret: %s", encoded)
	}
	if _, err := callControl(t, alice, "confirm", map[string]any{"onboarding_id": prepared["onboarding_id"], "nonce": status["nonce"]}); err != nil {
		t.Fatal(err)
	}
	enabled, err := callControl(t, alice, "enable", map[string]any{"onboarding_id": prepared["onboarding_id"]})
	if err != nil || enabled["binding_id"] == "" || enabled["phase"] != PhaseEnabled {
		t.Fatalf("enable=%v err=%v", enabled, err)
	}
	again, err := callControl(t, alice, "enable", map[string]any{"onboarding_id": prepared["onboarding_id"]})
	if err != nil || again["binding_id"] != enabled["binding_id"] {
		t.Fatalf("idempotent enable=%v want=%v err=%v", again, enabled["binding_id"], err)
	}
	aliceTools, err := alice.ListTools(context.Background(), nil)
	if err != nil {
		t.Fatal(err)
	}
	projected := ProjectedToolName("catalog-read", "1.0.0", "search")
	if !toolListed(aliceTools, projected) {
		t.Fatal("alice missing her projected tool")
	}
	bobTools, err := bob.ListTools(context.Background(), nil)
	if err != nil {
		t.Fatal(err)
	}
	if toolListed(bobTools, projected) {
		t.Fatal("bob listed alice binding")
	}
	if _, err := bob.CallTool(context.Background(), &mcp.CallToolParams{Name: projected, Arguments: map[string]any{"query": "x"}}); err == nil {
		t.Fatal("bob called alice binding")
	}
	if _, err := callControl(t, bob, "status", map[string]any{"onboarding_id": prepared["onboarding_id"]}); err == nil {
		t.Fatal("bob read alice onboarding")
	}
	if _, err := callControl(t, bob, "prepare_source", map[string]any{"source": githubCommitURL()}); err == nil {
		t.Fatal("bob self-install without grant")
	}
	if _, err := callControl(t, bob, "prepare_source", map[string]any{"definition_id": "catalog-read", "version": "1.0.0"}); err == nil {
		t.Fatal("bob granted admin-approved MCP without assignment")
	}
	self, err := callControl(t, alice, "prepare_source", map[string]any{"source": githubCommitURL(), "request_key": "self-1"})
	if err != nil {
		t.Fatal(err)
	}
	selfStatus, err := callControl(t, alice, "status", map[string]any{"onboarding_id": self["onboarding_id"]})
	if err != nil {
		t.Fatal(err)
	}
	selfConfirm, err := callControl(t, alice, "confirm", map[string]any{"onboarding_id": self["onboarding_id"], "nonce": selfStatus["nonce"]})
	if err != nil {
		t.Fatal(err)
	}
	enabledSelf, err := callControl(t, alice, "enable", map[string]any{"onboarding_id": self["onboarding_id"]})
	if err != nil {
		t.Fatal(err)
	}
	userBinding, _ := enabledSelf["binding_id"].(string)
	if userBinding == "" {
		userBinding, _ = selfConfirm["binding_id"].(string)
	}
	if err := fix.store.PromoteToCatalog("user-mcp", "1.0.0", "operator"); err != nil {
		t.Fatal(err)
	}
	after, err := fix.store.binding(userBinding)
	if err != nil || after.WorkloadClass != PerUser || after.PrincipalID != "alice" {
		t.Fatalf("promotion converted user binding: %+v err=%v", after, err)
	}
	if _, err := callControl(t, bob, "enable", map[string]any{"definition_id": "user-mcp", "version": "1.0.0"}); err == nil {
		t.Fatal("promotion auto-shared user binding to bob")
	}
}

func toolListed(listed *mcp.ListToolsResult, name string) bool {
	if listed == nil {
		return false
	}
	for _, tool := range listed.Tools {
		if tool.Name == name {
			return true
		}
	}
	return false
}

func TestControlResponsesAreNonSecret(t *testing.T) {
	fix := newControlFixture(t, nil)
	if err := fix.store.RegisterDefinition(catalogReadDefinition()); err != nil {
		t.Fatal(err)
	}
	if err := fix.store.PutGrant(OperatorGrant(GrantCatalogDefault, "alice", "", "")); err != nil {
		t.Fatal(err)
	}
	alice := fix.session(t, aliceToken)
	body, err := callControl(t, alice, "prepare_source", map[string]any{"definition_id": "catalog-read", "version": "1.0.0"})
	if err != nil {
		t.Fatal(err)
	}
	raw, _ := json.Marshal(body)
	if strings.Contains(string(raw), "GOOGLE_TOKEN=") || strings.Contains(string(raw), "password") {
		t.Fatalf("secret-like control body: %s", raw)
	}
	resp, err := http.Get(fix.server.URL + "/mcp")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	page, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if strings.Contains(string(page), "scan-secret-ORANGE-NINE-7721") {
		t.Fatal("test secret appeared in HTTP body")
	}
}

func TestControlMCPReportsProgress(t *testing.T) {
	fix := newControlFixture(t, nil)
	if err := fix.store.RegisterDefinition(catalogReadDefinition()); err != nil {
		t.Fatal(err)
	}
	if err := fix.store.PutGrant(OperatorGrant(GrantCatalogDefault, "alice", "", "")); err != nil {
		t.Fatal(err)
	}
	messages := make(chan string, 4)
	client := mcp.NewClient(&mcp.Implementation{Name: "progress-test", Version: "1"}, &mcp.ClientOptions{ProgressNotificationHandler: func(_ context.Context, request *mcp.ProgressNotificationClientRequest) {
		messages <- request.Params.Message
	}})
	session, err := client.Connect(context.Background(), &mcp.StreamableClientTransport{Endpoint: fix.server.URL + DefaultEndpointPath, HTTPClient: &http.Client{Transport: testBearerTransport{base: http.DefaultTransport, token: aliceToken}}}, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer session.Close()
	params := &mcp.CallToolParams{Name: "prepare_source", Meta: mcp.Meta{"progressToken": "install-1"}, Arguments: map[string]any{"definition_id": "catalog-read", "version": "1.0.0"}}
	if _, err := session.CallTool(context.Background(), params); err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"Source is being verified and built", "Source review and build completed"} {
		select {
		case got := <-messages:
			if got != want {
				t.Fatalf("progress=%q want=%q", got, want)
			}
		case <-time.After(time.Second):
			t.Fatalf("missing progress %q", want)
		}
	}
}
