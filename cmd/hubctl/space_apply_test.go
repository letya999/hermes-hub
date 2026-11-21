package main

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/letya999/hermes-hub/internal/identity"
	"github.com/letya999/hermes-hub/internal/stack"
)

func TestRecreateRemovedHostingControllerSweepsOrphan(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "spaces", "alice")
	writeManagedSpaceFixture(t, dir, "alice")
	stubSpawnClient(t, dir)
	previous := dockerCompose
	defer func() { dockerCompose = previous }()
	var got []string
	dockerCompose = func(_ context.Context, _ string, args ...string) error {
		got = append([]string(nil), args...)
		return nil
	}
	if err := applyHandlers(context.Background(), dir, filepath.Dir(filepath.Dir(dir)), "dev", "").Recreate("hosting-controller"); err != nil {
		t.Fatal(err)
	}
	if !slices.Contains(got, "--remove-orphans") || slices.Contains(got, "hosting-controller") {
		t.Fatalf("removed controller was not swept from restored project: %v", got)
	}
}

func TestRefreshSharedRuntimeTokensAfterSiblingApply(t *testing.T) {
	spaces := filepath.Join(t.TempDir(), "spaces")
	infra, sibling := filepath.Join(spaces, "alice"), filepath.Join(spaces, "bob")
	writeManagedSpaceDocs(t, infra, "alice", managedInfraDocs("alice", false))
	writeManagedSpaceFixture(t, sibling, "bob")
	ownerToken, siblingToken := strings.Repeat("a", 64), strings.Repeat("b", 64)
	for path, token := range map[string]string{infra: ownerToken, sibling: siblingToken} {
		if err := os.WriteFile(filepath.Join(path, "runtime.auth"), []byte("HUB_RUNTIME_AUTH="+token+"\n"), 0600); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.MkdirAll(filepath.Join(infra, "passports"), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(infra, "passports", "stack.yaml"), []byte("identity: {user: alice, infra: true}\n"), 0600); err != nil {
		t.Fatal(err)
	}
	settings, err := stack.ReadEffective(context.Background(), spacesRootReader{root: filepath.Dir(sibling)}, sibling, "dev")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(infra, "secrets.dev.env"), nil, 0600); err != nil {
		t.Fatal(err)
	}
	routing := filepath.Join(infra, "automation-routing", "dev.json")
	if err := os.MkdirAll(filepath.Dir(routing), 0750); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(routing, []byte(`{"organization_id":"personal","users":[{"id":"bob","enabled":true,"runtime_id":"bob","policy_version":"policy-old","telegram_ids":[22]}]}`), 0640); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(infra, "communication.users.yaml"), []byte(fmt.Sprintf("organization_id: personal\nusers:\n  - id: bob\n    enabled: true\n    runtime_id: bob\n    policy_version: %s\n    telegram_ids: [22]\n", stack.PolicyVersion(settings))), 0600); err != nil {
		t.Fatal(err)
	}
	if err := refreshSharedRuntimeTokens(context.Background(), spacesRootReader{root: filepath.Dir(sibling)}, sibling, "dev"); err != nil {
		t.Fatal(err)
	}
	body, err := os.ReadFile(filepath.Join(infra, "toolhub-tokens.json"))
	if err != nil {
		t.Fatal(err)
	}
	var enrolled map[string]identity.Envelope
	if err := json.Unmarshal(body, &enrolled); err != nil {
		t.Fatal(err)
	}
	if enrolled[siblingToken].PolicyVersion != stack.PolicyVersion(settings) {
		t.Fatal("sibling token kept a stale policy after apply")
	}
	var route struct {
		Users []struct {
			PolicyVersion string `json:"policy_version"`
		} `json:"users"`
	}
	body, err = os.ReadFile(routing)
	if err != nil || json.Unmarshal(body, &route) != nil || len(route.Users) != 1 || route.Users[0].PolicyVersion != stack.PolicyVersion(settings) {
		t.Fatal("shared automation routing kept a stale policy after apply")
	}
	if _, ok := enrolled[ownerToken]; ok {
		t.Fatal("infra owner's bearer leaked into sibling enrollment")
	}
}

