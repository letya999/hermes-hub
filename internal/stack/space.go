package stack

import (
	"errors"
	"fmt"
	"gopkg.in/yaml.v3"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	_ "time/tzdata"

	"github.com/letya999/hermes-hub/internal/passportview"
)

// finishManagedRead applies the already-loaded effective revision without
// consulting retired editable files.
func finishManagedRead(s Settings, dir string, eff passportview.Effective) (Settings, error) {
	s.Environment = "prod"
	s.SpaceDir = dir
	s.CapabilityMode = "managed"
	if err := s.applyEffective(eff); err != nil {
		return s, err
	}
	return finishSpaceRead(s, dir, true)
}

// finishSpaceRead is the shared tail of every space read: runtime
// validation and the governance overlay on the published revision.
func finishSpaceRead(s Settings, dir string, validate bool) (Settings, error) {
	for name := range droppedToolhubFamilies {
		delete(s.Tools, name)
	}
	if validate {
		// Runtime validation covers passport-owned fields (ports, mounts)
		// the intent view legitimately leaves empty.
		if err := s.Validate(); err != nil {
			return s, err
		}
	}
	return s, applyGovernance(&s)
}

// ReadEffective is the runtime read path. Every space resolves settings
// from its published revision alone; a missing or unloadable revision
// refuses rather than silently downgrading to retired files.
func ReadEffective(dir, environment string) (Settings, error) {
	s, err := ReadEffectiveSpace(dir)
	if err != nil {
		return s, err
	}
	return environmentTail(s, dir, environment)
}

// ReadEffectiveSpace is the prod-semantics managed read without the
// environment tail — for callers (the supervisor's policy resolution) that
// apply the environment adjustments themselves.
func ReadEffectiveSpace(dir string) (Settings, error) {
	if !passportRevision(dir) {
		return Settings{}, fmt.Errorf("space has no published revision; run space apply first")
	}
	eff, err := passportview.LoadEffective(dir)
	if err != nil {
		return Settings{}, fmt.Errorf("managed space has no loadable published revision: %w", err)
	}
	if filepath.Base(filepath.Dir(dir)) == "spaces" && (eff.Header.Metadata.Space != filepath.Base(dir) || eff.Stack.Identity.User != filepath.Base(dir)) {
		return Settings{}, fmt.Errorf("managed space passport identity does not match its directory")
	}
	return finishManagedRead(Settings{}, dir, eff)
}

// ReadEnvironment selects runtime settings without making dev/prod a user
// namespace. Managed spaces use the published revision just like runtime
// readers; legacy files cannot override an applied passport.
func ReadEnvironment(dir, environment string) (Settings, error) {
	return ReadEffective(dir, environment)
}

