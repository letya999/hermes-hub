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
	agent, err := os.ReadFile(filepath.Join(dir, "agent.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	workspace, err := os.ReadFile(filepath.Join(dir, "workspace.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(agent), "user: alice") || !strings.Contains(string(agent), "organization: acme") || !strings.Contains(string(workspace), "tools:") {
		t.Fatalf("seeded template missing substitutions:\n%s\n%s", agent, workspace)
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

// WriteSpace splits operator fields into agent.yaml and capability intent
// into workspace.yaml; Read on the directory reassembles the same Settings.
func TestWriteSpaceRoundTrip(t *testing.T) {
	dir := t.TempDir()
	s := Settings{
		Schema: 3, User: "alice", Organization: "acme", Environment: "prod",
		Model: "gemini-3-pro", ModelURL: "http://relay:8318/v1", Timezone: "UTC",
		BrowserPort: 6080, OAuthPort: 8000, SpaceDir: dir,
		Tools: map[string]ToolEntry{
			"terminal": {Via: "native"},
			"file":     {Via: "toolhub"},
			"browser":  {Via: "mcp", Server: "playwright", Access: "ro"},
			"gitea":    {Via: "mcp-raw", Server: "gitea"},
			"ssh":      {Via: "toolhub", Access: "ro"},
		},
		Ingress:   []string{"telegram"},
		Workspace: Workspace{Mounts: []Mount{{From: "docs", To: "/docs", Mode: "ro"}}},
		MCP:       map[string]MCPServer{"gitea": {URL: "http://gitea:3000/mcp"}},
	}
	if err := WriteSpace(dir, s); err != nil {
		t.Fatal(err)
	}
	// The pair keeps its separation: no capability keys leak into agent.yaml.
	agent, err := os.ReadFile(filepath.Join(dir, "agent.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	for _, key := range []string{"tools:", "ingress:", "mounts:", "mcp:"} {
		if strings.Contains(string(agent), key) {
			t.Fatalf("agent.yaml leaked capability key %s:\n%s", key, agent)
		}
	}
	workspace, err := os.ReadFile(filepath.Join(dir, "workspace.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	for _, key := range []string{"model:", "timezone:", "oauth_port:", "browser_port:", "model_url:"} {
		if strings.Contains(string(workspace), key) {
			t.Fatalf("workspace.yaml leaked runtime key %s:\n%s", key, workspace)
		}
	}
	back, err := Read(dir)
	if err != nil {
		t.Fatal(err)
	}
	if back.Tools["browser"].Access != "ro" || back.Tools["gitea"].Via != "mcp-raw" ||
		back.Model != s.Model || back.OAuthPort != s.OAuthPort {
		t.Fatalf("round-trip lost fields: %+v", back)
	}
}

// Cross-file placement is rejected: capability keys in agent.yaml and runtime
// keys in workspace.yaml are operator errors, not silent merges.
func TestSplitRejectsMisplacedKeys(t *testing.T) {
	dir := t.TempDir()
	agent := "schema: 3\nuser: alice\ntimezone: UTC\noauth_port: 8000\nbrowser_port: 6080\n"
	workspace := "tools: {terminal: native}\n"
	write := func(a, w string) {
		if err := os.WriteFile(filepath.Join(dir, "agent.yaml"), []byte(a), 0o600); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(dir, "workspace.yaml"), []byte(w), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	write(agent, workspace)
	if _, err := Read(dir); err != nil {
		t.Fatalf("valid pair rejected: %v", err)
	}
	write(agent+"tools: {file: toolhub}\n", workspace)
	if _, err := Read(dir); err == nil {
		t.Fatal("tools in agent.yaml accepted")
	}
	write(agent, workspace+"model: bogus\n")
	if _, err := Read(dir); err == nil {
		t.Fatal("model in workspace.yaml accepted")
	}
}

// access: ro suppresses every write-effect toggle of the entry — Has() is the
// predicate the runtime and org-action mapping read.
func TestAccessROSuppressesToggles(t *testing.T) {
	s := Settings{Schema: 3, User: "alice", Timezone: "UTC", BrowserPort: 6080, OAuthPort: 8000, SpaceDir: t.TempDir(),
		Tools: map[string]ToolEntry{
			"browser":       {Via: "mcp", Server: "playwright", Access: "ro"},
			"ssh":           {Via: "toolhub", Access: "ro"},
			"google":        {Via: "mcp", Access: "ro"},
			"telegram_user": {Via: "mcp", Access: "ro"},
		}}
	if s.Has("browser_act") || s.Has("ssh_write") || s.Has("google_write") || s.Has("telegram_write") {
		t.Fatalf("ro entry leaked a write toggle: %+v", s.Tools)
	}
	if !s.Has("browser") || !s.Has("ssh") || !s.Has("google") {
		t.Fatalf("ro suppressed the read side: %+v", s.Tools)
	}
}

// readOnlyCapabilities maps ro tool entries onto ToolHub capability IDs so the
// executor can hard-deny write-effect tools at call time; the compose env is
// the transport that carries them into the runtime.
func TestReadOnlyCapabilitiesEnv(t *testing.T) {
	s := Settings{User: "alice", Environment: "prod", SpaceDir: t.TempDir(),
		Tools: map[string]ToolEntry{
			"file": {Via: "toolhub", Access: "ro"},
			"ssh":  {Via: "toolhub", Access: "ro"},
			"docs": {Via: "toolhub"},
		}}
	ro := s.readOnlyCapabilities()
	for _, id := range []string{"files", "ssh"} {
		if !slices.Contains(ro, id) {
			t.Fatalf("ro capabilities missing %s: %v", id, ro)
		}
	}
	if slices.Contains(ro, "documents") {
		t.Fatalf("rw capability leaked into ro set: %v", ro)
	}
	env := Compose(s, "/source", s.SpaceDir)["services"].(M)["hermes-runtime"].(M)["environment"].(M)
	v, _ := env["HUB_TOOLS_RO"].(string)
	if !strings.Contains(v, "files") || !strings.Contains(v, "ssh") {
		t.Fatalf("HUB_TOOLS_RO rendered wrong: %q", v)
	}
}

// access: ro on a raw MCP server appends the reviewed mutation tools to
// tools.exclude; an unknown server must carry an explicit except list.
func TestMCPRawROExcludesMutations(t *testing.T) {
	dir := t.TempDir()
	s := Settings{Schema: 3, User: "alice", Environment: "prod", Timezone: "UTC", BrowserPort: 6080, OAuthPort: 8000, SpaceDir: dir,
		Tools: map[string]ToolEntry{"browser": {Via: "mcp-raw", Server: "playwright", Access: "ro"}},
		MCP:   map[string]MCPServer{"playwright": {Command: "npx", Args: []string{"-y", "@playwright/mcp"}}}}
	if err := s.Validate(); err != nil {
		t.Fatalf("ro raw MCP rejected: %v", err)
	}
	rendered := rawMCPServers(s)
	pw, _ := rendered["playwright"].(M)["tools"].(MCPTools)
	if len(pw.Exclude) == 0 {
		t.Fatalf("ro raw MCP rendered without excludes: %+v", rendered["playwright"])
	}
	for _, tool := range []string{"browser_type", "browser_click", "browser_fill_form"} {
		if !slices.Contains(pw.Exclude, tool) {
			t.Fatalf("mutation tool %s not excluded: %v", tool, pw.Exclude)
		}
	}
	// Unknown servers cannot prove enforcement — explicit except is required.
	s.Tools = map[string]ToolEntry{"gitea": {Via: "mcp-raw", Server: "gitea", Access: "ro"}}
	s.MCP = map[string]MCPServer{"gitea": {URL: "http://gitea/mcp"}}
	if err := s.Validate(); err == nil {
		t.Fatal("ro on unknown raw MCP accepted without except")
	}
	s.Tools = map[string]ToolEntry{"gitea": {Via: "mcp-raw", Server: "gitea", Access: "ro", Except: []string{"create_issue"}}}
	if err := s.Validate(); err != nil {
		t.Fatalf("ro with explicit except rejected: %v", err)
	}
	// rw raw MCP needs no except.
	s.Tools = map[string]ToolEntry{"gitea": {Via: "mcp-raw", Server: "gitea"}}
	if err := s.Validate(); err != nil {
		t.Fatalf("rw raw MCP rejected: %v", err)
	}
}

// Credential-shaped mount sources are rejected outright — NanoClaw blocklist.
func TestMountBlocklist(t *testing.T) {
	s := Settings{Schema: 3, User: "alice", Environment: "prod", Timezone: "UTC", BrowserPort: 6080, OAuthPort: 8000,
		SpaceDir: t.TempDir()}
	for _, from := range []string{
		".ssh", ".ssh/id_rsa", "keys/id_rsa_backup", ".aws", ".kube", ".docker",
		"app/.env", ".env.local", "svc/credentials.json", "creds.pem",
		".state", "runtime/managed", "settings.yaml", "workspace.yaml", "agent.yaml",
	} {
		s.Workspace = Workspace{Mounts: []Mount{{From: from, To: "/mnt/x"}}}
		if err := s.Validate(); err == nil {
			t.Fatalf("blocked mount source accepted: %s", from)
		}
	}
	for _, from := range []string{"docs", "shared/notes", "data"} {
		s.Workspace = Workspace{Mounts: []Mount{{From: from, To: "/mnt/" + filepath.Base(from)}}}
		if err := s.Validate(); err != nil {
			t.Fatalf("clean mount source rejected: %s: %v", from, err)
		}
	}
}

// Organization actions mapped to a read-only tool entry are stripped from the
// effective set and refused by AllowsOrgAction.
func TestOrgActionReadOnlyStrip(t *testing.T) {
	org := t.TempDir()
	if err := InitOrganization(org, "acme", "alice"); err != nil {
		t.Fatal(err)
	}
	s := Settings{Schema: 3, User: "alice", Organization: "acme", Timezone: "UTC",
		BrowserPort: 6080, OAuthPort: 8000, SpaceDir: t.TempDir(), OrganizationDir: org,
		OrgActions: []string{"slack.write", "hh.apply", "telegram.write"},
		Tools: map[string]ToolEntry{
			"slack":         {Via: "mcp", Access: "ro"},
			"hh":            {Via: "toolhub"},
			"telegram_user": {Via: "mcp", Access: "ro"},
		}}
	effective := s.effectiveOrgActions()
	if slices.Contains(effective, "slack.write") || slices.Contains(effective, "telegram.write") {
		t.Fatalf("ro actions leaked: %v", effective)
	}
	if !slices.Contains(effective, "hh.apply") {
		t.Fatalf("rw action stripped: %v", effective)
	}
	if s.AllowsOrgAction("slack.write") {
		t.Fatal("AllowsOrgAction honored a ro action")
	}
}
