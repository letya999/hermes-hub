package migration

import (
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/letya999/hermes-hub/internal/stack"
	"gopkg.in/yaml.v3"
)

// settingsYAMLKeys enumerates every YAML-visible Settings leaf so a field a
// future change adds but the migrator never names fails this test — not a
// production space. yaml:"-" render state and unexported fields are skipped.
func settingsYAMLKeys(t *testing.T) map[string]bool {
	t.Helper()
	out := map[string]bool{}
	rt := reflect.TypeOf(stack.Settings{})
	for i := 0; i < rt.NumField(); i++ {
		f := rt.Field(i)
		if f.PkgPath != "" {
			continue
		}
		tag := f.Tag.Get("yaml")
		if tag == "" || tag == "-" {
			continue
		}
		out[strings.Split(tag, ",")[0]] = true
	}
	if len(out) == 0 {
		t.Fatal("Settings carries no yaml keys")
	}
	return out
}

// TestMigrationV2MapsEveryLegacyField is the S8.1 completeness contract
// (SPEC-0092 R8.1): fixtures exercising every YAML-visible Settings key must
// see each one named in the migration report — mapped into a v2 leaf or
// reported as a conflict. A key landing in neither is dropped authority.
func TestMigrationV2MapsEveryLegacyField(t *testing.T) {
	allKeys := settingsYAMLKeys(t)
	fixtures := map[string]string{
		// capability_mode managed forbids hooks/memory/honcho/native tools and
		// raw MCP — those keys ride the unmanaged fixture instead.
		"unmanaged": `schema: 3
user: carol
organization: acme
infra: false
timezone: Europe/Moscow
memory: true
honcho: true
honcho_url: https://honcho.example.test
global_skills_dir: /opt/global-skills
model: default
model_url: https://models.example.test
google_email: carol@example.test
gitlab_host: gitlab.example.test
desktop_url: https://desktop.example.test
drafts_url: https://drafts.example.test
diagnostics: true
summary_model: sum-one
judge_model: judge-one
oauth_port: 8101
browser_port: 8102
slack_events_port: 8088
cliproxy: shared
cliproxy_credentials: shared
cli_runtime: runsc
cli_per_principal_uid: true
cli_registries:
  - pypi
cli_toolboxes:
  box-one: ["img@sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"]
hosting:
  slots: ["/site-a"]
  image: nginx
  runtime: prod
web:
  extract_char_limit: 2000
image_gen:
  provider: cliproxy
goals:
  engine: hub
  judge: auto
  max_turns: 3
context:
  compact:
    engine: hub
    engines: [hub, native]
compression:
  enabled: false
telegram_auth: true
ingress:
  - telegram
tools:
  files:
    via: toolhub
  web:
    via: native
  image_gen:
    via: toolhub
mcp_servers:
  my-srv:
    command: /usr/bin/srv
    args: ["--flag"]
hooks:
  pre_tool: ["echo ok"]
workspace:
  goal: ship-things
  bootstrap_revision: 2
  mounts:
    - from: shared
      to: /shared/data
      mode: ro
`,
		"managed": `schema: 3
user: bob
infra: false
capability_mode: managed
capability_profile_id: bob-default
capability_generation: 3
timezone: UTC
model: default
model_url: http://model-relay:8318/v1
oauth_port: 8200
tools:
  files:
    via: toolhub
`,
	}
	models := "models:\n    default:\n        ladder:\n            - provider: cliproxy\n              model: m1\n"
	covered := map[string]bool{}
	for name, body := range fixtures {
		dir := filepath.Join(t.TempDir(), "spaces", name)
		if err := os.MkdirAll(filepath.Join(dir, "runtime", "toolhub"), 0700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(dir, "settings.yaml"), []byte(body), 0600); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(dir, "models.yaml"), []byte(models), 0600); err != nil {
			t.Fatal(err)
		}
		var top map[string]any
		if err := yaml.Unmarshal([]byte(body), &top); err != nil {
			t.Fatalf("%s fixture does not parse: %v", name, err)
		}
		rep, err := MigratePassport(PassportOptions{Dir: dir, Environment: "dev"})
		if err != nil {
			t.Fatalf("%s: migrate: %v", name, err)
		}
		seen := strings.Join(append(append([]string{}, rep.Mapped...), rep.Conflicts...), "\n")
		for key := range top {
			covered[key] = true
			if !strings.Contains(seen, key) {
				t.Errorf("%s: Settings key %q is neither mapped nor reported as a conflict\nmapped:\n%s\nconflicts:\n%s",
					name, key, strings.Join(rep.Mapped, "\n"), strings.Join(rep.Conflicts, "\n"))
			}
		}
	}
	for key := range allKeys {
		if !covered[key] {
			t.Errorf("no fixture exercises Settings.%s — extend the sweep", key)
		}
	}
}
