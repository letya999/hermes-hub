package main

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/gofrs/flock"
	"github.com/letya999/hermes-hub/internal/agenttools"
	"github.com/letya999/hermes-hub/internal/catalog"
	"github.com/letya999/hermes-hub/internal/passport"
	"github.com/letya999/hermes-hub/internal/reconcile"
	"github.com/letya999/hermes-hub/internal/stack"
	"github.com/letya999/hermes-hub/internal/toolhub"
	"github.com/letya999/hermes-hub/internal/toolhub/api"
)

func TestRecreateRemovedHostingControllerSweepsOrphan(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "spaces", "alice")
	writeManagedSpaceFixture(t, dir, "alice")
	previous := dockerCompose
	defer func() { dockerCompose = previous }()
	var got []string
	dockerCompose = func(_ context.Context, _ string, args ...string) error {
		got = append([]string(nil), args...)
		return nil
	}
	if err := applyHandlers(context.Background(), dir, "dev").Recreate("hosting-controller"); err != nil {
		t.Fatal(err)
	}
	if !slices.Contains(got, "--remove-orphans") || slices.Contains(got, "hosting-controller") {
		t.Fatalf("removed controller was not swept from restored project: %v", got)
	}
}

// space apply --confirm must re-render the environment before reconcile
// acts: a newly declared per-space instance only exists in the generated
// compose file once the published revision is rendered — acting on the
// previous render hits "no such service".
func TestSpaceApplyRendersBeforeReconcile(t *testing.T) {
	root := t.TempDir()
	dir := filepath.Join(root, "spaces", "dana")
	for _, sub := range []string{"config", "generated/passports"} {
		if err := os.MkdirAll(filepath.Join(dir, sub), 0700); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.WriteFile(filepath.Join(dir, "agent.yaml"), []byte("schema: 3\ninfra: false\ncapability_profile_id: dana-default\ncapability_generation: 1\n"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "scope.yaml"), []byte("kind: user\nid: dana\n"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "secrets.dev.env"), []byte(""), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(root, "config"), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "config", "SOUL.md"), []byte("# persona\n"), 0600); err != nil {
		t.Fatal(err)
	}
	head := "apiVersion: six-passport/v2\nmetadata: {space: dana, environment: dev, revision: r-0123456789abcdef, generated_at: 2026-11-02T00:00:00Z}\n"
	rev := passport.Revision{ID: "r-0123456789abcdef", Docs: map[string][]byte{
		"stack":     []byte(head + "kind: stack\nidentity: {user: dana, infra: false}\nservices:\n    cliproxy-dana: {mode: per-space}\n    model-relay: {mode: per-space}\n    hermes-runtime: {mode: supervised}\n"),
		"config":    []byte(head + "kind: config\nruntime: {timezone: UTC}\nports: {oauth-callback: 8008, browser-vnc: 6081}\n"),
		"models":    []byte(head + "kind: models\ndefault: upstream\nroutes: {upstream: {ladder: [upstream]}}\nservices:\n    upstream: {via: openai-compatible, endpoint: http://model-relay:8318/v1, models: [{id: upstream}]}\n"),
		"workspace": []byte(head + "kind: workspace\nwritable: [/workspace]\nread_only: [/config]\n"),
		"tools":     []byte(head + "kind: tools\nbinding: {profile_id: dana-default, generation: 1}\ncore:\n  discovery: {via: toolhub}\n  files: {via: toolhub}\n"),
		"grants":    []byte(head + "kind: grants\nallow:\n  workspace: [read]\n"),
	}}
	handlers := reconcile.Handlers{
		Rebuild:  func(string) error { return nil },
		Recreate: func(string) error { return nil },
		Respawn:  func() error { return nil },
		Live:     map[string]func() error{},
	}
	_, err := applyConfirm(context.Background(), dir, root, "dev", rev, "", nil, handlers)
	if err != nil {
		t.Fatal(err)
	}
	compose, err := os.ReadFile(filepath.Join(dir, "generated", "compose.dev.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(compose), "cliproxy-dana") {
		t.Fatal("reconcile acted on a stale render: new instance missing from generated compose")
	}
	next := passport.Revision{ID: "r-0123456789abcdee", Docs: map[string][]byte{}}
	for name, body := range rev.Docs {
		next.Docs[name] = []byte(strings.ReplaceAll(string(body), rev.ID, next.ID))
	}
	next.Docs["stack"] = []byte(strings.Replace(string(next.Docs["stack"]), "    cliproxy-dana: {mode: per-space}\n", "", 1))
	handlers.Rebuild = func(string) error { return errors.New("synthetic reconcile failure") }
	if _, err := applyConfirm(context.Background(), dir, root, "dev", next, rev.ID, []reconcile.Action{{Kind: reconcile.Rebuild, Target: "model-relay"}}, handlers); err == nil {
		t.Fatal("failed reconcile reported success")
	}
	if current, err := passport.Current(dir); err != nil || current != rev.ID {
		t.Fatalf("failed reconcile changed active revision: %q, %v", current, err)
	}
	compose, err = os.ReadFile(filepath.Join(dir, "generated", "compose.dev.yaml"))
	if err != nil || !strings.Contains(string(compose), "cliproxy-dana") {
		t.Fatalf("failed reconcile left the new generated compose active: %v", err)
	}
}

func TestSpaceApplyRejectsLegacySourceWithoutSixPassports(t *testing.T) {
	space := filepath.Join(t.TempDir(), "spaces", "alice")
	if err := os.MkdirAll(filepath.Join(space, "passport-src"), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(space, "passport-src", "user.yaml"), []byte("name: alice\n"), 0600); err != nil {
		t.Fatal(err)
	}
	err := runSpaceApply(context.Background(), []string{"--dir", space, "--env", "dev"})
	if err == nil || !strings.Contains(err.Error(), "authored passports") {
		t.Fatalf("legacy source used as apply authority: %v", err)
	}
}

func TestSpaceApplyCompilesSixAuthoredPassports(t *testing.T) {
	space := filepath.Join(t.TempDir(), "spaces", "alice")
	if err := os.MkdirAll(filepath.Join(space, "passports"), 0700); err != nil {
		t.Fatal(err)
	}
	docs := map[string]string{
		"stack": "identity: {user: alice, infra: true}\nservices:\n" +
			"  hermes-runtime: {mode: supervised}\n  toolhub: {mode: shared}\n" +
			"  model-relay: {mode: shared}\n  communication-hub: {mode: shared}\n" +
			"  credential-broker: {mode: shared}\n  supervisor: {mode: shared}\n" +
			"  cliproxy-google-1: {mode: per-space}\n",
		"config": "runtime: {timezone: UTC}\nmemory: {enabled: false}\n",
		"models": "default: everyday\nroutes: {everyday: {ladder: [google-pro-1]}}\n" +
			"services:\n  google-pro-1:\n    via: cliproxy\n" +
			"    endpoint: http://cliproxy-google-1:8317/v1\n" +
			"    auth: {upstream: {kind: oauth-session, ref: google-one-1}, proxy_access: {kind: broker-key, ref: cliproxy-google-1}}\n" +
			"    models: [{id: gemini}]\n",
		"workspace": "writable: [/workspace]\nread_only: [/config]\n",
		"tools":     "binding: {profile_id: alice-default, generation: 1}\ncore: {files: {via: toolhub}}\n",
		"grants":    "allow: {}\n",
	}
	for name, body := range docs {
		if err := os.WriteFile(filepath.Join(space, "passports", name+".yaml"), []byte(body), 0600); err != nil {
			t.Fatal(err)
		}
	}
	if err := runSpaceApply(context.Background(), []string{"--dir", space, "--env", "dev"}); err != nil {
		t.Fatalf("six authored inputs did not compile: %v", err)
	}
	stackPath := filepath.Join(space, "passports", "stack.yaml")
	if err := os.WriteFile(stackPath, []byte(strings.Replace(docs["stack"], "user: alice, infra:", "user: alice, organization: acme, infra:", 1)), 0600); err != nil {
		t.Fatal(err)
	}
	if err := runSpaceApply(context.Background(), []string{"--dir", space, "--env", "dev"}); err == nil || !strings.Contains(strings.ToLower(err.Error()), "organization policy") {
		t.Fatalf("organization apply without protected policy: %v", err)
	}
	if err := os.WriteFile(stackPath, []byte(docs["stack"]), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(space, "passports", "grants.yaml"), []byte("allow: {tools: [use]}\n"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := runSpaceApply(context.Background(), []string{"--dir", space, "--env", "dev"}); err == nil || !strings.Contains(err.Error(), "trusted ceiling") {
		t.Fatalf("editing grants.yaml issued an unapproved right: %v", err)
	}
	if err := os.WriteFile(filepath.Join(space, "passports", "grants.yaml"), []byte("allow: {}\n"), 0600); err != nil {
		t.Fatal(err)
	}
	catalogDocs, err := catalog.Generate()
	if err != nil {
		t.Fatal(err)
	}
	rev, err := passport.CompileAuthored(space, "dev", time.Now(), catalogDocs, stack.ReservedSiblingPorts(space), map[string][]string{})
	if err != nil {
		t.Fatal(err)
	}
	if err := rev.Publish(space); err != nil {
		t.Fatal(err)
	}
	toolsPath := filepath.Join(space, "passports", "tools.yaml")
	if err := os.WriteFile(toolsPath, []byte("binding: {profile_id: alice-default, generation: 1}\nextended: {files: {via: toolhub, tools: {include: [file_read]}, scope: {hosts: [example.test]}}}\n"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := runSpaceApply(context.Background(), []string{"--dir", space, "--env", "dev", "--confirm"}); err == nil || !strings.Contains(err.Error(), "unsupported scope") {
		t.Fatalf("unsupported scope reached publication: %v", err)
	}
	if current, err := passport.Current(space); err != nil || current != rev.ID {
		t.Fatalf("rejected scope displaced current revision: %q %v", current, err)
	}
	if err := os.WriteFile(toolsPath, []byte("binding: {profile_id: alice-default, generation: 1}\nextended: {files: {via: toolhub}}\n"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := runSpaceApply(context.Background(), []string{"--dir", space, "--env", "dev"}); err == nil || !strings.Contains(err.Error(), "tools.configure") {
		t.Fatalf("tool selection changed without tools.configure: %v", err)
	}
	gov := toolhub.NewGovernanceStore(filepath.Join(space, "runtime", "toolhub", "governance.json"))
	grant := api.NewRightGrant("rightgrant-alice-configure", "alice", "tools", "configure", "", "fixture", "operator", time.Now().Add(time.Hour))
	if err := api.Confirm(&grant, "operator", time.Now()); err != nil {
		t.Fatal(err)
	}
	if err := gov.GrantRight(grant, catalogDocs.Rights, time.Now()); err != nil {
		t.Fatal(err)
	}
	if err := runSpaceApply(context.Background(), []string{"--dir", space, "--env", "dev"}); err == nil || !strings.Contains(err.Error(), "tools.configure") {
		t.Fatalf("issued but unpublished tools.configure changed selection: %v", err)
	}
	if err := os.WriteFile(toolsPath, []byte(docs["tools"]), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(space, "passports", "grants.yaml"), []byte("allow: {tools: [configure]}\n"), 0600); err != nil {
		t.Fatal(err)
	}
	rev, err = passport.CompileAuthored(space, "dev", time.Now(), catalogDocs, stack.ReservedSiblingPorts(space), map[string][]string{"tools": {"configure"}})
	if err != nil {
		t.Fatal(err)
	}
	if err := rev.Publish(space); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(toolsPath, []byte("binding: {profile_id: alice-default, generation: 1}\nextended: {files: {via: toolhub}}\n"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := runSpaceApply(context.Background(), []string{"--dir", space, "--env", "dev"}); err != nil {
		t.Fatalf("issued tools.configure failed to admit tool selection: %v", err)
	}
}

// R1.4: each passport's edits require its own current right — not just
// tools.yaml. A config.yaml change without config.edit_own in both the
// issued ceiling and the current published allow is refused by name.
func TestSpaceApplyRequiresPerPassportEditRight(t *testing.T) {
	space := filepath.Join(t.TempDir(), "spaces", "alice")
	if err := os.MkdirAll(filepath.Join(space, "passports"), 0700); err != nil {
		t.Fatal(err)
	}
	docs := map[string]string{
		"stack":     "identity: {user: alice, infra: false}\nservices:\n  hermes-runtime: {mode: supervised}\n  toolhub: {mode: shared}\n  model-relay: {mode: shared}\n  communication-hub: {mode: shared}\n  credential-broker: {mode: shared}\n  supervisor: {mode: shared}\n  cliproxy: {mode: shared}\n",
		"config":    "runtime: {timezone: UTC}\nports: {oauth-callback: 8008}\nmemory: {enabled: true}\n",
		"models":    "default: upstream\nroutes: {upstream: {ladder: [upstream]}}\nservices:\n  upstream: {via: openai-compatible, endpoint: http://model-relay:8318/v1, models: [{id: upstream}]}\n",
		"workspace": "writable: [/workspace]\nread_only: [/config]\n",
		"tools":     "binding: {profile_id: alice-default, generation: 1}\ncore: {files: {via: toolhub}}\n",
		"grants":    "allow: {}\n",
	}
	for name, body := range docs {
		if err := os.WriteFile(filepath.Join(space, "passports", name+".yaml"), []byte(body), 0600); err != nil {
			t.Fatal(err)
		}
	}
	catalogDocs, err := catalog.Generate()
	if err != nil {
		t.Fatal(err)
	}
	rev, err := passport.CompileAuthored(space, "dev", time.Now(), catalogDocs, stack.ReservedSiblingPorts(space), map[string][]string{})
	if err != nil {
		t.Fatal(err)
	}
	if err := rev.Publish(space); err != nil {
		t.Fatal(err)
	}
	configPath := filepath.Join(space, "passports", "config.yaml")
	if err := os.WriteFile(configPath, []byte("runtime: {timezone: Europe/Moscow}\nports: {oauth-callback: 8008}\nmemory: {enabled: true}\n"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := runSpaceApply(context.Background(), []string{"--dir", space, "--env", "dev"}); err == nil || !strings.Contains(err.Error(), "config.edit_own") {
		t.Fatalf("config.yaml changed without config.edit_own: %v", err)
	}
	gov := toolhub.NewGovernanceStore(filepath.Join(space, "runtime", "toolhub", "governance.json"))
	grant := api.NewRightGrant("rightgrant-alice-config", "alice", "config", "edit_own", "", "fixture", "operator", time.Now().Add(time.Hour))
	if err := api.Confirm(&grant, "operator", time.Now()); err != nil {
		t.Fatal(err)
	}
	if err := gov.GrantRight(grant, catalogDocs.Rights, time.Now()); err != nil {
		t.Fatal(err)
	}
	if err := runSpaceApply(context.Background(), []string{"--dir", space, "--env", "dev"}); err == nil || !strings.Contains(err.Error(), "config.edit_own") {
		t.Fatalf("issued but unpublished config.edit_own changed config: %v", err)
	}
	if err := os.WriteFile(filepath.Join(space, "passports", "grants.yaml"), []byte("allow: {config: [edit_own]}\n"), 0600); err != nil {
		t.Fatal(err)
	}
	rev, err = passport.CompileAuthored(space, "dev", time.Now(), catalogDocs, stack.ReservedSiblingPorts(space), map[string][]string{"config": {"edit_own"}})
	if err != nil {
		t.Fatal(err)
	}
	if err := rev.Publish(space); err != nil {
		t.Fatal(err)
	}
	if err := runSpaceApply(context.Background(), []string{"--dir", space, "--env", "dev"}); err != nil {
		t.Fatalf("issued config.edit_own failed to admit a config change: %v", err)
	}
}

// identity.kind is provisioning-stamped, not negotiable: even with
// stack.change issued and published, a revision that flips the scope kind
// is refused — a personal space cannot redeclare itself a group home.
func TestSpaceApplyRefusesScopeKindChange(t *testing.T) {
	space := filepath.Join(t.TempDir(), "spaces", "alice")
	if err := os.MkdirAll(filepath.Join(space, "passports"), 0700); err != nil {
		t.Fatal(err)
	}
	docs := map[string]string{
		"stack":     "identity: {user: alice, infra: false}\nservices:\n  hermes-runtime: {mode: supervised}\n  toolhub: {mode: shared}\n  model-relay: {mode: shared}\n  communication-hub: {mode: shared}\n  credential-broker: {mode: shared}\n  supervisor: {mode: shared}\n  cliproxy: {mode: shared}\n",
		"config":    "runtime: {timezone: UTC}\nports: {oauth-callback: 8008}\nmemory: {enabled: true}\n",
		"models":    "default: upstream\nroutes: {upstream: {ladder: [upstream]}}\nservices:\n  upstream: {via: openai-compatible, endpoint: http://model-relay:8318/v1, models: [{id: upstream}]}\n",
		"workspace": "writable: [/workspace]\nread_only: [/config]\n",
		"tools":     "binding: {profile_id: alice-default, generation: 1}\ncore: {files: {via: toolhub}}\n",
		"grants":    "allow: {stack: [change]}\n",
	}
	for name, body := range docs {
		if err := os.WriteFile(filepath.Join(space, "passports", name+".yaml"), []byte(body), 0600); err != nil {
			t.Fatal(err)
		}
	}
	catalogDocs, err := catalog.Generate()
	if err != nil {
		t.Fatal(err)
	}
	gov := toolhub.NewGovernanceStore(filepath.Join(space, "runtime", "toolhub", "governance.json"))
	grant := api.NewRightGrant("rightgrant-alice-stack", "alice", "stack", "change", "", "fixture", "operator", time.Now().Add(time.Hour))
	if err := api.Confirm(&grant, "operator", time.Now()); err != nil {
		t.Fatal(err)
	}
	if err := gov.GrantRight(grant, catalogDocs.Rights, time.Now()); err != nil {
		t.Fatal(err)
	}
	issued := map[string][]string{"stack": {"change"}}
	rev, err := passport.CompileAuthored(space, "dev", time.Now(), catalogDocs, stack.ReservedSiblingPorts(space), issued)
	if err != nil {
		t.Fatal(err)
	}
	if err := rev.Publish(space); err != nil {
		t.Fatal(err)
	}
	stackPath := filepath.Join(space, "passports", "stack.yaml")
	if err := os.WriteFile(stackPath, []byte("identity: {user: alice, infra: false, kind: group}\nservices:\n  hermes-runtime: {mode: supervised}\n  toolhub: {mode: shared}\n  model-relay: {mode: shared}\n  communication-hub: {mode: shared}\n  credential-broker: {mode: shared}\n  supervisor: {mode: shared}\n  cliproxy: {mode: shared}\n"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := runSpaceApply(context.Background(), []string{"--dir", space, "--env", "dev"}); err == nil || !strings.Contains(err.Error(), "scope kind") {
		t.Fatalf("scope kind retyped by passport edit: %v", err)
	}
}

// The remaining passports follow the same R1.4 rule: models.yaml,
// workspace.yaml and grants.yaml each carry a distinct configure right
// that must be issuable, then admit the edit once issued and published.
func TestSpaceApplyPassportRightsAreIssuable(t *testing.T) {
	space := filepath.Join(t.TempDir(), "spaces", "alice")
	if err := os.MkdirAll(filepath.Join(space, "passports"), 0700); err != nil {
		t.Fatal(err)
	}
	docs := map[string]string{
		"stack":     "identity: {user: alice, infra: false}\nservices:\n  hermes-runtime: {mode: supervised}\n  toolhub: {mode: shared}\n  model-relay: {mode: shared}\n  communication-hub: {mode: shared}\n  credential-broker: {mode: shared}\n  supervisor: {mode: shared}\n  cliproxy: {mode: shared}\n",
		"config":    "runtime: {timezone: UTC}\nports: {oauth-callback: 8008}\nmemory: {enabled: true}\n",
		"models":    "default: upstream\nroutes: {upstream: {ladder: [upstream]}}\nservices:\n  upstream: {via: openai-compatible, endpoint: http://model-relay:8318/v1, models: [{id: upstream}]}\n",
		"workspace": "writable: [/workspace]\nread_only: [/config]\n",
		"tools":     "binding: {profile_id: alice-default, generation: 1}\ncore: {files: {via: toolhub}}\n",
		"grants":    "allow: {}\n",
	}
	for name, body := range docs {
		if err := os.WriteFile(filepath.Join(space, "passports", name+".yaml"), []byte(body), 0600); err != nil {
			t.Fatal(err)
		}
	}
	catalogDocs, err := catalog.Generate()
	if err != nil {
		t.Fatal(err)
	}
	issued := map[string][]string{"models": {"configure"}, "workspace": {"configure"}, "grants": {"configure"}}
	rev, err := passport.CompileAuthored(space, "dev", time.Now(), catalogDocs, stack.ReservedSiblingPorts(space), issued)
	if err != nil {
		t.Fatalf("compile with configure ceiling: %v", err)
	}
	if err := rev.Publish(space); err != nil {
		t.Fatal(err)
	}
	gov := toolhub.NewGovernanceStore(filepath.Join(space, "runtime", "toolhub", "governance.json"))
	allow := map[string][]string{}
	for cap, ops := range issued {
		for _, op := range ops {
			grant := api.NewRightGrant("rightgrant-alice-"+cap, "alice", cap, op, "", "fixture", "operator", time.Now().Add(time.Hour))
			if err := api.Confirm(&grant, "operator", time.Now()); err != nil {
				t.Fatal(err)
			}
			if err := gov.GrantRight(grant, catalogDocs.Rights, time.Now()); err != nil {
				t.Fatalf("%s.%s is not issuable: %v", cap, op, err)
			}
			allow[cap] = append(allow[cap], op)
		}
	}
	var grantsBody strings.Builder
	grantsBody.WriteString("allow:\n")
	for _, cap := range []string{"grants", "models", "workspace"} {
		grantsBody.WriteString("  " + cap + ": [configure]\n")
	}
	if err := os.WriteFile(filepath.Join(space, "passports", "grants.yaml"), []byte(grantsBody.String()), 0600); err != nil {
		t.Fatal(err)
	}
	rev, err = passport.CompileAuthored(space, "dev", time.Now(), catalogDocs, stack.ReservedSiblingPorts(space), allow)
	if err != nil {
		t.Fatal(err)
	}
	if err := rev.Publish(space); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(space, "passports", "models.yaml"), []byte("default: upstream\nroutes: {upstream: {ladder: [upstream]}}\nservices:\n  upstream: {via: openai-compatible, endpoint: http://model-relay:8318/v1, models: [{id: upstream}, {id: alt}]}\n"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := runSpaceApply(context.Background(), []string{"--dir", space, "--env", "dev"}); err != nil {
		t.Fatalf("issued models.configure failed to admit a models change: %v", err)
	}
	if err := os.WriteFile(filepath.Join(space, "passports", "workspace.yaml"), []byte("writable: [/workspace, /inbox]\nread_only: [/config]\n"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := runSpaceApply(context.Background(), []string{"--dir", space, "--env", "dev"}); err != nil {
		t.Fatalf("issued workspace.configure failed to admit a workspace change: %v", err)
	}
	grantsBody.Reset()
	grantsBody.WriteString("allow:\n  grants: [configure]\n  models: [configure]\n")
	if err := os.WriteFile(filepath.Join(space, "passports", "grants.yaml"), []byte(grantsBody.String()), 0600); err != nil {
		t.Fatal(err)
	}
	if err := runSpaceApply(context.Background(), []string{"--dir", space, "--env", "dev"}); err != nil {
		t.Fatalf("issued grants.configure failed to admit a grants change: %v", err)
	}
}

// A revision compiled against a base that is no longer current must not
// publish: another apply landed between our read and our commit, so the diff,
// the plan and the derivation were computed against a stale state (SPEC-0095
// serialization). The already-active revision stays untouched.
func TestSpaceApplyRejectsStalePreviousRevision(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "secrets.dev.env"), []byte(""), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(dir, "config"), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "config", "SOUL.md"), []byte("# persona\n"), 0600); err != nil {
		t.Fatal(err)
	}
	mkRev := func(id string) passport.Revision {
		head := "apiVersion: six-passport/v2\nmetadata: {space: dana, environment: dev, revision: " + id + ", generated_at: 2026-11-02T00:00:00Z}\n"
		return passport.Revision{ID: id, Provenance: passport.Provenance{Environment: "dev"}, Docs: map[string][]byte{
			"stack":     []byte(head + "kind: stack\nidentity: {user: dana, infra: false}\nservices:\n    hermes-runtime: {mode: supervised}\n"),
			"config":    []byte(head + "kind: config\nruntime: {timezone: UTC}\nports: {oauth-callback: 8008, browser-vnc: 6081}\n"),
			"models":    []byte(head + "kind: models\ndefault: upstream\nroutes: {upstream: {ladder: [upstream]}}\nservices:\n    upstream: {via: openai-compatible, endpoint: http://model-relay:8318/v1, models: [{id: upstream}]}\n"),
			"workspace": []byte(head + "kind: workspace\nwritable: [/workspace]\nread_only: [/config]\n"),
			"tools":     []byte(head + "kind: tools\nbinding: {profile_id: dana-default, generation: 1}\ncore:\n  files: {via: toolhub}\n"),
			"grants":    []byte(head + "kind: grants\nallow: {}\n"),
		}}
	}
	old := mkRev("r-0123456789abcdef")
	if err := old.Publish(dir); err != nil {
		t.Fatal(err)
	}
	other := mkRev("r-0123456789abcdee")
	if err := other.Publish(dir); err != nil {
		t.Fatal(err)
	}
	next := mkRev("r-0123456789abcdff")
	handlers := reconcile.Handlers{
		Rebuild:  func(string) error { t.Fatal("reconcile ran against a stale base"); return nil },
		Recreate: func(string) error { return nil },
		Respawn:  func() error { return nil },
		Live:     map[string]func() error{},
	}
	_, err := applyConfirm(context.Background(), dir, dir, "dev", next, old.ID, nil, handlers)
	if err == nil || !strings.Contains(err.Error(), "stale") {
		t.Fatalf("apply on a stale base published: %v", err)
	}
	if active, err := passport.Current(dir); err != nil || active != other.ID {
		t.Fatalf("stale apply displaced the active revision: %q, %v", active, err)
	}
}

// When the rollback itself fails, the apply must report a rollback failure —
// never "rolled back to <rev>" while the pointer and the ledger diverge
// (SPEC-0095: no false successful rollback).
func TestSpaceApplyReportsRollbackFailure(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "secrets.dev.env"), []byte(""), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(dir, "config"), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "config", "SOUL.md"), []byte("# persona\n"), 0600); err != nil {
		t.Fatal(err)
	}
	mkRev := func(id string) passport.Revision {
		head := "apiVersion: six-passport/v2\nmetadata: {space: dana, environment: dev, revision: " + id + ", generated_at: 2026-11-02T00:00:00Z}\n"
		return passport.Revision{ID: id, Provenance: passport.Provenance{Environment: "dev"}, Docs: map[string][]byte{
			"stack":     []byte(head + "kind: stack\nidentity: {user: dana, infra: false}\nservices:\n    hermes-runtime: {mode: supervised}\n"),
			"config":    []byte(head + "kind: config\nruntime: {timezone: UTC}\nports: {oauth-callback: 8008, browser-vnc: 6081}\n"),
			"models":    []byte(head + "kind: models\ndefault: upstream\nroutes: {upstream: {ladder: [upstream]}}\nservices:\n    upstream: {via: openai-compatible, endpoint: http://model-relay:8318/v1, models: [{id: upstream}]}\n"),
			"workspace": []byte(head + "kind: workspace\nwritable: [/workspace]\nread_only: [/config]\n"),
			"tools":     []byte(head + "kind: tools\nbinding: {profile_id: dana-default, generation: 1}\ncore:\n  files: {via: toolhub}\n"),
			"grants":    []byte(head + "kind: grants\nallow: {}\n"),
		}}
	}
	old := mkRev("r-0123456789abcdef")
	if err := old.Publish(dir); err != nil {
		t.Fatal(err)
	}
	// Corrupt the previous revision on disk: restoring the pointer can no
	// longer reconstruct its config view, so rollback must fail.
	if err := os.Remove(filepath.Join(dir, "generated", "passports", old.ID, "tools.yaml")); err != nil {
		t.Fatal(err)
	}
	next := mkRev("r-0123456789abcdee")
	handlers := reconcile.Handlers{
		Rebuild:  func(string) error { return errors.New("synthetic reconcile failure") },
		Recreate: func(string) error { return nil },
		Respawn:  func() error { return nil },
		Live:     map[string]func() error{},
	}
	_, err := applyConfirm(context.Background(), dir, dir, "dev", next, old.ID,
		[]reconcile.Action{{Kind: reconcile.Rebuild, Target: "model-relay"}}, handlers)
	if err == nil {
		t.Fatal("failed reconcile reported success")
	}
	if strings.Contains(err.Error(), "rolled back to") {
		t.Fatalf("apply claims a rollback that failed: %v", err)
	}
	if !strings.Contains(err.Error(), "rollback") {
		t.Fatalf("apply hides the rollback failure: %v", err)
	}
}

// The pointer rolled back cleanly but the rights-ledger restore failed: the
// apply must surface that failure — never leave the ledger advertising the
// rejected revision's allow while reporting a clean rollback (SPEC-0095).
func TestSpaceApplySurfacesLedgerRestoreFailure(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "secrets.dev.env"), []byte(""), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(dir, "config"), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "config", "SOUL.md"), []byte("# persona\n"), 0600); err != nil {
		t.Fatal(err)
	}
	// The toolhub store exists; a malformed ledger during reconcile must
	// still surface as a restore failure rather than a clean rollback.
	store := toolhub.NewStore()
	if err := store.RegisterDefinition(agenttools.HubToolsDefinition()); err != nil {
		t.Fatal(err)
	}
	if err := store.Save(filepath.Join(dir, "runtime", "toolhub", "store.json")); err != nil {
		t.Fatal(err)
	}
	mkRev := func(id, allow string) passport.Revision {
		head := "apiVersion: six-passport/v2\nmetadata: {space: dana, environment: dev, revision: " + id + ", generated_at: 2026-11-02T00:00:00Z}\n"
		return passport.Revision{ID: id, Provenance: passport.Provenance{Environment: "dev"}, Docs: map[string][]byte{
			"stack":     []byte(head + "kind: stack\nidentity: {user: dana, infra: false}\nservices:\n    hermes-runtime: {mode: supervised}\n"),
			"config":    []byte(head + "kind: config\nruntime: {timezone: UTC}\nports: {oauth-callback: 8008, browser-vnc: 6081}\n"),
			"models":    []byte(head + "kind: models\ndefault: upstream\nroutes: {upstream: {ladder: [upstream]}}\nservices:\n    upstream: {via: openai-compatible, endpoint: http://model-relay:8318/v1, models: [{id: upstream}]}\n"),
			"workspace": []byte(head + "kind: workspace\nwritable: [/workspace]\nread_only: [/config]\n"),
			"tools":     []byte(head + "kind: tools\nbinding: {profile_id: dana-default, generation: 1}\ncore:\n  files: {via: toolhub}\n"),
			"grants":    []byte(head + "kind: grants\nallow: " + allow + "\n"),
		}}
	}
	old := mkRev("r-0123456789abcdef", "{workspace: [read]}")
	if err := old.Publish(dir); err != nil {
		t.Fatal(err)
	}
	next := mkRev("r-0123456789abcdee", "{}")
	handlers := reconcile.Handlers{
		Rebuild: func(string) error {
			if err := os.WriteFile(filepath.Join(dir, "runtime", "toolhub", "governance.json"), []byte("invalid"), 0600); err != nil {
				t.Fatal(err)
			}
			return errors.New("synthetic reconcile failure")
		},
		Recreate: func(string) error { return nil },
		Respawn:  func() error { return nil },
		Live:     map[string]func() error{},
	}
	report, err := applyConfirm(context.Background(), dir, dir, "dev", next, old.ID,
		[]reconcile.Action{{Kind: reconcile.Rebuild, Target: "model-relay"}}, handlers)
	if err == nil {
		t.Fatal("failed reconcile reported success")
	}
	if !strings.Contains(err.Error(), "ledger") {
		t.Fatalf("failed ledger restore hidden by rollback message: %v", err)
	}
	if report.RolledBack {
		t.Fatalf("failed ledger restore claimed a complete rollback: %+v", report)
	}
}

// A first apply has no previous revision to roll back to — the pointer is
// removed and the rights ledger must return to the empty ceiling, not keep
// the rejected revision's derived allow (SPEC-0095).
func TestSpaceApplyFirstApplyRollbackClearsLedger(t *testing.T) {
	// filepath.Base(dir) is the derived principal — the path must name the
	// user the grants and the profile belong to.
	dir := filepath.Join(t.TempDir(), "spaces", "dana")
	if err := os.MkdirAll(dir, 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "secrets.dev.env"), []byte(""), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(dir, "config"), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "config", "SOUL.md"), []byte("# persona\n"), 0600); err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC()
	store := toolhub.NewStore()
	if err := store.RegisterDefinition(agenttools.HubToolsDefinition()); err != nil {
		t.Fatal(err)
	}
	policy := api.CapabilityPolicy{Schema: api.SchemaVersion, PolicyID: "org-default", Members: []string{"dana"}, Revision: 1,
		IssuedBy: "operator", IssuedAt: now, Reason: "fixture", Status: api.ActiveStatus}
	if err := api.Confirm(&policy, "operator", now); err != nil {
		t.Fatal(err)
	}
	if err := store.PutCapabilityPolicy(policy); err != nil {
		t.Fatal(err)
	}
	profile := api.CapabilityProfile{Schema: api.SchemaVersion, ProfileID: "dana-default", PrincipalID: "dana", ContextID: "dana", RuntimeID: "runtime",
		Environment: "dev", Generation: 1, PolicyVersion: "policy-1", PolicyID: "org-default", PolicyRevision: 1,
		Revision: 1, IssuedBy: "operator", IssuedAt: now, Reason: "fixture", Status: api.ActiveStatus}
	if err := api.Confirm(&profile, "operator", now); err != nil {
		t.Fatal(err)
	}
	if err := store.PutCapabilityProfile(profile); err != nil {
		t.Fatal(err)
	}
	storePath := filepath.Join(dir, "runtime", "toolhub", "store.json")
	if err := store.Save(storePath); err != nil {
		t.Fatal(err)
	}
	catalogDocs, err := catalog.Generate()
	if err != nil {
		t.Fatal(err)
	}
	gov := toolhub.NewGovernanceStore(toolhub.GovernanceStorePath(storePath))
	grant := api.NewRightGrant("rightgrant-dana-read", "dana", "workspace", "read", "", "fixture", "operator", now.Add(time.Hour))
	if err := api.Confirm(&grant, "operator", now); err != nil {
		t.Fatal(err)
	}
	if err := gov.GrantRight(grant, catalogDocs.Rights, now); err != nil {
		t.Fatal(err)
	}
	head := "apiVersion: six-passport/v2\nmetadata: {space: dana, environment: dev, revision: r-0123456789abcdef, generated_at: 2026-11-02T00:00:00Z}\n"
	rev := passport.Revision{ID: "r-0123456789abcdef", Provenance: passport.Provenance{Environment: "dev"}, Docs: map[string][]byte{
		"stack":     []byte(head + "kind: stack\nidentity: {user: dana, infra: false}\nservices:\n    hermes-runtime: {mode: supervised}\n"),
		"config":    []byte(head + "kind: config\nruntime: {timezone: UTC}\nports: {oauth-callback: 8008, browser-vnc: 6081}\n"),
		"models":    []byte(head + "kind: models\ndefault: upstream\nroutes: {upstream: {ladder: [upstream]}}\nservices:\n    upstream: {via: openai-compatible, endpoint: http://model-relay:8318/v1, models: [{id: upstream}]}\n"),
		"workspace": []byte(head + "kind: workspace\nwritable: [/workspace]\nread_only: [/config]\n"),
		"tools":     []byte(head + "kind: tools\nbinding: {profile_id: dana-default, generation: 1}\ncore:\n  files: {via: toolhub}\n"),
		"grants":    []byte(head + "kind: grants\nallow: {workspace: [read]}\n"),
	}}
	handlers := reconcile.Handlers{
		Rebuild:  func(string) error { return errors.New("synthetic reconcile failure") },
		Recreate: func(string) error { return nil },
		Respawn:  func() error { return nil },
		Live:     map[string]func() error{},
	}
	if _, err := applyConfirm(context.Background(), dir, dir, "dev", rev, "",
		[]reconcile.Action{{Kind: reconcile.Rebuild, Target: "model-relay"}}, handlers); err == nil {
		t.Fatal("failed reconcile reported success")
	}
	body, err := os.ReadFile(storePath)
	if err != nil {
		t.Fatal(err)
	}
	var snap struct {
		Changes []struct {
			Profile *struct {
				ProfileID string `json:"profile_id"`
				Rights    []struct {
					Capability string `json:"capability"`
					Operation  string `json:"operation"`
				} `json:"rights,omitempty"`
			} `json:"profile,omitempty"`
		} `json:"capability_changes"`
	}
	if err := json.Unmarshal(body, &snap); err != nil {
		t.Fatal(err)
	}
	var last *struct {
		ProfileID string `json:"profile_id"`
		Rights    []struct {
			Capability string `json:"capability"`
			Operation  string `json:"operation"`
		} `json:"rights,omitempty"`
	}
	for i := range snap.Changes {
		if snap.Changes[i].Profile != nil && snap.Changes[i].Profile.ProfileID == "dana-default" {
			last = snap.Changes[i].Profile
		}
	}
	if last == nil || len(last.Rights) != 0 {
		t.Fatalf("rolled-back first apply left derived rights in the ledger: %+v", last)
	}
}

// Concurrent applies on one space must serialize (SPEC-0095): a second
// apply while the lock is held refuses rather than interleaving compile,
// publish, render, derivation and reconcile into a mixed state.
func TestSpaceApplySerializesConcurrentApply(t *testing.T) {
	space := filepath.Join(t.TempDir(), "spaces", "alice")
	if err := os.MkdirAll(filepath.Join(space, "passports"), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(space, "generated"), 0700); err != nil {
		t.Fatal(err)
	}
	docs := map[string]string{
		"stack": "identity: {user: alice, infra: true}\nservices:\n" +
			"  hermes-runtime: {mode: supervised}\n  toolhub: {mode: shared}\n" +
			"  model-relay: {mode: shared}\n  communication-hub: {mode: shared}\n" +
			"  credential-broker: {mode: shared}\n  supervisor: {mode: shared}\n" +
			"  cliproxy-google-1: {mode: per-space}\n",
		"config": "runtime: {timezone: UTC}\nmemory: {enabled: false}\n",
		"models": "default: everyday\nroutes: {everyday: {ladder: [google-pro-1]}}\n" +
			"services:\n  google-pro-1:\n    via: cliproxy\n" +
			"    endpoint: http://cliproxy-google-1:8317/v1\n" +
			"    auth: {upstream: {kind: oauth-session, ref: google-one-1}, proxy_access: {kind: broker-key, ref: cliproxy-google-1}}\n" +
			"    models: [{id: gemini}]\n",
		"workspace": "writable: [/workspace]\nread_only: [/config]\n",
		"tools":     "binding: {profile_id: alice-default, generation: 1}\ncore: {files: {via: toolhub}}\n",
		"grants":    "allow: {}\n",
	}
	for name, body := range docs {
		if err := os.WriteFile(filepath.Join(space, "passports", name+".yaml"), []byte(body), 0600); err != nil {
			t.Fatal(err)
		}
	}
	held := flock.New(filepath.Join(space, "generated", "apply.lock"))
	locked, err := held.TryLock()
	if err != nil || !locked {
		t.Fatalf("fixture lock did not take: %v %v", locked, err)
	}
	defer held.Close()
	err = runSpaceApply(context.Background(), []string{"--dir", space, "--env", "dev"})
	if err == nil || !strings.Contains(err.Error(), "in progress") {
		t.Fatalf("concurrent apply proceeded past the lock: %v", err)
	}
}

func TestSpaceApplyRenderFailureDoesNotActivateRevision(t *testing.T) {
	dir := t.TempDir()
	rev := passport.Revision{ID: "r-0123456789abcdef", Docs: map[string][]byte{}}
	for _, name := range passport.Names {
		rev.Docs[name] = []byte("apiVersion: six-passport/v2\nkind: " + name + "\n")
	}
	if _, err := applyConfirm(context.Background(), dir, dir, "dev", rev, "", nil, reconcile.Handlers{}); err == nil {
		t.Fatal("invalid render succeeded")
	}
	if active, err := passport.Current(dir); err == nil || active != "" {
		t.Fatalf("failed render activated %q: %v", active, err)
	}
}

func TestSpaceApplyRenderFailureRestoresPreviousView(t *testing.T) {
	dir := t.TempDir()
	old := passport.Revision{ID: "r-0123456789abcdef", Provenance: passport.Provenance{Environment: "dev"}, Docs: map[string][]byte{}}
	for _, name := range passport.Names {
		old.Docs[name] = []byte("apiVersion: six-passport/v2\nkind: " + name + "\n")
	}
	if err := old.Publish(dir); err != nil {
		t.Fatal(err)
	}
	next := passport.Revision{ID: "r-0123456789abcdee", Provenance: old.Provenance, Docs: map[string][]byte{}}
	for name, body := range old.Docs {
		next.Docs[name] = append([]byte(nil), body...)
	}
	next.Docs["tools"] = append(next.Docs["tools"], []byte("# new revision\n")...)
	if _, err := applyConfirm(context.Background(), dir, dir, "dev", next, old.ID, nil, reconcile.Handlers{}); err == nil {
		t.Fatal("invalid render succeeded")
	}
	if active, err := passport.Current(dir); err != nil || active != old.ID {
		t.Fatalf("failed render left current=%q: %v", active, err)
	}
	view, err := os.ReadFile(filepath.Join(dir, "generated", "config-view", "dev", "tools.yaml"))
	if err != nil || string(view) != string(old.Docs["tools"]) {
		t.Fatalf("failed render left a mixed view: %q, %v", view, err)
	}
}

// A removal action must sweep compose orphans — the handler has to run
// `up --remove-orphans` so a dropped stack.services instance's container
// actually disappears (imperative topology, both directions).
func TestApplyRemoveHandlerSweepsOrphans(t *testing.T) {
	var captured []string
	old := dockerCompose
	dockerCompose = func(ctx context.Context, dir string, args ...string) error {
		captured = append([]string(nil), args...)
		return nil
	}
	defer func() { dockerCompose = old }()

	h := applyHandlers(context.Background(), t.TempDir(), "dev")
	if h.Remove == nil {
		t.Fatal("remove action has no handler")
	}
	if err := h.Remove(); err != nil {
		t.Fatal(err)
	}
	joined := strings.Join(captured, " ")
	if !strings.Contains(joined, "--remove-orphans") {
		t.Fatalf("remove handler must sweep orphans, ran: %s", joined)
	}
}

// Every named live consumer in the registry needs a wired hook — otherwise a
// leaf declared live defers at apply instead of converging.
func TestApplyLiveHooksCoverNamedConsumers(t *testing.T) {
	h := applyHandlers(context.Background(), t.TempDir(), "dev")
	for _, name := range []string{"supervisor-policy", "model-relay", "file-admission", "tool-policy", "rights-ledger"} {
		hook, ok := h.Live[name]
		if !ok {
			t.Fatalf("live consumer %q has no hook — its applies would defer", name)
		}
		if err := hook(); err != nil {
			t.Fatalf("live consumer %q hook failed: %v", name, err)
		}
	}
}

// A recreate action names the stack instance; the compose service key is the
// rendered per-space name (browser-<user>), and a supervised hermes-runtime
// has no compose service at all — it respawns through the supervisor instead
// of compose up (A4: only the owning container is touched).
func TestApplyRecreateResolvesComposeNames(t *testing.T) {
	root := t.TempDir()
	dir := filepath.Join(root, "spaces", "dana")
	rev := "r-0123456789abcdef"
	head := "apiVersion: six-passport/v2\nmetadata: {space: dana, environment: dev, revision: " + rev + "}\n"
	docs := map[string]string{
		"stack":  head + "kind: stack\nidentity: {user: dana, infra: false}\nservices:\n    browser: {mode: per-space}\n    model-relay: {mode: per-space}\n    communication-hub: {mode: shared}\n    hermes-runtime: {mode: supervised}\n",
		"config": head + "kind: config\nruntime: {timezone: UTC}\nports: {oauth-callback: 8008, browser-vnc: 6081}\nchannels:\n    telegram: {bot: {ref: telegram-bot-dana}}\n",
	}
	for name, body := range docs {
		if err := os.MkdirAll(filepath.Join(dir, "generated", "passports", rev), 0700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(dir, "generated", "passports", rev, name+".yaml"), []byte(body), 0600); err != nil {
			t.Fatal(err)
		}
	}
	for _, name := range []string{"models", "workspace", "tools", "grants"} {
		body := head + "kind: " + name + "\n"
		if name == "models" {
			body += "default: m\nroutes: {m: {ladder: [upstream]}}\nservices:\n    upstream: {via: openai-compatible, endpoint: http://model-relay:8318/v1, models: [{id: m}]}\n"
		}
		if name == "tools" {
			body += "binding: {profile_id: dana-default, generation: 1}\n"
		}
		if err := os.WriteFile(filepath.Join(dir, "generated", "passports", rev, name+".yaml"), []byte(body), 0600); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.WriteFile(filepath.Join(dir, "generated", "passports", "current"), []byte(rev+"\n"), 0600); err != nil {
		t.Fatal(err)
	}
	// A migrated space: agent.yaml keeps the operator extensions — profile
	// identity without the capability_mode authority key — and no legacy
	// authority files remain (SPEC-0092 R8.1).
	if err := os.WriteFile(filepath.Join(dir, "agent.yaml"), []byte("schema: 3\ninfra: false\ncapability_profile_id: dana-default\ncapability_generation: 1\n"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "scope.yaml"), []byte("kind: user\nid: dana\n"), 0600); err != nil {
		t.Fatal(err)
	}
	// The infra sibling owns the shared compose project the channel
	// diff's communication-hub recreate must run against.
	infra := filepath.Join(root, "spaces", "infra")
	writeManagedSpaceDocs(t, infra, "infra", managedInfraDocs("infra", false))
	var captured []string
	old := dockerCompose
	dockerCompose = func(ctx context.Context, dir string, args ...string) error {
		captured = append([]string(nil), args...)
		return nil
	}
	defer func() { dockerCompose = old }()
	h := applyHandlers(context.Background(), dir, "dev")
	if err := h.Recreate("browser"); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(strings.Join(captured, " "), "browser-dana") {
		t.Fatalf("recreate must act on the rendered service key browser-dana: %v", captured)
	}
	captured = nil
	supervisor := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {}))
	defer supervisor.Close()
	t.Setenv("HUB_RUNTIME_SUPERVISOR_URL", supervisor.URL)
	if err := h.Recreate("hermes-runtime"); err != nil {
		t.Fatal(err)
	}
	if len(captured) != 0 {
		t.Fatalf("supervised hermes-runtime has no compose service — respawn, not compose up: %v", captured)
	}
	// A shared service renders only in the infra space's project — the
	// sibling's own compose file has no communication-hub to recreate.
	captured = nil
	if err := h.Recreate("communication-hub"); err != nil {
		t.Fatal(err)
	}
	joined := strings.Join(captured, " ")
	if !strings.Contains(joined, filepath.Join("infra", "generated", "compose.dev.yaml")) || !strings.Contains(joined, "communication-hub") {
		t.Fatalf("shared service must recreate in the infra project: %v", captured)
	}
	// Rebuild names instances too: compose build takes the emitted key.
	captured = nil
	if err := h.Rebuild("browser"); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(strings.Join(captured, " "), "browser-dana") {
		t.Fatalf("rebuild must build the rendered service key browser-dana: %v", captured)
	}
	// A shared service has no rendered key in the sibling's own file —
	// rebuild must run against the infra project like recreate does, never
	// pass an empty service name to compose build.
	captured = nil
	if err := h.Rebuild("communication-hub"); err != nil {
		t.Fatal(err)
	}
	joined = strings.Join(captured, " ")
	if !strings.Contains(joined, filepath.Join("infra", "generated", "compose.dev.yaml")) || !strings.Contains(joined, "build") || !strings.Contains(joined, "communication-hub") {
		t.Fatalf("shared service must rebuild in the infra project: %v", captured)
	}
	// config.supervision.* is live-class: applyConfirm already rendered the
	// policy file before reconcile, so the hook confirms applied.
	if hook := h.Live["supervisor-policy"]; hook == nil {
		t.Fatal("supervision leaves must resolve to an applied live hook")
	}
}

// respawnRuntime runs on the operator's host while the recorded
// supervisor_url names the in-network endpoint — a host apply can never
// reach it. The operator-set HUB_RUNTIME_SUPERVISOR_URL must therefore win
// over the selection record, or every recreate/respawn of a supervised
// runtime fails unreachable (A4's owning-service recreate).
func TestRespawnRuntimePrefersEnvEndpoint(t *testing.T) {
	dir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(dir, "generated", "passports"), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "agent.yaml"), []byte("schema: 3\ninfra: false\ncapability_profile_id: dana-default\ncapability_generation: 1\n"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "scope.yaml"), []byte("kind: user\nid: dana\n"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "execution.dev.json"), []byte(`{"schema":1,"user":"dana","environment":"dev","mode":"supervisor","supervisor_url":"http://supervisor:8876","native_cron":"disabled","compatibility_release":"0.3.0"}`), 0600); err != nil {
		t.Fatal(err)
	}
	rev := "r-0123456789abcdef"
	head := "apiVersion: six-passport/v2\nmetadata: {space: dana, environment: dev, revision: " + rev + "}\n"
	if err := os.MkdirAll(filepath.Join(dir, "generated", "passports", rev), 0700); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"stack", "config", "models", "workspace", "tools", "grants"} {
		body := head + "kind: " + name + "\n"
		if name == "stack" {
			body += "identity: {user: dana, infra: false}\nservices:\n    hermes-runtime: {mode: supervised}\n    model-relay: {mode: per-space}\n"
		}
		if name == "models" {
			body += "default: m\nroutes: {m: {ladder: [upstream]}}\nservices:\n    upstream: {via: openai-compatible, endpoint: http://model-relay:8318/v1, models: [{id: m}]}\n"
		}
		if name == "tools" {
			body += "binding: {profile_id: dana-default, generation: 1}\n"
		}
		if err := os.WriteFile(filepath.Join(dir, "generated", "passports", rev, name+".yaml"), []byte(body), 0600); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.WriteFile(filepath.Join(dir, "generated", "passports", "current"), []byte(rev+"\n"), 0600); err != nil {
		t.Fatal(err)
	}
	hit := false
	api := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/v1/respawn" {
			hit = true
		}
	}))
	defer api.Close()
	// Without the env override the recorded in-network endpoint is attempted
	// — unreachable here, but it must not silently no-op.
	if err := respawnRuntime(context.Background(), dir, "dev"); err == nil {
		t.Fatal("recorded supervisor endpoint must be attempted, not skipped")
	}
	t.Setenv("HUB_RUNTIME_SUPERVISOR_URL", api.URL)
	if err := respawnRuntime(context.Background(), dir, "dev"); err != nil {
		t.Fatal(err)
	}
	if !hit {
		t.Fatal("env endpoint was not consulted before the recorded in-network URL")
	}
}

