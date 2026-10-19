package toolhub

import (
	"context"
	"errors"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/letya999/hermes-hub/internal/identity"
	"github.com/letya999/hermes-hub/internal/toolhub/api"
)

func m13P3Auth(t *testing.T) identity.Envelope {
	t.Helper()
	return identity.TelegramEnvelope("alice", 7, "runtime", "policy-1")
}

// S3.1: the operation journal persists, reloads and is owner-scoped.
func TestOperationStoreRoundTrip(t *testing.T) {
	path := filepath.Join(t.TempDir(), "store.json")
	store := NewStore()
	auth := m13P3Auth(t)
	op := api.Operation{
		Schema: api.SchemaVersion, OperationID: api.DeterministicID("op", "alice", "install", "github"),
		Intent: api.OpIntentInstall, Target: "github", RequestKey: api.OperationRequestKey(auth, api.OpIntentInstall, "github"),
		PrincipalID: auth.PrincipalID, ContextID: auth.ContextID, RuntimeID: auth.RuntimeID, PolicyVersion: auth.PolicyVersion,
		State: api.OpStateRunning, CreatedAt: time.Now().UTC(),
		Steps: []api.OperationStep{{Name: "resolve", State: api.OpStepDone}},
	}
	if err := store.PutOperation(op); err != nil {
		t.Fatalf("put: %v", err)
	}
	if err := store.Save(path); err != nil {
		t.Fatalf("save: %v", err)
	}
	reloaded, err := Load(path)
	if err != nil {
		t.Fatalf("reload: %v", err)
	}
	got, err := reloaded.OperationFor(auth, op.OperationID)
	if err != nil {
		t.Fatalf("for: %v", err)
	}
	if got.Intent != api.OpIntentInstall || got.State != api.OpStateRunning || len(got.Steps) != 1 || got.Steps[0].Name != "resolve" {
		t.Fatalf("round-trip mismatch: %+v", got)
	}
	// A foreign principal cannot read it.
	foreign := identity.TelegramEnvelope("bobuser", 8, "runtime", "policy-1")
	if _, err := reloaded.OperationFor(foreign, op.OperationID); err == nil {
		t.Fatal("foreign principal read the operation")
	}
}

// S3.1: request key derives from (principal, intent, normalized target) — a
// duplicate install call resolves the same operation.
func TestOperationRequestKeyIdempotent(t *testing.T) {
	auth := m13P3Auth(t)
	keyA := api.OperationRequestKey(auth, api.OpIntentInstall, " https://github.com/Org/Repo ")
	keyB := api.OperationRequestKey(auth, api.OpIntentInstall, "https://github.com/org/repo")
	if keyA == "" || keyA != keyB {
		t.Fatalf("request key not normalized: %q vs %q", keyA, keyB)
	}
	if api.OperationRequestKey(auth, api.OpIntentRemove, "https://github.com/org/repo") == keyA {
		t.Fatal("intent did not change the key")
	}
	other := identity.TelegramEnvelope("bobuser", 8, "runtime", "policy-1")
	if api.OperationRequestKey(other, api.OpIntentInstall, "https://github.com/org/repo") == keyA {
		t.Fatal("principal did not change the key")
	}
}

// S3.1: OperationForRequestKey resolves the live operation for the same
// (principal, intent, target); superseded records are never candidates.
func TestOperationForRequestKey(t *testing.T) {
	store := NewStore()
	auth := m13P3Auth(t)
	target := "github"
	op := api.Operation{
		Schema: api.SchemaVersion, OperationID: api.DeterministicID("op", "a1"),
		Intent: api.OpIntentInstall, Target: target, RequestKey: api.OperationRequestKey(auth, api.OpIntentInstall, target),
		PrincipalID: auth.PrincipalID, ContextID: auth.ContextID, RuntimeID: auth.RuntimeID, PolicyVersion: auth.PolicyVersion,
		State: api.OpStateDone, CreatedAt: time.Now().UTC(),
	}
	if err := store.PutOperation(op); err != nil {
		t.Fatalf("put: %v", err)
	}
	got, ok := store.OperationForRequestKey(auth, api.OpIntentInstall, "GitHub")
	if !ok || got.OperationID != op.OperationID {
		t.Fatalf("request key lookup failed: %v %+v", ok, got)
	}
	if _, ok := store.OperationForRequestKey(auth, api.OpIntentRemove, target); ok {
		t.Fatal("different intent matched")
	}
	op.State = api.OpStateFailed
	op.SupersededBy = "op-next0000000000000000000"
	if err := store.PutOperation(op); err != nil {
		t.Fatalf("put2: %v", err)
	}
	if _, ok := store.OperationForRequestKey(auth, api.OpIntentInstall, target); ok {
		t.Fatal("superseded operation matched the request key")
	}
}

