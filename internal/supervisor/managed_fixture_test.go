package supervisor

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/letya999/hermes-hub/internal/catalog"
	"github.com/letya999/hermes-hub/internal/passport"
	"github.com/letya999/hermes-hub/internal/stack"
)

// publishAliceRevision authors the six passports and runs the real
// authored → compile → publish path (SPEC-0095 R1): the fixture exercises
// the same revision the supervisor's loadSelected reads, with the retired
// settings.yaml/agent.yaml out of the loop entirely. mutate may adjust the
// authored docs before compilation.
func publishAliceRevision(t *testing.T, ctxRoot string, mutate func(map[string]string)) {
	t.Helper()
	publishSpaceRevision(t, ctxRoot, "alice", mutate)
}

func publishSpaceRevision(t *testing.T, ctxRoot, user string, mutate func(map[string]string)) {
	t.Helper()
	docs := map[string]string{
		"stack": "identity: {user: " + user + ", infra: false}\nservices:\n" +
			"  hermes-runtime: {mode: supervised}\n" +
			"  model-relay: {mode: per-space}\n" +
			"  toolhub-relay: {mode: per-space}\n" +
			"  toolhub: {mode: shared}\n" +
			"  credential-broker: {mode: shared}\n" +
			"  communication-hub: {mode: shared}\n" +
			"  supervisor: {mode: shared}\n" +
			"  cliproxy: {mode: per-space}\n",
		"config": "runtime: {timezone: UTC}\nmemory: {enabled: false}\n",
		"models": "default: synthetic\nroutes: {synthetic: {ladder: [cliproxy]}}\nservices:\n" +
			"  cliproxy:\n    via: cliproxy\n    endpoint: http://cliproxy:8317/v1\n" +
			"    auth: {proxy_access: {kind: broker-key, ref: cliproxy}}\n    models: [{id: synthetic}]\n",
		"workspace": "writable: [/workspace]\nread_only: [/vault/memory]\n",
		"tools":     "binding: {profile_id: " + user + "-default, generation: 1}\ncore:\n  files: {via: toolhub}\n",
		"grants":    "allow: {}\n",
	}
	if mutate != nil {
		mutate(docs)
	}
	if err := os.MkdirAll(filepath.Join(ctxRoot, "passports"), 0700); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"stack", "config", "models", "workspace", "tools", "grants"} {
		if err := os.WriteFile(filepath.Join(ctxRoot, "passports", name+".yaml"), []byte(docs[name]), 0600); err != nil {
			t.Fatal(err)
		}
	}
	catalogDocs, err := catalog.Generate()
	if err != nil {
		t.Fatal(err)
	}
	rev, err := passport.CompileAuthored(ctxRoot, "prod", time.Now(), catalogDocs, stack.ReservedSiblingPorts(ctxRoot), map[string][]string{})
	if err != nil {
		t.Fatal(err)
	}
	if err := rev.Publish(ctxRoot); err != nil {
		t.Fatal(err)
	}
}

// writeManagedFixtures installs the host-side files a managed supervised
// context needs: a published six-passport revision, the managed env file,
// a zero-template source config and the supervisor execution selection.
func writeManagedFixtures(t *testing.T, ctxRoot string) stack.Settings {
	t.Helper()
	publishAliceRevision(t, ctxRoot, nil)
	if err := os.WriteFile(filepath.Join(ctxRoot, "managed-runtime.prod.env"), []byte("HUB_TOOLHUB_ENDPOINT=http://toolhub:8090/mcp\n"), 0600); err != nil {
		t.Fatal(err)
	}
	selection := stack.ExecutionSelection{Schema: 1, User: "alice", Environment: "prod", Mode: "supervisor", SupervisorURL: "http://localhost:8876", NativeCron: "disabled", CompatibilityRelease: "0.3.0"}
	selectionBody, err := json.Marshal(selection)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(ctxRoot, stack.ExecutionPath("prod")), selectionBody, 0600); err != nil {
		t.Fatal(err)
	}
	settings, err := stack.ReadEnvironment(ctxRoot, "prod")
	if err != nil {
		t.Fatal(err)
	}
	return settings
}

// writeManagedRevisionFixtures installs a managed space carried entirely by
// the published six-document revision: settings.yaml, agent.yaml and
// workspace.yaml are retired (SPEC-0092 R8.1, SPEC-0095).
func writeManagedRevisionFixtures(t *testing.T, ctxRoot string) {
	t.Helper()
	publishAliceRevision(t, ctxRoot, nil)
	if err := os.WriteFile(filepath.Join(ctxRoot, "managed-runtime.prod.env"), []byte("HUB_TOOLHUB_ENDPOINT=http://toolhub:8090/mcp\n"), 0600); err != nil {
		t.Fatal(err)
	}
	selection := stack.ExecutionSelection{Schema: 1, User: "alice", Environment: "prod", Mode: "supervisor", SupervisorURL: "http://localhost:8876", NativeCron: "disabled", CompatibilityRelease: "0.3.0"}
	selectionBody, err := json.Marshal(selection)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(ctxRoot, stack.ExecutionPath("prod")), selectionBody, 0600); err != nil {
		t.Fatal(err)
	}
}
