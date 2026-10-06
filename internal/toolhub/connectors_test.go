package toolhub

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

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
		Backend:      RoutingBackend{},
		Tokens: map[string]identity.Envelope{
			"alice-principal-token-0000000000000": auth,
			"bob-principal-token-000000000000000": bob,
		},
		projections: map[string]*gatewayProjection{
			"alice-principal-token-0000000000000": {auth: auth},
			"bob-principal-token-000000000000000": {auth: bob},
		},
	}
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

// The restart notifier adopts the first snapshot as baseline, then POSTs
// /v1/restart-request once per changed projection; failures stay unacked so
// the next tick retries them.
func TestRuntimeRestartNotifierPrimesDiffsAndRetries(t *testing.T) {
	var posts []string
	var fail bool
	api := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
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
	defer api.Close()
	n := &RuntimeRestartNotifier{URL: api.URL, Token: "control-token"}
	snapshot := []ProjectionTarget{{PrincipalID: "alice", ContextID: "alice", RuntimeID: "alice", Revision: 2}}
	if err := n.Notify(snapshot); err != nil || len(posts) != 0 {
		t.Fatalf("first snapshot must prime, not notify: %v %v", err, posts)
	}
	snapshot = append(snapshot, ProjectionTarget{PrincipalID: "bob", ContextID: "bob", RuntimeID: "bob", Revision: 1})
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

// RuntimeRestartNotifierFromEnv stays nil without the supervisor pair, so
// unmanaged deployments keep the file-marker contract.
func TestRuntimeRestartNotifierFromEnvRequiresSupervisor(t *testing.T) {
	t.Setenv("HUB_RUNTIME_SUPERVISOR_URL", "")
	t.Setenv("HUB_COMMUNICATION_AUTH", "")
	if RuntimeRestartNotifierFromEnv() != nil {
		t.Fatal("notifier without supervisor config")
	}
	t.Setenv("HUB_RUNTIME_SUPERVISOR_URL", "http://supervisor:8876/")
	t.Setenv("HUB_COMMUNICATION_AUTH", "token")
	n := RuntimeRestartNotifierFromEnv()
	if n == nil || n.URL != "http://supervisor:8876" || n.Token != "token" {
		t.Fatalf("notifier env: %+v", n)
	}
}
