// Package stack generates a private, per-user Hermes deployment.
package stack

import (
	"errors"
	"fmt"
	"io"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"time"
	_ "time/tzdata"

	"github.com/letya999/hermes-hub/internal/media"
	"gopkg.in/yaml.v3"
)

type Settings struct {
	ExecutionMode        string `yaml:"-"`
	SupervisorURL        string `yaml:"-"`
	NativeCron           string `yaml:"-"`
	Environment          string `yaml:"-"`
	CapabilityMode       string `yaml:"capability_mode,omitempty"`
	CapabilityProfileID  string `yaml:"capability_profile_id,omitempty"`
	CapabilityGeneration uint64 `yaml:"capability_generation,omitempty"`
	// Tools is the single capability surface: each entry names a toolset,
	// capability family or MCP capability and picks its backend — `native`
	// (carved out of the managed denylist, no per-call admission), `toolhub`
	// (managed executor with per-call admission and audit), `mcp[:server]`
	// (builtin connector or an mcp_servers definition) or `off`. Absent
	// means denied.
	Tools           map[string]ToolEntry `yaml:"tools,omitempty"`
	Ingress         []string             `yaml:"ingress,omitempty"`
	TelegramAuth    bool                 `yaml:"telegram_auth,omitempty"`
	Workspace       Workspace            `yaml:"workspace,omitempty"`
	MCP             map[string]MCPServer `yaml:"mcp_servers,omitempty"`
	Hooks           map[string]any       `yaml:"hooks,omitempty"`
	Memory          bool                 `yaml:"memory"`
	Honcho          bool                 `yaml:"honcho,omitempty"`
	HonchoURL       string               `yaml:"honcho_url,omitempty"`
	GlobalSkillsDir string               `yaml:"global_skills_dir,omitempty"`
	Schema          int                  `yaml:"schema"`
	User            string               `yaml:"user"`
	Organization    string               `yaml:"organization,omitempty"`
	Model           string               `yaml:"model"`
	ModelURL        string               `yaml:"model_url"`
	Timezone        string               `yaml:"timezone"`
	GoogleEmail     string               `yaml:"google_email"`
	GitLabHost      string               `yaml:"gitlab_host"`
	DesktopURL      string               `yaml:"desktop_url"`
	DraftsURL       string               `yaml:"drafts_url"`
	OAuthPort       int                  `yaml:"oauth_port"`
	BrowserPort     int                  `yaml:"browser_port"`
	SlackEventsPort int                  `yaml:"slack_events_port,omitempty"`
	// Infra marks the space that renders the shared control plane (ToolHub,
	// Credential Broker, workload-controller, cliproxy, communication-hub).
	// Exactly one deployed space must render it; secondary spaces set
	// `infra: false` and consume the shared services over the shared network.
	Infra                 *bool    `yaml:"infra,omitempty"`
	Diagnostics           *bool    `yaml:"diagnostics,omitempty"`
	OrganizationDir       string   `yaml:"-"`
	OrganizationDocsDir   string   `yaml:"-"`
	OrganizationSkillsDir string   `yaml:"-"`
	SpaceDir              string   `yaml:"-"`
	OrganizationRole      string   `yaml:"-"`
	OrgActions            []string `yaml:"-"`
	// ImageGen configures the opt-in image_gen capability. Empty fields use
	// cliproxy, that provider's default model, and workspace delivery.
	ImageGen media.ImageGen `yaml:"image_gen,omitempty"`
	// Web configures the always-on Hermes web toolset: provider selection,
	// cache and keyless policy, and the deep-research bounds the bundled
	// skill enforces. Empty keeps upstream autodetect and defaults.
	Web WebSettings `yaml:"web,omitempty"`
}
type Feature struct {
	Name     string   `json:"name"`
	Requires []string `json:"requires"`
	Scope    string   `json:"scope"`
}

