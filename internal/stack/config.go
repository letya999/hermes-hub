package stack

import (
	"fmt"
	"github.com/letya999/hermes-hub/internal/media"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"time"
	_ "time/tzdata"
)

// Package stack generates a private, per-user Hermes deployment.

type Settings struct {
	defaultGlobalSkills  bool
	ExecutionMode        string `yaml:"-"`
	SupervisorURL        string `yaml:"-"`
	NativeCron           string `yaml:"-"`
	Environment          string `yaml:"-"`
	CapabilityMode       string `yaml:"capability_mode,omitempty"`
	CapabilityProfileID  string `yaml:"capability_profile_id,omitempty"`
	CapabilityGeneration uint64 `yaml:"capability_generation,omitempty"`
	// CLIRegistries widens only the controller's CLI ceiling. Each reviewed
	// definition still requests its own hosts; runtimes receive no new egress.
	CLIRegistries []string `yaml:"cli_registries,omitempty"`
	// CLIRuntime selects the OCI runtime tier for bounded-cli cells:
	// "runc" (default), "runsc" or "kata". The daemon must list the runtime;
	// an unavailable one fails closed at admission.
	CLIRuntime string `yaml:"cli_runtime,omitempty"`
	// CLIPerPrincipalUID maps each principal to a stable cell uid (20000+)
	// so files a read-write cell leaves in a workspace carry a per-principal
	// owner instead of the shared cli uid.
	CLIPerPrincipalUID bool `yaml:"cli_per_principal_uid,omitempty"`
	// CLIToolboxes names operator-pinned toolsets: toolbox id -> member
	// images pinned as image@sha256 manifest digests. Toolbox-lifecycle
	// definitions share one warm cell per principal with every member
	// mounted read-only under /tools/<i>.
	CLIToolboxes map[string][]string `yaml:"cli_toolboxes,omitempty"`
	// Tools is the single capability surface: each entry names a toolset,
	// capability family or MCP capability and picks its backend — `native`
	// (carved out of the managed denylist, no per-call admission), `toolhub`
	// (managed executor with per-call admission and audit), `mcp[:server]`
	// (builtin connector or an mcp_servers definition) or `off`. Absent
	// means denied.
	Tools   map[string]ToolEntry `yaml:"tools,omitempty"`
	Ingress []string             `yaml:"ingress,omitempty"`
	// TelegramAuth is tri-state: nil auto-enables QR login whenever telegram
	// ingress runs, true forces it on (still requires telegram), false opts out.
	TelegramAuth    *bool                `yaml:"telegram_auth,omitempty"`
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
	// Cliproxy selects the proxy topology: "shared" (default) resolves the
	// infra space's instance; "dedicated" renders a cliproxy per space.
	Cliproxy            string `yaml:"cliproxy,omitempty"`
	CliproxyCredentials string `yaml:"cliproxy_credentials,omitempty"`
	Timezone            string `yaml:"timezone"`
	GoogleEmail         string `yaml:"google_email"`
	GitLabHost          string `yaml:"gitlab_host"`
	DesktopURL          string `yaml:"desktop_url"`
	DraftsURL           string `yaml:"drafts_url"`
	OAuthPort           int    `yaml:"oauth_port"`
	BrowserPort         int    `yaml:"browser_port"`
	SlackEventsPort     int    `yaml:"slack_events_port,omitempty"`
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
	// gov is the per-load tool-governance view (issue 139): the evaluated
	// decisions and cap:read overrides the render consumers enforce. Nil when
	// the Settings were built without Read — the load path is the boundary.
	gov *toolGovernance
	// ImageGen configures the opt-in image_gen capability. Empty fields use
	// cliproxy, that provider's default model, and workspace delivery.
	ImageGen media.ImageGen `yaml:"image_gen,omitempty"`
	// Web configures the always-on Hermes web toolset: provider selection,
	// cache and keyless policy, and the deep-research bounds the bundled
	// skill enforces. Empty keeps upstream autodetect and defaults.
	Web WebSettings `yaml:"web,omitempty"`
	// Compression declares bounded compression.* policy leaves (the
	// selfsettings allowlist, provisioned at render time) plus the
	// hub_sweep between-jobs policy.
	Compression map[string]any `yaml:"compression,omitempty"`
	// Context carries the compaction-engine grant. The yaml key is `context`
	// (not `features`) because schema-1 files already own `features:` as a
	// legacy string list; it renders to features.compact in the effective
	// config, matching the selfsettings overlay path.
	Context      ContextSection `yaml:"context,omitempty"`
	Goals        ContextGoals   `yaml:"goals,omitempty"`
	SummaryModel string         `yaml:"summary_model,omitempty"`
	JudgeModel   string         `yaml:"judge_model,omitempty"`
}

