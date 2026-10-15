package stack

import (
	"errors"
	"fmt"
	"gopkg.in/yaml.v3"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	_ "time/tzdata"
)

// legacySettingsFile keeps schema-1 capability fields parseable so Read can
// migrate them into the unified `tools:`/`ingress:` surface. New files write
// only the new keys.
type legacySettingsFile struct {
	Settings       `yaml:",inline"`
	LegacyFeatures []string `yaml:"features,omitempty"`
	LegacyNative   []string `yaml:"native_toolsets,omitempty"`
	LegacyMCP      []string `yaml:"disabled_mcp,omitempty"`
}

// workspaceFile is the user-facing half of a space: the capability surface
// from tools.go plus raw MCP server definitions. It holds wishes only —
// identity, model, ports and the capability pin live in agent.yaml, which
// the user does not edit.
type workspaceFile struct {
	Schema    int                  `yaml:"schema,omitempty"`
	Tools     map[string]ToolEntry `yaml:"tools,omitempty"`
	Ingress   []string             `yaml:"ingress,omitempty"`
	Workspace Workspace            `yaml:"workspace,omitempty"`
	MCP       map[string]MCPServer `yaml:"mcp,omitempty"`
}

// workspaceYAMLKeys is the complete top-level vocabulary of workspace.yaml.
// Anything else is either a mistake or an operator key that belongs in
// agent.yaml — reported by name rather than as a generic unknown field.
var workspaceYAMLKeys = map[string]bool{"schema": true, "tools": true, "ingress": true, "workspace": true, "mcp": true}

// agentYAMLForbidden are top-level keys agent.yaml must not carry: the
// capability surface lives in workspace.yaml, and legacy capability fields
// must migrate there instead of silently widening the operator file.
var agentYAMLForbidden = map[string]string{
	"tools": "tools belongs in workspace.yaml", "ingress": "ingress belongs in workspace.yaml",
	"workspace": "workspace belongs in workspace.yaml", "mcp": "mcp belongs in workspace.yaml",
	"mcp_servers":     "mcp_servers belongs in workspace.yaml as mcp:",
	"features":        "features is schema-1 — use tools: in workspace.yaml",
	"native_toolsets": "native_toolsets is schema-1 — use tools: <name>: native in workspace.yaml",
	"disabled_mcp":    "disabled_mcp is schema-1 — use tools: <name>: off in workspace.yaml",
}

// topLevelKeys returns the mapping keys of a single-document YAML file.
func topLevelKeys(b []byte) ([]string, error) {
	var doc map[string]any
	d := yaml.NewDecoder(strings.NewReader(string(b)))
	if err := d.Decode(&doc); err != nil {
		return nil, err
	}
	keys := make([]string, 0, len(doc))
	for k := range doc {
		keys = append(keys, k)
	}
	return keys, nil
}

// readSpaceYAML parses one split file through a strict decoder after checking
// that no key was written into the wrong half of the pair.
func readSpaceYAML(path string, target any, check func(key string) error) error {
	b, err := os.ReadFile(path)
	if err != nil {
		return err
	}
	keys, err := topLevelKeys(b)
	if err != nil {
		return err
	}
	for _, key := range keys {
		if err := check(key); err != nil {
			return fmt.Errorf("%s: %w", filepath.Base(path), err)
		}
	}
	d := yaml.NewDecoder(strings.NewReader(string(b)))
	d.KnownFields(true)
	if err := d.Decode(target); err != nil {
		return fmt.Errorf("%s: %w", filepath.Base(path), err)
	}
	var extra any
	if err = d.Decode(&extra); err != io.EOF {
		return fmt.Errorf("%s: one YAML document expected", filepath.Base(path))
	}
	return nil
}

// featureToolMigration maps each schema-1 feature to its tools entry.
// Modifiers land on the parent entry's `tools:` toggle map; transports move
// to `ingress`; `workspace` is unconditional and drops silently.
var featureToolMigration = map[string]ToolEntry{
	"browser":       {Via: ToolViaMCP, Server: "playwright"},
	"web":           {Via: ToolViaNative},
	"deep_research": {Via: ToolViaNative},
	"hh":            {Via: ToolViaToolHub},
	"ssh":           {Via: ToolViaToolHub},
	"image_gen":     {Via: ToolViaToolHub},
	"meet":          {Via: ToolViaNative},
	"transcription": {Via: ToolViaNative},
	"google":        {Via: ToolViaMCP},
	"gitlab":        {Via: ToolViaMCP},
	"github":        {Via: ToolViaMCP},
	"slack":         {Via: ToolViaMCP},
	"atlassian":     {Via: ToolViaMCP},
	"telegram_user": {Via: ToolViaMCP},
	"desktop":       {Via: ToolViaMCP},
	"drafts":        {Via: ToolViaMCP},
}