var Features = []Feature{
	{"workspace", nil, "Bounded workspace and read-only extracted archive; Markdown drafts"},
	{"web", nil, "Web search and page extraction via configured providers; anonymous keyless tier needs no key"},
	{"deep_research", nil, "Bundled deep-research skill: bounded multi-round research on disk; requires web"},
	{"browser", nil, "Persistent plus anonymous Chromium via Playwright MCP; manual login through private noVNC; read and navigation tools only"},
	{"browser_act", nil, "Adds browser mutation tools (click, type, submit, evaluate) to both browser MCP servers; requires browser and a concrete owner instruction"},
	{"ssh", nil, "SSH to owner-configured host aliases: pinned host keys, allowlisted read commands and remote file reads; keys via Credential Broker or the read-only ssh config mount"},
	{"ssh_write", nil, "Adds allowlisted write commands and bounded remote file writes on SSH hosts; requires ssh"},
	{"ssh_shell", nil, "Adds bounded interactive PTY shells on SSH hosts; requires ssh"},
	{"ssh_tunnel", nil, "Adds managed loopback tunnels to pre-approved remote endpoints over SSH; requires ssh"},
	{"hh", nil, "Public vacancy API; applicant OAuth for resumes and explicitly requested applications"},
	{"telegram", []string{"TELEGRAM_BOT_TOKEN", "TELEGRAM_ALLOWED_USERS"}, "Telegram bot transport into Hermes; does not grant personal Telegram access"},
	{"slack_app", []string{"SLACK_SIGNING_SECRET", "SLACK_BOT_TOKEN"}, "Slack App Events API transport into Hermes; does not grant workspace data tools"},
	{"telegram_user", []string{"TELEGRAM_API_ID", "TELEGRAM_API_HASH", "TELEGRAM_SESSION_STRING"}, "Personal Telegram account MCP; does not receive bot messages"},
	{"telegram_write", nil, "Adds send/reply/save_draft to Telegram MCP; requires telegram_user"},
	{"google", []string{"GOOGLE_OAUTH_CLIENT_ID", "GOOGLE_OAUTH_CLIENT_SECRET"}, "Calendar, Drive, Gmail, Docs, Sheets, Slides and Tasks over OAuth"},
	{"google_write", nil, "Adds Google Workspace mutation tools; requires google"},
	{"gitlab", []string{"GITLAB_TOKEN"}, "GitLab through the bundled glab CLI and personal access token"},
	{"meet", nil, "Hermes Google Meet plugin; explicit joining and caption transcripts"},
	{"transcription", nil, "Local faster-whisper through Hermes, downloads model on first use"},
	{"image_gen", nil, "Opt-in image generation through the configured model endpoint or the external fal provider"},
	{"github", []string{"GITHUB_TOKEN"}, "Official remote GitHub MCP"},
	{"slack", []string{"SLACK_MCP_XOXP_TOKEN"}, "OAuth user token, search/read; posting opt-in per channel"},
	{"atlassian", []string{"JIRA_URL", "JIRA_USERNAME", "JIRA_API_TOKEN"}, "Direct mcp-atlassian Jira MCP; optional Confluence credentials can be added later"},
	{"desktop", []string{"DESKTOP_TOKEN"}, "Authenticated companion on a native Windows/macOS/Linux desktop"},
	{"drafts", []string{"DRAFTS_TOKEN"}, "Authenticated companion for the macOS Drafts app"},
}
var idPattern = regexp.MustCompile(`^[a-z][a-z0-9_-]{0,39}$`)
var envReferencePattern = regexp.MustCompile(`\$\{([A-Z][A-Z0-9_]*)\}`)

// userSoulStub is the placeholder Init writes; Render upgrades it to the global
// template while preserving any other user-edited content.
const userSoulStub = "# User instructions\n\n"

// IsUserSoulStub reports the Init placeholder. Real user edits are anything else.
func IsUserSoulStub(body []byte) bool { return string(body) == userSoulStub }

// HealUserSoul copies the global template over a missing file or the Init stub.
// Any other content is the user's own instructions and is left in place.
func HealUserSoul(spaceDir, templatePath string) error {
	template, err := os.ReadFile(templatePath)
	if err != nil {
		return err
	}
	soulPath := filepath.Join(spaceDir, "SOUL.md")
	existing, readErr := os.ReadFile(soulPath)
	if readErr != nil && !os.IsNotExist(readErr) {
		return readErr
	}
	if os.IsNotExist(readErr) || IsUserSoulStub(existing) {
		return atomic(soulPath, template)
	}
	return nil
}

