package toolhub

import (
	"context"
	"encoding/json"
	"errors"
	"github.com/letya999/hermes-hub/internal/toolhub/api"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/letya999/hermes-hub/internal/identity"
)

// /v1/connectors is the read-only per-principal inventory communication-hub
// falls back to when the local registry file is unavailable. A principal's
// bearer pins its own envelope; the control-plane bearer selects a principal
// through X-Hub-Principal and can never widen beyond enrolled envelopes.
func TestConnectorsListsOnlyCallerProjection(t *testing.T) {
	store, auth, _ := seededStore(t)
	bob := identity.TelegramEnvelope("bob", 8, "runtime-bob", "policy-1")
	g := &Gateway{
		Store:        store,
		ControlToken: "control-plane-token-which-is-32-chars!",
		Backend:      api.RoutingBackend{},
		Tokens: map[string]identity.Envelope{
			"alice-principal-token-0000000000000": auth,
			"bob-principal-token-000000000000000": bob,
		},
		projections: map[string]*gatewayProjection{
			"alice-principal-token-0000000000000": {auth: auth},
			"bob-principal-token-000000000000000": {auth: bob},
		},
	}
	attachGatewayAudit(t, g)
	handler, err := g.Handler()
	if err != nil {
		t.Fatal(err)
	}
	call := func(token, principal string) (*httptest.ResponseRecorder, map[string][]struct {
		DefinitionID string   `json:"definition_id"`
		Transport    string   `json:"transport"`
		Tools        []string `json:"tools"`
	}) {
		req := httptest.NewRequest(http.MethodGet, "/v1/connectors", nil)
		if token != "" {
			req.Header.Set("Authorization", "Bearer "+token)
		}
		if principal != "" {
			req.Header.Set("X-Hub-Principal", principal)
		}
		w := httptest.NewRecorder()
		handler.ServeHTTP(w, req)
		var payload struct {
			Connectors []struct {
				DefinitionID string   `json:"definition_id"`
				Transport    string   `json:"transport"`
				Tools        []string `json:"tools"`
			} `json:"connectors"`
		}
		_ = json.Unmarshal(w.Body.Bytes(), &payload)
		return w, map[string][]struct {
			DefinitionID string   `json:"definition_id"`
			Transport    string   `json:"transport"`
			Tools        []string `json:"tools"`
		}{"connectors": payload.Connectors}
	}
	w, payload := call("alice-principal-token-0000000000000", "")
	if w.Code != http.StatusOK || len(payload["connectors"]) != 1 || payload["connectors"][0].DefinitionID != "google-work" {
		t.Fatalf("alice connectors: %d %v", w.Code, payload)
	}
	// The control-plane bearer reaches the same envelope through the header.
	w, payload = call("control-plane-token-which-is-32-chars!", "alice")
	if w.Code != http.StatusOK || len(payload["connectors"]) != 1 {
		t.Fatalf("control token connectors: %d %v", w.Code, payload)
	}
	// A principal bearer ignores the header entirely: bob stays bob.
	w, payload = call("bob-principal-token-000000000000000", "alice")
	if w.Code != http.StatusOK || len(payload["connectors"]) != 0 {
		t.Fatalf("bob must see his own (empty) inventory: %d %v", w.Code, payload)
	}
	// The header alone never authenticates; unknown principals get 401.
	for _, tc := range [][2]string{
		{"control-plane-token-which-is-32-chars!", "mallory"},
		{"control-plane-token-which-is-32-chars!", ""},
		{"", "alice"},
		{"mallory-token-00000000000000000000000", "alice"},
	} {
		if w, _ := call(tc[0], tc[1]); w.Code != http.StatusUnauthorized {
			t.Fatalf("token=%q principal=%q → %d, want 401", tc[0], tc[1], w.Code)
		}
	}
}

