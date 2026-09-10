package stack

import (
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"

	"gopkg.in/yaml.v3"
)

var orgActionPattern = regexp.MustCompile(`^[a-z][a-z0-9_.:-]{0,63}$`)

// OrganizationSettings is the host-owned policy overlay. It is never written
// into a user's runtime volume.
type OrganizationSettings struct {
	Schema       int                  `yaml:"schema"`
	Organization string               `yaml:"organization"`
	Members      map[string]string    `yaml:"members"`
	Features     []string             `yaml:"features"`
	MCP          map[string]MCPServer `yaml:"mcp_servers,omitempty"`
	ReadOnlyMCP  []string             `yaml:"read_only_mcp,omitempty"`
	OrgActions   []string             `yaml:"org_actions,omitempty"`
}

func (o OrganizationSettings) Validate() error {
	if o.Schema != 1 || !idPattern.MatchString(o.Organization) {
		return fmt.Errorf("organization schema must be 1 and id a lowercase identifier")
	}
	for user, role := range o.Members {
		if !idPattern.MatchString(user) {
			return fmt.Errorf("invalid organization member %q", user)
		}
		if role != "member" && role != "admin" && role != "owner" {
			return fmt.Errorf("invalid role %q for organization member %q", role, user)
		}
	}
	seen := map[string]bool{}
	for _, name := range o.Features {
		if seen[name] || !slices.ContainsFunc(Features, func(f Feature) bool { return f.Name == name }) {
			return fmt.Errorf("organization has unknown or duplicate feature %q", name)
		}
		seen[name] = true
	}
	if err := validateMCP(o.MCP); err != nil {
		return err
	}
	seen = map[string]bool{}
	for _, name := range o.ReadOnlyMCP {
		if seen[name] {
			return fmt.Errorf("duplicate read-only organization MCP %q", name)
		}
		server, ok := o.MCP[name]
		if !ok || server.Tools == nil || len(server.Tools.Include) == 0 || len(server.Tools.Exclude) > 0 {
			return fmt.Errorf("organization MCP %q must have a non-empty tools.include allowlist and no tools.exclude", name)
		}
		seen[name] = true
	}
	for name := range o.MCP {
		if !slices.Contains(o.ReadOnlyMCP, name) {
			return fmt.Errorf("organization MCP %q must be listed in read_only_mcp", name)
		}
	}
	seen = map[string]bool{}
	for _, action := range o.OrgActions {
		if seen[action] || !orgActionPattern.MatchString(action) {
			return fmt.Errorf("invalid or duplicate organization action %q", action)
		}
		seen[action] = true
	}
	return nil
}

func ReadOrganization(path string) (OrganizationSettings, error) {
	var o OrganizationSettings
	b, err := os.ReadFile(path)
	if err != nil {
		return o, err
	}
	d := yaml.NewDecoder(strings.NewReader(string(b)))
	d.KnownFields(true)
	if err = d.Decode(&o); err != nil {
		return o, err
	}
	var extra any
	if err = d.Decode(&extra); err != io.EOF {
		return o, fmt.Errorf("one YAML document expected")
	}
	return o, o.Validate()
}

// ApplyOrganization resolves the host-owned organization policy for one user.
// The caller is responsible for authenticating the user before selecting the
// user space; this function prevents a non-member or an unapproved config from
// becoming an effective runtime.
func ApplyOrganization(o OrganizationSettings, s Settings, orgDir string) (Settings, error) {
	if s.Organization == "" {
		return s, fmt.Errorf("user has no organization")
	}
	if o.Organization != s.Organization {
		return s, fmt.Errorf("organization config mismatch: want %q", s.Organization)
	}
	role, ok := o.Members[s.User]
	if !ok {
		return s, fmt.Errorf("user %q is not a member of organization %q", s.User, s.Organization)
	}
	allowed := map[string]bool{}
	for _, name := range o.Features {
		allowed[name] = true
	}
	for _, name := range s.Features {
		if !allowed[name] {
			return s, fmt.Errorf("feature %q is not enabled for organization %q", name, s.Organization)
		}
	}
	if s.Has("telegram_write") && !slices.Contains(o.OrgActions, "telegram.write") {
		return s, fmt.Errorf("organization action telegram.write is not allowed")
	}
	if len(s.MCP) > 0 {
		return s, fmt.Errorf("user MCP definitions are not allowed in organization scope; add them to the organization")
	}
	for _, name := range s.DisabledMCP {
		if _, ok := o.MCP[name]; !ok {
			return s, fmt.Errorf("MCP %q is not approved by organization %q", name, s.Organization)
		}
	}
	mcp := make(map[string]MCPServer, len(o.MCP))
	for name, server := range o.MCP {
		if !slices.Contains(s.DisabledMCP, name) {
			mcp[name] = server
		}
	}
	docsDir := filepath.Join(orgDir, "docs")
	info, err := os.Stat(docsDir)
	if err != nil {
		return s, fmt.Errorf("organization docs directory: %w", err)
	}
	if !info.IsDir() {
		return s, fmt.Errorf("organization docs path is not a directory")
	}
	s.MCP = mcp
	s.OrganizationDir = orgDir
	s.OrganizationDocsDir = docsDir
	s.OrganizationRole = role
	s.OrgActions = append([]string(nil), o.OrgActions...)
	return s, nil
}