// S3.1: Validate rejects operations without identity, with unknown
// intent/state/code or malformed steps.
func TestOperationValidate(t *testing.T) {
	auth := m13P3Auth(t)
	base := func() api.Operation {
		return api.Operation{
			Schema: api.SchemaVersion, OperationID: api.DeterministicID("op", "v"),
			Intent: api.OpIntentInstall, Target: "x", RequestKey: api.OperationRequestKey(auth, api.OpIntentInstall, "x"),
			PrincipalID: auth.PrincipalID, ContextID: auth.ContextID, RuntimeID: auth.RuntimeID, PolicyVersion: auth.PolicyVersion,
			State: api.OpStateQueued, CreatedAt: time.Now().UTC(),
		}
	}
	if err := base().Validate(); err != nil {
		t.Fatalf("valid op rejected: %v", err)
	}
	cases := map[string]func(*api.Operation){
		"bad state":      func(o *api.Operation) { o.State = "exploding" },
		"bad intent":     func(o *api.Operation) { o.Intent = "vibes" },
		"missing target": func(o *api.Operation) { o.Target = "" },
		"bad principal":  func(o *api.Operation) { o.PrincipalID = "!!" },
		"bad step state": func(o *api.Operation) { o.Steps = []api.OperationStep{{Name: "resolve", State: "lol"}} },
		"bad code":       func(o *api.Operation) { o.ErrorCode = "because reasons" },
		"dup step": func(o *api.Operation) {
			o.Steps = []api.OperationStep{{Name: "resolve", State: api.OpStepDone}, {Name: "resolve", State: api.OpStepPending}}
		},
	}
	for name, mutate := range cases {
		op := base()
		mutate(&op)
		if err := op.Validate(); err == nil {
			t.Fatalf("%s: invalid operation accepted", name)
		}
	}
}

func m13P3Onboarding(auth identity.Envelope, suffix, definitionID, source, phase string) api.Onboarding {
	return api.Onboarding{
		Schema: api.SchemaVersion, OnboardingID: api.DeterministicID("onboard", auth.PrincipalID, suffix),
		PrincipalID: auth.PrincipalID, ContextID: auth.ContextID, RuntimeID: auth.RuntimeID, PolicyVersion: auth.PolicyVersion,
		Mode: api.OnboardingCatalog, DefinitionID: definitionID, DefinitionVersion: "1.0.0",
		SourceURL: source, Phase: phase, Revision: 1, CreatedAt: time.Now().UTC(),
	}
}

// S3.2: deterministic selector — exact id → alias → source URL → basename →
// registry name; ambiguity lists candidates; removed and foreign records
// never match.
func TestConnectorTargetSelector(t *testing.T) {
	store := NewStore()
	auth := m13P3Auth(t)
	gh := m13P3Onboarding(auth, "gh", "github", "https://github.com/github/github-mcp-server", api.PhaseEnabled)
	db := m13P3Onboarding(auth, "db", "dbhub", "https://github.com/bytebase/dbhub", api.PhaseEnabled)
	removed := m13P3Onboarding(auth, "rm", "removed-def", "https://github.com/org/gone-mcp", api.PhaseRemoved)
	foreign := m13P3Onboarding(identity.TelegramEnvelope("bobuser", 8, "runtime", "policy-1"), "fb", "foreign-def", "https://github.com/org/foreign-mcp", api.PhaseEnabled)
	for _, o := range []api.Onboarding{gh, db, removed, foreign} {
		if err := store.PutOnboarding(o); err != nil {
			t.Fatalf("seed %s: %v", o.OnboardingID, err)
		}
	}
	cases := []struct {
		name, target, wantOnboarding string
	}{
		{"exact onboarding id", gh.OnboardingID, gh.OnboardingID},
		{"exact definition id", "github", gh.OnboardingID},
		{"registry alias", "sql", db.OnboardingID},
		{"source url", "https://github.com/bytebase/dbhub", db.OnboardingID},
		{"repo basename", "github-mcp-server", gh.OnboardingID},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := store.ResolveConnectorTarget(auth, tc.target)
			if err != nil {
				t.Fatalf("resolve %q: %v", tc.target, err)
			}
			if got.OnboardingID != tc.wantOnboarding {
				t.Fatalf("resolve %q = %q, want %q", tc.target, got.OnboardingID, tc.wantOnboarding)
			}
		})
	}
	for _, target := range []string{"gone-mcp", "removed-def", removed.OnboardingID, "foreign-mcp", "foreign-def", foreign.OnboardingID} {
		if _, err := store.ResolveConnectorTarget(auth, target); err == nil {
			t.Fatalf("removed/foreign target %q resolved", target)
		}
	}
}