func TestConnectorsHTTPIncludesScopedConnectionInventory(t *testing.T) {
	store, auth, _ := seededStore(t)
	bob := identity.TelegramEnvelope("bob", 8, "runtime-bob", "policy-1")
	store.connections["bob-only"] = api.Connection{ConnectionID: "bob-only", DefinitionID: "bob-tool", Owner: api.OwnerRef{Type: api.PrincipalOwner, ID: bob.PrincipalID}, Status: api.DegradedStatus}
	control := &ControlPlane{Store: store}
	g := &Gateway{Store: store, Control: control, Backend: api.RoutingBackend{}, Tokens: map[string]identity.Envelope{"alice-principal-token-0000000000000": auth}, projections: map[string]*gatewayProjection{"alice-principal-token-0000000000000": {auth: auth}}}
	attachGatewayAudit(t, g)
	handler, err := g.Handler()
	if err != nil {
		t.Fatal(err)
	}
	req := httptest.NewRequest(http.MethodGet, "/v1/connectors", nil)
	req.Header.Set("Authorization", "Bearer alice-principal-token-0000000000000")
	w := httptest.NewRecorder()
	handler.ServeHTTP(w, req)
	var body struct {
		Inventory []api.OperationCandidate `json:"inventory"`
	}
	if w.Code != http.StatusOK || json.Unmarshal(w.Body.Bytes(), &body) != nil {
		t.Fatalf("HTTP inventory response: %d", w.Code)
	}
	listed, err := control.connectorList(auth)
	if err != nil {
		t.Fatal(err)
	}
	if len(body.Inventory) != len(listed.Candidates) || len(body.Inventory) == 0 {
		t.Fatalf("HTTP inventory differs from connector_list: %+v vs %+v", body.Inventory, listed.Candidates)
	}
	for _, item := range body.Inventory {
		if item.ConnectionID == "bob-only" {
			t.Fatal("foreign connection leaked")
		}
	}
}

// /v1/cli-jobs/release is the job-end hook: auth pins the principal the same
// way /v1/connectors does, so a caller can only release its own task cells.
func TestCLIJobReleasePinsPrincipalAndJob(t *testing.T) {
	store, auth, _ := seededStore(t)
	bob := identity.TelegramEnvelope("bob", 8, "runtime-bob", "policy-1")
	var got api.CliReleaseRequest
	g := &Gateway{
		Store:        store,
		ControlToken: "control-plane-token-which-is-32-chars!",
		Backend:      api.RoutingBackend{},
		Tokens: map[string]identity.Envelope{
			"alice-principal-token-0000000000000": auth,
			"bob-principal-token-000000000000000": bob,
		},
		projections: map[string]*gatewayProjection{
			"alice-principal-token-0000000000000": {auth: auth},
			"bob-principal-token-000000000000000": {auth: bob},
		},
		CLIRelease: func(_ context.Context, release api.CliReleaseRequest) error {
			got = release
			return nil
		},
	}
	attachGatewayAudit(t, g)
	handler, err := g.Handler()
	if err != nil {
		t.Fatal(err)
	}
	call := func(token, principal, body string) *httptest.ResponseRecorder {
		req := httptest.NewRequest(http.MethodPost, "/v1/cli-jobs/release", strings.NewReader(body))
		if token != "" {
			req.Header.Set("Authorization", "Bearer "+token)
		}
		if principal != "" {
			req.Header.Set("X-Hub-Principal", principal)
		}
		w := httptest.NewRecorder()
		handler.ServeHTTP(w, req)
		return w
	}
	if w := call("alice-principal-token-0000000000000", "", `{"job_id":"job-1"}`); w.Code != http.StatusNoContent {
		t.Fatalf("alice release: %d", w.Code)
	}
	if got.PrincipalID != "alice" || got.JobID != "job-1" || got.Reason != "job-end" {
		t.Fatalf("release=%+v", got)
	}
	// The control-plane bearer selects the principal through the header.
	got = api.CliReleaseRequest{}
	if w := call("control-plane-token-which-is-32-chars!", "bob", `{"job_id":"job-2"}`); w.Code != http.StatusNoContent {
		t.Fatalf("control release: %d", w.Code)
	}
	if got.PrincipalID != "bob" || got.JobID != "job-2" {
		t.Fatalf("control release=%+v", got)
	}
	// Bad shapes and foreign headers fail closed.
	for _, tc := range []struct{ token, principal, body string }{
		{"alice-principal-token-0000000000000", "", `{"job_id":"!"}`},
		{"alice-principal-token-0000000000000", "", `{"job_id":"x","extra":1}`},
		{"alice-principal-token-0000000000000", "", `not-json`},
		{"mallory-token-00000000000000000000000", "", `{"job_id":"job-1"}`},
		{"control-plane-token-which-is-32-chars!", "mallory", `{"job_id":"job-1"}`},
	} {
		if w := call(tc.token, tc.principal, tc.body); w.Code == http.StatusNoContent {
			t.Fatalf("%+v → %d, want denial", tc, w.Code)
		}
	}
	// GET is refused; the release is a mutation.
	if w := func() *httptest.ResponseRecorder {
		req := httptest.NewRequest(http.MethodGet, "/v1/cli-jobs/release", nil)
		req.Header.Set("Authorization", "Bearer alice-principal-token-0000000000000")
		w := httptest.NewRecorder()
		handler.ServeHTTP(w, req)
		return w
	}(); w.Code != http.StatusMethodNotAllowed {
		t.Fatalf("GET release: %d", w.Code)
	}
}

