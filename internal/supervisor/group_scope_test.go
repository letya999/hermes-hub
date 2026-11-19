package supervisor

import (
	"context"
	"github.com/letya999/hermes-hub/internal/commodel"
	"github.com/letya999/hermes-hub/internal/identityhub"
	"os"
	"path/filepath"
	"testing"

	"github.com/letya999/hermes-hub/internal/identity"
	hubruntime "github.com/letya999/hermes-hub/internal/runtime"
)

// publishScopeKind stamps a published revision whose stack.identity carries
// the requested scope kind — the published revision, not a retired
// scope.yaml, is where the supervisor learns a home's scope (ADR-0096 D2).
func publishScopeKind(t *testing.T, ctxRoot, kind string) {
	t.Helper()
	revDir := filepath.Join(ctxRoot, "generated", "passports", "r-scope-"+kind)
	if err := os.MkdirAll(revDir, 0700); err != nil {
		t.Fatal(err)
	}
	env := "apiVersion: six-passport/v2\nmetadata: {space: alice, environment: prod, revision: r-scope-" + kind + ", generated_at: 2026-11-04T00:00:00Z, catalogs: \"4\"}\n"
	docs := map[string]string{
		"stack": "kind: stack\nidentity: {user: alice, kind: " + kind + ", infra: false}\nservices:\n" +
			"  hermes-runtime: {mode: supervised}\n" +
			"  model-relay: {mode: per-space}\n" +
			"  toolhub-relay: {mode: per-space}\n" +
			"  toolhub: {mode: shared}\n" +
			"  credential-broker: {mode: shared}\n",
		"config":    "kind: config\nruntime: {timezone: UTC}\nports: {oauth-callback: 8008, browser-vnc: 6081}\n",
		"models":    "kind: models\ndefault: m\nroutes:\n  m: {ladder: [cliproxy]}\nservices:\n  cliproxy: {via: cliproxy, endpoint: http://cliproxy:8317/v1, models: [{id: m}]}\n",
		"workspace": "kind: workspace\nwritable: [/workspace]\nread_only: [/vault/memory]\n",
		"tools":     "kind: tools\nbinding: {profile_id: alice-default, generation: 1}\ncore:\n  discovery: {via: toolhub}\n  files: {via: toolhub}\n",
		"grants":    "kind: grants\nallow: {}\n",
	}
	for name, body := range docs {
		if err := os.WriteFile(filepath.Join(revDir, name+".yaml"), []byte(env+body), 0600); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.WriteFile(filepath.Join(ctxRoot, "generated", "passports", "current"), []byte("r-scope-"+kind+"\n"), 0600); err != nil {
		t.Fatal(err)
	}
	view := filepath.Join(ctxRoot, "generated", "config-view", "prod")
	if err := os.MkdirAll(view, 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(view, "revision"), []byte("r-scope-"+kind+"\n"), 0600); err != nil {
		t.Fatal(err)
	}
}

func TestSupervisorGroupBindingRequiresDedicatedGroupHome(t *testing.T) {
	m, root := testManager(t, func(ctx context.Context, args ...string) ([]byte, error) { return []byte("running"), nil }, nil)
	if err := os.WriteFile(filepath.Join(root, "managed-runtime.prod.env"), []byte("HUB_TOOLHUB_ENDPOINT=http://toolhub:8090/mcp\n"), 0600); err != nil {
		t.Fatal(err)
	}
	publishScopeKind(t, root, "group")
	registry, err := identityhub.Open(filepath.Join(t.TempDir(), "identity.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	defer registry.Close()
	if _, err := registry.ImportLegacyConfiguration(context.Background(), identityhub.LegacyConfiguration{Name: "group", Organization: "personal", Users: []commodel.User{{ID: "alice", Enabled: true}}, SharedSpaces: []identityhub.LegacySharedSpace{{ID: "alice", Members: []int64{11}}}}); err != nil {
		t.Fatal(err)
	}
	m.cfg.Identity = registry
	r := hubruntime.ExecuteRequest{Envelope: identity.TelegramEnvelope("alice", 11, "alice", "policy-1"), UserID: "alice", ActorID: "alice", ScopeID: "group:alice", OrganizationID: "personal"}
	bound, err := m.bindingFor(r)
	if err != nil || bound.ContextRoot != root || bound.PrincipalID != "alice" {
		t.Errorf("dedicated group binding rejected: %+v %v", bound, err)
	}
	publishScopeKind(t, root, "user")
	if _, err := m.bindingFor(r); err == nil {
		t.Error("group request selected a personal home")
	}
}
