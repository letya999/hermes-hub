package stack

import (
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
func basicRemote(url, key string) M {
	return M{"url": url, "timeout": 90, "skip_preflight": true, "headers": M{"Authorization": "Basic ${" + key + "}"}}
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
		servers["atlassian"] = basicRemote("https://mcp.atlassian.com/v2/mcp", "ATLASSIAN_BASIC_AUTH")
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
	userHome := filepath.ToSlash(dir)
	organizationID := s.Organization
	if organizationID == "" {
		organizationID = "personal"
	}
	host := s.GitLabHost
	if host == "" {
		host = "gitlab.com"
	}
	runtimeEnv := M{"HUB_SHARED_GID": fmt.Sprint(max(0, os.Getgid())), "HUB_ORG_SCOPED": fmt.Sprint(s.OrgScoped()), "HUB_ORG_ACTIONS": strings.Join(s.OrgActions, ","), "HUB_SELF_ENV_KEYS": strings.Join(selfEnvKeys(s), ","), "HUB_PROTECTED_ENV_KEYS": organizationSecretKeys(s), "HERMES_HOME": "/scope/user/hermes", "HOME": "/scope/user/connections/home", "HUB_STATE": "/scope/user/connections", "HUB_WORKSPACE": "/scope/user/workspace", "HUB_RUNTIME_USER_ID": s.User, "HUB_RUNTIME_USER_HOME": "/scope/user", "HUB_USER_ID": s.User, "HUB_ORGANIZATION_ID": organizationID, "HUB_RUNTIME_ORGANIZATION_ID": organizationID, "HUB_RUNTIME_ORGANIZATION_HOME": "/scope/org", "HUB_RUNTIME_CONFIG": "/scope/user/generated/hermes." + s.Environment + ".yaml", "HUB_FEATURES": strings.Join(s.Features, ","), "HUB_SCOPE_ID": "user:" + s.User, "TZ": s.Timezone, "HUB_BROWSER": fmt.Sprint(s.Has("browser")), "HUB_MEET": fmt.Sprint(s.Has("meet")), "GITLAB_HOST": host, "GOOGLE_EMAIL": s.GoogleEmail, "GOOGLE_OAUTH_REDIRECT_URI": fmt.Sprintf("http://localhost:%d/oauth2callback", s.OAuthPort), "PYTHONDONTWRITEBYTECODE": "1", "XDG_CACHE_HOME": "/scope/user/connections/cache", "HUB_RUNTIME_BIND": ":9090"}
	if s.Has("telegram") {
		runtimeEnv["HUB_RUNTIME_TOKEN"] = "${HUB_RUNTIME_TOKEN}"
	}
	runtimeVolumes := []any{M{"type": "bind", "source": userHome, "target": "/scope/user"}, M{"type": "bind", "source": filepath.ToSlash(filepath.Join(dir, "archive")), "target": "/scope/user/archive", "read_only": true}}
	if s.OrgScoped() {
		runtimeVolumes = append(runtimeVolumes, M{"type": "bind", "source": filepath.ToSlash(s.OrganizationDir), "target": "/scope/org", "read_only": true})
	}
	runtimeCommand := "idle"
	if s.Has("telegram") {
		runtimeCommand = "serve"
	}
	runtime := M{"build": M{"context": filepath.ToSlash(projectRoot), "dockerfile": "docker/Dockerfile", "target": s.Environment}, "image": "hermes-hub:0.2.0-" + s.Environment, "init": true, "restart": "unless-stopped", "user": fmt.Sprintf("10001:%d", max(0, os.Getgid())), "read_only": true, "cap_drop": []string{"ALL"}, "security_opt": []string{"no-new-privileges:true"}, "shm_size": "1gb", "tmpfs": []string{"/tmp:uid=10001,gid=10001,mode=1777"}, "env_file": []any{M{"path": filepath.ToSlash(filepath.Join(dir, "generated", "runtime."+s.Environment+".env")), "format": "raw"}}, "environment": runtimeEnv, "volumes": runtimeVolumes, "ports": []string{}, "extra_hosts": []string{"host.docker.internal:host-gateway"}, "command": []string{runtimeCommand}, "healthcheck": M{"test": []string{"CMD", "hub-runtime", "health"}, "interval": "30s", "timeout": "5s", "retries": 3}}
	runtime["networks"] = []string{"private"}
	if s.Environment == "dev" {
		runtime["restart"] = "no"
		runtime["read_only"] = false
		for _, name := range []string{"cmd", "internal", "docker", "config", "docs", "specs", ".work", ".github", "go.mod", "go.sum", "justfile", "AGENTS.md", "README.md", "SETUP.md", "START_HERE.ru.md", "LICENSE", "SECURITY.md", "CONTRIBUTING.md"} {
			runtimeVolumes = append(runtimeVolumes, M{"type": "bind", "source": filepath.ToSlash(filepath.Join(projectRoot, name)), "target": "/src/" + name})
		}
		runtime["volumes"] = runtimeVolumes
		runtime["environment"].(M)["GOCACHE"] = "/scope/user/connections/go-build"
		runtime["environment"].(M)["GOMODCACHE"] = "/scope/user/connections/go-mod"
	}
	services := M{"hermes-runtime": runtime}
	volumes := M{"communication-hub-data": M{}}
	if s.Has("telegram") {
		communication := M{"image": "hermes-hub:0.2.0-" + s.Environment, "depends_on": M{"hermes-runtime": M{"condition": "service_healthy"}}, "restart": "unless-stopped", "read_only": true, "cap_drop": []string{"ALL"}, "security_opt": []string{"no-new-privileges:true"}, "tmpfs": []string{"/tmp:uid=10001,gid=10001,mode=1777"}, "entrypoint": []string{"/usr/local/bin/communication-hub"}, "env_file": []any{M{"path": filepath.ToSlash(filepath.Join(dir, "generated", "communication."+s.Environment+".env")), "format": "raw"}}, "environment": M{"HUB_USER_ID": s.User, "HUB_ORGANIZATION_ID": organizationID, "HUB_FEATURES": strings.Join(s.Features, ","), "HUB_COMMUNICATION_SPOOL": "/gateway-data", "HUB_RUNTIME_URL": "http://hermes-runtime:9090"}, "volumes": []any{M{"type": "volume", "source": "communication-hub-data", "target": "/gateway-data"}}, "networks": []string{"private"}}
		services["communication-hub"] = communication
	}
	return M{"name": "hermes-hub-" + s.User + "-" + s.Environment, "services": services, "volumes": volumes, "networks": M{"private": M{"internal": true}}}
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
	if s.OrgScoped() {
		if _, err = ReadOrganizationSecrets(s, s.Environment); err != nil {
			return err
		}
	}
	orgSecrets, err := ReadOrganizationSecrets(s, s.Environment)
	if err != nil {
		return err
	}
	for key := range orgSecrets {
		if _, duplicate := secrets[key]; duplicate {
			return fmt.Errorf("secret key duplicated between organization and user scope: %s", key)
		}
	}
	if err := os.MkdirAll(filepath.Join(dir, "generated"), 0700); err != nil {
		return err
	}
	if err := writeRuntimeEnv(filepath.Join(dir, "generated", "runtime."+environment+".env"), secrets, orgSecrets); err != nil {
		return err
	}
	if err := writeGatewayEnv(filepath.Join(dir, "generated", "communication."+environment+".env"), secrets); err != nil {
		return err
	}
	if err = os.MkdirAll(filepath.Join(dir, "archive"), 0755); err != nil {
		return err
	}
	outputs := map[string]any{"hermes." + environment + ".yaml": Config(s), "compose." + environment + ".yaml": Compose(s, root, dir)}
	_ = secrets
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

func writeRuntimeEnv(path string, user, organization map[string]string) error {
	values := map[string]string{}
	for key, value := range organization {
		values[key] = value
	}
	for key, value := range user {
		if key != "TELEGRAM_BOT_TOKEN" && key != "TELEGRAM_ALLOWED_USERS" {
			values[key] = value
		}
	}
	keys := make([]string, 0, len(values))
	for key := range values {
		keys = append(keys, key)
	}
	slices.Sort(keys)
	body := strings.Builder{}
	for _, key := range keys {
		if key == "TELEGRAM_BOT_TOKEN" || key == "TELEGRAM_ALLOWED_USERS" {
			continue
		}
		body.WriteString(key)
		body.WriteByte('=')
		body.WriteString(values[key])
		body.WriteByte('\n')
	}
	return atomic(path, []byte(body.String()))
}

func writeGatewayEnv(path string, secrets map[string]string) error {
	values := map[string]string{}
	for _, key := range []string{"TELEGRAM_BOT_TOKEN", "TELEGRAM_ALLOWED_USERS", "HUB_RUNTIME_TOKEN"} {
		if value := secrets[key]; value != "" {
			values[key] = value
		}
	}
	keys := make([]string, 0, len(values))
	for key := range values {
		keys = append(keys, key)
	}
	slices.Sort(keys)
	body := strings.Builder{}
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