var featureToggleMigration = map[string][2]string{
	"browser_act":    {"browser", "act"},
	"ssh_write":      {"ssh", "write"},
	"ssh_shell":      {"ssh", "shell"},
	"ssh_tunnel":     {"ssh", "tunnel"},
	"google_write":   {"google", "write"},
	"telegram_write": {"telegram_user", "write"},
}

func migrateLegacyTools(lf legacySettingsFile) (Settings, error) {
	s := lf.Settings
	legacy := len(lf.LegacyFeatures) + len(lf.LegacyNative) + len(lf.LegacyMCP)
	if legacy == 0 {
		return s, nil
	}
	if len(s.Tools) > 0 || len(s.Ingress) > 0 {
		return s, fmt.Errorf("cannot mix tools:/ingress: with legacy features/native_toolsets/disabled_mcp")
	}
	tools, ingress, err := LegacyFeatureTools(lf.LegacyFeatures)
	if err != nil {
		return s, err
	}
	s.Ingress = ingress
	for _, name := range lf.LegacyNative {
		if _, dup := tools[name]; dup {
			return s, fmt.Errorf("legacy native_toolsets %q conflicts with a feature entry", name)
		}
		tools[name] = ToolEntry{Via: ToolViaNative}
	}
	for _, name := range lf.LegacyMCP {
		tools[name] = ToolEntry{Via: ToolViaOff}
	}
	if len(tools) > 0 {
		s.Tools = tools
	}
	return s, nil
}

// Read loads a space. The path may be the space directory, the split pair
// workspace.yaml + agent.yaml, or the legacy single settings.yaml — all of
// them merge into the same Settings. A bare workspace/agent filename reads
// its containing directory so the pair is always validated together.
func Read(path string) (Settings, error) {
	if info, err := os.Stat(path); err == nil && info.IsDir() {
		return readSpace(path)
	}
	switch filepath.Base(path) {
	case "workspace.yaml", "agent.yaml":
		return readSpace(filepath.Dir(path))
	default:
		return readLegacySettings(path)
	}
}

func readLegacySettings(path string) (Settings, error) {
	var s Settings
	b, err := os.ReadFile(path)
	if err != nil {
		return s, err
	}
	d := yaml.NewDecoder(strings.NewReader(string(b)))
	d.KnownFields(true)
	var lf legacySettingsFile
	if err = d.Decode(&lf); err != nil {
		return s, err
	}
	var extra any
	if err = d.Decode(&extra); err != io.EOF {
		return s, fmt.Errorf("one YAML document expected")
	}
	if s, err = migrateLegacyTools(lf); err != nil {
		return s, err
	}
	return finishRead(s, filepath.Dir(path))
}

// readSpace merges agent.yaml (operator identity/runtime) with workspace.yaml
// (user capability surface). Neither file is ever mounted into the runtime
// container — the agent only sees the rendered, attested config.
func readSpace(dir string) (Settings, error) {
	agentPath := filepath.Join(dir, "agent.yaml")
	workspacePath := filepath.Join(dir, "workspace.yaml")
	if _, err := os.Stat(workspacePath); err != nil {
		if os.IsNotExist(err) {
			if _, agentErr := os.Stat(agentPath); os.IsNotExist(agentErr) {
				return readLegacySettings(filepath.Join(dir, "settings.yaml"))
			}
			return Settings{}, fmt.Errorf("agent.yaml without workspace.yaml")
		}
		return Settings{}, err
	}
	if _, err := os.Stat(agentPath); err != nil {
		if os.IsNotExist(err) {
			return Settings{}, fmt.Errorf("workspace.yaml without agent.yaml")
		}
		return Settings{}, err
	}
	var lf legacySettingsFile
	if err := readSpaceYAML(agentPath, &lf, func(key string) error {
		if msg, bad := agentYAMLForbidden[key]; bad {
			return errors.New(msg)
		}
		return nil
	}); err != nil {
		return Settings{}, err
	}
	if len(lf.LegacyFeatures)+len(lf.LegacyNative)+len(lf.LegacyMCP) > 0 {
		return Settings{}, fmt.Errorf("agent.yaml holds no capability fields — use workspace.yaml")
	}
	s := lf.Settings
	var wf workspaceFile
	if err := readSpaceYAML(workspacePath, &wf, func(key string) error {
		if !workspaceYAMLKeys[key] {
			return fmt.Errorf("%s belongs in agent.yaml", key)
		}
		return nil
	}); err != nil {
		return Settings{}, err
	}
	if wf.Schema != 0 && wf.Schema != 3 {
		return Settings{}, fmt.Errorf("workspace.yaml schema must be 3")
	}
	s.Tools, s.Ingress, s.Workspace = wf.Tools, wf.Ingress, wf.Workspace
	s.MCP = wf.MCP
	return finishRead(s, dir)
}

