package stack

import (
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"gopkg.in/yaml.v3"
)

func saveYAML(t *testing.T, path string, value any) {
	t.Helper()
	b, err := yaml.Marshal(value)
	if err != nil {
		t.Fatal(err)
	}
	if err = os.WriteFile(path, b, 0600); err != nil {
		t.Fatal(err)
	}
}

func TestOrganizationOverlayAndMembership(t *testing.T) {
	root := t.TempDir()
	orgDir := filepath.Join(root, "organizations", "acme")
	space := filepath.Join(root, "spaces", "alice")
	if err := InitOrganization(orgDir, "acme", "alice"); err != nil {
		t.Fatal(err)
	}
	if err := InitEnvironmentWithOrganization(space, "alice", "prod", "acme"); err != nil {
		t.Fatal(err)
	}
	o, err := ReadOrganization(filepath.Join(orgDir, "settings.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	o.MCP = map[string]MCPServer{"clickhouse": {URL: "https://clickhouse.invalid/mcp", Tools: &MCPTools{Include: []string{"query"}}}}
	o.ReadOnlyMCP = []string{"clickhouse"}
	o.OrgActions = []string{"hh.apply", "slack.write"}
	saveYAML(t, filepath.Join(orgDir, "settings.yaml"), o)

	s, err := ReadEnvironment(space, "prod")
	if err != nil {
		t.Fatal(err)
	}
	if !s.OrgScoped() || s.OrganizationRole != "owner" || s.OrganizationDocsDir != filepath.Join(orgDir, "docs") {
		t.Fatalf("bad scope: %+v", s)
	}
	if _, ok := s.MCP["clickhouse"]; !ok {
		t.Fatal("organization MCP was not inherited")
	}
	if !s.AllowsOrgAction("hh.apply") || s.AllowsOrgAction("telegram.write") {
		t.Fatal("organization action policy not applied")
	}

	s.DisabledMCP = []string{"clickhouse"}
	s.MCP = nil
	saveYAML(t, filepath.Join(space, "settings.yaml"), s)
	s, err = ReadEnvironment(space, "prod")
	if err != nil {
		t.Fatal(err)
	}
	if len(s.MCP) != 0 {
		t.Fatal("disabled organization MCP remained enabled")
	}
}

func TestOrganizationRejectsEscalationAndNonMember(t *testing.T) {
	root := t.TempDir()
	orgDir := filepath.Join(root, "organizations", "acme")
	if err := InitOrganization(orgDir, "acme", "alice"); err != nil {
		t.Fatal(err)
	}
	org, err := ReadOrganization(filepath.Join(orgDir, "settings.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	org.MCP = map[string]MCPServer{"approved": {URL: "https://approved.invalid/mcp", Tools: &MCPTools{Include: []string{"read"}}}}
	org.ReadOnlyMCP = []string{"approved"}
	saveYAML(t, filepath.Join(orgDir, "settings.yaml"), org)

	for _, user := range []string{"bob", "alice"} {
		space := filepath.Join(root, "spaces", user)
		if err := InitEnvironmentWithOrganization(space, user, "prod", "acme"); err != nil {
			t.Fatal(err)
		}
		s, err := Read(filepath.Join(space, "settings.yaml"))
		if err != nil {
			t.Fatal(err)
		}
		if user == "bob" {
			if _, err = ReadEnvironment(space, "prod"); err == nil || !strings.Contains(err.Error(), "not a member") {
				t.Fatalf("non-member accepted: %v", err)
			}
			continue
		}
		s.MCP = map[string]MCPServer{"unapproved": {URL: "https://unapproved.invalid/mcp"}}
		saveYAML(t, filepath.Join(space, "settings.yaml"), s)
		if _, err = ReadEnvironment(space, "prod"); err == nil || !strings.Contains(err.Error(), "user MCP definitions") {
			t.Fatalf("user MCP escalation accepted: %v", err)
		}
	}
}

func TestDoctorScopeUsesSeparateSecrets(t *testing.T) {
	s := Settings{Schema: 1, Environment: "prod", User: "alice", Model: "model", ModelURL: "http://model.invalid/v1", Timezone: "UTC", BrowserPort: 6080, OAuthPort: 8000, MCP: map[string]MCPServer{"org": {URL: "https://service.invalid/mcp", Headers: map[string]string{"Authorization": "Bearer ${ORG_TOKEN}"}}}}
	if issues := DoctorScope(s, map[string]string{"OPENAI_API_KEY": "user"}, map[string]string{"ORG_TOKEN": "org"}); len(issues) != 0 {
		t.Fatal(issues)
	}
	if issues := DoctorScope(s, map[string]string{"ORG_TOKEN": "user"}, map[string]string{"ORG_TOKEN": "org"}); !strings.Contains(strings.Join(issues, " "), "duplicated") {
		t.Fatal(issues)
	}
}

func TestOrganizationPolicyReachesRuntimeConfig(t *testing.T) {
	s := Settings{Schema: 1, Environment: "prod", User: "alice", Organization: "acme", OrgActions: []string{}, Features: []string{"workspace", "slack"}, MCP: map[string]MCPServer{"org": {URL: "https://org.invalid/mcp", Tools: &MCPTools{Include: []string{"query"}}}}, OrganizationDir: "/host/organizations/acme", OrganizationDocsDir: "/host/organizations/acme/docs"}
	c := Config(s)
	hub := c["mcp_servers"].(M)["hub"].(M)
	args := hub["args"].([]string)
	if !slices.Contains(args, "--organization") {
		t.Fatal("organization root was not passed to hub MCP")
	}
	hubEnv := hub["env"].(M)
	if hubEnv["HUB_SELF_ENV_KEYS"] != "${HUB_SELF_ENV_KEYS}" || hubEnv["HUB_STATE"] != "/state" {
		t.Fatal("self-env runtime wiring missing")
	}
	orgMCP := c["mcp_servers"].(M)["org"].(M)
	if orgMCP["tools"].(*MCPTools).Include[0] != "query" {
		t.Fatal("organization MCP tool allowlist was not rendered")
	}
	slack := c["mcp_servers"].(M)["slack"].(M)["env"].(M)
	if slack["SLACK_MCP_ADD_MESSAGE_TOOL"] != "" {
		t.Fatal("Slack write was not disabled by default")
	}
	compose := Compose(s, "/source", "/host/spaces/alice")
	agent := compose["services"].(M)["agent"].(M)
	envFiles := agent["env_file"].([]any)
	if envFiles[0].(M)["path"] != "/host/organizations/acme/secrets.prod.env" {
		t.Fatal("organization secrets were not kept separate")
	}
	volumes := agent["volumes"].([]any)
	foundOrg := false
	for _, raw := range volumes {
		volume := raw.(M)
		if volume["target"] == "/org" {
			foundOrg = volume["read_only"] == true
		}
	}
	if !foundOrg {
		t.Fatal("organization mount is not read-only")
	}
}

func TestOrganizationPolicyErrors(t *testing.T) {
	server := MCPServer{URL: "https://org.invalid/mcp", Tools: &MCPTools{Include: []string{"read"}}}
	valid := OrganizationSettings{Schema: 1, Organization: "acme", Members: map[string]string{"alice": "member"}, Features: []string{"workspace"}}
	for _, bad := range []OrganizationSettings{
		{Schema: 2, Organization: "acme"},
		{Schema: 1, Organization: "acme", Members: map[string]string{"../alice": "member"}},
		{Schema: 1, Organization: "acme", Members: map[string]string{"alice": "invalid"}},
		{Schema: 1, Organization: "acme", Features: []string{"workspace", "workspace"}},
		{Schema: 1, Organization: "acme", Features: []string{"missing"}},
		{Schema: 1, Organization: "acme", MCP: map[string]MCPServer{"org": {URL: "https://org.invalid/mcp"}}},
		{Schema: 1, Organization: "acme", MCP: map[string]MCPServer{"org": server}, ReadOnlyMCP: []string{"missing"}},
		{Schema: 1, Organization: "acme", MCP: map[string]MCPServer{"org": server}, ReadOnlyMCP: []string{"org", "org"}},
		{Schema: 1, Organization: "acme", MCP: map[string]MCPServer{"org": {URL: "https://org.invalid/mcp", Tools: &MCPTools{Exclude: []string{"write"}}}}, ReadOnlyMCP: []string{"org"}},
		{Schema: 1, Organization: "acme", MCP: map[string]MCPServer{"org": server}},
		{Schema: 1, Organization: "acme", OrgActions: []string{"Bad"}},
		{Schema: 1, Organization: "acme", OrgActions: []string{"read", "read"}},
	} {
		if bad.Validate() == nil {
			t.Fatalf("invalid organization accepted: %+v", bad)
		}
	}
	if err := valid.Validate(); err != nil {
		t.Fatal(err)
	}

	root := t.TempDir()
	orgDir := filepath.Join(root, "organizations", "acme")
	if err := InitOrganization(orgDir, "acme", "alice"); err != nil {
		t.Fatal(err)
	}
	if InitOrganization(orgDir, "acme", "alice") == nil || InitOrganization(root, "../acme", "alice") == nil || InitOrganization(root, "acme", "../alice") == nil {
		t.Fatal("invalid org-init accepted")
	}
	user := Settings{Schema: 1, User: "alice", Organization: "acme", Features: []string{"workspace"}}
	org := OrganizationSettings{Schema: 1, Organization: "acme", Members: map[string]string{"alice": "member"}, Features: []string{"workspace"}}
	for _, bad := range []struct {
		org OrganizationSettings
		set Settings
		dir string
	}{
		{org: org, set: Settings{Schema: 1, User: "alice", Features: []string{"workspace"}}, dir: orgDir},
		{org: OrganizationSettings{Schema: 1, Organization: "other"}, set: user, dir: orgDir},
		{org: org, set: Settings{Schema: 1, User: "bob", Organization: "acme", Features: []string{"workspace"}}, dir: orgDir},
		{org: org, set: Settings{Schema: 1, User: "alice", Organization: "acme", Features: []string{"browser"}}, dir: orgDir},
		{org: org, set: Settings{Schema: 1, User: "alice", Organization: "acme", Features: []string{"workspace", "telegram_write"}}, dir: orgDir},
		{org: org, set: Settings{Schema: 1, User: "alice", Organization: "acme", Features: []string{"workspace"}, MCP: map[string]MCPServer{"user": {URL: "https://user.invalid/mcp"}}}, dir: orgDir},
		{org: org, set: Settings{Schema: 1, User: "alice", Organization: "acme", Features: []string{"workspace"}, DisabledMCP: []string{"missing"}}, dir: orgDir},
		{org: org, set: user, dir: filepath.Join(root, "missing")},
	} {
		if _, err := ApplyOrganization(bad.org, bad.set, bad.dir); err == nil {
			t.Fatalf("invalid organization overlay accepted: %+v", bad)
		}
	}
	if standalone, err := ApplyOrganization(org, Settings{Schema: 1, User: "alice"}, orgDir); err == nil || !strings.Contains(err.Error(), "no organization") || standalone.User != "alice" {
		t.Fatal("missing organization was accepted")
	}

	secrets, err := ReadOrganizationSecrets(Settings{}, "prod")
	if err != nil || secrets != nil {
		t.Fatal(secrets, err)
	}
	if _, err = ReadOrganizationSecrets(Settings{Organization: "acme", OrganizationDir: orgDir}, "prod"); err != nil {
		t.Fatal(err)
	}
	if _, err = ReadOrganization(filepath.Join(root, "missing.yaml")); err == nil {
		t.Fatal("missing organization accepted")
	}
	path := filepath.Join(root, "bad.yaml")
	if err = os.WriteFile(path, []byte("unknown: true\n"), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err = ReadOrganization(path); err == nil {
		t.Fatal("unknown organization field accepted")
	}
	if err = os.WriteFile(path, []byte("schema: 1\norganization: acme\n---\nschema: 1\norganization: acme\n"), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err = ReadOrganization(path); err == nil {
		t.Fatal("multiple organization documents accepted")
	}
}