// A respawn runs destroy+spawn+readiness server-side — the client bound must
// outlive the server's operation context or the apply fails mid-replacement.
func TestRespawnClientTimeoutCoversServerBound(t *testing.T) {
	if respawnHTTPClient.Timeout <= 4*time.Minute {
		t.Fatalf("respawn client timeout %v must exceed the server's 4-minute bound", respawnHTTPClient.Timeout)
	}
}

// A supervisor image older than the respawn route answers 404 — the operator
// path degrades to the deferred restart-request marker rather than failing
// the whole apply.
func TestRespawnRuntimeFallsBackToRestartRequest(t *testing.T) {
	dir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(dir, "generated", "passports"), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "agent.yaml"), []byte("schema: 3\ninfra: false\ncapability_profile_id: dana-default\ncapability_generation: 1\n"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "scope.yaml"), []byte("kind: user\nid: dana\n"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "execution.dev.json"), []byte(`{"schema":1,"user":"dana","environment":"dev","mode":"supervisor","supervisor_url":"http://supervisor:8876","native_cron":"disabled","compatibility_release":"0.3.0"}`), 0600); err != nil {
		t.Fatal(err)
	}
	rev := "r-0123456789abcdef"
	head := "apiVersion: six-passport/v2\nmetadata: {space: dana, environment: dev, revision: " + rev + "}\n"
	if err := os.MkdirAll(filepath.Join(dir, "generated", "passports", rev), 0700); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"stack", "config", "models", "workspace", "tools", "grants"} {
		body := head + "kind: " + name + "\n"
		if name == "stack" {
			body += "identity: {user: dana, infra: false}\nservices:\n    hermes-runtime: {mode: supervised}\n    model-relay: {mode: per-space}\n"
		}
		if name == "models" {
			body += "default: m\nroutes: {m: {ladder: [upstream]}}\nservices:\n    upstream: {via: openai-compatible, endpoint: http://model-relay:8318/v1, models: [{id: m}]}\n"
		}
		if name == "tools" {
			body += "binding: {profile_id: dana-default, generation: 1}\n"
		}
		if err := os.WriteFile(filepath.Join(dir, "generated", "passports", rev, name+".yaml"), []byte(body), 0600); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.WriteFile(filepath.Join(dir, "generated", "passports", "current"), []byte(rev+"\n"), 0600); err != nil {
		t.Fatal(err)
	}
	marked := false
	api := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/v1/respawn":
			http.NotFound(w, r)
		case "/v1/restart-request":
			marked = true
		}
	}))
	defer api.Close()
	t.Setenv("HUB_RUNTIME_SUPERVISOR_URL", api.URL)
	if err := respawnRuntime(context.Background(), dir, "dev"); err != nil {
		t.Fatal(err)
	}
	if !marked {
		t.Fatal("404 on /v1/respawn did not fall back to /v1/restart-request")
	}
}