// S3.2: ambiguity lists candidates and fails with the typed
// ambiguous_target code; superseded records are never candidates.
func TestConnectorTargetAmbiguity(t *testing.T) {
	store := NewStore()
	auth := m13P3Auth(t)
	one := m13P3Onboarding(auth, "a", "alpha-mcp", "https://github.com/orgone/shared-name", api.PhaseEnabled)
	two := m13P3Onboarding(auth, "b", "beta-mcp", "https://github.com/orgtwo/shared-name", api.PhaseEnabled)
	superseded := m13P3Onboarding(auth, "s", "gamma-mcp", "https://github.com/orgthree/shared-name", api.PhaseEnabled)
	superseded.SupersededBy = two.OnboardingID
	for _, o := range []api.Onboarding{one, two, superseded} {
		if err := store.PutOnboarding(o); err != nil {
			t.Fatalf("seed: %v", err)
		}
	}
	_, err := store.ResolveConnectorTarget(auth, "shared-name")
	var ambiguous *ErrAmbiguousTarget
	if !errors.As(err, &ambiguous) {
		t.Fatalf("expected ambiguous_target, got %v", err)
	}
	if len(ambiguous.Candidates) != 2 {
		t.Fatalf("expected 2 candidates (superseded excluded), got %+v", ambiguous.Candidates)
	}
	seen := map[string]bool{}
	for _, candidate := range ambiguous.Candidates {
		seen[candidate.ID] = true
	}
	if !seen[one.OnboardingID] || !seen[two.OnboardingID] {
		t.Fatalf("candidates missing live records: %+v", ambiguous.Candidates)
	}
}

// S3.2: connection ids resolve; registry display names resolve through the
// catalog; failed records never match.
func TestConnectorTargetRegistryAndFailed(t *testing.T) {
	store := NewStore()
	auth := m13P3Auth(t)
	conn := m13P3Onboarding(auth, "c", "dbhub", "https://github.com/bytebase/dbhub", api.PhaseEnabled)
	conn.ConnectionID = "conn-deadbeef00"
	failed := m13P3Onboarding(auth, "f", "failed-def", "https://github.com/org/dead-mcp", api.PhaseFailed)
	for _, o := range []api.Onboarding{conn, failed} {
		if err := store.PutOnboarding(o); err != nil {
			t.Fatalf("seed: %v", err)
		}
	}
	got, err := store.ResolveConnectorTarget(auth, "conn-deadbeef00")
	if err != nil || got.OnboardingID != conn.OnboardingID {
		t.Fatalf("connection id did not resolve: %v %+v", err, got)
	}
	got, err = store.ResolveConnectorTarget(auth, "DBHub (fallback SQL)")
	if err != nil || got.OnboardingID != conn.OnboardingID {
		t.Fatalf("registry name did not resolve: %v %+v", err, got)
	}
	for _, target := range []string{"dead-mcp", "failed-def", failed.OnboardingID} {
		if _, err := store.ResolveConnectorTarget(auth, target); err == nil {
			t.Fatalf("failed target %q resolved", target)
		}
	}
}