func (s Settings) OrgScoped() bool { return s.Organization != "" }

func (s Settings) AllowsOrgAction(action string) bool {
	if !s.OrgScoped() {
		return true
	}
	return slices.Contains(s.OrgActions, action)
}

func organizationDir(spaceDir, organization string) string {
	abs, err := filepath.Abs(spaceDir)
	if err != nil {
		return filepath.Join(filepath.Dir(spaceDir), "organizations", organization)
	}
	root := filepath.Dir(abs)
	if filepath.Base(root) == "spaces" {
		root = filepath.Dir(root)
	}
	return filepath.Join(root, "organizations", organization)
}

func ReadOrganizationSecrets(s Settings, environment string) (map[string]string, error) {
	if !s.OrgScoped() {
		return nil, nil
	}
	return ReadSecrets(filepath.Join(s.OrganizationDir, "secrets."+environment+".env"))
}

func organizationSecretKeys(s Settings) string {
	if !s.OrgScoped() {
		return ""
	}
	secrets, err := ReadOrganizationSecrets(s, s.Environment)
	if err != nil {
		return ""
	}
	keys := make([]string, 0, len(secrets))
	for key := range secrets {
		keys = append(keys, key)
	}
	slices.Sort(keys)
	return strings.Join(keys, ",")
}

func InitOrganization(dir, organization, owner string) error {
	if !idPattern.MatchString(organization) {
		return fmt.Errorf("invalid organization")
	}
	if owner != "" && !idPattern.MatchString(owner) {
		return fmt.Errorf("invalid organization owner")
	}
	if err := os.MkdirAll(filepath.Join(dir, "docs"), 0700); err != nil {
		return err
	}
	for _, name := range []string{"settings.yaml", "secrets.dev.env", "secrets.prod.env"} {
		if _, err := os.Lstat(filepath.Join(dir, name)); err == nil {
			return fmt.Errorf("%s already exists; org-init never overwrites", name)
		} else if !os.IsNotExist(err) {
			return err
		}
	}
	members := map[string]string{}
	if owner != "" {
		members[owner] = "owner"
	}
	o := OrganizationSettings{
		Schema: 1, Organization: organization, Members: members,
		Features: organizationFeatures(),
	}
	b, err := yaml.Marshal(o)
	if err != nil {
		return err
	}
	if err = os.WriteFile(filepath.Join(dir, "settings.yaml"), b, 0600); err != nil {
		return err
	}
	envTemplate := "# Fill locally. Never commit or send this file in chat.\n"
	for _, environment := range []string{"dev", "prod"} {
		if err = os.WriteFile(filepath.Join(dir, "secrets."+environment+".env"), []byte(envTemplate), 0600); err != nil {
			return err
		}
	}
	memory := filepath.Join(dir, "docs", "MEMORY.md")
	if _, err = os.Stat(memory); os.IsNotExist(err) {
		if err = os.WriteFile(memory, []byte("# Organization memory\n\n"), 0600); err != nil {
			return err
		}
	}
	return nil
}

func organizationFeatures() []string {
	result := make([]string, 0, len(Features)-1)
	for _, feature := range Features {
		if feature.Name != "telegram_write" {
			result = append(result, feature.Name)
		}
	}
	return result
}
