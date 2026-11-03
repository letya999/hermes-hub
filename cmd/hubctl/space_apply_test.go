package main

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/letya999/hermes-hub/internal/passport"
	"github.com/letya999/hermes-hub/internal/reconcile"
)

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
		"stack":     []byte(head + "kind: stack\nidentity: {user: dana}\nservices:\n    cliproxy-dana: {mode: per-space}\n    model-relay: {mode: per-space}\n    hermes-runtime: {mode: supervised}\n"),
		"config":    []byte(head + "kind: config\nruntime: {timezone: UTC}\nports: {oauth-callback: 8008, browser-vnc: 6081}\n"),
		"models":    []byte(head + "kind: models\ndefault: upstream\nroutes: {upstream: {ladder: [upstream]}}\nservices:\n    upstream: {via: openai-compatible, endpoint: http://model-relay:8318/v1, models: [{id: upstream}]}\n"),
		"workspace": []byte(head + "kind: workspace\nwritable: [/workspace]\nread_only: [/config]\n"),
		"tools":     []byte(head + "kind: tools\ncore:\n  discovery: {via: toolhub}\n  files: {via: toolhub}\n"),
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
		"stack":  head + "kind: stack\nidentity: {user: dana}\nservices:\n    browser: {mode: per-space}\n    model-relay: {mode: per-space}\n    communication-hub: {mode: shared}\n    hermes-runtime: {mode: supervised}\n",
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
	if err := os.MkdirAll(infra, 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(infra, "agent.yaml"), []byte("schema: 3\ninfra: true\n"), 0600); err != nil {
		t.Fatal(err)
	}
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
			body += "identity: {user: dana}\nservices:\n    hermes-runtime: {mode: supervised}\n    model-relay: {mode: per-space}\n"
		}
		if name == "models" {
			body += "default: m\nroutes: {m: {ladder: [upstream]}}\nservices:\n    upstream: {via: openai-compatible, endpoint: http://model-relay:8318/v1, models: [{id: m}]}\n"
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
			body += "identity: {user: dana}\nservices:\n    hermes-runtime: {mode: supervised}\n    model-relay: {mode: per-space}\n"
		}
		if name == "models" {
			body += "default: m\nroutes: {m: {ladder: [upstream]}}\nservices:\n    upstream: {via: openai-compatible, endpoint: http://model-relay:8318/v1, models: [{id: m}]}\n"
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
