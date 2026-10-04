package stack

import (
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"gopkg.in/yaml.v3"
)

// Scalar and mapping forms of a tools entry decode to the same ToolEntry;
// `mcp:<server>` splits onto the server field.
func TestToolEntryYAMLForms(t *testing.T) {
	var s Settings
	if err := yaml.Unmarshal([]byte("schema: 2\nuser: alice\ntimezone: UTC\nbrowser_port: 6080\noauth_port: 8000\ntools:\n  terminal: native\n  browser: mcp:playwright\n  file:\n    via: toolhub\n    paths: [docs]\n    limits: {output_bytes: 1024}\n  ssh:\n    via: toolhub\n    tools: {write: false}\n"), &s); err != nil {
		t.Fatal(err)
	}
	s.Environment = "prod"
	if s.Tools["terminal"].Via != "native" {
		t.Fatalf("scalar backend lost: %+v", s.Tools["terminal"])
	}
	if s.Tools["browser"].Via != "mcp" || s.Tools["browser"].Server != "playwright" {
		t.Fatalf("mcp:server split lost: %+v", s.Tools["browser"])
	}
	file := s.Tools["file"]
	if file.Via != "toolhub" || !slices.Equal(file.Paths, []string{"docs"}) || file.Limits["output_bytes"] != 1024 {
		t.Fatalf("mapping form lost fields: %+v", file)
	}
	if v, ok := s.Tools["ssh"].Tools["write"]; !ok || v {
		t.Fatalf("per-tool toggle lost: %+v", s.Tools["ssh"])
	}
	if err := s.Validate(); err != nil {
		t.Fatal(err)
	}
}

// validateTools rejects malformed entries across every backend discipline.
func TestValidateToolsRejectsMalformedEntries(t *testing.T) {
	base := Settings{Schema: 2, User: "alice", Timezone: "UTC", BrowserPort: 6080, OAuthPort: 8000}
	for _, tools := range []map[string]ToolEntry{
		{"Bad Name": {Via: "native"}},
		{"file": {Via: "bogus"}},
		{"delegation": {Via: "native"}},
		{"file": {Via: "native", Paths: []string{"docs"}}},
		{"notafamily": {Via: "toolhub"}},
		{"file": {Via: "toolhub", Server: "x"}},
		{"unknown_mcp": {Via: "mcp"}},
		{"browser": {Via: "mcp", Server: "nowhere"}},
		{"code_exec": {Via: "off", Only: []string{"run"}}},
		{"file": {Via: "toolhub", Only: []string{"Bad-Name"}}},
		{"file": {Via: "toolhub", Paths: []string{"../escape"}}},
		{"file": {Via: "toolhub", Paths: []string{"/abs"}}},
		{"file": {Via: "toolhub", Limits: map[string]int{"output_bytes": 0}}},
		{"browser": {Via: "mcp", Tools: map[string]bool{"bad token": true}}},
	} {
		s := base
		s.Tools = tools
		if err := s.Validate(); err == nil {
			t.Fatalf("malformed tools accepted: %+v", tools)
		}
	}
}

// Workspace mounts: traversal, reserved roots, duplicates and the org
// read-only invariant are all rejected before render.
func TestWorkspaceMountValidation(t *testing.T) {
	good := Settings{Schema: 2, User: "alice", Environment: "prod", Timezone: "UTC", BrowserPort: 6080, OAuthPort: 8000,
		Workspace: Workspace{Mounts: []Mount{{From: "docs", To: "/docs"}, {From: "shared/in", To: "/mnt/in", Mode: "ro"}, {From: "org:docs", To: "/orgdocs"}}}}
	good.SpaceDir = t.TempDir()
	good.OrganizationDir = t.TempDir()
	if err := good.Validate(); err != nil {
		t.Fatalf("valid mounts rejected: %v", err)
	}
	for _, mounts := range [][]Mount{
		{{From: "", To: "/x"}},
		{{From: "/abs", To: "/x"}},
		{{From: "../up", To: "/x"}},
		{{From: "a\\b", To: "/x"}},
		{{From: "docs", To: "relative"}},
		{{From: "docs", To: "/docs/../x"}},
		{{From: "docs", To: "/state/evil"}},
		{{From: "docs", To: "/workspace"}},
		{{From: "docs", To: "/etc/x"}},
		{{From: "docs", To: "/x", Mode: "append"}},
		{{From: "org:docs", To: "/x", Mode: "rw"}},
		{{From: "docs", To: "/x"}, {From: "other", To: "/x"}},
	} {
		s := good
		s.Workspace = Workspace{Mounts: mounts}
		if err := s.Validate(); err == nil {
			t.Fatalf("invalid mounts accepted: %+v", mounts)
		}
	}
}