// GatewaySecretKeys stay in communication-hub. They are never injected into
// Hermes runtime env or ToolHub connectors.
func GatewaySecretKeys() []string {
	return []string{"TELEGRAM_BOT_TOKEN", "TELEGRAM_ALLOWED_USERS", "SLACK_SIGNING_SECRET", "SLACK_BOT_TOKEN", "SLACK_APP_TOKEN", "SLACK_ALLOWED_USERS", "HUB_CLIPROXY_MGMT_URL", "HUB_CLIPROXY_MGMT_KEY", "HUB_CLIPROXY_AUTH_INDEX"}
}

func GatewayOwnedSecret(key string) bool {
	return slices.Contains(GatewaySecretKeys(), key)
}

func selfEnvKeys(s Settings) []string {
	keys := map[string]bool{"OPENAI_API_KEY": true, "FIRECRAWL_API_KEY": true, "TAVILY_API_KEY": true}
	for _, service := range ServiceCatalog() {
		if service.SelfService {
			for _, key := range service.Requires {
				keys[key] = true
			}
		}
	}
	for _, enabled := range s.featureList() {
		for _, feature := range Features {
			if feature.Name == enabled {
				for _, key := range feature.Requires {
					keys[key] = true
				}
				break
			}
		}
	}
	if s.Has("web") {
		for _, key := range s.Web.envKeys() {
			keys[key] = true
		}
	}
	if key := s.imageCredential(); key != "" {
		keys[key] = true
	}
	// Slack posting arrives only through the connector's add_message env;
	// access: ro withholds it so the entry is provably read-only.
	if s.Has("slack") && s.toolEntry("slack").Access != ToolAccessRO && (!s.OrgScoped() || s.AllowsOrgAction("slack.write")) {
		keys["SLACK_MCP_ADD_MESSAGE_TOOL"] = true
	}
	for _, server := range s.MCP {
		for _, values := range []map[string]string{server.Headers, server.Env} {
			for _, value := range values {
				for _, match := range envReferencePattern.FindAllStringSubmatch(value, -1) {
					keys[match[1]] = true
				}
			}
		}
	}
	result := make([]string, 0, len(keys))
	for key := range keys {
		if GatewayOwnedSecret(key) {
			continue
		}
		result = append(result, key)
	}
	slices.Sort(result)
	return result
}

// imageCredential is FAL_KEY only for the external fal provider.
// The cliproxy provider uses the model credential already required for chat.
func (s Settings) imageCredential() string {
	if !s.Has("image_gen") {
		return ""
	}
	gen, err := s.ImageGen.Normalize()
	if err != nil || gen.Provider != media.FalProvider {
		return ""
	}
	return "FAL_KEY"
}

// RendersInfra reports whether this space owns the shared control plane
// services. Unset keeps the historical single-space render (everything in one
// project); only explicitly secondary spaces opt out.
func (s Settings) RendersInfra() bool       { return s.Infra == nil || *s.Infra }
func (s Settings) DiagnosticsEnabled() bool { return s.Diagnostics == nil || *s.Diagnostics }