// S3.3: a profile with connectors=personal projects exactly the nine
// connector_* tools and no legacy control operation.
func TestConnectorToolsProjection(t *testing.T) {
	s, auth, _, profile := managedStore(t)
	profile.Revision++
	profile.Connectors = api.ConnectorsPersonal
	profile.ControlOperations = []string{"status", "remove"}
	if err := putProfile(t, s, profile); err != nil {
		t.Fatal(err)
	}
	g := &Gateway{Store: s, Tokens: map[string]identity.Envelope{aliceToken: auth},
		Control: &ControlPlane{Store: s, Now: time.Now},
		Backend: backendFunc(func(context.Context, api.EffectiveBinding, api.ToolSpec, map[string]any) (api.BackendResult, error) {
			return api.BackendResult{Text: "ok"}, nil
		})}
	handler, err := g.Handler()
	if err != nil {
		t.Fatal(err)
	}
	server := httptest.NewServer(handler)
	t.Cleanup(server.Close)
	client := mcpConnect(t, server.URL, aliceToken)
	listed, err := client.ListTools(t.Context(), nil)
	if err != nil {
		t.Fatal(err)
	}
	want := map[string]bool{}
	for _, name := range api.ConnectorTools {
		want[name] = true
	}
	want["workspace_read"] = true // capability selection still projects
	got := map[string]bool{}
	for _, tool := range listed.Tools {
		got[tool.Name] = true
		if strings.HasPrefix(tool.Name, "connector_") && !want[tool.Name] {
			t.Fatalf("unexpected connector tool %q", tool.Name)
		}
	}
	for name := range want {
		if !got[name] {
			t.Fatalf("projection missing %q; tools: %v", name, got)
		}
	}
	for _, legacy := range api.ControlOperations {
		if got[legacy] {
			t.Fatalf("legacy control op %q leaked into connector projection", legacy)
		}
	}
	if got["invoke"] {
		t.Fatal("invoke leaked into connector projection")
	}
}

// S3.3: the connectors bundle is a closed enum; unknown values fail profile
// validation.
func TestConnectorsBundleValidation(t *testing.T) {
	_, _, _, profile := managedStore(t)
	for _, value := range []string{"", api.ConnectorsNone, api.ConnectorsPersonal, api.ConnectorsPersonalTeam} {
		p := profile
		p.Connectors = value
		if err := p.ValidateStructure(); err != nil {
			t.Fatalf("connectors %q rejected: %v", value, err)
		}
	}
	p := profile
	p.Connectors = "everyone"
	if err := p.ValidateStructure(); err == nil {
		t.Fatal("invalid connectors value accepted")
	}
}

// S3.3: personal+team admits the same nine tools (team scope is enforced
// per operation); none admits nothing.
func TestConnectorsBundleAllowedOps(t *testing.T) {
	s, auth, _, profile := managedStore(t)
	profile.Revision++
	profile.Connectors = api.ConnectorsPersonalTeam
	if err := putProfile(t, s, profile); err != nil {
		t.Fatal(err)
	}
	allowed, err := s.AllowedControlOperations(auth)
	if err != nil {
		t.Fatal(err)
	}
	for _, tool := range api.ConnectorTools {
		if !allowed[tool] {
			t.Fatalf("personal+team lost %q", tool)
		}
	}
	if len(allowed) != len(api.ConnectorTools) {
		t.Fatalf("personal+team leaked legacy ops: %v", allowed)
	}
	profile.Revision++
	profile.Connectors = api.ConnectorsNone
	profile.ControlOperations = []string{"status"} // legacy list must not leak under none
	if err := putProfile(t, s, profile); err != nil {
		t.Fatal(err)
	}
	allowed, err = s.AllowedControlOperations(auth)
	if err != nil || len(allowed) != 0 {
		t.Fatalf("none must admit nothing: %v %v", allowed, err)
	}
}

