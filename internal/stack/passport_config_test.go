package stack

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/letya999/hermes-hub/internal/passportview"
)

// writeSixPassports publishes a minimal revision under dir/generated/passports
// so Read picks up the imperative overlay; docs overrides the per-file bodies.
func writeSixPassports(t *testing.T, dir, space, rev string, docs map[string]string) {
	writeSixPassportsInEnvironment(t, dir, space, "dev", rev, docs)
}

func writeSixPassportsInEnvironment(t *testing.T, dir, space, environment, rev string, docs map[string]string) {
	t.Helper()
	revDir := filepath.Join(dir, "generated", "passports", rev)
	if err := os.MkdirAll(revDir, 0700); err != nil {
		t.Fatal(err)
	}
	header := func(kind string) string {
		return "apiVersion: six-passport/v2\nkind: " + kind + "\nmetadata: {space: " + space + ", environment: " + environment + ", revision: " + rev + "}\n"
	}
	for _, name := range []string{"stack", "config", "models", "workspace", "tools", "grants"} {
		body, ok := docs[name+".yaml"]
		if !ok {
			body = header(name)
			switch name {
			case "stack":
				infra := "false"
				if space == "infra" {
					infra = "true"
				}
				body += "identity: {user: " + space + ", infra: " + infra + "}\nservices:\n  hermes-runtime: {mode: per-space}\n  model-relay: {mode: per-space}\n  toolhub-relay: {mode: per-space}\n  toolhub: {mode: shared}\n  credential-broker: {mode: shared}\n"
			case "config":
				body += "runtime: {timezone: UTC}\n"
			case "models":
				body += "default: m\nroutes: {m: {ladder: [cliproxy]}}\nservices: {cliproxy: {via: cliproxy, endpoint: 'http://cliproxy:8317/v1', models: [{id: m}]}}\n"
			case "workspace":
				body += "writable: [/workspace]\nread_only: [/config]\n"
			case "tools":
				body += "binding: {profile_id: " + space + "-default, generation: 1}\n"
			}
		}
		if name == "tools" && !strings.Contains(body, "binding:") {
			body += "binding: {profile_id: " + space + "-default, generation: 1}\n"
		}
		if err := os.WriteFile(filepath.Join(revDir, name+".yaml"), []byte(body), 0600); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.WriteFile(filepath.Join(dir, "generated", "passports", "current"), []byte(rev+"\n"), 0600); err != nil {
		t.Fatal(err)
	}
	viewDir := filepath.Join(dir, "generated", "config-view", environment)
	if err := os.MkdirAll(viewDir, 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(viewDir, "revision"), []byte(rev+"\n"), 0600); err != nil {
		t.Fatal(err)
	}
}

func TestManagedWebPolicyFromConfigPassport(t *testing.T) {
	root := t.TempDir()
	dir := filepath.Join(root, "spaces", "alice")
	rev := "r-0123456789abcdef"
	writeSixPassports(t, dir, "alice", rev, map[string]string{
		"config.yaml": "apiVersion: six-passport/v2\nkind: config\nmetadata: {space: alice, environment: dev, revision: " + rev + "}\n" +
			"runtime: {timezone: UTC}\nweb: {search_backend: keenable, extract_backend: keenable, search_providers: [keenable, ddgs], cache_enabled: true}\n",
		"tools.yaml": "apiVersion: six-passport/v2\nkind: tools\nmetadata: {space: alice, environment: dev, revision: " + rev + "}\n" +
			"binding: {profile_id: alice-default, generation: 1}\ncore: {web: {via: native}}\n",
	})
	if err := os.WriteFile(filepath.Join(dir, "agent.yaml"), []byte("schema: 3\nuser: alice\n"), 0600); err != nil {
		t.Fatal(err)
	}
	s, err := ReadEffective(dir, "dev")
	if err != nil {
		t.Fatal(err)
	}
	if got := s.managedWebSettings(); got.SearchBackend != "keenable" || got.ExtractBackend != "keenable" ||
		len(got.SearchProviders) != 2 || got.SearchProviders[1] != "ddgs" || got.CacheEnabled == nil || !*got.CacheEnabled {
		t.Fatalf("managed web policy lost: %+v", got)
	}
}

// config.ports is the imperative host-port contract: each declared name lands
// on the service that owns it — oauth-callback on the runtime listener,
// browser-vnc on the browser sidecar, slack-events on the shared gateway —
// and the OAuth redirect URI re-derives from the same leaf (SPEC-0092 R4.2).
func TestComposePortsFromConfigPassport(t *testing.T) {
	root := t.TempDir()
	dir := filepath.Join(root, "spaces", "infra")
	rev := "r-0123456789abcdef"
	writeSixPassports(t, dir, "infra", rev, map[string]string{
		"stack.yaml": "apiVersion: six-passport/v2\nkind: stack\nmetadata: {space: infra, environment: dev, revision: " + rev + "}\n" +
			"identity: {user: infra}\n" +
			"services:\n" +
			"    hermes-runtime: {mode: per-space}\n" +
			"    browser: {mode: per-space}\n" +
			"    communication-hub: {mode: shared}\n",
		"config.yaml": "apiVersion: six-passport/v2\nkind: config\nmetadata: {space: infra, environment: dev, revision: " + rev + "}\n" +
			"runtime: {timezone: UTC}\n" +
			"ports: {oauth-callback: 8008, browser-vnc: 6081, slack-events: 9090}\n" +
			"channels:\n" +
			"    telegram: {bot: {ref: telegram-bot-infra}}\n" +
			"    slack_app: {bot: {ref: slack-bot-infra}}\n",
		"tools.yaml": "apiVersion: six-passport/v2\nkind: tools\nmetadata: {space: infra, environment: dev, revision: " + rev + "}\n" +
			"binding: {profile_id: infra-default, generation: 1}\nextended:\n    browser: {via: mcp:playwright}\n",
	})
	if err := os.WriteFile(filepath.Join(dir, "agent.yaml"), []byte("schema: 3\ninfra: true\n"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "scope.yaml"), []byte("kind: user\nid: infra\n"), 0600); err != nil {
		t.Fatal(err)
	}
	s, err := ReadEffective(dir, "prod")
	if err != nil {
		t.Fatal(err)
	}
	services := Compose(s, root, dir)["services"].(M)
	services["hermes-runtime"] = RuntimeService(s, root, dir)
	ports := func(service string) []string {
		list, _ := services[service].(M)["ports"].([]string)
		return list
	}
	want := map[string]string{
		"browser-infra":     "127.0.0.1:6081:6080",
		"communication-hub": "127.0.0.1:9090:8081",
	}
	for service, wantPort := range want {
		found := false
		for _, p := range ports(service) {
			if p == wantPort {
				found = true
			}
		}
		if !found {
			t.Fatalf("%s does not publish %s (ports: %v)", service, wantPort, ports(service))
		}
	}
	env := services["hermes-runtime"].(M)["environment"].(M)
	if got := env["GOOGLE_OAUTH_REDIRECT_URI"]; got != "http://localhost:8008/oauth2callback" {
		t.Fatalf("OAuth redirect URI must follow ports.oauth-callback, got %v", got)
	}
}

// config.runtime is the imperative runtime section: timezone and locale ride
// the rendered environment, compaction lands in the context policy the
// effective Hermes config materializes, and parallel_lanes is the shared
// gateway's per-context lane cap (SPEC-0092 R4.2).
func TestEffectiveHermesConfigFromConfigPassport(t *testing.T) {
	root := t.TempDir()
	dir := filepath.Join(root, "spaces", "infra")
	rev := "r-0123456789abcdef"
	writeSixPassports(t, dir, "infra", rev, map[string]string{
		"stack.yaml": "apiVersion: six-passport/v2\nkind: stack\nmetadata: {space: infra, environment: dev, revision: " + rev + "}\n" +
			"identity: {user: infra}\n" +
			"services:\n" +
			"    hermes-runtime: {mode: per-space}\n" +
			"    communication-hub: {mode: shared}\n",
		"config.yaml": "apiVersion: six-passport/v2\nkind: config\nmetadata: {space: infra, environment: dev, revision: " + rev + "}\n" +
			"runtime: {timezone: Europe/Moscow, locale: ru, context: {compaction: auto, parallel_lanes: 2}}\n" +
			"ports: {oauth-callback: 8008}\n" +
			"channels:\n    telegram: {bot: {ref: telegram-bot-infra}}\n",
	})
	if err := os.WriteFile(filepath.Join(dir, "agent.yaml"), []byte("schema: 3\ninfra: true\n"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "scope.yaml"), []byte("kind: user\nid: infra\n"), 0600); err != nil {
		t.Fatal(err)
	}
	s, err := ReadEffective(dir, "prod")
	if err != nil {
		t.Fatal(err)
	}
	services := Compose(s, root, dir)["services"].(M)
	runtimeEnv := RuntimeService(s, root, dir)["environment"].(M)
	if got := runtimeEnv["TZ"]; got != "Europe/Moscow" {
		t.Fatalf("runtime.timezone must render TZ, got %v", got)
	}
	if got := runtimeEnv["HUB_DEFAULT_LANG"]; got != "ru" {
		t.Fatalf("runtime.locale must render HUB_DEFAULT_LANG, got %v", got)
	}
	ctxJSON, _ := runtimeEnv["HUB_MANAGED_CONTEXT"].(string)
	if !strings.Contains(ctxJSON, `"compact_engine":"auto"`) {
		t.Fatalf("runtime.context.compaction must ride the context policy env, got %v", ctxJSON)
	}
	gatewayEnv := services["communication-hub"].(M)["environment"].(M)
	if got := gatewayEnv["HUB_CONTEXT_MAX_PARALLEL"]; got != "2" {
		t.Fatalf("runtime.context.parallel_lanes must render the gateway lane cap, got %v", got)
	}
	if got := gatewayEnv["HUB_DEFAULT_LANG"]; got != "ru" {
		t.Fatalf("runtime.locale must render on the gateway too, got %v", got)
	}
}

// config.channels is the imperative channel section: a named channel is its
// own ingress on the gateway, and its voice block wires the STT/TTS service
// endpoints — independent of the legacy transcription feature (SPEC-0092
// R4.2). Credential refs stay names; the env/secret path resolves values at
// service start.
func TestGatewayChannelsFromConfigPassport(t *testing.T) {
	root := t.TempDir()
	dir := filepath.Join(root, "spaces", "infra")
	rev := "r-0123456789abcdef"
	writeSixPassports(t, dir, "infra", rev, map[string]string{
		"stack.yaml": "apiVersion: six-passport/v2\nkind: stack\nmetadata: {space: infra, environment: dev, revision: " + rev + "}\n" +
			"identity: {user: infra}\n" +
			"services:\n" +
			"    hermes-runtime: {mode: per-space}\n" +
			"    communication-hub: {mode: shared}\n" +
			"    hub-stt: {mode: per-space}\n" +
			"    hub-tts: {mode: per-space}\n",
		"config.yaml": "apiVersion: six-passport/v2\nkind: config\nmetadata: {space: infra, environment: dev, revision: " + rev + "}\n" +
			"runtime: {timezone: UTC}\n" +
			"ports: {oauth-callback: 8008}\n" +
			"channels:\n" +
			"    telegram:\n" +
			"        bot: {ref: telegram-bot-infra}\n" +
			"        allowed_users: {ref: telegram-allow-infra}\n" +
			"        voice: {in: transcribe, out: tts}\n",
	})
	if err := os.WriteFile(filepath.Join(dir, "agent.yaml"), []byte("schema: 3\ninfra: true\n"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "scope.yaml"), []byte("kind: user\nid: infra\n"), 0600); err != nil {
		t.Fatal(err)
	}
	s, err := ReadEffective(dir, "prod")
	if err != nil {
		t.Fatal(err)
	}
	if !s.Has("telegram") {
		t.Fatal("channels.telegram must land on the space's ingress surface")
	}
	services := Compose(s, root, dir)["services"].(M)
	gatewayEnv := services["communication-hub"].(M)["environment"].(M)
	if got := gatewayEnv["HUB_STT_URL"]; got != "http://hub-stt:8090" {
		t.Fatalf("channels.telegram.voice.in must wire the STT endpoint, got %v", got)
	}
	if got := gatewayEnv["HUB_TTS_URL"]; got != "http://hub-tts:8090" {
		t.Fatalf("channels.telegram.voice.out must wire the TTS endpoint, got %v", got)
	}
}

// config.supervision renders beside the runtime env as the policy file the
// supervisor re-reads per decision (SPEC-0092 R4.2); no supervision section
// means no file — the deployment defaults govern.
func TestSupervisorPolicyFileFromConfigPassport(t *testing.T) {
	dir := t.TempDir()
	s := Settings{Environment: "dev"}
	if err := writeSupervisorPolicy(dir, s); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, "runtime.dev.policy.json")
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Fatalf("empty supervision must not emit a policy file: %v", err)
	}
	s.PassportConfig = &passportview.Config{Supervision: passportview.Supervision{
		IdleStop: "5m", Restart: "on-failure",
		Watchers: map[string]passportview.Watcher{"stack-health": {Protected: true, Every: "1m"}},
	}}
	if err := writeSupervisorPolicy(dir, s); err != nil {
		t.Fatal(err)
	}
	body, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	got := string(body)
	for _, want := range []string{`"idle_stop":"5m"`, `"restart":"on-failure"`, `"stack-health"`, `"protected":true`, `"every":"1m"`} {
		if !strings.Contains(got, want) {
			t.Fatalf("policy file missing %s: %s", want, got)
		}
	}
}

// Managed memory cannot bypass ToolHub through a legacy backend extension.
func TestManagedMemoryRejectsLegacyBackend(t *testing.T) {
	root := t.TempDir()
	dir := filepath.Join(root, "spaces", "infra")
	rev := "r-0123456789abcdef"
	writeSixPassports(t, dir, "infra", rev, map[string]string{
		"config.yaml": "apiVersion: six-passport/v2\nkind: config\nmetadata: {space: infra, environment: dev, revision: " + rev + "}\n" +
			"runtime: {timezone: UTC}\n" +
			"memory: {backend: honcho}\n",
	})
	if err := os.WriteFile(filepath.Join(dir, "agent.yaml"), []byte("schema: 3\ninfra: true\nhoncho_url: http://honcho:8000\n"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "scope.yaml"), []byte("kind: user\nid: infra\n"), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := ReadEffective(dir, "prod"); err == nil {
		t.Fatal("managed memory admitted the legacy Honcho backend")
	}
}