// The restart notifier adopts the first snapshot as baseline, then POSTs
// /v1/restart-request once per changed projection; failures stay unacked so
// the next tick retries them.
func TestRuntimeRestartNotifierPrimesDiffsAndRetries(t *testing.T) {
	var posts []string
	var fail bool
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/restart-request" || r.Header.Get("Authorization") != "Bearer control-token" {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		var body map[string]string
		_ = json.NewDecoder(r.Body).Decode(&body)
		if fail {
			w.WriteHeader(http.StatusBadGateway)
			return
		}
		posts = append(posts, body["principal_id"])
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()
	n := &api.RuntimeRestartNotifier{URL: srv.URL, Token: "control-token"}
	snapshot := []api.ProjectionTarget{{PrincipalID: "alice", ContextID: "alice", RuntimeID: "alice", Revision: 2}}
	if err := n.Notify(snapshot); err != nil || len(posts) != 0 {
		t.Fatalf("first snapshot must prime, not notify: %v %v", err, posts)
	}
	snapshot = append(snapshot, api.ProjectionTarget{PrincipalID: "bob", ContextID: "bob", RuntimeID: "bob", Revision: 1})
	snapshot[0].Revision = 3
	if err := n.Notify(snapshot); err != nil || len(posts) != 2 {
		t.Fatalf("changed+new targets notified once: %v %v", err, posts)
	}
	// A failed post is not acknowledged: the next tick retries it.
	fail = true
	snapshot[0].Revision = 4
	if err := n.Notify(snapshot); err == nil {
		t.Fatal("failed notify must surface the error")
	}
	fail = false
	if err := n.Notify(snapshot); err != nil || posts[len(posts)-1] != "alice" {
		t.Fatalf("unacked revision must retry: %v %v", err, posts)
	}
}

// ProjectionRevisions is the notifier's snapshot source: one target per
// persisted (principal, context, runtime) projection, revision ≥ 1.
func TestProjectionRevisionsSnapshotsBoundTargets(t *testing.T) {
	store, _, _ := seededStore(t)
	targets, err := store.ProjectionRevisions()
	if err != nil {
		t.Fatal(err)
	}
	if len(targets) != 1 || targets[0].PrincipalID != "alice" || targets[0].RuntimeID != "runtime" || targets[0].Revision == 0 {
		t.Fatalf("targets=%+v", targets)
	}
}