// S3.4: connector_list returns the caller's own connectors with the uniform
// result shape; foreign connectors never appear.
func TestConnectorList(t *testing.T) {
	store, auth, _, profile := managedStore(t)
	profile.Revision++
	profile.Connectors = api.ConnectorsPersonal
	if err := putProfile(t, store, profile); err != nil {
		t.Fatal(err)
	}
	mine := m13P3Onboarding(auth, "gh", "github", "https://github.com/github/github-mcp-server", api.PhaseEnabled)
	foreign := m13P3Onboarding(identity.TelegramEnvelope("bobuser", 8, "runtime", "policy-1"), "fb", "foreign-def", "https://github.com/org/foreign-mcp", api.PhaseEnabled)
	for _, o := range []api.Onboarding{mine, foreign} {
		if err := store.PutOnboarding(o); err != nil {
			t.Fatal(err)
		}
	}
	c := &ControlPlane{Store: store, Now: time.Now}
	result, err := c.InvokeConnector(context.Background(), auth, "connector_list", nil)
	if err != nil {
		t.Fatal(err)
	}
	if result.State != api.OpStateDone {
		t.Fatalf("list state: %+v", result)
	}
	if len(result.Candidates) != 1 || result.Candidates[0].ID != mine.OnboardingID {
		t.Fatalf("list must contain exactly my connector: %+v", result.Candidates)
	}
	if result.Candidates[0].Source != mine.SourceURL || result.Candidates[0].Kind == "" {
		t.Fatalf("candidate fields: %+v", result.Candidates[0])
	}
	// A principal without the connectors bundle cannot call the facade.
	unmanaged := m13P3Auth(t)
	empty := NewStore()
	c2 := &ControlPlane{Store: empty, Now: time.Now}
	if _, err := c2.InvokeConnector(context.Background(), unmanaged, "connector_list", nil); !errors.Is(err, api.ErrUnauthorized) {
		t.Fatalf("unmanaged caller admitted: %v", err)
	}
	// Authority-laundering arguments are rejected before dispatch.
	if _, err := c.InvokeConnector(context.Background(), auth, "connector_list", map[string]any{"owner": "bobuser"}); err == nil {
		t.Fatal("authority argument accepted")
	}
}

// S3.4: connector_search queries the reviewed catalogs and returns typed
// candidates — never credentials or URLs the caller did not name.
func TestConnectorSearch(t *testing.T) {
	store, auth, _, profile := managedStore(t)
	profile.Revision++
	profile.Connectors = api.ConnectorsPersonal
	if err := putProfile(t, store, profile); err != nil {
		t.Fatal(err)
	}
	c := &ControlPlane{Store: store, Now: time.Now}
	result, err := c.InvokeConnector(context.Background(), auth, "connector_search", map[string]any{"query": "github"})
	if err != nil {
		t.Fatal(err)
	}
	if result.State != api.OpStateDone || len(result.Candidates) == 0 {
		t.Fatalf("search found nothing: %+v", result)
	}
	found := false
	for _, candidate := range result.Candidates {
		if candidate.ID == "github" {
			found = true
		}
	}
	if !found {
		t.Fatalf("github entry missing: %+v", result.Candidates)
	}
	// An empty query is a typed failure, not a panic.
	empty, err := c.InvokeConnector(context.Background(), auth, "connector_search", map[string]any{})
	if err == nil && empty.Code == "" {
		t.Fatalf("empty query accepted: %+v", empty)
	}
}