// DefaultGlobalSkillsDir resolves the bundled catalog without changing operator rights.
func (s *Settings) DefaultGlobalSkillsDir(dir string) {
	if s.CapabilityMode != "managed" && strings.TrimSpace(s.GlobalSkillsDir) == "" {
		s.GlobalSkillsDir = dir
		s.defaultGlobalSkills = true
	}
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
	{"exposure", nil, "Named outbound public endpoints over a per-binding tunnel workload (sish/cloudflared); requires a reviewed connections/exposure/config.yaml"},
}
var idPattern = regexp.MustCompile(`^[a-z][a-z0-9_-]{0,39}$`)
var envReferencePattern = regexp.MustCompile(`\$\{([A-Z][A-Z0-9_]*)\}`)
var digestRefPattern = regexp.MustCompile(`^sha256:[0-9a-f]{64}$`)

func toolNameID(name string) bool { return idPattern.MatchString(name) }

// toolboxMemberRef accepts only image@sha256 manifest digest pins; bare tags
// would let the admitted member surface drift silently.
func toolboxMemberRef(ref string) bool {
	at := strings.LastIndex(ref, "@")
	return at > 0 && digestRefPattern.MatchString(ref[at+1:]) && !strings.ContainsAny(ref[:at], " \t\r\n") && !strings.Contains(ref, "..")
}

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
	return []string{"TELEGRAM_BOT_TOKEN", "TELEGRAM_ALLOWED_USERS", "SLACK_SIGNING_SECRET", "SLACK_BOT_TOKEN", "SLACK_APP_TOKEN", "SLACK_ALLOWED_USERS"}
}

// ModelRelaySecretKeys own the CLIProxy management surface. They render into
// model-relay.<env>.env — never into the gateway env or the agent runtime.
func ModelRelaySecretKeys() []string {
	return []string{"HUB_CLIPROXY_MGMT_URL", "HUB_CLIPROXY_MGMT_KEY", "HUB_CLIPROXY_AUTH_INDEX"}
}

func GatewayOwnedSecret(key string) bool {
	return slices.Contains(GatewaySecretKeys(), key) || slices.Contains(ModelRelaySecretKeys(), key)
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
	seenRegistries := map[string]bool{}
	for _, name := range s.CLIRegistries {
		if len(cliRegistryHosts[name]) == 0 || seenRegistries[name] {
			return fmt.Errorf("cli_registries: unknown or duplicate registry %q", name)
		}
		seenRegistries[name] = true
	}
	switch s.CLIRuntime {
	case "", "runc", "runsc", "kata":
	default:
		return fmt.Errorf("cli_runtime must be runc, runsc or kata")
	}
	if len(s.CLIToolboxes) > 16 {
		return fmt.Errorf("cli_toolboxes: too many toolboxes")
	}
	for name, members := range s.CLIToolboxes {
		if !toolNameID(name) || len(members) == 0 || len(members) > 32 {
			return fmt.Errorf("cli_toolboxes: invalid toolbox %q", name)
		}
		for _, ref := range members {
			if !toolboxMemberRef(ref) {
				return fmt.Errorf("cli_toolboxes: member of %q must pin image@sha256", name)
			}
		}
	}
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
	switch s.Cliproxy {
	case "", "shared":
	case "dedicated":
		if s.RendersInfra() {
			return fmt.Errorf("cliproxy: dedicated cannot render on the infra space itself")
		}
	default:
		return fmt.Errorf("cliproxy must be shared or dedicated")
	}
	if s.CliproxyCredentials == "private" && s.Cliproxy != "dedicated" {
		return fmt.Errorf("private cliproxy credentials require dedicated placement; shared proxy identity routing is not proven")
	}
	if s.CliproxyCredentials != "" && s.CliproxyCredentials != "private" && s.CliproxyCredentials != "shared" {
		return fmt.Errorf("cliproxy_credentials must be private or shared")
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
	if s.TelegramAuth != nil && *s.TelegramAuth && !s.Has("telegram") {
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
	if err := s.validateContext(); err != nil {
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