// runtimerestartnotifierfromenv stays nil without the supervisor pair, so
// unmanaged deployments keep the file-marker contract.
func TestRuntimeRestartNotifierFromEnvRequiresSupervisor(t *testing.T) {
	t.Setenv("HUB_RUNTIME_SUPERVISOR_URL", "")
	t.Setenv("HUB_COMMUNICATION_AUTH", "")
	if api.RuntimeRestartNotifierFromEnv() != nil {
		t.Fatal("notifier without supervisor config")
	}
	t.Setenv("HUB_RUNTIME_SUPERVISOR_URL", "http://supervisor:8876/")
	t.Setenv("HUB_COMMUNICATION_AUTH", "token")
	n := api.RuntimeRestartNotifierFromEnv()
	if n == nil || n.URL != "http://supervisor:8876" || n.Token != "token" {
		t.Fatalf("notifier env: %+v", n)
	}
}

func TestRestartNotifierRecoversUnappliedRevision(t *testing.T) {
	store := NewStore()
	path := filepath.Join(t.TempDir(), "store.json")
	if err := store.Save(path); err != nil {
		t.Fatal(err)
	}
	var err error
	store, err = Load(path)
	if err != nil {
		t.Fatal(err)
	}
	target := api.ProjectionTarget{PrincipalID: "alice", ContextID: "alice", RuntimeID: "runtime", Revision: 3}
	posts := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		posts++
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()
	n := &api.RuntimeRestartNotifier{URL: srv.URL, Token: "fixture", State: store}
	if err := n.Notify([]api.ProjectionTarget{target}); err != nil || posts != 1 {
		t.Fatalf("first notify: posts=%d err=%v", posts, err)
	}
	reloaded, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	n = &api.RuntimeRestartNotifier{URL: srv.URL, Token: "fixture", State: reloaded}
	if err := n.Notify([]api.ProjectionTarget{target}); err != nil || posts != 1 {
		t.Fatalf("applied revision replayed after restart: posts=%d err=%v", posts, err)
	}
	target.Revision++
	n = &api.RuntimeRestartNotifier{URL: srv.URL, Token: "fixture", State: reloaded}
	if err := n.Notify([]api.ProjectionTarget{target}); err != nil || posts != 2 {
		t.Fatalf("unapplied revision lost after restart: posts=%d err=%v", posts, err)
	}
}

func TestRestartAppliedRevisionCannotRegress(t *testing.T) {
	store := NewStore()
	target := api.ProjectionTarget{PrincipalID: "alice", ContextID: "alice", RuntimeID: "runtime", Revision: 4}
	if err := store.MarkRestartApplied(target); err != nil {
		t.Fatal(err)
	}
	target.Revision = 3
	if err := store.MarkRestartApplied(target); err != nil {
		t.Fatal(err)
	}
	if got := store.AppliedRestartRevision(target); got != 4 {
		t.Fatalf("applied revision regressed: %d", got)
	}
}

func TestRestartAppliedRevisionReloadsAcrossStoreInstances(t *testing.T) {
	path := filepath.Join(t.TempDir(), "store.json")
	first := NewStore()
	if err := first.Save(path); err != nil {
		t.Fatal(err)
	}
	var err error
	first, err = Load(path)
	if err != nil {
		t.Fatal(err)
	}
	second, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	target := api.ProjectionTarget{PrincipalID: "alice", ContextID: "alice", RuntimeID: "runtime", Revision: 4}
	if err := first.MarkRestartApplied(target); err != nil {
		t.Fatal(err)
	}
	if err := second.Reload(); err != nil {
		t.Fatal(err)
	}
	if got := second.AppliedRestartRevision(target); got != 4 {
		t.Fatalf("applied revision not reloaded: %d", got)
	}
}