// space apply --confirm must re-render the environment before reconcile
// acts: a newly declared per-space instance only exists in the generated
// compose file once the published revision is rendered — acting on the
// previous render hits "no such service".
func TestApplyRemoveHandlerSweepsOrphans(t *testing.T) {
	dir := t.TempDir()
	writeManagedSpaceDocs(t, dir, "alice", nil)
	stubSpawnClient(t, dir)
	var captured []string
	old := dockerCompose
	dockerCompose = func(ctx context.Context, dir string, args ...string) error {
		captured = append([]string(nil), args...)
		return nil
	}
	defer func() { dockerCompose = old }()

	h := applyHandlers(context.Background(), dir, t.TempDir(), "dev", "")
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

func TestApplyRebuildsSupervisedRuntimeImage(t *testing.T) {
	root := t.TempDir()
	dir := filepath.Join(root, "spaces", "alice")
	writeManagedSpaceFixture(t, dir, "alice")
	stubSpawnClient(t, dir)
	previous := applyImageBuild
	defer func() { applyImageBuild = previous }()
	var got []string
	applyImageBuild = func(_ context.Context, args []string) error {
		got = append([]string(nil), args...)
		return nil
	}
	if err := applyHandlers(context.Background(), dir, root, "dev", "").Rebuild("hermes-runtime"); err != nil {
		t.Fatal(err)
	}
	joined := strings.Join(got, " ")
	if !strings.HasPrefix(joined, "build --dir "+dir) || !strings.HasSuffix(joined, "--image runtime") {
		t.Fatalf("supervised runtime rebuild did not produce the agent image: %v", got)
	}
}

func TestApplyLiveHooksCoverNamedConsumers(t *testing.T) {
	h := applyHandlers(context.Background(), t.TempDir(), t.TempDir(), "dev", "")
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
	view := filepath.Join(dir, "generated", "config-view", "dev")
	if err := os.MkdirAll(view, 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(view, "revision"), []byte(rev+"\n"), 0600); err != nil {
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
	infraDocs := managedInfraDocs("infra", false)
	infraDocs["stack"] = strings.Replace(infraDocs["stack"], "model-relay: {mode: shared}", "model-relay: {mode: per-space}", 1)
	writeManagedSpaceDocs(t, infra, "infra", infraDocs)
	if err := os.MkdirAll(filepath.Join(infra, "passports"), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(infra, "passports", "stack.yaml"), []byte("identity: {user: infra, infra: true}\n"), 0600); err != nil {
		t.Fatal(err)
	}
	var captured []string
	old := dockerCompose
	dockerCompose = func(ctx context.Context, dir string, args ...string) error {
		captured = append([]string(nil), args...)
		return nil
	}
	defer func() { dockerCompose = old }()
	stubSpawnClient(t, dir)
	h := applyHandlers(context.Background(), dir, filepath.Dir(filepath.Dir(dir)), "dev", "")
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
	if !strings.Contains(joined, filepath.Join("infra", "generated", "compose.shared.dev.yaml")) || !strings.Contains(joined, "communication-hub") {
		t.Fatalf("shared service must recreate in the infra project: %v", captured)
	}
	// Rebuild delegates the selected image to the same path as hubctl build.
	oldBuild := applyImageBuild
	defer func() { applyImageBuild = oldBuild }()
	applyImageBuild = func(_ context.Context, args []string) error {
		captured = append([]string(nil), args...)
		return nil
	}
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
	if !strings.Contains(joined, "--dir "+infra) || !strings.HasPrefix(joined, "build ") || !strings.HasSuffix(joined, "communication-hub") {
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
	view := filepath.Join(dir, "generated", "config-view", "dev")
	if err := os.MkdirAll(view, 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(view, "revision"), []byte(rev+"\n"), 0600); err != nil {
		t.Fatal(err)
	}
	stubSpawnClient(t, dir)
	hit, applyHeader := false, ""
	api := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/v1/respawn" {
			hit = true
			applyHeader = r.Header.Get("X-Hub-Apply-Revision")
		}
	}))
	defer api.Close()
	// Without the env override the recorded in-network endpoint is attempted
	// — unreachable here, but it must not silently no-op.
	if err := respawnRuntime(context.Background(), dir, "dev", ""); err == nil {
		t.Fatal("recorded supervisor endpoint must be attempted, not skipped")
	}
	t.Setenv("HUB_RUNTIME_SUPERVISOR_URL", api.URL)
	if err := respawnRuntime(context.Background(), dir, "dev", ""); err != nil {
		t.Fatal(err)
	}
	if !hit {
		t.Fatal("env endpoint was not consulted before the recorded in-network URL")
	}
	if applyHeader != "" {
		t.Fatal("ordinary respawn carried an apply proof")
	}
	// S7d: the apply passes its activated digest as the proof — the v0
	// .policy indirection that carried it on disk is gone.
	digest := "sha256:" + strings.Repeat("ab", 32)
	if err := respawnRuntime(context.Background(), dir, "dev", digest); err != nil || applyHeader != digest {
		t.Fatalf("apply respawn proof=%q err=%v", applyHeader, err)
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
	view := filepath.Join(dir, "generated", "config-view", "dev")
	if err := os.MkdirAll(view, 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(view, "revision"), []byte(rev+"\n"), 0600); err != nil {
		t.Fatal(err)
	}
	marked := false
	stubSpawnClient(t, dir)
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
	if err := respawnRuntime(context.Background(), dir, "dev", ""); err != nil {
		t.Fatal(err)
	}
	if !marked {
		t.Fatal("404 on /v1/respawn did not fall back to /v1/restart-request")
	}
}