// resolveMount binds space-relative sources under SpaceDir and org: sources
// under OrganizationDir with a forced read-only flag; rendered volumes carry
// the same flags through Compose.
func TestResolveMountAndComposeVolumes(t *testing.T) {
	space, org := t.TempDir(), t.TempDir()
	s := Settings{Schema: 2, User: "alice", Environment: "prod", Timezone: "UTC", BrowserPort: 6080, OAuthPort: 8000, SpaceDir: space, OrganizationDir: org,
		Workspace: Workspace{Mounts: []Mount{
			{From: "docs", To: "/docs"},
			{From: "in", To: "/in", Mode: "ro"},
			{From: "org:shared", To: "/orgdocs"},
		}}}
	source, ro, err := s.resolveMount(s.Workspace.Mounts[0])
	if err != nil || source != filepath.Join(space, "docs") || ro {
		t.Fatalf("space mount resolved wrong: %q %v %v", source, ro, err)
	}
	source, ro, err = s.resolveMount(s.Workspace.Mounts[2])
	if err != nil || source != filepath.Join(org, "shared") || !ro {
		t.Fatalf("org mount not forced read-only: %q %v %v", source, ro, err)
	}
	s.OrganizationDir = ""
	if _, _, err := s.resolveMount(Mount{From: "org:x", To: "/x"}); err == nil {
		t.Fatal("org mount resolved without an organization dir")
	}
	s.OrganizationDir = org
	volumes := Compose(s, "/source", space)["services"].(M)["hermes-runtime"].(M)["volumes"].([]any)
	var docs, orgdocs map[string]any
	for _, v := range volumes {
		m, ok := v.(map[string]any)
		if !ok {
			continue
		}
		switch m["target"] {
		case "/docs":
			docs = m
		case "/orgdocs":
			orgdocs = m
		}
	}
	if docs == nil || orgdocs == nil {
		t.Fatalf("declared mounts missing from compose volumes: %v", volumes)
	}
	if docs["read_only"] == true || orgdocs["read_only"] != true {
		t.Fatalf("mount modes rendered wrong: %v %v", docs, orgdocs)
	}
}

