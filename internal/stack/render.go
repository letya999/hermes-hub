package stack

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"

	"gopkg.in/yaml.v3"
)

type M = map[string]any

func env(names ...string) M {
	m := M{}
	for _, n := range names {
		m[n] = "${" + n + "}"
	}
	return m
}
func stdio(command string, args []string, e M) M {
	return M{"command": command, "args": args, "env": e, "timeout": 120}
}
func remote(url, key string) M {
	m := M{"url": url, "timeout": 90, "skip_preflight": true}
	if key != "" {
		m["headers"] = M{"Authorization": "Bearer ${" + key + "}"}
	}
	return m
}
func Config(s Settings) M {
	organizationSkills := s.OrganizationSkillsDir
	if organizationSkills == "" && s.OrganizationDir != "" {
		organizationSkills = filepath.Join(s.OrganizationDir, "hermes", "skills")
	}
	servers := M{}
	if s.Has("workspace") || s.Has("hh") {
		e := env("HH_TOKEN", "HH_USER_AGENT")
		e["HUB_HH_ENABLED"] = fmt.Sprint(s.Has("hh"))
		e["HUB_ORG_ACTIONS"] = "${HUB_ORG_ACTIONS}"
		e["HUB_SELF_ENV_KEYS"] = "${HUB_SELF_ENV_KEYS}"
		e["HUB_PROTECTED_ENV_KEYS"] = "${HUB_PROTECTED_ENV_KEYS}"
		e["HUB_STATE"] = "/state"
		for _, service := range ServiceCatalog() {
			if !service.SelfService {
				continue
			}
			for _, key := range service.Requires {
				e[key] = "${" + key + "}"
			}
		}
		args := []string{"tools", "--workspace", "/workspace", "--archive", "/archive"}
		if s.OrgScoped() {
			args = append(args, "--organization", "/org")
		}
		servers["hub"] = stdio("/usr/local/bin/hubctl", args, e)
	}
	if s.Has("telegram_user") {
		e := env("TELEGRAM_API_ID", "TELEGRAM_API_HASH", "TELEGRAM_SESSION_STRING")
		e["TELEGRAM_EXPOSED_TOOLS"] = "read-only"
		if s.Has("telegram_write") {
			e["TELEGRAM_EXPOSED_TOOLS"] = "read-only+send_message,reply_to_message,save_draft"
		}
		e["TELEGRAM_TRANSCRIBE"] = "off"
		e["TELEGRAM_TRANSCRIPT_CACHE_DIR"] = "/state/telegram/transcripts"
		e["XDG_STATE_HOME"] = "/state/telegram"
		servers["telegram_user"] = stdio("/opt/telegram/.venv/bin/python", []string{"/opt/telegram/main.py"}, e)
	}
	if s.Has("google") {
		e := env("GOOGLE_OAUTH_CLIENT_ID", "GOOGLE_OAUTH_CLIENT_SECRET")
		e["USER_GOOGLE_EMAIL"] = "${GOOGLE_EMAIL}"
		e["WORKSPACE_MCP_CREDENTIALS_DIR"] = "/state/google"
		e["WORKSPACE_MCP_PORT"] = "8000"
		e["WORKSPACE_MCP_HOST"] = "0.0.0.0"
		e["WORKSPACE_MCP_BASE_URI"] = "http://0.0.0.0"
		e["GOOGLE_OAUTH_REDIRECT_URI"] = "${GOOGLE_OAUTH_REDIRECT_URI}"
		e["WORKSPACE_MCP_MAX_FILE_BYTES"] = "16777216"
		args := []string{"/opt/google/main.py", "--transport", "stdio", "--single-user", "--tool-tier", "extended", "--tools", "gmail", "drive", "calendar", "docs", "sheets", "slides", "tasks"}
		if !s.Has("google_write") {
			args = append(args, "--read-only")
		}
		servers["google"] = stdio("/opt/google/.venv/bin/python", args, e)
	}
	if s.Has("browser") {
		servers["browser"] = stdio("node", []string{"/opt/browser/node_modules/@playwright/mcp/cli.js", "--cdp-endpoint", "http://127.0.0.1:9222", "--caps", "vision,pdf", "--output-dir", "/workspace/browser"}, nil)
	}
	if s.Has("github") {
		servers["github"] = remote("https://api.githubcopilot.com/mcp/", "GITHUB_TOKEN")
	}
	if s.Has("slack") {
		e := env("SLACK_MCP_XOXP_TOKEN")
		if !s.OrgScoped() || s.AllowsOrgAction("slack.write") {
			e["SLACK_MCP_ADD_MESSAGE_TOOL"] = "${SLACK_MCP_ADD_MESSAGE_TOOL}"
		} else {
			e["SLACK_MCP_ADD_MESSAGE_TOOL"] = ""
		}
		servers["slack"] = stdio("/usr/local/bin/slack-mcp-server", []string{"--transport", "stdio"}, e)
	}
	if s.Has("atlassian") {
		servers["atlassian"] = stdio("/opt/mcp-atlassian/.venv/bin/mcp-atlassian", nil, env("JIRA_URL", "JIRA_USERNAME", "JIRA_API_TOKEN"))
	}
	if s.Has("desktop") {
		servers["desktop"] = remote(s.DesktopURL, "DESKTOP_TOKEN")
	}
	if s.Has("drafts") {
		servers["drafts"] = remote(s.DraftsURL, "DRAFTS_TOKEN")
	}
	for name, server := range s.MCP {
		servers[name] = server.Config()
	}
	toolsets := []string{"terminal", "file", "web", "vision", "skills", "todo", "cronjob", "messaging", "memory", "session_search"}
	if s.ExecutionMode == "supervisor" {
		toolsets = slices.DeleteFunc(toolsets, func(name string) bool { return name == "cronjob" })
	}
	if s.Has("meet") {
		toolsets = append(toolsets, "google_meet")
	}
	skills := M{}
	if organizationSkills != "" {
		skills["external_dirs"] = []string{"/org/hermes/skills"}
	}
	return M{"model": M{"default": s.Model, "provider": "custom", "base_url": s.ModelURL, "api_key": "${OPENAI_API_KEY}"}, "terminal": M{"backend": "local", "cwd": "/workspace", "timeout": 120}, "platform_toolsets": M{"cli": toolsets, "telegram": toolsets}, "mcp_servers": servers, "skills": skills, "stt": M{"enabled": s.Has("transcription"), "provider": "local", "language": "", "local": M{"model": "small"}}, "timezone": s.Timezone, "hooks": s.Hooks, "memory": M{"memory_enabled": s.Memory, "user_profile_enabled": s.Memory}}
}
func Compose(s Settings, projectRoot, dir string) M {
	return compose(s, projectRoot, dir, true)
}