func TestRestartAppliedRevisionWriteConflictDoesNotAcknowledge(t *testing.T) {
	for _, initial := range []uint64{0, 2} {
		t.Run(map[uint64]string{0: "new", 2: "existing"}[initial], func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "store.json")
			first := NewStore()
			if err := first.Save(path); err != nil {
				t.Fatal(err)
			}
			if initial != 0 {
				var err error
				first, err = Load(path)
				if err != nil {
					t.Fatal(err)
				}
				if err := first.MarkRestartApplied(api.ProjectionTarget{PrincipalID: "alice", ContextID: "alice", RuntimeID: "runtime", Revision: initial, Digest: "sha256:old"}); err != nil {
					t.Fatal(err)
				}
			}
			stale, err := Load(path)
			if err != nil {
				t.Fatal(err)
			}
			fresh, err := Load(path)
			if err != nil {
				t.Fatal(err)
			}
			if err := fresh.MarkRestartApplied(api.ProjectionTarget{PrincipalID: "bob", ContextID: "bob", RuntimeID: "runtime", Revision: 1}); err != nil {
				t.Fatal(err)
			}
			target := api.ProjectionTarget{PrincipalID: "alice", ContextID: "alice", RuntimeID: "runtime", Revision: 4, Digest: "sha256:new"}
			if err := stale.MarkRestartApplied(target); !errors.Is(err, api.ErrConflict) {
				t.Fatalf("stale write: %v", err)
			}
			if got := stale.AppliedRestartRevision(target); got != initial {
				t.Fatalf("failed write acknowledged revision %d", got)
			}
			wantDigest := ""
			if initial != 0 {
				wantDigest = "sha256:old"
			}
			if got := stale.AppliedRestartDigest(target); got != wantDigest {
				t.Fatalf("failed write acknowledged digest %q", got)
			}
		})
	}
}

func TestSemanticSurfaceUnchangedDoesNotRestart(t *testing.T) {
	seed, _, binding := seededStore(t)
	path := filepath.Join(t.TempDir(), "store.json")
	if err := seed.Save(path); err != nil {
		t.Fatal(err)
	}
	store, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	posts := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		posts++
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()
	n := &api.RuntimeRestartNotifier{URL: srv.URL, Token: "fixture", State: store}
	targets, err := store.ProjectionRevisions()
	if err != nil || len(targets) != 1 {
		t.Fatalf("initial targets=%v err=%v", targets, err)
	}
	if err := n.Notify(targets); err != nil || posts != 1 {
		t.Fatalf("initial restart posts=%d err=%v", posts, err)
	}
	key := projectionKey("alice", "alice", "runtime")
	store.mu.Lock()
	store.projectionRevisions[key]++ // synthetic metadata-only revision
	store.mu.Unlock()
	if err := store.Save(path); err != nil {
		t.Fatal(err)
	}
	targets, err = store.ProjectionRevisions()
	if err != nil {
		t.Fatal(err)
	}
	if err := n.Notify(targets); err != nil || posts != 1 || store.AppliedRestartRevision(targets[0]) != targets[0].Revision {
		t.Fatalf("metadata-only restart posts=%d applied=%d revision=%d err=%v", posts, store.AppliedRestartRevision(targets[0]), targets[0].Revision, err)
	}
	if err := store.SetBindingStatus(binding.ToolBindingID, api.DisabledStatus); err != nil {
		t.Fatal(err)
	}
	targets, err = store.ProjectionRevisions()
	if err != nil {
		t.Fatal(err)
	}
	if err := n.Notify(targets); err != nil || posts != 2 {
		t.Fatalf("semantic change skipped: posts=%d err=%v", posts, err)
	}
}

