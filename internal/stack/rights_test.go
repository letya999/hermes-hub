package stack

import (
	"github.com/letya999/hermes-hub/internal/toolhub/api"
	"reflect"
	"testing"
)

func policyBaseline() Settings {
	return Settings{Schema: 1, User: "alice", Environment: "prod", Timezone: "UTC",
		ExecutionMode: "supervisor", OAuthPort: 8000, BrowserPort: 6080,
		Model: "m1", ModelURL: "http://model:8318/v1",
		Tools: map[string]ToolEntry{"file": {Via: ToolViaToolHub}}, Ingress: []string{"telegram"}}
}

func TestDefaultSkillsLocationDoesNotChangePolicy(t *testing.T) {
	base := policyBaseline()
	for _, root := range []string{"C:/repo/config/skills", "/config/skills"} {
		resolved := base
		resolved.DefaultGlobalSkillsDir(root)
		if resolved.GlobalSkillsDir != root || PolicyVersion(resolved) != PolicyVersion(base) {
			t.Fatalf("default location changed rights: %s", root)
		}
	}
	explicit := base
	explicit.GlobalSkillsDir = "/operator/skills"
	explicit.DefaultGlobalSkillsDir("/config/skills")
	if explicit.GlobalSkillsDir != "/operator/skills" || PolicyVersion(explicit) == PolicyVersion(base) {
		t.Fatal("explicit catalog was replaced or lost from rights")
	}
}

// SPEC-0053 R1.1: deployment and presentation state must not move the policy
// hash — a model switch or a port change keeps every previously visible tool
// bound (acceptance A1.1).
func TestPolicyVersionIgnoresNonRightsFields(t *testing.T) {
	base := policyBaseline()
	stable := policyVersion(base)
	cases := map[string]func(*Settings){
		"model":             func(s *Settings) { s.Model = "other-model" },
		"model_url":         func(s *Settings) { s.ModelURL = "http://other:9000/v1" },
		"timezone":          func(s *Settings) { s.Timezone = "Europe/Berlin" },
		"oauth_port":        func(s *Settings) { s.OAuthPort = 9999 },
		"browser_port":      func(s *Settings) { s.BrowserPort = 9998 },
		"slack_events_port": func(s *Settings) { s.SlackEventsPort = 9997 },
		"execution_mode":    func(s *Settings) { s.ExecutionMode = "api" },
		"supervisor_url":    func(s *Settings) { s.SupervisorURL = "http://other:8001" },
		"native_cron":       func(s *Settings) { s.NativeCron = "native" },
		"environment":       func(s *Settings) { s.Environment = "dev" },
		"cli_runtime":       func(s *Settings) { s.CLIRuntime = "runsc" },
		"cli_uid":           func(s *Settings) { s.CLIPerPrincipalUID = true },
		"schema":            func(s *Settings) { s.Schema = 9 },
		"user":              func(s *Settings) { s.User = "bob" },
		"space_dir":         func(s *Settings) { s.SpaceDir = "/other/dir" },
		"infra":             func(s *Settings) { v := true; s.Infra = &v },
		"diagnostics":       func(s *Settings) { v := false; s.Diagnostics = &v },
	}
	for name, mutate := range cases {
		s := policyBaseline()
		mutate(&s)
		if got := policyVersion(s); got != stable {
			t.Errorf("non-rights change %q moved the policy hash to %s", name, got)
		}
	}
}

