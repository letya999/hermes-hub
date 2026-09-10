// Package stack generates a private, per-user Hermes deployment.
package stack

import (
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

	"gopkg.in/yaml.v3"
)

type Settings struct {
	Environment           string               `yaml:"-"`
	MCP                   map[string]MCPServer `yaml:"mcp_servers,omitempty"`
	Hooks                 map[string]any       `yaml:"hooks,omitempty"`
	Memory                bool                 `yaml:"memory"`
	Schema                int                  `yaml:"schema"`
	User                  string               `yaml:"user"`
	Organization          string               `yaml:"organization,omitempty"`
	DisabledMCP           []string             `yaml:"disabled_mcp,omitempty"`
	Model                 string               `yaml:"model"`
	ModelURL              string               `yaml:"model_url"`
	Timezone              string               `yaml:"timezone"`
	Features              []string             `yaml:"features"`
	GoogleEmail           string               `yaml:"google_email"`
	GitLabHost            string               `yaml:"gitlab_host"`
	DesktopURL            string               `yaml:"desktop_url"`
	DraftsURL             string               `yaml:"drafts_url"`
	OAuthPort             int                  `yaml:"oauth_port"`
	BrowserPort           int                  `yaml:"browser_port"`
	OrganizationDir       string               `yaml:"-"`
	OrganizationDocsDir   string               `yaml:"-"`
	OrganizationSkillsDir string               `yaml:"-"`
	SpaceDir              string               `yaml:"-"`
	OrganizationRole      string               `yaml:"-"`
	OrgActions            []string             `yaml:"-"`
}
type Feature struct {
	Name     string   `json:"name"`
	Requires []string `json:"requires"`
	Scope    string   `json:"scope"`
}

var Features = []Feature{
	{"workspace", nil, "Bounded workspace and read-only extracted archive; Markdown drafts"},
	{"browser", nil, "Persistent Chromium via Playwright MCP; manual login through private noVNC"},
	{"hh", nil, "Public vacancy API; applicant OAuth for resumes and explicitly requested applications"},
	{"telegram", []string{"TELEGRAM_BOT_TOKEN", "TELEGRAM_ALLOWED_USERS"}, "Telegram bot transport into Hermes; does not grant personal Telegram access"},
	{"telegram_user", []string{"TELEGRAM_API_ID", "TELEGRAM_API_HASH", "TELEGRAM_SESSION_STRING"}, "Personal Telegram account MCP; does not receive bot messages"},
	{"telegram_write", nil, "Adds send/reply/save_draft to Telegram MCP; requires telegram_user"},
	{"google", []string{"GOOGLE_OAUTH_CLIENT_ID", "GOOGLE_OAUTH_CLIENT_SECRET"}, "Calendar, Drive, Gmail, Docs, Sheets, Slides and Tasks over OAuth"},
	{"google_write", nil, "Adds Google Workspace mutation tools; requires google"},
	{"gitlab", []string{"GITLAB_TOKEN"}, "GitLab through the bundled glab CLI and personal access token"},
	{"meet", nil, "Hermes Google Meet plugin; explicit joining and caption transcripts"},
	{"transcription", nil, "Local faster-whisper through Hermes, downloads model on first use"},
	{"github", []string{"GITHUB_TOKEN"}, "Official remote GitHub MCP"},
	{"slack", []string{"SLACK_MCP_XOXP_TOKEN"}, "OAuth user token, search/read; posting opt-in per channel"},
	{"atlassian", []string{"JIRA_URL", "JIRA_USERNAME", "JIRA_API_TOKEN"}, "Direct mcp-atlassian Jira MCP; optional Confluence credentials can be added later"},
	{"desktop", []string{"DESKTOP_TOKEN"}, "Authenticated companion on a native Windows/macOS/Linux desktop"},
	{"drafts", []string{"DRAFTS_TOKEN"}, "Authenticated companion for the macOS Drafts app"},
}
var idPattern = regexp.MustCompile(`^[a-z][a-z0-9_-]{0,39}$`)
var envReferencePattern = regexp.MustCompile(`\$\{([A-Z][A-Z0-9_]*)\}`)