// usesDockerSocket reports whether the rendered surface can drive the Docker
// engine: toolhub-managed executors, ToolHub connector workloads, the managed
// agent channel and the diagnostics collector all consume it. A stack of
// native and mcp-raw tools with diagnostics off mounts no socket at all.
func (s Settings) usesDockerSocket() bool {
	if s.CapabilityMode == "managed" || s.DiagnosticsEnabled() {
		return true
	}
	for _, entry := range s.Tools {
		if entry.Via == ToolViaToolHub || entry.Via == ToolViaMCP {
			return true
		}
	}
	return false
}
func (s Settings) Validate() error {
	if s.CapabilityMode != "" && s.CapabilityMode != "managed" {
		return fmt.Errorf("capability_mode must be managed or absent for an unmigrated deployment")
	}
	if s.CapabilityMode == "managed" {
		if s.Model == "" || len(s.Model) > 256 || strings.TrimSpace(s.Model) != s.Model || strings.ContainsAny(s.Model, "\x00\r\n") {
			return fmt.Errorf("managed model requires an exact reviewed identifier")
		}
		if s.ModelURL != "http://model-relay:8318/v1" {
			return fmt.Errorf("managed model_url must use the reviewed model relay")
		}
		if !idPattern.MatchString(s.CapabilityProfileID) || s.CapabilityGeneration == 0 {
			return fmt.Errorf("managed capability profile needs an exact id and generation")
		}
		if _, err := ManagedCapabilityInventory(); err != nil {
			return err
		}
		if len(s.Hooks) > 0 || s.Memory || s.Honcho || s.GlobalSkillsDir != "" {
			return fmt.Errorf("managed capabilities require reviewed ToolHub definitions, not hooks or native extensions")
		}
		// image_gen stays an operator key: under managed mode the block carries
		// only the normalized grant (provider/model/delivery) into the effective
		// config — admission still goes through the ToolHub capability, so an
		// operator block without the toolhub selection is a rejected ambiguity.
		if s.ImageGen != (media.ImageGen{}) && s.toolEntry("image_gen").Via != ToolViaToolHub {
			return fmt.Errorf("image_gen belongs to a tools.image_gen: toolhub selection under managed mode")
		}
		// Under managed mode raw MCP servers may only come from the
		// organization catalog (merged in by ApplyOrganization); definitions
		// written into a personal space stay forbidden. Which of them render
		// is selected per-entry with via: mcp-raw.
		if len(s.MCP) > 0 && !s.OrgScoped() {
			return fmt.Errorf("managed capabilities require reviewed ToolHub definitions, not direct MCP or native extensions")
		}
		for name, entry := range s.Tools {
			switch entry.Via {
			case "", ToolViaOff, ToolViaToolHub, ToolViaMCP, ToolViaMCPRaw:
			case ToolViaNative:
				if !nativeCarveoutToolsets[name] && !pseudoToolsets[name] {
					return fmt.Errorf("tools.%s: %q cannot be enabled natively under managed mode", name, name)
				}
			default:
				return fmt.Errorf("tools.%s: unknown backend %q", name, entry.Via)
			}
		}
	}
	if s.CapabilityMode == "" && (s.CapabilityProfileID != "" || s.CapabilityGeneration != 0) {
		return fmt.Errorf("managed capability identity requires capability_mode managed")
	}
	if s.Schema < 1 || s.Schema > 3 || !idPattern.MatchString(s.User) {
		return fmt.Errorf("schema must be 1..3 and profile a lowercase identifier")
	}
	if s.Organization != "" && !idPattern.MatchString(s.Organization) {
		return fmt.Errorf("organization must be a lowercase identifier")
	}
	if _, err := time.LoadLocation(s.Timezone); err != nil {
		return fmt.Errorf("invalid timezone")
	}
	for _, address := range []string{s.ModelURL, s.DesktopURL, s.DraftsURL, s.HonchoURL} {
		if address == "" {
			continue
		}
		u, err := url.Parse(address)
		if err != nil || u.Host == "" || (u.Scheme != "http" && u.Scheme != "https") || u.User != nil || u.RawQuery != "" || u.Fragment != "" {
			return fmt.Errorf("URLs must be HTTP(S), without embedded credentials, query or fragment")
		}
	}
	if s.Honcho && strings.TrimSpace(s.HonchoURL) == "" {
		return fmt.Errorf("honcho requires honcho_url from the official Honcho contract")
	}
	if s.Environment != "dev" && s.Environment != "prod" {
		return fmt.Errorf("environment must be dev or prod")
	}
	if err := validateMCP(s.MCP); err != nil {
		return err
	}
	if err := s.validateTools(); err != nil {
		return err
	}
	if err := s.Workspace.validate(); err != nil {
		return err
	}
	if s.Has("telegram_write") && !s.Has("telegram_user") {
		return fmt.Errorf("telegram_write requires telegram_user")
	}
	if s.TelegramAuth && !s.Has("telegram") {
		return fmt.Errorf("telegram_auth requires telegram")
	}
	if s.Has("google_write") && !s.Has("google") {
		return fmt.Errorf("google_write requires google")
	}
	for _, sub := range []string{"ssh_write", "ssh_shell", "ssh_tunnel"} {
		if s.Has(sub) && !s.Has("ssh") {
			return fmt.Errorf("%s requires ssh", sub)
		}
	}
	if s.Has("deep_research") && !s.Has("web") {
		return fmt.Errorf("deep_research requires web")
	}
	if !s.Has("web") && !s.Web.empty() {
		return fmt.Errorf("web: settings require the web feature")
	}
	if s.Has("browser_act") && !s.Has("browser") {
		return fmt.Errorf("browser_act requires browser")
	}
	if s.Has("google") && (!strings.Contains(s.GoogleEmail, "@") || s.OAuthPort < 1024) {
		return fmt.Errorf("google requires email and oauth_port >=1024")
	}
	if s.Has("gitlab") && s.GitLabHost != "" && !regexp.MustCompile(`^[A-Za-z0-9.-]+$`).MatchString(s.GitLabHost) {
		return fmt.Errorf("gitlab_host must be a hostname")
	}
	if s.Has("desktop") && s.DesktopURL == "" {
		return fmt.Errorf("desktop_url required")
	}
	if s.Has("drafts") && s.DraftsURL == "" {
		return fmt.Errorf("drafts_url required")
	}
	if s.Has("image_gen") || s.ImageGen != (media.ImageGen{}) {
		if _, err := s.ImageGen.Normalize(); err != nil {
			return err
		}
	}
	if err := s.Web.validate(); err != nil {
		return err
	}
	if s.BrowserPort < 1024 || s.BrowserPort > 65535 || s.OAuthPort > 65535 || s.BrowserPort == s.OAuthPort {
		return fmt.Errorf("invalid or colliding host ports")
	}
	if s.Has("slack_app") {
		port := slackEventsHostPort(s)
		if port < 1024 || port > 65535 || port == s.BrowserPort || port == s.OAuthPort {
			return fmt.Errorf("invalid or colliding host ports")
		}
	}
	return nil
}