// Schema-1 capability fields migrate into tools:/ingress:; mixing old and
// new keys in one file is rejected instead of silently merged.
func TestLegacySettingsMigration(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "settings.yaml")
	body := "schema: 1\nuser: alice\ntimezone: UTC\nbrowser_port: 6080\noauth_port: 8000\nfeatures: [workspace, browser, browser_act, telegram, ssh_write, hh]\nnative_toolsets: [terminal]\ndisabled_mcp: [clickhouse]\n"
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	s, err := Read(path)
	if err != nil {
		t.Fatal(err)
	}
	if s.Tools["browser"].Via != "mcp" || !s.Tools["browser"].Tools["act"] {
		t.Fatalf("browser+act migration lost: %+v", s.Tools["browser"])
	}
	if !s.Tools["ssh"].Tools["write"] {
		t.Fatalf("ssh_write migration lost: %+v", s.Tools["ssh"])
	}
	if s.Tools["terminal"].Via != "native" || s.Tools["hh"].Via != "toolhub" || s.Tools["clickhouse"].Via != "off" {
		t.Fatalf("legacy fields not migrated: %+v", s.Tools)
	}
	if !slices.Equal(s.Ingress, []string{"telegram"}) {
		t.Fatalf("ingress migration lost: %v", s.Ingress)
	}
	if !s.Has("browser_act") || !s.Has("ssh_write") || !s.Has("browser") || s.Has("clickhouse") {
		t.Fatal("Has() lost migrated state")
	}
	// Mixing legacy fields with the new surface is an operator error.
	mixed := "schema: 1\nuser: alice\ntimezone: UTC\nbrowser_port: 6080\noauth_port: 8000\nfeatures: [browser]\ntools: {file: toolhub}\n"
	if err := os.WriteFile(path, []byte(mixed), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := Read(path); err == nil {
		t.Fatal("mixed legacy and tools accepted")
	}
	// A feature colliding with an explicit native grant is an operator error.
	conflict := "schema: 1\nuser: alice\ntimezone: UTC\nbrowser_port: 6080\noauth_port: 8000\nfeatures: [terminal]\nnative_toolsets: [terminal]\n"
	if err := os.WriteFile(path, []byte(conflict), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := Read(path); err == nil {
		t.Fatal("conflicting native grant accepted")
	}
	// Unknown legacy feature names surface a migration error, not silence.
	unknown := "schema: 1\nuser: alice\ntimezone: UTC\nbrowser_port: 6080\noauth_port: 8000\nfeatures: [bogus_feature]\n"
	if err := os.WriteFile(path, []byte(unknown), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := Read(path); err == nil {
		t.Fatal("unknown legacy feature accepted")
	}
}

// ToolPlan groups entries per backend and defaults the MCP server name to
// the entry name — the compiled view the CLI prints.
func TestToolPlanGrouping(t *testing.T) {
	s := Settings{Tools: map[string]ToolEntry{
		"terminal":  {Via: "native"},
		"file":      {Via: "toolhub"},
		"github":    {Via: "mcp"},
		"browser":   {Via: "mcp", Server: "playwright"},
		"code_exec": {Via: "off"},
	}, Ingress: []string{"telegram"}, Workspace: Workspace{Mounts: []Mount{{From: "docs", To: "/docs"}}}}
	plan := s.ToolPlan()
	if !slices.Equal(plan.Native, []string{"terminal"}) || !slices.Equal(plan.ToolHub, []string{"file"}) || !slices.Equal(plan.Off, []string{"code_exec"}) {
		t.Fatalf("plan grouping wrong: %+v", plan)
	}
	if plan.MCP["github"] != "github" || plan.MCP["browser"] != "playwright" {
		t.Fatalf("plan MCP servers wrong: %+v", plan.MCP)
	}
	if !slices.Equal(plan.Ingress, []string{"telegram"}) || len(plan.Mounts) != 1 {
		t.Fatalf("plan ingress/mounts wrong: %+v", plan)
	}
	if !slices.Equal(s.NativeCarveouts(), []string{"terminal"}) {
		t.Fatalf("native carve-outs wrong: %v", s.NativeCarveouts())
	}
}

// Init seeds the embedded default template with the space identity and an
// organization claim; the file validates and mounts render for that space.
func TestInitSeedsDefaultTemplate(t *testing.T) {
	root := t.TempDir()
	if err := InitOrganization(filepath.Join(root, "organizations", "acme"), "acme", "alice"); err != nil {
		t.Fatal(err)
	}
	dir := filepath.Join(root, "spaces", "alice")
	if err := InitEnvironmentWithOrganization(dir, "alice", "prod", "acme"); err != nil {
		t.Fatal(err)
	}
	body, err := os.ReadFile(filepath.Join(dir, "settings.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(body), "user: alice") || !strings.Contains(string(body), "organization: acme") || !strings.Contains(string(body), "tools:") {
		t.Fatalf("seeded template missing substitutions:\n%s", body)
	}
	s, err := ReadEnvironment(dir, "prod")
	if err != nil {
		t.Fatal(err)
	}
	if s.Tools["browser"].Via != "mcp" || s.Tools["hh"].Via != "toolhub" || s.Tools["memory"].Via != "native" {
		t.Fatalf("seeded tools wrong: %+v", s.Tools)
	}
	// A second init never overwrites an existing space.
	if err := Init(dir, "alice"); err == nil {
		t.Fatal("init overwrote an existing space")
	}
}