func selfEnvKeys(s Settings) []string {
	keys := map[string]bool{"OPENAI_API_KEY": true, "FIRECRAWL_API_KEY": true, "TAVILY_API_KEY": true}
	for _, service := range ServiceCatalog() {
		if service.SelfService {
			for _, key := range service.Requires {
				keys[key] = true
			}
		}
	}
	for _, enabled := range s.Features {
		for _, feature := range Features {
			if feature.Name == enabled {
				for _, key := range feature.Requires {
					keys[key] = true
				}
				break
			}
		}
	}
	if s.Has("slack") && (!s.OrgScoped() || s.AllowsOrgAction("slack.write")) {
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
		if key == "TELEGRAM_BOT_TOKEN" || key == "TELEGRAM_ALLOWED_USERS" {
			continue
		}
		result = append(result, key)
	}
	slices.Sort(result)
	return result
}

func (s Settings) Has(name string) bool { return slices.Contains(s.Features, name) }
func (s Settings) Validate() error {
	if s.Schema != 1 || !idPattern.MatchString(s.User) {
		return fmt.Errorf("schema must be 1 and profile a lowercase identifier")
	}
	if s.Organization != "" && !idPattern.MatchString(s.Organization) {
		return fmt.Errorf("organization must be a lowercase identifier")
	}
	seenMCP := map[string]bool{}
	for _, name := range s.DisabledMCP {
		if seenMCP[name] || !idPattern.MatchString(name) {
			return fmt.Errorf("invalid disabled MCP name %q", name)
		}
		seenMCP[name] = true
	}
	if _, err := time.LoadLocation(s.Timezone); err != nil {
		return fmt.Errorf("invalid timezone")
	}
	for _, address := range []string{s.ModelURL, s.DesktopURL, s.DraftsURL} {
		if address == "" {
			continue
		}
		u, err := url.Parse(address)
		if err != nil || u.Host == "" || (u.Scheme != "http" && u.Scheme != "https") || u.User != nil || u.RawQuery != "" || u.Fragment != "" {
			return fmt.Errorf("URLs must be HTTP(S), without embedded credentials, query or fragment")
		}
	}
	if s.Environment != "dev" && s.Environment != "prod" {
		return fmt.Errorf("environment must be dev or prod")
	}
	if err := validateMCP(s.MCP); err != nil {
		return err
	}
	seen := map[string]bool{}
	for _, name := range s.Features {
		if seen[name] || !slices.ContainsFunc(Features, func(f Feature) bool { return f.Name == name }) {
			return fmt.Errorf("unknown or duplicate feature %q", name)
		}
		seen[name] = true
	}
	if s.Has("telegram_write") && !s.Has("telegram_user") {
		return fmt.Errorf("telegram_write requires telegram_user")
	}
	if s.Has("google_write") && !s.Has("google") {
		return fmt.Errorf("google_write requires google")
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
	if s.BrowserPort < 1024 || s.BrowserPort > 65535 || s.OAuthPort > 65535 || s.BrowserPort == s.OAuthPort {
		return fmt.Errorf("invalid or colliding host ports")
	}
	return nil
}
func Read(path string) (Settings, error) {
	var s Settings
	b, err := os.ReadFile(path)
	if err != nil {
		return s, err
	}
	d := yaml.NewDecoder(strings.NewReader(string(b)))
	d.KnownFields(true)
	if err = d.Decode(&s); err != nil {
		return s, err
	}
	var extra any
	if err = d.Decode(&extra); err != io.EOF {
		return s, fmt.Errorf("one YAML document expected")
	}
	s.Environment = "prod"
	s.SpaceDir = filepath.Dir(path)
	if scope, scopeErr := ReadScope(s.SpaceDir); scopeErr == nil {
		if scope.Kind != UserScope || scope.ID != s.User || scope.Organization != s.Organization {
			return s, fmt.Errorf("user scope does not match settings.yaml")
		}
	} else if !os.IsNotExist(scopeErr) {
		return s, scopeErr
	}
	return s, s.Validate()
}

// ReadEnvironment selects runtime settings without making dev/prod a user namespace.
func ReadEnvironment(dir, environment string) (Settings, error) {
	s, err := Read(filepath.Join(dir, "settings.yaml"))
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
	if environment == "dev" {
		s.BrowserPort++
		s.OAuthPort++
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
	for _, p := range []string{"scope.yaml", "settings.yaml", "secrets.dev.env", "secrets.prod.env"} {
		if _, err := os.Lstat(filepath.Join(dir, p)); err == nil {
			return fmt.Errorf("%s already exists; init never overwrites", p)
		} else if !os.IsNotExist(err) {
			return err
		}
	}
	s := Settings{Schema: 1, Environment: environment, Memory: true, User: profile, Organization: organization, Timezone: "UTC", GitLabHost: "gitlab.com", Features: []string{"workspace", "browser", "hh"}, OAuthPort: 8000, BrowserPort: 6080}
	b, err := yaml.Marshal(s)
	if err != nil {
		return err
	}
	scopeBody, err := yaml.Marshal(Scope{Kind: UserScope, ID: profile, Organization: organization})
	if err != nil {
		return err
	}
	if err = os.WriteFile(filepath.Join(dir, "scope.yaml"), scopeBody, 0600); err != nil {
		return err
	}
	if err = os.WriteFile(filepath.Join(dir, "settings.yaml"), b, 0600); err != nil {
		return err
	}
	content := "# Fill locally. Never commit or send this file in chat.\nOPENAI_API_KEY=\nFIRECRAWL_API_KEY=\nTAVILY_API_KEY=\nTELEGRAM_BOT_TOKEN=\nTELEGRAM_ALLOWED_USERS=\nTELEGRAM_API_ID=\nTELEGRAM_API_HASH=\nTELEGRAM_SESSION_STRING=\nGOOGLE_EMAIL=\nGOOGLE_OAUTH_CLIENT_ID=\nGOOGLE_OAUTH_CLIENT_SECRET=\nHH_TOKEN=\nHH_USER_AGENT=hermes-hub/0.1\nGITHUB_TOKEN=\nGITLAB_TOKEN=\nJIRA_URL=\nJIRA_USERNAME=\nJIRA_API_TOKEN=\nSLACK_MCP_XOXP_TOKEN=\nSLACK_MCP_ADD_MESSAGE_TOOL=\nDESKTOP_TOKEN=\nDRAFTS_TOKEN=\n"
	for _, env := range []string{"dev", "prod"} {
		if err := os.WriteFile(filepath.Join(dir, "secrets."+env+".env"), []byte(content), 0600); err != nil {
			return err
		}
	}
	for _, name := range []string{"hermes/memories", "hermes/skills", "hermes/sessions", "hermes/hooks", "hermes/plugins", "connections/google", "connections/telegram", "connections/browser", "workspace", "archive", "generated"} {
		if err := os.MkdirAll(filepath.Join(dir, name), 0700); err != nil {
			return err
		}
	}
	if err := os.WriteFile(filepath.Join(dir, "SOUL.md"), []byte("# User instructions\n\n"), 0600); err != nil {
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
			return nil, fmt.Errorf("invalid secrets.env line %d", i+1)
		}
		if _, ok = out[key]; ok {
			return nil, fmt.Errorf("duplicate env key %s", key)
		}
		v = strings.TrimSpace(v)
		if strings.HasPrefix(v, "\"") || strings.HasPrefix(v, "'") {
			return nil, fmt.Errorf("use literal unquoted values at line %d", i+1)
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
		issues = append(issues, "set model in settings.yaml")
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
	if s.Has("telegram") && secrets["TELEGRAM_ALLOWED_USERS"] != "" {
		for _, id := range strings.Split(secrets["TELEGRAM_ALLOWED_USERS"], ",") {
			if !regexp.MustCompile(`^[1-9][0-9]*$`).MatchString(strings.TrimSpace(id)) {
				issues = append(issues, "TELEGRAM_ALLOWED_USERS must contain numeric owner IDs, never '*'")
			}
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