// S3.4: connector_status maps the target's phase onto the operation-state
// vocabulary and resolves operation ids too.
func TestConnectorStatus(t *testing.T) {
	store, auth, _, profile := managedStore(t)
	profile.Revision++
	profile.Connectors = api.ConnectorsPersonal
	if err := putProfile(t, store, profile); err != nil {
		t.Fatal(err)
	}
	enabled := m13P3Onboarding(auth, "gh", "github", "https://github.com/github/github-mcp-server", api.PhaseEnabled)
	waiting := m13P3Onboarding(auth, "wt", "gitlab", "https://gitlab.example.com/mcp", api.PhaseAwaitingCreds)
	for _, o := range []api.Onboarding{enabled, waiting} {
		if err := store.PutOnboarding(o); err != nil {
			t.Fatal(err)
		}
	}
	c := &ControlPlane{Store: store, Now: time.Now}
	result, err := c.InvokeConnector(context.Background(), auth, "connector_status", map[string]any{"target": "github"})
	if err != nil {
		t.Fatal(err)
	}
	if result.State != api.OpStateDone {
		t.Fatalf("enabled connector status: %+v", result)
	}
	result, err = c.InvokeConnector(context.Background(), auth, "connector_status", map[string]any{"target": "gitlab"})
	if err != nil {
		t.Fatal(err)
	}
	if result.State != api.OpStateAwaitingUser {
		t.Fatalf("awaiting-credentials connector status: %+v", result)
	}
	// operation_id lookups reach the journal.
	op := api.Operation{Schema: api.SchemaVersion, OperationID: api.DeterministicID("op", "st"),
		Intent: api.OpIntentInstall, Target: "github", RequestKey: api.OperationRequestKey(auth, api.OpIntentInstall, "github"),
		PrincipalID: auth.PrincipalID, ContextID: auth.ContextID, RuntimeID: auth.RuntimeID, PolicyVersion: auth.PolicyVersion,
		State: api.OpStateAwaitingConfirm, CreatedAt: time.Now().UTC()}
	if err := store.PutOperation(op); err != nil {
		t.Fatal(err)
	}
	result, err = c.InvokeConnector(context.Background(), auth, "connector_status", map[string]any{"operation_id": op.OperationID})
	if err != nil {
		t.Fatal(err)
	}
	if result.State != api.OpStateAwaitingConfirm || result.OperationID != op.OperationID {
		t.Fatalf("operation status: %+v", result)
	}
	// A foreign operation stays invisible.
	foreign := identity.TelegramEnvelope("bobuser", 8, "runtime", "policy-1")
	if _, err := c.InvokeConnector(context.Background(), foreign, "connector_status", map[string]any{"operation_id": op.OperationID}); err == nil {
		t.Fatal("foreign operation status leaked")
	}
}

// S3.5: connector_install journals one operation; a credentialed catalog
// target ends awaiting_user with link_sent; a repeat call is idempotent.
func TestConnectorInstallJournal(t *testing.T) {
	store, auth, _, profile := managedStore(t)
	profile.Revision++
	profile.Connectors = api.ConnectorsPersonal
	if err := putProfile(t, store, profile); err != nil {
		t.Fatal(err)
	}
	c := &ControlPlane{Store: store, Now: time.Now, ConfirmationTTL: time.Minute}
	result, err := c.InvokeConnector(context.Background(), auth, "connector_install", map[string]any{"target": "google-work", "version": "2.0.0"})
	if err != nil {
		t.Fatal(err)
	}
	if result.OperationID == "" || result.State != api.OpStateAwaitingUser || !result.LinkSent {
		t.Fatalf("install result: %+v", result)
	}
	op, err := store.OperationFor(auth, result.OperationID)
	if err != nil {
		t.Fatal(err)
	}
	if op.Intent != api.OpIntentInstall || op.OnboardingID == "" {
		t.Fatalf("operation: %+v", op)
	}
	stepStates := map[string]string{}
	for _, step := range op.Steps {
		stepStates[step.Name] = step.State
	}
	if stepStates[api.OpStepResolve] != api.OpStepDone || stepStates[api.OpStepFetchArtifact] != api.OpStepDone || stepStates[api.OpStepCollectCredentials] != api.OpStepDone {
		t.Fatalf("install journal steps: %+v", op.Steps)
	}
	// The onboarding really waits on the protected form.
	onboarding, err := store.OnboardingFor(auth, op.OnboardingID)
	if err != nil || onboarding.Phase != api.PhaseAwaitingCreds || onboarding.FormNonce == "" {
		t.Fatalf("onboarding after install: %v %+v", err, onboarding)
	}
	// Same request key → same operation, no duplicate onboarding.
	again, err := c.InvokeConnector(context.Background(), auth, "connector_install", map[string]any{"target": "google-work", "version": "2.0.0"})
	if err != nil {
		t.Fatal(err)
	}
	if again.OperationID != result.OperationID {
		t.Fatalf("idempotent install opened a second operation: %q vs %q", again.OperationID, result.OperationID)
	}
}