func TestSemanticDigestTracksAuthorityAndIgnoresObservation(t *testing.T) {
	store, _, binding := seededStore(t)
	target := func() api.ProjectionTarget {
		t.Helper()
		targets, err := store.ProjectionRevisions()
		if err != nil || len(targets) != 1 {
			t.Fatalf("targets=%v err=%v", targets, err)
		}
		return targets[0]
	}
	before := target().Digest
	if before == "" {
		t.Error("missing semantic digest")
	}
	store.mu.Lock()
	connection := store.connections[binding.ConnectionID]
	connection.HealthCause = "probe timeout"
	store.connections[binding.ConnectionID] = connection
	store.mu.Unlock()
	if got := target().Digest; got != before {
		t.Fatalf("health observation changed digest: %q -> %q", before, got)
	}
	store.mu.Lock()
	credential := store.credentials[binding.CredentialRefID]
	credential.Status = api.RevokedStatus
	store.credentials[binding.CredentialRefID] = credential
	store.mu.Unlock()
	if got := target().Digest; got == before {
		t.Error("credential revocation did not change digest")
	}
	credential.Status = api.ActiveStatus
	store.mu.Lock()
	store.credentials[binding.CredentialRefID] = credential
	for id, grant := range store.grants {
		if grant.PrincipalID == "alice" {
			grant.Status = api.RevokedStatus
			store.grants[id] = grant
			break
		}
	}
	store.mu.Unlock()
	if got := target().Digest; got == before {
		t.Error("grant revocation did not change digest")
	}
	store.mu.Lock()
	for id, grant := range store.grants {
		if grant.PrincipalID == "alice" {
			grant.Status = api.ActiveStatus
			store.grants[id] = grant
			break
		}
	}
	definitionKey := definitionKey(binding.DefinitionID, binding.DefinitionVersion)
	originalDefinition := store.definitions[definitionKey]
	definition := originalDefinition
	definition.Tools = append([]api.ToolSpec(nil), definition.Tools...)
	definition.Tools[0].Name += "_changed"
	store.definitions[definitionKey] = definition
	store.mu.Unlock()
	if got := target().Digest; got == before {
		t.Error("tool surface change did not change digest")
	}
	store.mu.Lock()
	store.definitions[definitionKey] = originalDefinition
	current := store.bindings[binding.ToolBindingID]
	current.PolicyVersion = "policy-2"
	store.bindings[binding.ToolBindingID] = current
	store.mu.Unlock()
	if got := target().Digest; got == before {
		t.Error("policy change did not change digest")
	}
	store.mu.Lock()
	current.PolicyVersion = binding.PolicyVersion
	store.bindings[binding.ToolBindingID] = current
	connection = store.connections[binding.ConnectionID]
	connection.Status = api.DisabledStatus
	store.connections[binding.ConnectionID] = connection
	store.mu.Unlock()
	if got := target().Digest; got == before {
		t.Error("connection disable did not change digest")
	}
	store.mu.Lock()
	connection.Status = api.ActiveStatus
	store.connections[binding.ConnectionID] = connection
	current.CredentialRevision++
	store.bindings[binding.ToolBindingID] = current
	store.mu.Unlock()
	if got := target().Digest; got == before {
		t.Error("credential revision did not change digest")
	}
	store.mu.Lock()
	current.CredentialRevision = binding.CredentialRevision
	current.CredentialRefID = "different-ref"
	store.bindings[binding.ToolBindingID] = current
	store.mu.Unlock()
	if got := target().Digest; got == before {
		t.Error("credential reference did not change digest")
	}
	store.mu.Lock()
	current.CredentialRefID = binding.CredentialRefID
	store.bindings[binding.ToolBindingID] = current
	var grantID string
	var originalGrant api.Grant
	for id, grant := range store.grants {
		if grant.PrincipalID == "alice" {
			grantID, originalGrant = id, grant
			break
		}
	}
	store.mu.Unlock()
	for name, mutate := range map[string]func(*api.Grant){
		"kind":       func(g *api.Grant) { g.Kind = api.GrantControlOperation },
		"definition": func(g *api.Grant) { g.DefinitionID += "-changed" },
		"version":    func(g *api.Grant) { g.DefinitionVersion = "2.0.0" },
		"operation":  func(g *api.Grant) { g.Operation = "revoke" },
	} {
		grant := originalGrant
		mutate(&grant)
		store.mu.Lock()
		store.grants[grantID] = grant
		store.mu.Unlock()
		if got := target().Digest; got == before {
			t.Errorf("grant %s did not change digest", name)
		}
	}
	store.mu.Lock()
	store.grants[grantID] = originalGrant
	store.mu.Unlock()
}