// WriteSpace serializes a Settings back into the agent.yaml + workspace.yaml
// pair, splitting top-level keys the way readSpace expects. Comments are not
// preserved — callers that need them edit through yaml.Node instead.
func WriteSpace(dir string, s Settings) error {
	body, err := yaml.Marshal(s)
	if err != nil {
		return err
	}
	var doc yaml.Node
	if err := yaml.Unmarshal(body, &doc); err != nil {
		return err
	}
	agent := &yaml.Node{Kind: yaml.MappingNode, Tag: "!!map"}
	workspace := &yaml.Node{Kind: yaml.MappingNode, Tag: "!!map"}
	root := doc.Content[0]
	for i := 0; i+1 < len(root.Content); i += 2 {
		key := root.Content[i]
		target := agent
		if (workspaceYAMLKeys[key.Value] && key.Value != "schema") || key.Value == "mcp_servers" {
			target = workspace
			if key.Value == "mcp_servers" {
				key.Value = "mcp"
			}
		}
		target.Content = append(target.Content, key, root.Content[i+1])
	}
	write := func(name string, mapping *yaml.Node) error {
		out, err := yaml.Marshal(mapping)
		if err != nil {
			return err
		}
		return os.WriteFile(filepath.Join(dir, name), out, 0600)
	}
	if err := write("agent.yaml", agent); err != nil {
		return err
	}
	return write("workspace.yaml", workspace)
}

func finishRead(s Settings, dir string) (Settings, error) {
	s.Environment = "prod"
	s.SpaceDir = dir
	if scope, scopeErr := ReadScope(dir); scopeErr == nil {
		if scope.Kind != UserScope || scope.ID != s.User || scope.Organization != s.Organization {
			return s, fmt.Errorf("user scope does not match the space files")
		}
	} else if !os.IsNotExist(scopeErr) {
		return s, scopeErr
	}
	if err := s.Validate(); err != nil {
		return s, err
	}
	return s, applyGovernance(&s)
}