// S3.5: after the human steps land the connector enabled, resuming the
// journaled operation completes every remaining step exactly once — the
// install tool reports done, not a fresh run.
func TestConnectorInstallResumeCompletion(t *testing.T) {
	store, auth, _, profile := managedStore(t)
	profile.Revision++
	profile.Connectors = api.ConnectorsPersonal
	if err := putProfile(t, store, profile); err != nil {
		t.Fatal(err)
	}
	c := &ControlPlane{Store: store, Now: time.Now, ConfirmationTTL: time.Minute}
	result, err := c.InvokeConnector(context.Background(), auth, "connector_install", map[string]any{"target": "google-work", "version": "2.0.0"})
	if err != nil {
		t.Fatal(err)
	}
	op, err := store.OperationFor(auth, result.OperationID)
	if err != nil {
		t.Fatal(err)
	}
	// Simulate the human path completing out-of-band: the connector is
	// enabled under the same onboarding record.
	onboarding, err := store.OnboardingFor(auth, op.OnboardingID)
	if err != nil {
		t.Fatal(err)
	}
	onboarding.Phase = api.PhaseEnabled
	if err := store.PutOnboarding(onboarding); err != nil {
		t.Fatal(err)
	}
	resumed, err := c.InvokeConnector(context.Background(), auth, "connector_install", map[string]any{"target": "google-work", "version": "2.0.0"})
	if err != nil {
		t.Fatal(err)
	}
	if resumed.OperationID != result.OperationID || resumed.State != api.OpStateDone {
		t.Fatalf("resumed install: %+v", resumed)
	}
	finished, err := store.OperationFor(auth, result.OperationID)
	if err != nil {
		t.Fatal(err)
	}
	for _, step := range finished.Steps {
		if step.State != api.OpStepDone {
			t.Fatalf("step %s not completed on resume: %+v", step.Name, finished.Steps)
		}
	}
	// A third call reports the finished operation — no side effects rerun.
	third, err := c.InvokeConnector(context.Background(), auth, "connector_install", map[string]any{"target": "google-work", "version": "2.0.0"})
	if err != nil || third.State != api.OpStateDone {
		t.Fatalf("third install: %v %+v", err, third)
	}
}

// S3.5: a catalog CLI definition installs through the same journal and
// lands the same onboarding shape the primitive catalog path produces —
// no credentials means the human gate is confirmation, not a form.
func TestConnectorInstallCatalogCLI(t *testing.T) {
	store, auth, policy, profile := managedStore(t)
	definition := api.CliRipgrepDefinition()
	if err := store.RegisterDefinition(definition); err != nil {
		t.Fatal(err)
	}
	connection := api.Connection{Schema: api.SchemaVersion, ConnectionID: "cli-rg", Owner: api.OwnerRef{Type: api.PrincipalOwner, ID: "alice"}, DefinitionID: definition.DefinitionID, Revision: 1, Status: api.ActiveStatus}
	if err := store.PutConnection(connection); err != nil {
		t.Fatal(err)
	}
	binding := api.ToolBinding{Schema: api.SchemaVersion, PrincipalID: "alice", ContextID: "alice", RuntimeID: "runtime", DefinitionID: definition.DefinitionID, DefinitionVersion: definition.Version, ConnectionID: connection.ConnectionID, ConnectionRevision: connection.Revision, PolicyVersion: auth.PolicyVersion, WorkloadClass: definition.Workload.Class, Status: api.ActiveStatus, Revision: 1, ProjectionRevision: 1}
	binding.ToolBindingID = api.DeterministicBindingID(binding.PrincipalID, binding.ContextID, binding.RuntimeID, binding.DefinitionID, binding.DefinitionVersion, binding.ConnectionID, binding.CredentialRefID)
	if err := store.PutBinding(binding); err != nil {
		t.Fatal(err)
	}
	rule := api.CapabilityRule{CapabilityID: "cli.search", ImplementationDigest: api.DefinitionDigest(definition), Action: "search", Resource: "files", ConnectionID: connection.ConnectionID, Limits: api.CapabilityLimits{OutputBytes: 8192, TimeoutSeconds: 10}}
	policy.Revision++
	policy.Ceiling = append(policy.Ceiling, rule)
	policy.Defaults = append(policy.Defaults, rule)
	if err := putPolicy(t, store, policy); err != nil {
		t.Fatal(err)
	}
	profile.Revision++
	profile.PolicyRevision = policy.Revision
	profile.Connectors = api.ConnectorsPersonal
	profile.Selections = append(profile.Selections, api.CapabilitySelection{CapabilityID: "cli.search", DefinitionID: definition.DefinitionID, DefinitionVersion: definition.Version,
		ImplementationDigest: api.DefinitionDigest(definition), ToolName: "search", Name: "rg_search", ConnectionID: connection.ConnectionID})
	if err := putProfile(t, store, profile); err != nil {
		t.Fatal(err)
	}
	c := &ControlPlane{Store: store, Now: time.Now, ConfirmationTTL: time.Minute}
	result, err := c.InvokeConnector(context.Background(), auth, "connector_install", map[string]any{"target": "cli-rg-search", "version": definition.Version})
	if err != nil {
		t.Fatal(err)
	}
	if result.OperationID == "" || result.State != api.OpStateAwaitingConfirm || !result.LinkSent {
		t.Fatalf("cli catalog install result: %+v", result)
	}
	op, err := store.OperationFor(auth, result.OperationID)
	if err != nil {
		t.Fatal(err)
	}
	onboarding, err := store.OnboardingFor(auth, op.OnboardingID)
	if err != nil || onboarding.Mode != api.OnboardingCatalog || onboarding.Phase != api.PhaseAwaitingConfirm || onboarding.ConfirmationNonce == "" {
		t.Fatalf("cli catalog onboarding: %v %+v", err, onboarding)
	}
}