func TestSemanticDigestFallsBackForUnknownAuthority(t *testing.T) {
	store, _, binding := seededStore(t)
	store.mu.Lock()
	store.capabilityProfiles["managed"] = api.CapabilityProfile{PrincipalID: "alice", ContextID: "alice", RuntimeID: "runtime"}
	store.mu.Unlock()
	targets, err := store.ProjectionRevisions()
	if err != nil || len(targets) != 1 || targets[0].Digest != "" {
		t.Fatalf("managed digest must fall back to revision-based restart: %v %v", targets, err)
	}
	store.mu.Lock()
	delete(store.capabilityProfiles, "managed")
	connection := store.connections[binding.ConnectionID]
	connection.Owner.Type = api.TeamOwner
	store.connections[binding.ConnectionID] = connection
	store.mu.Unlock()
	targets, err = store.ProjectionRevisions()
	if err != nil || len(targets) != 1 || targets[0].Digest != "" {
		t.Fatalf("team digest must fall back to revision-based restart: %v %v", targets, err)
	}
	store.mu.Lock()
	connection.Owner.Type = api.PrincipalOwner
	store.connections[binding.ConnectionID] = connection
	definitionKey := definitionKey(binding.DefinitionID, binding.DefinitionVersion)
	definition := store.definitions[definitionKey]
	delete(store.definitions, definitionKey)
	store.mu.Unlock()
	targets, _ = store.ProjectionRevisions()
	if targets[0].Digest != "" {
		t.Error("missing definition produced a trusted digest")
	}
	store.mu.Lock()
	store.definitions[definitionKey] = definition
	delete(store.connections, binding.ConnectionID)
	store.mu.Unlock()
	targets, _ = store.ProjectionRevisions()
	if targets[0].Digest != "" {
		t.Error("missing connection produced a trusted digest")
	}
	store.mu.Lock()
	store.connections[binding.ConnectionID] = connection
	credential := store.credentials[binding.CredentialRefID]
	delete(store.credentials, binding.CredentialRefID)
	store.mu.Unlock()
	targets, _ = store.ProjectionRevisions()
	if targets[0].Digest != "" {
		t.Error("missing credential produced a trusted digest")
	}
	store.mu.Lock()
	store.credentials[binding.CredentialRefID] = credential
	store.mu.Unlock()
}

func TestSemanticDigestAppliedSurvivesReload(t *testing.T) {
	seed, _, _ := seededStore(t)
	path := filepath.Join(t.TempDir(), "store.json")
	if err := seed.Save(path); err != nil {
		t.Fatal(err)
	}
	store, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	shadow, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	posts := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		posts++
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()
	targets, err := store.ProjectionRevisions()
	if err != nil {
		t.Fatal(err)
	}
	if err := (&api.RuntimeRestartNotifier{URL: srv.URL, Token: "fixture", State: store}).Notify(targets); err != nil || posts != 1 {
		t.Fatalf("initial notify posts=%d err=%v", posts, err)
	}
	if err := shadow.Reload(); err != nil {
		t.Fatal(err)
	}
	if got := shadow.AppliedRestartDigest(targets[0]); got != targets[0].Digest {
		t.Fatalf("applied digest not reloaded: %q", got)
	}
	store, err = Load(path)
	if err != nil {
		t.Fatal(err)
	}
	store.mu.Lock()
	store.projectionRevisions[projectionKey("alice", "alice", "runtime")]++
	store.mu.Unlock()
	if err := store.Save(path); err != nil {
		t.Fatal(err)
	}
	targets, err = store.ProjectionRevisions()
	if err != nil {
		t.Fatal(err)
	}
	if err := (&api.RuntimeRestartNotifier{URL: srv.URL, Token: "fixture", State: store}).Notify(targets); err != nil || posts != 1 {
		t.Fatalf("reloaded semantic ack lost: posts=%d err=%v", posts, err)
	}
}

