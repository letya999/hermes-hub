package stack

import (
	"github.com/letya999/hermes-hub/internal/media"
	"github.com/letya999/hermes-hub/internal/toolhub/api"
)

// settingsFieldRights classifies every Settings field for the policy hash
// (SPEC-0053 R1.1, ADR-0036 D5). The value names the rightsView field the
// setting lands in; "" means the field carries no grants and must not move the
// hash — model identity, ports, timezone, environment, transport URLs and
// executor-topology knobs are deployment state, not rights.
// TestEverySettingsFieldClassified pins the table to the struct.
var settingsFieldRights = map[string]string{
	"defaultGlobalSkills": "",
	"ExecutionMode":       "",
	"SupervisorURL":       "",
	"NativeCron":          "",
	"Environment":         "",
	"CLIRuntime":          "",
	"CLIPerPrincipalUID":  "",
	"Schema":              "",
	"User":                "",
	"Model":               "",
	"ModelURL":            "",
	"Timezone":            "",
	"OAuthPort":           "",
	"BrowserPort":         "",
	"SlackEventsPort":     "",
	"Infra":               "",
	"Diagnostics":         "",
	"SpaceDir":            "",

	"CapabilityMode":        "CapabilityMode",
	"CapabilityProfileID":   "CapabilityProfileID",
	"CapabilityGeneration":  "CapabilityGeneration",
	"CLIRegistries":         "CLIRegistries",
	"CLIToolboxes":          "CLIToolboxes",
	"Tools":                 "Tools",
	"Ingress":               "Ingress",
	"TelegramAuth":          "TelegramAuth",
	"Workspace":             "Workspace",
	"MCP":                   "MCP",
	"Hooks":                 "Hooks",
	"Memory":                "Memory",
	"Honcho":                "Honcho",
	"HonchoURL":             "HonchoURL",
	"GlobalSkillsDir":       "GlobalSkillsDir",
	"Organization":          "Organization",
	"OrganizationDir":       "OrganizationDir",
	"OrganizationDocsDir":   "OrganizationDocsDir",
	"OrganizationSkillsDir": "OrganizationSkillsDir",
	"OrganizationRole":      "OrganizationRole",
	"OrgActions":            "OrgActions",
	"GoogleEmail":           "GoogleEmail",
	"GitLabHost":            "GitLabHost",
	"DesktopURL":            "DesktopURL",
	"DraftsURL":             "DraftsURL",
	"ImageGen":              "ImageGen",
	"Web":                   "Web",
	// The evaluated tool-governance view is itself a grant fence: its decisions
	// decide which sections may render and which are capped to read-only.
	"gov": "Governance",
}

// mcpRights keeps only the grant-defining parts of an mcp_servers entry —
// the upstream identity (command/url/args) and which of its tools are exposed.
// Env, headers, auth and timeout move credentials and transport, never the
// surface.
type mcpRights struct {
	URL     string    `json:"url,omitempty"`
	Command string    `json:"command,omitempty"`
	Args    []string  `json:"args,omitempty"`
	Tools   *MCPTools `json:"tools,omitempty"`
}

// rightsView is the canonical rights-bearing subset of Settings hashed into
// HUB_POLICY_VERSION. Field names pair with settingsFieldRights.
type rightsView struct {
	CapabilityMode        string                        `json:"capability_mode,omitempty"`
	CapabilityProfileID   string                        `json:"capability_profile_id,omitempty"`
	CapabilityGeneration  uint64                        `json:"capability_generation,omitempty"`
	CLIRegistries         []string                      `json:"cli_registries,omitempty"`
	CLIToolboxes          map[string][]string           `json:"cli_toolboxes,omitempty"`
	Tools                 map[string]ToolEntry          `json:"tools,omitempty"`
	Ingress               []string                      `json:"ingress,omitempty"`
	TelegramAuth          bool                          `json:"telegram_auth,omitempty"`
	Workspace             Workspace                     `json:"workspace"`
	MCP                   map[string]mcpRights          `json:"mcp,omitempty"`
	Hooks                 map[string]any                `json:"hooks,omitempty"`
	Memory                bool                          `json:"memory,omitempty"`
	Honcho                bool                          `json:"honcho,omitempty"`
	HonchoURL             string                        `json:"honcho_url,omitempty"`
	GlobalSkillsDir       string                        `json:"global_skills_dir,omitempty"`
	Organization          string                        `json:"organization,omitempty"`
	OrganizationDir       string                        `json:"organization_dir,omitempty"`
	OrganizationDocsDir   string                        `json:"organization_docs_dir,omitempty"`
	OrganizationSkillsDir string                        `json:"organization_skills_dir,omitempty"`
	OrganizationRole      string                        `json:"organization_role,omitempty"`
	OrgActions            []string                      `json:"org_actions,omitempty"`
	GoogleEmail           string                        `json:"google_email,omitempty"`
	GitLabHost            string                        `json:"gitlab_host,omitempty"`
	DesktopURL            string                        `json:"desktop_url,omitempty"`
	DraftsURL             string                        `json:"drafts_url,omitempty"`
	ImageGen              media.ImageGen                `json:"image_gen"`
	Web                   WebSettings                   `json:"web"`
	Governance            map[string]api.PolicyDecision `json:"governance,omitempty"`
}

func rightsProjection(s Settings) rightsView {
	view := rightsView{
		CapabilityMode:        s.CapabilityMode,
		CapabilityProfileID:   s.CapabilityProfileID,
		CapabilityGeneration:  s.CapabilityGeneration,
		CLIRegistries:         s.CLIRegistries,
		CLIToolboxes:          s.CLIToolboxes,
		Tools:                 s.Tools,
		Ingress:               s.Ingress,
		TelegramAuth:          s.TelegramAuth,
		Workspace:             s.Workspace,
		MCP:                   mcpRightsMap(s.MCP),
		Hooks:                 s.Hooks,
		Memory:                s.Memory,
		Honcho:                s.Honcho,
		HonchoURL:             s.HonchoURL,
		GlobalSkillsDir:       s.GlobalSkillsDir,
		Organization:          s.Organization,
		OrganizationDir:       s.OrganizationDir,
		OrganizationDocsDir:   s.OrganizationDocsDir,
		OrganizationSkillsDir: s.OrganizationSkillsDir,
		OrganizationRole:      s.OrganizationRole,
		OrgActions:            s.OrgActions,
		GoogleEmail:           s.GoogleEmail,
		GitLabHost:            s.GitLabHost,
		DesktopURL:            s.DesktopURL,
		DraftsURL:             s.DraftsURL,
		ImageGen:              s.ImageGen,
		Web:                   s.Web,
	}
	if s.gov != nil {
		view.Governance = s.gov.decisions
	}
	if s.defaultGlobalSkills {
		view.GlobalSkillsDir = ""
	}
	return view
}

func mcpRightsMap(servers map[string]MCPServer) map[string]mcpRights {
	if len(servers) == 0 {
		return nil
	}
	out := make(map[string]mcpRights, len(servers))
	for name, srv := range servers {
		out[name] = mcpRights{URL: srv.URL, Command: srv.Command, Args: srv.Args, Tools: srv.Tools}
	}
	return out
}