// environmentTail is the post-load tail both read paths share: the
// organization merge, the environment's execution selection and the dev
// port offsets.
func environmentTail(s Settings, dir, environment string) (Settings, error) {
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
	// The stack passport is the spawn contract (SPEC-0092 R3.2): a declared
	// supervised hermes-runtime selects the supervisor even when the legacy
	// execution file was never written — the file stays rollout metadata.
	if s.PassportServices["hermes-runtime"] == "supervised" && s.ExecutionMode != "supervisor" {
		s.ExecutionMode = "supervisor"
		if s.SupervisorURL == "" {
			s.SupervisorURL = "http://supervisor:8876"
		}
		if s.NativeCron == "" {
			s.NativeCron = "disabled"
		}
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
	return initScopeEnvironment(dir, profile, environment, organization, UserScope)
}

func InitGroupEnvironment(dir, profile, environment, organization string) error {
	return initScopeEnvironment(dir, profile, environment, organization, GroupScope)
}

// InitEnvironmentProvision is `hubctl init --profile`: an explicit operator
// profile choice for a plain (non-org) space.
func InitEnvironmentProvision(dir, profile, environment, provisionProfile string) error {
	return initScopeEnvironmentProfile(dir, profile, environment, "", UserScope, provisionProfile)
}

func initScopeEnvironment(dir, profile, environment, organization string, kind ScopeKind) error {
	return initScopeEnvironmentProfile(dir, profile, environment, organization, kind, "")
}

func initScopeEnvironmentProfile(dir, profile, environment, organization string, kind ScopeKind, provisionProfile string) error {
	if environment != "dev" && environment != "prod" {
		return fmt.Errorf("environment must be dev or prod")
	}
	if !idPattern.MatchString(profile) {
		return fmt.Errorf("invalid profile")
	}
	if organization != "" && !idPattern.MatchString(organization) {
		return fmt.Errorf("invalid organization")
	}
	scope := Scope{Kind: kind, ID: profile, Organization: organization}
	if err := scope.Validate(); err != nil {
		return err
	}
	for _, part := range strings.FieldsFunc(dir, func(r rune) bool { return r == '/' || r == '\\' }) {
		if part == ".." {
			return errors.New("scope path traversal is not allowed")
		}
	}
	if err := noSymlinkPath(dir); err != nil {
		return err
	}
	if err := os.MkdirAll(dir, 0700); err != nil {
		return err
	}
	for _, p := range []string{"scope.yaml", "settings.yaml", "agent.yaml", "workspace.yaml", "passports", "profile.lock", "secrets.dev.env", "secrets.prod.env"} {
		if _, err := os.Lstat(filepath.Join(dir, p)); err == nil {
			return fmt.Errorf("%s already exists; init never overwrites", p)
		} else if !os.IsNotExist(err) {
			return err
		}
	}
	provisioned, err := resolveInitProfile(dir, profile, organization, provisionProfile)
	if err != nil {
		return err
	}
	passports, err := renderProfilePassports(provisioned, profile, organization)
	if err != nil {
		return err
	}
	if kind == GroupScope || organization != "" {
		var stackDoc map[string]any
		if err := yaml.Unmarshal(passports["stack"], &stackDoc); err != nil {
			return err
		}
		identity, _ := stackDoc["identity"].(map[string]any)
		if kind == GroupScope {
			identity["kind"] = string(GroupScope)
		}
		if organization != "" {
			identity["organization"] = organization
		}
		stackDoc["identity"] = identity
		if passports["stack"], err = yaml.Marshal(stackDoc); err != nil {
			return err
		}
	}
	if err := os.Mkdir(filepath.Join(dir, "passports"), 0700); err != nil {
		return err
	}
	for _, name := range passportview.Names {
		if err := os.WriteFile(filepath.Join(dir, "passports", name+".yaml"), passports[name], 0600); err != nil {
			return err
		}
	}
	content := "# Fill locally. Never commit or send this file in chat.\nOPENAI_API_KEY=\nFAL_KEY=\nFIRECRAWL_API_KEY=\nFIRECRAWL_API_URL=\nTAVILY_API_KEY=\nTAVILY_BASE_URL=\nEXA_API_KEY=\nPARALLEL_API_KEY=\nPERPLEXITY_API_KEY=\nPERPLEXITY_BASE_URL=\nBRAVE_SEARCH_API_KEY=\nKEENABLE_API_KEY=\nSEARXNG_URL=\nXAI_API_KEY=\nTELEGRAM_BOT_TOKEN=\nTELEGRAM_ALLOWED_USERS=\nTELEGRAM_API_ID=\nTELEGRAM_API_HASH=\nTELEGRAM_SESSION_STRING=\nGOOGLE_EMAIL=\nGOOGLE_OAUTH_CLIENT_ID=\nGOOGLE_OAUTH_CLIENT_SECRET=\nHH_TOKEN=\nHH_USER_AGENT=hermes-hub/0.1\nGITHUB_TOKEN=\nGITLAB_TOKEN=\nJIRA_URL=\nJIRA_USERNAME=\nJIRA_API_TOKEN=\nSLACK_MCP_XOXP_TOKEN=\nSLACK_MCP_ADD_MESSAGE_TOOL=\nSLACK_SIGNING_SECRET=\nSLACK_BOT_TOKEN=\nSLACK_APP_TOKEN=\nSLACK_ALLOWED_USERS=\nDESKTOP_TOKEN=\nDRAFTS_TOKEN=\n"
	for _, env := range []string{"dev", "prod"} {
		if err := os.WriteFile(filepath.Join(dir, "secrets."+env+".env"), []byte(content), 0600); err != nil {
			return err
		}
	}
	for _, name := range []string{"hermes/memories", "hermes/skills", "hermes/sessions", "hermes/hooks", "hermes/plugins", "connections/google", "connections/telegram", "connections/browser", "connections/ssh/keys", "workspace", "workspace/browser", "workspace/inbox", "workspace/inbox/people", "workspace/inbox/services", "workspace/work", "workspace/projects", "workspace/artifacts", "workspace/artifacts/documents", "workspace/artifacts/images", "workspace/artifacts/videos", "workspace/artifacts/archives", "workspace/artifacts/services", "workspace/outbox", "archive", "generated", "skills"} {
		if err := os.MkdirAll(filepath.Join(dir, name), 0700); err != nil {
			return err
		}
	}
	if err := os.WriteFile(filepath.Join(dir, "SOUL.md"), []byte(userSoulStub), 0600); err != nil {
		return err
	}
	if err := createProfileLayout(dir, provisioned); err != nil {
		return err
	}
	stamped := make(map[string][]byte, len(passports))
	for name, body := range passports {
		stamped["passports/"+name+".yaml"] = body
	}
	return writeProvisioningLock(dir, provisioned, stamped)
}

func ReadSecrets(path string) (map[string]string, error) {
	b, err := os.ReadFile(path) // #nosec G703 -- the space's own secrets.env computed by the stack, not request input
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