// SPEC-0053 R1.1: every rights-bearing field must move the policy hash — a
// grant change without a new hash would leave old bindings silently stale.
// Mutators are keyed by Settings field name and pinned to settingsFieldRights,
// so a field classified rights-bearing without a mutator fails here.
func TestPolicyVersionChangesOnRightsFields(t *testing.T) {
	mutators := map[string]func(*Settings){
		"CapabilityMode":        func(s *Settings) { s.CapabilityMode = "managed" },
		"CapabilityProfileID":   func(s *Settings) { s.CapabilityProfileID = "p-1" },
		"CapabilityGeneration":  func(s *Settings) { s.CapabilityGeneration = 7 },
		"CLIRegistries":         func(s *Settings) { s.CLIRegistries = []string{"ghcr.io/x"} },
		"CLIToolboxes":          func(s *Settings) { s.CLIToolboxes = map[string][]string{"box": {"img@sha256:aa"}} },
		"Tools":                 func(s *Settings) { s.Tools["web"] = ToolEntry{Via: ToolViaMCP} },
		"Ingress":               func(s *Settings) { s.Ingress = append(s.Ingress, "slack") },
		"TelegramAuth":          func(s *Settings) { off := false; s.TelegramAuth = &off },
		"Workspace":             func(s *Settings) { s.Workspace.Mounts = []Mount{{From: "/a", To: "/b", Mode: "ro"}} },
		"MCP":                   func(s *Settings) { s.MCP = map[string]MCPServer{"ext": {Command: "npx"}} },
		"Hooks":                 func(s *Settings) { s.Hooks = map[string]any{"pre": "x"} },
		"Memory":                func(s *Settings) { s.Memory = true },
		"Honcho":                func(s *Settings) { s.Honcho = true },
		"HonchoURL":             func(s *Settings) { s.HonchoURL = "http://honcho" },
		"GlobalSkillsDir":       func(s *Settings) { s.GlobalSkillsDir = "/skills" },
		"Organization":          func(s *Settings) { s.Organization = "acme" },
		"OrganizationDir":       func(s *Settings) { s.OrganizationDir = "/org" },
		"OrganizationDocsDir":   func(s *Settings) { s.OrganizationDocsDir = "/docs" },
		"OrganizationSkillsDir": func(s *Settings) { s.OrganizationSkillsDir = "/oskills" },
		"OrganizationRole":      func(s *Settings) { s.OrganizationRole = "member" },
		"OrgActions":            func(s *Settings) { s.OrgActions = []string{"read"} },
		"GoogleEmail":           func(s *Settings) { s.GoogleEmail = "a@b.c" },
		"GitLabHost":            func(s *Settings) { s.GitLabHost = "gl.example" },
		"DesktopURL":            func(s *Settings) { s.DesktopURL = "http://desktop" },
		"DraftsURL":             func(s *Settings) { s.DraftsURL = "http://drafts" },
		"ImageGen":              func(s *Settings) { s.ImageGen.Provider = "fal" },
		"Web":                   func(s *Settings) { s.Web.Backend = "tavily" },
		"gov": func(s *Settings) {
			s.gov = &toolGovernance{decisions: map[string]api.PolicyDecision{
				"file": {Effect: api.RuleDeny},
			}}
		},
	}
	stable := policyVersion(policyBaseline())
	for name, mutate := range mutators {
		if settingsFieldRights[name] == "" {
			t.Errorf("mutator for %q but the field is not classified rights-bearing", name)
			continue
		}
		s := policyBaseline()
		mutate(&s)
		if got := policyVersion(s); got == stable {
			t.Errorf("rights change %q left the policy hash at %s", name, stable)
		}
	}
	for name, projection := range settingsFieldRights {
		if projection != "" && mutators[name] == nil {
			t.Errorf("rights-bearing %q has no mutator in TestPolicyVersionChangesOnRightsFields", name)
		}
	}
}

// The plan scopes MCP rights to command/url/args and the tools include/exclude
// lists: connection and credential knobs (timeout, env, headers, auth) move a
// transport, not a grant, so they must not move the hash.
func TestPolicyVersionIgnoresMCPTransportFields(t *testing.T) {
	server := func() MCPServer {
		return MCPServer{Command: "npx", Args: []string{"-y", "srv"},
			Env: map[string]string{"KEY": "v1"}, Timeout: 30}
	}
	s := policyBaseline()
	s.MCP = map[string]MCPServer{"ext": server()}
	stable := policyVersion(s)

	changed := server()
	changed.Timeout = 99
	changed.Env = map[string]string{"KEY": "rotated"}
	changed.Headers = map[string]string{"Auth": "rotated"}
	changed.Auth = "other"
	s.MCP = map[string]MCPServer{"ext": changed}
	if got := policyVersion(s); got != stable {
		t.Errorf("mcp transport change moved the policy hash to %s", got)
	}
}

// SPEC-0053 R1.1: every Settings field is classified — settingsFieldRights maps
// it to its rightsView field name, or "" when it carries no grants and must not
// move the policy hash. The guard fails on a new unclassified field, on a
// rights-classified field missing from the projection, and on a projection
// field with no classification.
func TestEverySettingsFieldClassified(t *testing.T) {
	settingsType := reflect.TypeOf(Settings{})
	for i := 0; i < settingsType.NumField(); i++ {
		name := settingsType.Field(i).Name
		if _, ok := settingsFieldRights[name]; !ok {
			t.Errorf("Settings.%s is not classified in settingsFieldRights", name)
		}
	}
	for name := range settingsFieldRights {
		if _, ok := settingsType.FieldByName(name); !ok {
			t.Errorf("settingsFieldRights names removed Settings field %q", name)
		}
	}
	projectionType := reflect.TypeOf(rightsView{})
	projected := map[string]bool{}
	for i := 0; i < projectionType.NumField(); i++ {
		projected[projectionType.Field(i).Name] = true
	}
	for name, projectionField := range settingsFieldRights {
		if projectionField == "" {
			continue
		}
		if !projected[projectionField] {
			t.Errorf("rights-bearing Settings.%s never reaches rightsView.%s", name, projectionField)
		}
		delete(projected, projectionField)
	}
	for name := range projected {
		t.Errorf("rightsView.%s is not bound to a Settings classification", name)
	}
}