// RuntimeService keeps supervised and static containers on the same data/env contract.
func RuntimeService(s Settings, projectRoot, dir string) M {
	return compose(s, projectRoot, dir, false)["services"].(M)["hermes-runtime"].(M)
}

func compose(s Settings, projectRoot, dir string, includeGateway bool) M {
	stateVolumes := []any{
		M{"type": "bind", "source": filepath.ToSlash(filepath.Join(dir, "runtime")), "target": "/state"},
		M{"type": "bind", "source": filepath.ToSlash(filepath.Join(dir, "hermes")), "target": "/state/hermes"},
		M{"type": "bind", "source": filepath.ToSlash(filepath.Join(dir, "connections", "google")), "target": "/state/google"},
		M{"type": "bind", "source": filepath.ToSlash(filepath.Join(dir, "connections", "telegram")), "target": "/state/telegram"},
		M{"type": "bind", "source": filepath.ToSlash(filepath.Join(dir, "connections", "browser")), "target": "/state/browser"},
		M{"type": "bind", "source": filepath.ToSlash(filepath.Join(dir, "home")), "target": "/state/home"},
		M{"type": "bind", "source": filepath.ToSlash(filepath.Join(dir, "cache")), "target": "/state/cache"},
		M{"type": "bind", "source": filepath.ToSlash(filepath.Join(dir, "workspace")), "target": "/workspace"},
		M{"type": "bind", "source": filepath.ToSlash(filepath.Join(dir, "archive")), "target": "/archive", "read_only": true},
		M{"type": "bind", "source": filepath.ToSlash(filepath.Join(dir, "hermes."+s.Environment+".yaml")), "target": "/config/config.yaml", "read_only": true},
		M{"type": "bind", "source": filepath.ToSlash(filepath.Join(dir, "SOUL.md")), "target": "/config/SOUL.md", "read_only": true},
	}
	ports := []string{}
	if s.Has("browser") || s.Has("meet") {
		ports = append(ports, fmt.Sprintf("127.0.0.1:%d:6080", s.BrowserPort))
	}
	// Keep the loopback OAuth callback available for a connector enabled from chat.
	ports = append(ports, fmt.Sprintf("127.0.0.1:%d:8000", s.OAuthPort))
	runtimeEnvFiles := []any{M{"path": filepath.ToSlash(filepath.Join(dir, "runtime."+s.Environment+".env")), "format": "raw"}, M{"path": filepath.ToSlash(filepath.Join(dir, "runtime.auth")), "format": "raw"}}
	organizationID := s.Organization
	if organizationID == "" {
		organizationID = "personal"
	}
	host := s.GitLabHost
	if host == "" {
		host = "gitlab.com"
	}
	policy := policyVersion(s)
	runtimeEnv := M{"HUB_SHARED_GID": fmt.Sprint(max(0, os.Getgid())), "HUB_ORG_SCOPED": fmt.Sprint(s.OrgScoped()), "HUB_ORG_ACTIONS": strings.Join(s.OrgActions, ","), "HUB_SELF_ENV_KEYS": strings.Join(runtimeSelfEnvKeys(s), ","), "HUB_PROTECTED_ENV_KEYS": strings.Join(runtimeProtectedEnvKeys(s), ","), "HERMES_HOME": "/state/hermes", "HOME": "/state/home", "HUB_STATE": "/state", "HUB_WORKSPACE": "/workspace", "HUB_USER_ID": s.User, "HUB_ORGANIZATION_ID": organizationID, "HUB_RUNTIME_ID": s.User, "HUB_POLICY_VERSION": policy, "HUB_FEATURES": strings.Join(s.Features, ","), "HUB_RUNTIME_LISTEN": "0.0.0.0:8080", "TZ": s.Timezone, "HUB_BROWSER": fmt.Sprint(s.Has("browser")), "HUB_MEET": fmt.Sprint(s.Has("meet")), "GITLAB_HOST": host, "GOOGLE_EMAIL": s.GoogleEmail, "GOOGLE_OAUTH_REDIRECT_URI": fmt.Sprintf("http://localhost:%d/oauth2callback", s.OAuthPort), "PYTHONDONTWRITEBYTECODE": "1", "XDG_CACHE_HOME": "/state/cache"}
	if s.OrgScoped() {
		stateVolumes = append(stateVolumes, M{"type": "bind", "source": filepath.ToSlash(s.OrganizationDocsDir), "target": "/org", "read_only": true})
	}
	common := M{"build": M{"context": filepath.ToSlash(projectRoot), "dockerfile": "docker/Dockerfile", "target": s.Environment}, "image": "hermes-hub:0.3.0-" + s.Environment, "init": true, "restart": "unless-stopped", "user": fmt.Sprintf("10001:%d", max(0, os.Getgid())), "read_only": true, "cap_drop": []string{"ALL"}, "security_opt": []string{"no-new-privileges:true"}, "shm_size": "1gb", "tmpfs": []string{"/tmp:uid=10001,gid=10001,mode=1777"}, "extra_hosts": []string{"host.docker.internal:host-gateway"}}
	runtimeService := cloneMap(common)
	runtimeService["env_file"] = runtimeEnvFiles
	runtimeService["environment"] = runtimeEnv
	runtimeService["volumes"] = stateVolumes
	runtimeService["ports"] = ports
	runtimeService["command"] = []string{"serve"}
	if !s.Has("telegram") && s.NativeCron != "unmigrated" {
		runtimeService["command"] = []string{"idle"}
	}
	runtimeService["healthcheck"] = M{"test": []string{"CMD", "hub-runtime", "health"}, "interval": "30s", "timeout": "5s", "retries": 3}
	if s.Environment == "dev" {
		runtimeService["restart"] = "no"
		runtimeService["read_only"] = false
		for _, name := range []string{"cmd", "internal", "docker", "config", "docs", "specs", ".work", ".github", "go.mod", "go.sum", "justfile", "AGENTS.md", "README.md", "SETUP.md", "START_HERE.ru.md", "LICENSE", "SECURITY.md", "CONTRIBUTING.md"} {
			stateVolumes = append(stateVolumes, M{"type": "bind", "source": filepath.ToSlash(filepath.Join(projectRoot, name)), "target": "/src/" + name})
		}
		runtimeService["volumes"] = stateVolumes
		runtimeService["environment"].(M)["GOCACHE"] = "/state/go-build"
		runtimeService["environment"].(M)["GOMODCACHE"] = "/state/go-mod"
	}
	services := M{"hermes-runtime": runtimeService}
	if includeGateway && s.Has("telegram") {
		supervisorURL := strings.TrimSpace(os.Getenv("HUB_RUNTIME_SUPERVISOR_URL"))
		if s.ExecutionMode != "" {
			supervisorURL = s.SupervisorURL
		}
		gateway := cloneMap(common)
		gateway["entrypoint"] = []string{"communication-hub"}
		gatewayEnvFiles := []any{M{"path": filepath.ToSlash(filepath.Join(dir, "communication."+s.Environment+".env")), "format": "raw"}}
		gatewayEnvironment := M{"HUB_USER_ID": s.User, "HUB_ORGANIZATION_ID": organizationID, "HUB_RUNTIME_ID": s.User, "HUB_POLICY_VERSION": policy, "HUB_FEATURES": strings.Join(s.Features, ","), "HUB_RUNTIME_URL": "http://hermes-runtime:8080", "HUB_RUNTIME_SUPERVISOR_URL": "${HUB_RUNTIME_SUPERVISOR_URL}", "HUB_COMMUNICATION_SPOOL": "/data", "HUB_CONFIGURED_ENV": strings.Join(configuredEnvKeys(s, dir), ",")}
		if s.ExecutionMode != "" {
			gatewayEnvironment["HUB_RUNTIME_SUPERVISOR_URL"] = supervisorURL
		}
		if supervisorURL == "" {
			gatewayEnvFiles = append(gatewayEnvFiles, M{"path": filepath.ToSlash(filepath.Join(dir, "runtime.auth")), "format": "raw"})
		} else {
			gatewayEnvironment["HUB_SUPERVISOR_AUTH"] = "${HUB_SUPERVISOR_AUTH}"
		}
		gateway["env_file"] = gatewayEnvFiles
		gateway["environment"] = gatewayEnvironment
		gateway["volumes"] = []any{M{"type": "volume", "source": "communication-hub-data", "target": "/data"}}
		if supervisorURL == "" {
			gateway["depends_on"] = M{"hermes-runtime": M{"condition": "service_healthy"}}
		}
		gateway["restart"] = "unless-stopped"
		services["communication-hub"] = gateway
		if supervisorURL != "" {
			delete(services, "hermes-runtime")
		}
	}
	return M{"name": "hermes-hub-" + s.User + "-" + s.Environment, "services": services, "volumes": M{"communication-hub-data": M{}}}
}