// ReadEnvironment selects runtime settings without making dev/prod a user namespace.
func ReadEnvironment(dir, environment string) (Settings, error) {
	s, err := Read(dir)
	if err != nil {
		return s, err
	}
	if s.Organization != "" {
		orgDir := resolvedOrganizationDir(dir, s.Organization)
		org, e := ReadOrganization(orgDir)
		if e != nil {
			return s, e
		}
		s, e = ApplyOrganization(org, s, orgDir)
		if e != nil {
			return s, e
		}
		// The merged organization catalog is the org_mcp surface; re-run
		// governance so org-scope rules and the org floor apply to it too.
		if e = applyGovernance(&s); e != nil {
			return s, e
		}
	}
	s.Environment = environment
	selection, found, err := ReadExecution(dir, environment, s.User)
	if err != nil {
		return s, err
	}
	if found {
		s.ExecutionMode, s.SupervisorURL, s.NativeCron = selection.Mode, selection.SupervisorURL, selection.NativeCron
	}
	if environment == "dev" {
		s.BrowserPort++
		s.OAuthPort++
		if s.Has("slack_app") {
			s.SlackEventsPort = slackEventsHostPort(s) + 1
		}
	}
	return s, s.Validate()
}
func Init(dir, profile string) error { return InitEnvironment(dir, profile, "prod") }
func InitEnvironment(dir, profile, environment string) error {
	return initEnvironment(dir, profile, environment, "")
}
func InitEnvironmentWithOrganization(dir, profile, environment, organization string) error {
	return initEnvironment(dir, profile, environment, organization)
}
func initEnvironment(dir, profile, environment, organization string) error {
	if environment != "dev" && environment != "prod" {
		return fmt.Errorf("environment must be dev or prod")
	}
	if !idPattern.MatchString(profile) {
		return fmt.Errorf("invalid profile")
	}
	if organization != "" && !idPattern.MatchString(organization) {
		return fmt.Errorf("invalid organization")
	}
	if err := ensureScope(dir, Scope{Kind: UserScope, ID: profile, Organization: organization}); err != nil {
		return err
	}
	if err := os.MkdirAll(dir, 0700); err != nil {
		return err
	}
	for _, p := range []string{"scope.yaml", "settings.yaml", "agent.yaml", "workspace.yaml", "secrets.dev.env", "secrets.prod.env"} {
		if _, err := os.Lstat(filepath.Join(dir, p)); err == nil {
			return fmt.Errorf("%s already exists; init never overwrites", p)
		} else if !os.IsNotExist(err) {
			return err
		}
	}
	agentBody, err := renderDefaultAgent(dir, profile, organization, allocatePorts(dir))
	if err != nil {
		return err
	}
	workspaceBody := policyTemplate(dir, "workspace-default.yaml", defaultWorkspaceYAML)
	scopeBody, err := yaml.Marshal(Scope{Kind: UserScope, ID: profile, Organization: organization})
	if err != nil {
		return err
	}
	if err = os.WriteFile(filepath.Join(dir, "scope.yaml"), scopeBody, 0600); err != nil {
		return err
	}
	if err = os.WriteFile(filepath.Join(dir, "agent.yaml"), agentBody, 0600); err != nil {
		return err
	}
	if err = os.WriteFile(filepath.Join(dir, "workspace.yaml"), workspaceBody, 0600); err != nil {
		return err
	}
	content := "# Fill locally. Never commit or send this file in chat.\nOPENAI_API_KEY=\nFAL_KEY=\nFIRECRAWL_API_KEY=\nFIRECRAWL_API_URL=\nTAVILY_API_KEY=\nTAVILY_BASE_URL=\nEXA_API_KEY=\nPARALLEL_API_KEY=\nPERPLEXITY_API_KEY=\nPERPLEXITY_BASE_URL=\nBRAVE_SEARCH_API_KEY=\nKEENABLE_API_KEY=\nSEARXNG_URL=\nXAI_API_KEY=\nTELEGRAM_BOT_TOKEN=\nTELEGRAM_ALLOWED_USERS=\nTELEGRAM_API_ID=\nTELEGRAM_API_HASH=\nTELEGRAM_SESSION_STRING=\nGOOGLE_EMAIL=\nGOOGLE_OAUTH_CLIENT_ID=\nGOOGLE_OAUTH_CLIENT_SECRET=\nHH_TOKEN=\nHH_USER_AGENT=hermes-hub/0.1\nGITHUB_TOKEN=\nGITLAB_TOKEN=\nJIRA_URL=\nJIRA_USERNAME=\nJIRA_API_TOKEN=\nSLACK_MCP_XOXP_TOKEN=\nSLACK_MCP_ADD_MESSAGE_TOOL=\nSLACK_SIGNING_SECRET=\nSLACK_BOT_TOKEN=\nSLACK_APP_TOKEN=\nSLACK_ALLOWED_USERS=\nDESKTOP_TOKEN=\nDRAFTS_TOKEN=\n"
	for _, env := range []string{"dev", "prod"} {
		if err := os.WriteFile(filepath.Join(dir, "secrets."+env+".env"), []byte(content), 0600); err != nil {
			return err
		}
	}
	for _, name := range []string{"hermes/memories", "hermes/skills", "hermes/sessions", "hermes/hooks", "hermes/plugins", "connections/google", "connections/telegram", "connections/browser", "connections/ssh/keys", "workspace", "workspace/browser", "archive", "generated"} {
		if err := os.MkdirAll(filepath.Join(dir, name), 0700); err != nil {
			return err
		}
	}
	if err := os.WriteFile(filepath.Join(dir, "SOUL.md"), []byte(userSoulStub), 0600); err != nil {
		return err
	}
	return nil
}

func ReadSecrets(path string) (map[string]string, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	out := map[string]string{}
	for i, line := range strings.Split(string(b), "\n") {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		key, v, ok := strings.Cut(line, "=")
		if !ok || !regexp.MustCompile(`^[A-Z][A-Z0-9_]*$`).MatchString(key) {
			return nil, fmt.Errorf("invalid secrets.env line %d in %s", i+1, filepath.Base(path))
		}
		if _, ok = out[key]; ok {
			return nil, fmt.Errorf("duplicate env key %s in %s", key, filepath.Base(path))
		}
		v = strings.TrimSpace(v)
		if strings.HasPrefix(v, "\"") || strings.HasPrefix(v, "'") {
			return nil, fmt.Errorf("use literal unquoted values at line %d in %s", i+1, filepath.Base(path))
		}
		out[key] = v
	}
	return out, nil
}
