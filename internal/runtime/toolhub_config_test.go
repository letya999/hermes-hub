package runtime

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/letya999/hermes-hub/internal/stack"
	"gopkg.in/yaml.v3"
)

func TestMaterializeOptionsFromEnvIsOptInAndUsesEnvReference(t *testing.T) {
	t.Setenv("HUB_TOOLHUB_ENDPOINT", "")
	if got := toolHubEndpoint(); got != "" {
		t.Fatalf("unset ToolHub endpoint is not opt-in: %q", got)
	}
	dir := t.TempDir()
	source := filepath.Join(dir, "source.yaml")
	if err := os.WriteFile(source, []byte("model: {}\n"), 0600); err != nil {
		t.Fatal(err)
	}
	dest := filepath.Join(dir, "config.yaml")
	opts, err := materializeOptionsFromEnv()
	if err != nil {
		t.Fatal(err)
	}
	if err := stack.MaterializeHermesConfig(source, dest, opts); err == nil {
		t.Fatal("config materialized without a scoped ToolHub endpoint")
	}

	t.Setenv("HUB_TOOLHUB_ENDPOINT", "http://127.0.0.1:8090/mcp")
	t.Setenv("HUB_RUNTIME_AUTH", strings.Repeat("a", 32))
	t.Setenv("HUB_TOOLHUB_RECONNECT", "")
	managed, err := yaml.Marshal(stack.Config(stack.Settings{Model: "synthetic", ModelURL: "http://model-relay:8318/v1", Timezone: "UTC"}))
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(source, managed, 0600); err != nil {
		t.Fatal(err)
	}
	opts, err = materializeOptionsFromEnv()
	if err != nil {
		t.Fatal(err)
	}
	if err := stack.MaterializeHermesConfig(source, dest, opts); err != nil {
		t.Fatal(err)
	}
	body, err := os.ReadFile(dest)
	if err != nil {
		t.Fatal(err)
	}
	text := string(body)
	if !strings.Contains(text, "http://127.0.0.1:8080/mcp") || !strings.Contains(text, "${HUB_RUNTIME_AUTH}") || !strings.Contains(text, "timeout: 1800") || !strings.Contains(text, "mcp_reload_confirm: false") || strings.Contains(text, strings.Repeat("a", 32)) {
		t.Fatalf("ToolHub config leaked or omitted token reference: %s", text)
	}
	t.Setenv("HUB_TOOLHUB_RECONNECT", "false")
	opts, err = materializeOptionsFromEnv()
	if err != nil {
		t.Fatal(err)
	}
	if err := stack.MaterializeHermesConfig(source, dest, opts); err != nil {
		t.Fatal(err)
	}
	body, err = os.ReadFile(dest)
	if err != nil || strings.Contains(string(body), "mcp_reload_confirm") {
		t.Fatalf("disabled reconnect still injected the approval: %s %v", body, err)
	}
}

func TestMaterializeOptionsFromEnvCarriesNativeToolsets(t *testing.T) {
	t.Setenv("HUB_NATIVE_TOOLSETS", "terminal, memory ,,todo")
	opts, err := materializeOptionsFromEnv()
	if err != nil {
		t.Fatal(err)
	}
	if len(opts.NativeToolsets) != 3 || opts.NativeToolsets[0] != "terminal" || opts.NativeToolsets[1] != "memory" || opts.NativeToolsets[2] != "todo" {
		t.Fatalf("native toolsets lost in env transit: %+v", opts)
	}
	t.Setenv("HUB_NATIVE_TOOLSETS", "")
	opts, err = materializeOptionsFromEnv()
	if err != nil {
		t.Fatal(err)
	}
	if got := opts.NativeToolsets; len(got) != 0 {
		t.Fatalf("empty env produced grants: %v", got)
	}
}

func TestMaterializeOptionsFromEnvCarriesManagedContext(t *testing.T) {
	t.Setenv("HUB_MANAGED_CONTEXT", "")
	opts, err := materializeOptionsFromEnv()
	if err != nil {
		t.Fatal(err)
	}
	if opts.Context != nil {
		t.Fatalf("empty env produced context: %+v", opts.Context)
	}
	t.Setenv("HUB_MANAGED_CONTEXT", `{"compression":{"threshold_tokens":150000},"goals_engine":"hub","goals_max_turns":10}`)
	opts, err = materializeOptionsFromEnv()
	if err != nil {
		t.Fatal(err)
	}
	if opts.Context == nil || opts.Context.GoalsEngine != "hub" || opts.Context.GoalsMaxTurns != 10 || opts.Context.Compression["threshold_tokens"] != float64(150000) {
		t.Fatalf("managed context lost in env transit: %+v", opts.Context)
	}
	t.Setenv("HUB_MANAGED_CONTEXT", `{`)
	if _, err := materializeOptionsFromEnv(); err == nil {
		t.Fatal("invalid managed context env accepted")
	}
}

// HUB_MANAGED_BROWSER carries the render-admitted sidecar grant into the
// container; the in-container preflight must replay it verbatim (and pick the
// sidecar user from HUB_USER_ID) so the emitted server set matches the
// host-materialized config byte for byte.
func TestMaterializeOptionsFromEnvCarriesBrowserGrant(t *testing.T) {
	opts, err := materializeOptionsFromEnv()
	if err != nil {
		t.Fatal(err)
	}
	if opts.Browser != (stack.BrowserGrant{}) {
		t.Fatalf("absent env produced a grant: %+v", opts.Browser)
	}
	t.Setenv("HUB_MANAGED_BROWSER", `{"persistent":true,"guest":true,"act":true}`)
	t.Setenv("HUB_USER_ID", "alice")
	opts, err = materializeOptionsFromEnv()
	if err != nil {
		t.Fatal(err)
	}
	if opts.Browser != (stack.BrowserGrant{Persistent: true, Guest: true, Act: true}) || opts.User != "alice" {
		t.Fatalf("browser grant lost in env transit: %+v user=%q", opts.Browser, opts.User)
	}
	t.Setenv("HUB_MANAGED_BROWSER", `{`)
	if _, err := materializeOptionsFromEnv(); err == nil {
		t.Fatal("invalid browser grant env accepted")
	}
}

// HUB_MANAGED_MEMORY_VAULT carries the render-admitted subsystem flag into
// the container; the in-container preflight replays it so the emitted bytes
// match the host render. The retired HUB_MANAGED_KNOWLEDGE_UI is ignored —
// no render may reproduce open_knowledge under ToolHub vault ownership.
func TestMaterializeOptionsFromEnvCarriesFeatureFlags(t *testing.T) {
	opts, err := materializeOptionsFromEnv()
	if err != nil {
		t.Fatal(err)
	}
	if opts.MemoryVault {
		t.Fatalf("absent env produced feature flags: %+v", opts)
	}
	t.Setenv("HUB_MANAGED_KNOWLEDGE_UI", "1")
	t.Setenv("HUB_MANAGED_MEMORY_VAULT", "1")
	opts, err = materializeOptionsFromEnv()
	if err != nil {
		t.Fatal(err)
	}
	if !opts.MemoryVault {
		t.Fatalf("feature flags lost in env transit: %+v", opts)
	}
}