// PolicyVersion identifies the effective runtime policy used by transport and supervisor.
func PolicyVersion(s Settings) string { return policyVersion(s) }

func policyVersion(s Settings) string {
	// Executor rollout is transport state, not a grant of additional tool rights.
	s.ExecutionMode = ""
	body, _ := yaml.Marshal(Config(s))
	hash := sha256.Sum256(body)
	return "policy-" + hex.EncodeToString(hash[:8])
}

func cloneMap(source M) M {
	result := M{}
	for key, value := range source {
		result[key] = value
	}
	return result
}

func runtimeSelfEnvKeys(s Settings) []string {
	keys := []string{}
	for _, key := range selfEnvKeys(s) {
		if key != "TELEGRAM_BOT_TOKEN" && key != "TELEGRAM_ALLOWED_USERS" {
			keys = append(keys, key)
		}
	}
	return keys
}

func runtimeProtectedEnvKeys(s Settings) []string {
	keys := []string{"TELEGRAM_BOT_TOKEN", "TELEGRAM_ALLOWED_USERS"}
	for _, key := range strings.Split(organizationSecretKeys(s), ",") {
		if key != "" && !slices.Contains(keys, key) {
			keys = append(keys, key)
		}
	}
	slices.Sort(keys)
	return keys
}

func configuredEnvKeys(s Settings, dir string) []string {
	keys := []string{}
	secrets, err := ReadSecrets(filepath.Join(dir, "secrets."+s.Environment+".env"))
	if err == nil {
		for key, value := range secrets {
			if value != "" {
				keys = append(keys, key)
			}
		}
	}
	slices.Sort(keys)
	return keys
}
func Render(dir, root string) error { return RenderEnvironment(dir, root, "prod") }
func RenderEnvironment(dir, root, environment string) error {
	dir, err := filepath.Abs(dir)
	if err != nil {
		return err
	}
	root, err = filepath.Abs(root)
	if err != nil {
		return err
	}
	s, err := ReadEnvironment(dir, environment)
	if err != nil {
		return err
	}
	secrets, err := ReadSecrets(filepath.Join(dir, "secrets."+s.Environment+".env"))
	if err != nil {
		return err
	}
	var orgSecrets map[string]string
	if s.OrgScoped() {
		orgSecrets, err = ReadOrganizationSecrets(s, s.Environment)
		if err != nil {
			return err
		}
	}
	for _, name := range []string{"archive", "runtime", "hermes", "connections", "connections/google", "connections/telegram", "connections/browser", "home", "cache", "workspace"} {
		if err = os.MkdirAll(filepath.Join(dir, name), 0700); err != nil {
			return err
		}
	}
	if err = ensureRuntimeAuth(filepath.Join(dir, "runtime.auth")); err != nil {
		return err
	}
	if err = writeRuntimeEnvFiles(dir, s.Environment, secrets, orgSecrets); err != nil {
		return err
	}
	outputs := map[string]any{"hermes." + environment + ".yaml": Config(s), "compose." + environment + ".yaml": Compose(s, root, dir)}
	// Generated files are reproducible; secrets and agent memories are never overwritten.
	for name, v := range outputs {
		b, err := yaml.Marshal(v)
		if err != nil {
			return err
		}
		if err = atomic(filepath.Join(dir, "generated", name), b); err != nil {
			return err
		}
		// One-release compatibility for callers that still look beside settings.yaml.
		if err = atomic(filepath.Join(dir, name), b); err != nil {
			return err
		}
	}
	soul, err := os.ReadFile(filepath.Join(root, "config", "SOUL.md"))
	if err != nil {
		return err
	}
	soulPath := filepath.Join(dir, "SOUL.md")
	if _, err = os.Stat(soulPath); os.IsNotExist(err) {
		if err = atomic(soulPath, soul); err != nil {
			return err
		}
	}
	for _, p := range []string{"hermes." + environment + ".yaml", "SOUL.md"} {
		if err = os.Chmod(filepath.Join(dir, p), 0644); err != nil {
			return err
		}
	}
	if err = os.Chmod(filepath.Join(dir, "archive"), 0755); err != nil {
		return err
	}
	return nil
}