func slackEventsHostPort(s Settings) int {
	if s.SlackEventsPort > 0 {
		return s.SlackEventsPort
	}
	return 8081
}

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
	return s, s.Validate()
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
func Doctor(s Settings, secrets map[string]string) []string {
	return DoctorScope(s, secrets, nil)
}
func DoctorScope(s Settings, userSecrets, orgSecrets map[string]string) []string {
	secrets := map[string]string{}
	for key, value := range orgSecrets {
		secrets[key] = value
	}
	issues := []string{}
	for key, value := range userSecrets {
		if _, exists := secrets[key]; exists {
			issues = append(issues, "secret key duplicated between organization and user scope: "+key)
			continue
		}
		secrets[key] = value
	}
	if s.Model == "" {
		issues = append(issues, "set model in agent.yaml")
	}
	if s.ModelURL == "" {
		issues = append(issues, "set model_url to your OpenAI-compatible /v1 endpoint")
	}
	if secrets["OPENAI_API_KEY"] == "" {
		issues = append(issues, "set OPENAI_API_KEY (or local provider's documented nonempty value)")
	}
	for _, f := range Features {
		if !s.Has(f.Name) {
			continue
		}
		for _, key := range f.Requires {
			if secrets[key] == "" {
				issues = append(issues, "set "+key+" for "+f.Name)
			}
		}
	}
	if s.imageCredential() == "FAL_KEY" && secrets["FAL_KEY"] == "" {
		issues = append(issues, "set FAL_KEY for image_gen")
	}
	if s.Has("web") {
		issues = append(issues, s.Web.doctorIssues(secrets)...)
	}
	if s.Has("telegram") && secrets["TELEGRAM_ALLOWED_USERS"] != "" {
		for _, id := range strings.Split(secrets["TELEGRAM_ALLOWED_USERS"], ",") {
			if !regexp.MustCompile(`^[1-9][0-9]*$`).MatchString(strings.TrimSpace(id)) {
				issues = append(issues, "TELEGRAM_ALLOWED_USERS must contain numeric owner IDs, never '*'")
			}
		}
	}
	if s.Has("ssh") && s.SpaceDir != "" {
		if info, err := os.Lstat(filepath.Join(s.SpaceDir, "connections", "ssh", "config.yaml")); err != nil || !info.Mode().IsRegular() {
			issues = append(issues, "create connections/ssh/config.yaml for ssh (see docs/operations.md)")
		}
	}
	for name, server := range s.MCP {
		for _, values := range []map[string]string{server.Headers, server.Env} {
			for _, value := range values {
				for _, match := range regexp.MustCompile(`\$\{([A-Z][A-Z0-9_]*)\}`).FindAllStringSubmatch(value, -1) {
					if secrets[match[1]] == "" {
						issues = append(issues, "set "+match[1]+" for MCP "+name)
					}
				}
			}
		}
	}
	return issues
}