func TestSemanticDigestTracksStaleFences(t *testing.T) {
	for name, mutate := range map[string]func(*Store, api.ToolBinding){
		"connection_revision": func(s *Store, b api.ToolBinding) {
			c := s.connections[b.ConnectionID]
			c.Revision++
			s.connections[b.ConnectionID] = c
		},
		"binding_connection_revision": func(s *Store, b api.ToolBinding) {
			b.ConnectionRevision++
			s.bindings[b.ToolBindingID] = b
		},
		"credential_revision": func(s *Store, b api.ToolBinding) {
			c := s.credentials[b.CredentialRefID]
			c.Revision++
			s.credentials[b.CredentialRefID] = c
		},
		"connection_owner": func(s *Store, b api.ToolBinding) {
			c := s.connections[b.ConnectionID]
			c.Owner.ID = "bob"
			s.connections[b.ConnectionID] = c
		},
		"connection_credential": func(s *Store, b api.ToolBinding) {
			c := s.connections[b.ConnectionID]
			c.CredentialRefID = "other-ref"
			s.connections[b.ConnectionID] = c
		},
		"tool_pin": func(s *Store, b api.ToolBinding) {
			b.ToolDigest = "sha256:changed"
			s.bindings[b.ToolBindingID] = b
		},
	} {
		t.Run(name, func(t *testing.T) {
			store, _, binding := seededStore(t)
			targets, err := store.ProjectionRevisions()
			if err != nil || len(targets) != 1 || targets[0].Digest == "" {
				t.Fatalf("initial digest: %v %v", targets, err)
			}
			before := targets[0].Digest
			store.mu.Lock()
			mutate(store, binding)
			store.mu.Unlock()
			targets, err = store.ProjectionRevisions()
			if err != nil || len(targets) != 1 || targets[0].Digest == before {
				t.Fatalf("stale fence did not change digest: %v %v", targets, err)
			}
		})
	}
}

// The generic invoke route carries every connector_* facade verb through the
// same InvokeConnector the agent tools reach — hubctl --service lives here.
func TestConnectorInvokeRoutesFacadeVerbs(t *testing.T) {
	store, auth, _, profile := managedStore(t)
	profile.Revision++
	profile.Connectors = api.ConnectorsPersonal
	if err := putProfile(t, store, profile); err != nil {
		t.Fatal(err)
	}
	g := &Gateway{
		Store:   store,
		Control: &ControlPlane{Store: store, Now: time.Now},
		Backend: api.RoutingBackend{},
		Tokens:  map[string]identity.Envelope{"svc-token-00000000000000000000000": auth},
	}
	attachGatewayAudit(t, g)
	handler, err := g.Handler()
	if err != nil {
		t.Fatal(err)
	}
	invoke := func(token string, body string) *httptest.ResponseRecorder {
		req := httptest.NewRequest(http.MethodPost, "/v1/connectors:invoke", strings.NewReader(body))
		if token != "" {
			req.Header.Set("Authorization", "Bearer "+token)
		}
		rec := httptest.NewRecorder()
		handler.ServeHTTP(rec, req)
		return rec
	}
	rec := invoke("svc-token-00000000000000000000000", `{"tool":"connector_list","args":{}}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("connector_list: %d %s", rec.Code, rec.Body.String())
	}
	var result api.OperationResult
	if err := json.Unmarshal(rec.Body.Bytes(), &result); err != nil || result.Summary == "" {
		t.Fatalf("invoke must return the OperationResult: %v %s", err, rec.Body.String())
	}
	// Unknown and dead tokens are refused before the facade runs.
	if rec := invoke("mallory-token-0000000000000000000000", `{"tool":"connector_list","args":{}}`); rec.Code != http.StatusUnauthorized {
		t.Fatalf("unknown token status=%d", rec.Code)
	}
	// Non-connector tools are rejected by the facade, not silently run.
	if rec := invoke("svc-token-00000000000000000000000", `{"tool":"settings_edit","args":{}}`); rec.Code != http.StatusNotFound {
		t.Fatalf("non-connector tool must be refused: %d %s", rec.Code, rec.Body.String())
	}
}
