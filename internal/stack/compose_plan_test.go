package stack

import (
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

// managedPassportSibling is the settings shape a managed, non-infra space
// carries into compose: supervisor-spawned runtime, relays on the shared
// network, dedicated cliproxy emission when the passport declares a
// per-space cliproxy instance.
func managedPassportSibling() Settings {
	infra := false
	return Settings{
		Schema: 1, Environment: "dev", User: "alice",
		Timezone: "UTC", OAuthPort: 8000, BrowserPort: 6080,
		Model: "m", ModelURL: "http://model-relay:8318/v1",
		CapabilityMode: "managed", CapabilityProfileID: "alice-default", CapabilityGeneration: 1,
		ExecutionMode: "supervisor", SupervisorURL: "http://supervisor:8876",
		Infra: &infra, Cliproxy: "dedicated",
	}
}

func serviceKeys(services any) []string {
	keys := []string{}
	for k := range services.(M) {
		keys = append(keys, k)
	}
	slices.Sort(keys)
	return keys
}

func TestHostingControllerRecreateNamesRenderedCompanion(t *testing.T) {
	s := managedPassportSibling()
	s.PassportServices = map[string]string{"hosting": "per-space"}
	if got, want := ComposeServiceName(s, "hosting-controller"), hostingName(s)+"-ctl"; got != want {
		t.Fatalf("hosting controller target %q, want %q", got, want)
	}
}

func TestSharedCliproxyRebuildNamesRenderedService(t *testing.T) {
	s := managedPassportSibling()
	infra := true
	s.Infra = &infra
	s.PassportServices = map[string]string{"cliproxy": "shared"}
	if got := ComposeServiceName(s, "cliproxy"); got != "cliproxy" {
		t.Fatalf("shared cliproxy rebuild targets %q, want rendered service cliproxy", got)
	}
	infra = false
	if got := ComposeServiceName(s, "cliproxy"); got != "" {
		t.Fatalf("sibling must rebuild shared cliproxy in infra project, got %q", got)
	}
}

// R3.2: the stack passport bounds the compose topology. A service the
// settings features would render but the services map never declared is
// dropped — the published revision, not feature flags, decides the set.
func TestComposePlanDropsUndeclaredServices(t *testing.T) {
	s := managedPassportSibling()
	s.Tools = testTools("browser")
	s.Ingress = testIngress("browser")
	s.PassportServices = map[string]string{
		"hermes-runtime":    "supervised",
		"communication-hub": "shared",
		"toolhub":           "shared",
		"credential-broker": "shared",
		"supervisor":        "shared",
		"model-relay":       "per-space",
		"toolhub-relay":     "per-space",
		"cliproxy-alice":    "per-space",
	}
	services := Compose(s, t.TempDir(), t.TempDir())["services"].(M)
	if _, ok := services["browser-alice"]; ok {
		t.Fatalf("browser emitted though the stack passport never declared it: %v", serviceKeys(services))
	}
	for _, name := range []string{"model-relay", "toolhub-relay", "cliproxy-alice"} {
		if _, ok := services[name]; !ok {
			t.Fatalf("declared per-space %s missing: %v", name, serviceKeys(services))
		}
	}
	if _, ok := services["cliproxy"]; ok {
		t.Fatal("sibling must not render the shared cliproxy alias")
	}
}

// R3.2: adding a per-space cliproxy instance adds exactly one container;
// several cliproxy-* instances each become their own service on the shared
// network.
func TestComposePlanRendersEachCliproxyInstance(t *testing.T) {
	s := managedPassportSibling()
	s.PassportServices = map[string]string{
		"hermes-runtime":    "supervised",
		"model-relay":       "per-space",
		"toolhub-relay":     "per-space",
		"cliproxy-google-1": "per-space",
		"cliproxy-google-2": "per-space",
	}
	services := Compose(s, t.TempDir(), t.TempDir())["services"].(M)
	if _, ok := services["cliproxy-alice"]; ok {
		t.Fatalf("legacy cliproxy-<user> emitted beside declared instances: %v", serviceKeys(services))
	}
	for _, name := range []string{"cliproxy-google-1", "cliproxy-google-2"} {
		svc, ok := services[name].(M)
		if !ok {
			t.Fatalf("declared %s missing: %v", name, serviceKeys(services))
		}
		nets, _ := svc["networks"].(M)
		if _, ok := nets[sharedNetworkName]; !ok {
			t.Fatalf("%s must reach the shared network: %v", name, nets)
		}
	}
	// Each instance mounts its own staging dir — different provider accounts
	// never share the cliproxy/ config tree.
	for _, name := range []string{"cliproxy-google-1", "cliproxy-google-2"} {
		vols := services[name].(M)["volumes"].([]any)
		src := vols[0].(M)["source"].(string)
		if !strings.HasSuffix(src, "/cliproxy/"+name) {
			t.Fatalf("%s must bind cliproxy/<instance>, got %s", name, src)
		}
	}
}

// R3.2: a declared per-space instance the emission pipeline cannot produce
// is drift between settings and the published revision — the render fails
// closed instead of silently serving a smaller topology.
func TestComposeCheckedFailsOnUnrenderableInstance(t *testing.T) {
	s := managedPassportSibling()
	s.PassportServices = map[string]string{
		"model-relay": "per-space",
		"browser":     "per-space", // feature flag absent → nothing emits it
	}
	_, err := ComposeChecked(s, t.TempDir(), t.TempDir())
	if err == nil || !strings.Contains(err.Error(), "browser") {
		t.Fatalf("expected topology drift error naming the missing instance, got %v", err)
	}
}

// The published revision carries its services map into the render: a
// per-space cliproxy instance flips the space to the dedicated branch.
func TestApplyPassportCarriesServiceTopology(t *testing.T) {
	dir := t.TempDir()
	src := filepath.Join("..", "passport", "testdata", "normative")
	rev := filepath.Join(dir, "generated", "passports", "r-0123456789abcdef")
	if err := os.MkdirAll(rev, 0700); err != nil {
		t.Fatal(err)
	}
	for _, doc := range []string{"stack.yaml", "config.yaml", "models.yaml", "workspace.yaml", "tools.yaml", "grants.yaml"} {
		body, err := os.ReadFile(filepath.Join(src, doc))
		if err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(rev, doc), body, 0600); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.WriteFile(filepath.Join(dir, "generated", "passports", "current"), []byte("r-0123456789abcdef\n"), 0600); err != nil {
		t.Fatal(err)
	}
	infra := false
	s := Settings{Infra: &infra}
	if err := s.applyPassport(dir); err != nil {
		t.Fatal(err)
	}
	if s.PassportServices["cliproxy-google-1"] != "per-space" || s.PassportServices["toolhub"] != "shared" {
		t.Fatalf("services map did not reach settings: %v", s.PassportServices)
	}
	if s.Cliproxy != "dedicated" {
		t.Fatalf("per-space cliproxy instances must select the dedicated branch, got %q", s.Cliproxy)
	}
	if s.PassportServices["hermes-runtime"] != "supervised" {
		t.Fatalf("runtime must stay supervised: %v", s.PassportServices["hermes-runtime"])
	}
}

// RenderEnvironment gates on the declared topology: a published revision
// whose per-space instances the render cannot emit fails the render —
// never a silently smaller stack on disk.
func TestRenderEnvironmentFailsClosedOnTopologyDrift(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "spaces", "alice")
	src := filepath.Join("..", "passport", "testdata", "normative")
	rev := filepath.Join(dir, "generated", "passports", "r-0123456789abcdef")
	if err := os.MkdirAll(rev, 0700); err != nil {
		t.Fatal(err)
	}
	for _, doc := range []string{"stack.yaml", "config.yaml", "models.yaml", "workspace.yaml", "tools.yaml", "grants.yaml"} {
		body, err := os.ReadFile(filepath.Join(src, doc))
		if err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(rev, doc), body, 0600); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.WriteFile(filepath.Join(dir, "generated", "passports", "current"), []byte("r-0123456789abcdef\n"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "agent.yaml"), []byte("schema: 3\ncapability_profile_id: alice-default\ncapability_generation: 1\n"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "secrets.dev.env"), []byte(""), 0600); err != nil {
		t.Fatal(err)
	}
	for _, sub := range []string{"config"} {
		if err := os.MkdirAll(filepath.Join(dir, sub), 0700); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.WriteFile(filepath.Join(dir, "config", "SOUL.md"), []byte("# persona\n"), 0600); err != nil {
		t.Fatal(err)
	}
	org := filepath.Join(dir, "..", "acme")
	for _, sub := range []string{"docs", "hermes/skills", "skills"} {
		if err := os.MkdirAll(filepath.Join(org, sub), 0700); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.WriteFile(filepath.Join(org, "settings.yaml"), []byte("kind: organization\nid: acme\nschema: 1\norganization: acme\nmembers: {alice: {role: member}}\nfeatures: [telegram]\n"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(org, "secrets.dev.env"), []byte(""), 0600); err != nil {
		t.Fatal(err)
	}
	// The normative fixture declares cliproxy-google-* per-space instances —
	// a settings-less passport-only space emits none of them. config/tools
	// docs are trimmed: no channel secrets, no mcp/raw toolset projections.
	envelope := "apiVersion: six-passport/v2\nmetadata: {space: alice, environment: dev, revision: r-0123456789abcdef, generated_at: 2026-11-02T00:00:00Z, template: platform/personal@3, catalogs: \"4\"}\n"
	minimal := envelope + "kind: tools\nbinding: {profile_id: alice-default, generation: 1}\ncore:\n  discovery: {via: toolhub}\n  files: {via: toolhub}\n"
	if err := os.WriteFile(filepath.Join(rev, "tools.yaml"), []byte(minimal), 0600); err != nil {
		t.Fatal(err)
	}
	minimal = envelope + "kind: config\nruntime: {timezone: UTC}\nports: {oauth-callback: 8008, browser-vnc: 6081}\n"
	if err := os.WriteFile(filepath.Join(rev, "config.yaml"), []byte(minimal), 0600); err != nil {
		t.Fatal(err)
	}
	view := filepath.Join(dir, "generated", "config-view", "dev")
	if err := os.MkdirAll(view, 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(view, "revision"), []byte("r-0123456789abcdef\n"), 0600); err != nil {
		t.Fatal(err)
	}
	err := RenderEnvironment(dir, dir, "dev")
	if err == nil || !strings.Contains(err.Error(), "no rendered container") {
		t.Fatalf("expected topology drift error, got %v", err)
	}
}

// Singleton per-space kinds collapse to bare catalog ids in stack.yaml; the
// plan re-expands them to the space's emitted names — hosting carries the
// environment suffix its compose key always had.
func TestPlanServiceNameExpandsSingletonKinds(t *testing.T) {
	s := managedPassportSibling()
	if got := planServiceName("hosting", s); got != "hosting-alice-dev" {
		t.Fatalf("hosting instance must expand to the env-suffixed compose key, got %q", got)
	}
	if got := planServiceName("browser", s); got != "browser-alice" {
		t.Fatalf("browser instance must expand to the user-suffixed compose key, got %q", got)
	}
}

// Shared instances are the infra stack's containers: a sibling consumes
// them by name over the shared network and renders nothing, while the infra
// space renders exactly those containers.
func TestComposePlanSharedModesFollowInfra(t *testing.T) {
	s := managedPassportSibling()
	s.PassportServices = map[string]string{
		"toolhub":        "shared",
		"model-relay":    "per-space",
		"hermes-runtime": "supervised",
	}
	if newComposePlan(s).local["toolhub"] != "" {
		t.Fatal("sibling must not plan shared services locally")
	}
	s.Infra = nil // infra default renders the shared stack
	plan := newComposePlan(s)
	if _, ok := plan.local["toolhub"]; !ok {
		t.Fatal("infra space must render its declared shared instances")
	}
}

// Emission companions (<base>-init/-ctl) inherit their base service's
// declaration, and hermes-runtime keeps the ExecutionMode contract: a
// resident runtime that the settings emitted survives the filter even
// though services map says supervised — the mode is the spawn contract,
// not the resident-runtime toggle.
func TestComposePlanKeepsCompanionsAndResidentRuntime(t *testing.T) {
	s := managedPassportSibling()
	s.PassportServices = map[string]string{
		"hermes-runtime": "supervised",
		"model-relay":    "per-space",
		"hosting":        "per-space",
	}
	plan := newComposePlan(s)
	services := M{
		"hermes-runtime":         M{},
		"model-relay":            M{},
		"hosting-alice-dev":      M{},
		"hosting-alice-dev-init": M{},
		"hosting-alice-dev-ctl":  M{},
		"stray-svc":              M{},
	}
	got := plan.filter(services)
	for _, keep := range []string{"hermes-runtime", "model-relay", "hosting-alice-dev", "hosting-alice-dev-init", "hosting-alice-dev-ctl"} {
		if _, ok := got[keep]; !ok {
			t.Fatalf("%s must survive the plan filter: %v", keep, serviceKeys(got))
		}
	}
	if _, ok := got["stray-svc"]; ok {
		t.Fatal("undeclared service must be dropped")
	}
}

// The build set follows the plan: a supervised sibling renders no
// hermes-runtime service, yet the supervisor spawns its runtime by image
// tag — the agent image must be built even though no compose service
// produces it.
func TestBuildAgentImageRequiredFromStackPlan(t *testing.T) {
	nonInfra := false
	infra := true
	for _, tc := range []struct {
		name     string
		s        Settings
		rendered M
		want     bool
	}{
		{"supervised sibling", Settings{Infra: &nonInfra, PassportServices: map[string]string{"hermes-runtime": "supervised"}}, M{}, true},
		{"runtime rendered", Settings{Infra: &nonInfra, PassportServices: map[string]string{"hermes-runtime": "supervised"}}, M{"hermes-runtime": M{}}, false},
		{"infra without runtime service", Settings{Infra: &infra}, M{}, true},
		{"plain space", Settings{Infra: &nonInfra}, M{}, false},
		{"shared-only services", Settings{Infra: &nonInfra, PassportServices: map[string]string{"toolhub": "shared"}}, M{}, false},
	} {
		if got := AgentImageRequired(tc.s, tc.rendered); got != tc.want {
			t.Errorf("%s: AgentImageRequired = %v, want %v", tc.name, got, tc.want)
		}
	}
}

// execution.<env>.json is rollout state, not authority: a managed space
// whose stack passport spawns hermes-runtime supervised selects the
// supervisor even when the file was never written.
func TestSupervisedRuntimeSelectionFromStack(t *testing.T) {
	root := t.TempDir()
	dir := filepath.Join(root, "spaces", "dana")
	rev := filepath.Join(dir, "generated", "passports", "r-0123456789abcdef")
	if err := os.MkdirAll(rev, 0700); err != nil {
		t.Fatal(err)
	}
	head := "apiVersion: six-passport/v2\nmetadata: {space: dana, environment: dev, revision: r-0123456789abcdef}\n"
	for doc, body := range map[string]string{
		"stack.yaml":     head + "kind: stack\nidentity: {user: dana, infra: false}\nservices:\n    hermes-runtime: {mode: supervised}\n    model-relay: {mode: per-space}\n    toolhub: {mode: shared}\n",
		"config.yaml":    head + "kind: config\nruntime: {timezone: UTC}\nports: {oauth-callback: 8002, browser-vnc: 6081}\n",
		"models.yaml":    head + "kind: models\ndefault: default\nroutes: {default: {ladder: [upstream]}}\nservices:\n    upstream: {via: openai-compatible, endpoint: http://model-relay:8318/v1}\n",
		"workspace.yaml": head + "kind: workspace\nwritable: [/workspace]\nread_only: [/config]\n",
		"tools.yaml":     head + "kind: tools\nbinding: {profile_id: dana-default, generation: 1}\ncore:\n    files: {via: toolhub}\n",
		"grants.yaml":    head + "kind: grants\nallow:\n    workspace: [read]\n",
	} {
		if err := os.WriteFile(filepath.Join(rev, doc), []byte(body), 0600); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.WriteFile(filepath.Join(dir, "generated", "passports", "current"), []byte("r-0123456789abcdef\n"), 0600); err != nil {
		t.Fatal(err)
	}
	view := filepath.Join(dir, "generated", "config-view", "dev")
	if err := os.MkdirAll(view, 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(view, "revision"), []byte("r-0123456789abcdef\n"), 0600); err != nil {
		t.Fatal(err)
	}
	agent := "schema: 3\ninfra: false\ncapability_mode: managed\ncapability_profile_id: dana-default\ncapability_generation: 1\n"
	if err := os.WriteFile(filepath.Join(dir, "agent.yaml"), []byte(agent), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "scope.yaml"), []byte("kind: user\nid: dana\n"), 0600); err != nil {
		t.Fatal(err)
	}
	s, err := ReadEnvironment(dir, "dev")
	if err != nil {
		t.Fatal(err)
	}
	if s.ExecutionMode != "supervisor" {
		t.Fatalf("supervised stack declaration must select the supervisor, got %q", s.ExecutionMode)
	}
}