func ensureRuntimeAuth(path string) error {
	info, err := os.Lstat(path)
	if err == nil {
		if !info.Mode().IsRegular() {
			return fmt.Errorf("runtime auth must be a regular file")
		}
		return nil
	}
	if !os.IsNotExist(err) {
		return err
	}
	value := make([]byte, 32)
	if _, err = rand.Read(value); err != nil {
		return err
	}
	content := []byte("HUB_RUNTIME_AUTH=" + hex.EncodeToString(value) + "\n")
	f, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0600)
	if err != nil {
		if os.IsExist(err) {
			return ensureRuntimeAuth(path)
		}
		return err
	}
	if _, err = f.Write(content); err != nil {
		_ = f.Close()
		_ = os.Remove(path)
		return err
	}
	if err = f.Sync(); err != nil {
		_ = f.Close()
		_ = os.Remove(path)
		return err
	}
	return f.Close()
}

func writeRuntimeEnvFiles(dir, environment string, user, organization map[string]string) error {
	merged := map[string]string{}
	for key, value := range organization {
		merged[key] = value
	}
	for key, value := range user {
		if _, exists := merged[key]; exists {
			return fmt.Errorf("secret key duplicated between organization and user scope: %s", key)
		}
		merged[key] = value
	}
	runtime := map[string]string{}
	for key, value := range merged {
		if key != "TELEGRAM_BOT_TOKEN" && key != "TELEGRAM_ALLOWED_USERS" {
			runtime[key] = value
		}
	}
	gateway := map[string]string{}
	for _, key := range []string{"TELEGRAM_BOT_TOKEN", "TELEGRAM_ALLOWED_USERS"} {
		if value := user[key]; value != "" {
			gateway[key] = value
		}
	}
	if err := writeEnvFile(filepath.Join(dir, "runtime."+environment+".env"), runtime); err != nil {
		return err
	}
	return writeEnvFile(filepath.Join(dir, "communication."+environment+".env"), gateway)
}

func writeEnvFile(path string, values map[string]string) error {
	keys := make([]string, 0, len(values))
	for key := range values {
		keys = append(keys, key)
	}
	slices.Sort(keys)
	var body strings.Builder
	for _, key := range keys {
		body.WriteString(key)
		body.WriteByte('=')
		body.WriteString(values[key])
		body.WriteByte('\n')
	}
	return atomic(path, []byte(body.String()))
}

func atomic(path string, b []byte) error {
	f, err := os.CreateTemp(filepath.Dir(path), ".render-*")
	if err != nil {
		return err
	}
	name := f.Name()
	defer os.Remove(name)
	if err = f.Chmod(0600); err != nil {
		_ = f.Close()
		return err
	}
	if _, err = f.Write(b); err != nil {
		_ = f.Close()
		return err
	}
	if err = f.Sync(); err != nil {
		_ = f.Close()
		return err
	}
	if err = f.Close(); err != nil {
		return err
	}
	return os.Rename(name, path)
}
