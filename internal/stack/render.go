package stack

import (
	"fmt"
	"os"
	"path/filepath"

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
	servers := M{}
	if s.Has("workspace") || s.Has("hh") {
		e := env("HH_TOKEN", "HH_USER_AGENT")
		e["HUB_HH_ENABLED"] = fmt.Sprint(s.Has("hh"))
		servers["hub"] = stdio("/usr/local/bin/hubctl", []string{"tools", "--workspace", "/workspace", "--archive", "/archive"}, e)
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
		e["USER_GOOGLE_EMAIL"] = s.GoogleEmail
		e["WORKSPACE_MCP_CREDENTIALS_DIR"] = "/state/google"
		e["WORKSPACE_MCP_PORT"] = "8000"
		e["WORKSPACE_MCP_HOST"] = "0.0.0.0"
		e["WORKSPACE_MCP_BASE_URI"] = "http://0.0.0.0"
		e["GOOGLE_OAUTH_REDIRECT_URI"] = fmt.Sprintf("http://localhost:%d/oauth2callback", s.OAuthPort)
		e["WORKSPACE_MCP_MAX_FILE_BYTES"] = "16777216"
		servers["google"] = stdio("/opt/google/.venv/bin/python", []string{"/opt/google/main.py", "--transport", "stdio", "--single-user", "--tool-tier", "extended", "--tools", "gmail", "drive", "calendar", "docs", "sheets", "slides", "tasks"}, e)
	}
	if s.Has("browser") {
		servers["browser"] = stdio("node", []string{"/opt/browser/node_modules/@playwright/mcp/cli.js", "--cdp-endpoint", "http://127.0.0.1:9222", "--caps", "vision,pdf", "--output-dir", "/workspace/browser"}, nil)
	}
	if s.Has("github") {
		servers["github"] = remote("https://api.githubcopilot.com/mcp/", "GITHUB_TOKEN")
	}
	if s.Has("slack") {
		servers["slack"] = stdio("/usr/local/bin/slack-mcp-server", []string{"--transport", "stdio"}, env("SLACK_MCP_XOXP_TOKEN", "SLACK_MCP_ADD_MESSAGE_TOOL"))
	}
	if s.Has("atlassian") {
		m := remote("https://mcp.atlassian.com/v2/mcp", "")
		m["auth"] = "oauth"
		servers["atlassian"] = m
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
	return M{"model": M{"default": s.Model, "provider": "custom", "base_url": s.ModelURL}, "terminal": M{"backend": "local", "cwd": "/workspace", "timeout": 120}, "platform_toolsets": M{"cli": toolsets, "telegram": toolsets}, "mcp_servers": servers, "stt": M{"enabled": s.Has("transcription"), "provider": "local", "language": "", "local": M{"model": "small"}}, "timezone": s.Timezone, "hooks": s.Hooks, "memory": M{"memory_enabled": s.Memory, "user_profile_enabled": s.Memory}}
}
func Compose(s Settings, projectRoot, dir string) M {
	volumes := []any{
		M{"type": "volume", "source": "state", "target": "/state"},
		M{"type": "volume", "source": "workspace", "target": "/workspace"},
		M{"type": "bind", "source": filepath.ToSlash(filepath.Join(dir, "archive")), "target": "/archive", "read_only": true},
		M{"type": "bind", "source": filepath.ToSlash(filepath.Join(dir, "hermes."+s.Environment+".yaml")), "target": "/config/config.yaml", "read_only": true},
		M{"type": "bind", "source": filepath.ToSlash(filepath.Join(dir, "SOUL.md")), "target": "/config/SOUL.md", "read_only": true},
	}
	ports := []string{}
	if s.Has("browser") || s.Has("meet") {
		ports = append(ports, fmt.Sprintf("127.0.0.1:%d:6080", s.BrowserPort))
	}
	if s.Has("google") {
		ports = append(ports, fmt.Sprintf("127.0.0.1:%d:8000", s.OAuthPort))
	}
	mode := "idle"
	if s.Has("telegram") {
		mode = "gateway"
	}
	agent := M{"build": M{"context": filepath.ToSlash(projectRoot), "dockerfile": "docker/Dockerfile", "target": s.Environment}, "image": "hermes-hub:0.2.0-" + s.Environment, "init": true, "restart": "unless-stopped", "user": fmt.Sprintf("10001:%d", max(0, os.Getgid())), "read_only": true, "cap_drop": []string{"ALL"}, "security_opt": []string{"no-new-privileges:true"}, "shm_size": "1gb", "tmpfs": []string{"/tmp:uid=10001,gid=10001,mode=1777"}, "env_file": []any{M{"path": filepath.ToSlash(filepath.Join(dir, "secrets."+s.Environment+".env")), "format": "raw"}}, "environment": M{"HUB_SHARED_GID": fmt.Sprint(max(0, os.Getgid())), "HERMES_HOME": "/state/hermes", "HOME": "/state/home", "TZ": s.Timezone, "HUB_BROWSER": fmt.Sprint(s.Has("browser")), "HUB_MEET": fmt.Sprint(s.Has("meet")), "PYTHONDONTWRITEBYTECODE": "1", "XDG_CACHE_HOME": "/state/cache"}, "volumes": volumes, "ports": ports, "extra_hosts": []string{"host.docker.internal:host-gateway"}, "command": []string{mode}, "healthcheck": M{"test": []string{"CMD", "hub-runtime", "health"}, "interval": "30s", "timeout": "5s", "retries": 3}}
	if s.Environment == "dev" {
		agent["restart"] = "no"
		agent["read_only"] = false
		for _, name := range []string{"cmd", "internal", "docker", "config", "docs", "specs", ".work", ".github", "go.mod", "go.sum", "justfile", "AGENTS.md", "README.md", "SETUP.md", "START_HERE.ru.md", "LICENSE", "SECURITY.md", "CONTRIBUTING.md"} {
			volumes = append(volumes, M{"type": "bind", "source": filepath.ToSlash(filepath.Join(projectRoot, name)), "target": "/src/" + name})
		}
		agent["volumes"] = volumes
		agent["environment"].(M)["GOCACHE"] = "/state/go-build"
		agent["environment"].(M)["GOMODCACHE"] = "/state/go-mod"
	}
	services := M{"agent": agent}
	return M{"name": "hermes-hub-" + s.User + "-" + s.Environment, "services": services, "volumes": M{"state": M{}, "workspace": M{}}}
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