// S3.5: concurrent installs of the same target serialize onto one
// operation and one onboarding.
func TestConnectorInstallConcurrent(t *testing.T) {
	store, auth, _, profile := managedStore(t)
	profile.Revision++
	profile.Connectors = api.ConnectorsPersonal
	if err := putProfile(t, store, profile); err != nil {
		t.Fatal(err)
	}
	c := &ControlPlane{Store: store, Now: time.Now, ConfirmationTTL: time.Minute}
	ids := make(chan string, 8)
	var wg sync.WaitGroup
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			result, err := c.InvokeConnector(context.Background(), auth, "connector_install", map[string]any{"target": "google-work", "version": "2.0.0"})
			if err != nil {
				t.Error(err)
				return
			}
			ids <- result.OperationID
		}()
	}
	wg.Wait()
	close(ids)
	var first string
	for id := range ids {
		if first == "" {
			first = id
		} else if id != first {
			t.Fatalf("concurrent installs diverged: %q vs %q", first, id)
		}
	}
	var onboardings int
	for _, o := range store.onboardings {
		if o.DefinitionID == "google-work" && o.Mode == api.OnboardingCatalog {
			onboardings++
		}
	}
	if onboardings != 1 {
		t.Fatalf("concurrent install created %d onboardings", onboardings)
	}
}

// S3.5: a prepared-catalog id installs through the same journal. Without a
// reviewer the pipeline fails the onboarding and the operation records the
// failure instead of hanging.
func TestConnectorInstallPrepared(t *testing.T) {
	store, auth, _, profile := managedStore(t)
	profile.Revision++
	profile.Connectors = api.ConnectorsPersonal
	profile.SelfInstall = true
	if err := putProfile(t, store, profile); err != nil {
		t.Fatal(err)
	}
	c := &ControlPlane{Store: store, Now: time.Now, ConfirmationTTL: time.Minute, PrepareSyncWindow: -1}
	result, err := c.InvokeConnector(context.Background(), auth, "connector_install", map[string]any{"target": "github"})
	if err != nil {
		t.Fatal(err)
	}
	if result.OperationID == "" || result.State != api.OpStateFailed {
		t.Fatalf("prepared install result: %+v", result)
	}
	op, err := store.OperationFor(auth, result.OperationID)
	if err != nil {
		t.Fatal(err)
	}
	if op.State != api.OpStateFailed || op.OnboardingID == "" {
		t.Fatalf("operation: %+v", op)
	}
	onboarding, err := store.OnboardingFor(auth, op.OnboardingID)
	if err != nil || (onboarding.Mode != api.OnboardingSelfInstall && onboarding.Mode != api.OnboardingRemote) {
		t.Fatalf("prepared install onboarding: %v %+v", err, onboarding)
	}
}
