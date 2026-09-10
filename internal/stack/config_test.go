package stack

import (
	"gopkg.in/yaml.v3"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

func TestInitNeverOverwrites(t *testing.T) {
	d := t.TempDir()
	if err := Init(d, "artem"); err != nil {
		t.Fatal(err)
	}
	p := filepath.Join(d, "secrets.prod.env")
	before, _ := os.ReadFile(p)
	if err := Init(d, "artem"); err == nil {
		t.Fatal("overwrote deployment")
	}
	after, _ := os.ReadFile(p)
	if string(before) != string(after) {
		t.Fatal("secrets changed")
	}
	secrets, err := ReadSecrets(p)
	if err != nil || secrets["OPENAI_API_KEY"] != "" {
		t.Fatal(err)
	}
	s, err := Read(filepath.Join(d, "settings.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	if len(Doctor(s, secrets)) != 3 {
		t.Fatal("missing model credentials not reported")
	}
}
func TestValidation(t *testing.T) {
	s := Settings{Schema: 1, Environment: "prod", User: "me", Timezone: "UTC", BrowserPort: 6080, OAuthPort: 8000}
	for _, modify := range []func(*Settings){func(s *Settings) { s.User = "../other" }, func(s *Settings) { s.Features = []string{"telegram_write"} }, func(s *Settings) { s.Features = []string{"google_write"} }, func(s *Settings) { s.Features = []string{"missing"} }, func(s *Settings) { s.ModelURL = "http://" + "user:secret" + "@host/v1" }, func(s *Settings) { s.Timezone = "invalid/zone" }, func(s *Settings) { s.BrowserPort = 8000 }} {
		copy := s
		modify(&copy)
		if copy.Validate() == nil {
			t.Fatal("invalid config accepted")
		}
	}
	s.Features = []string{"telegram"}
	secrets := map[string]string{"OPENAI_API_KEY": "x", "TELEGRAM_BOT_TOKEN": "x", "TELEGRAM_ALLOWED_USERS": "*"}
	if !strings.Contains(strings.Join(Doctor(s, secrets), " "), "numeric owner") {
		t.Fatal("wildcard owner accepted")
	}
}
func TestRenderAllFeatures(t *testing.T) {
	d := t.TempDir()
	root := t.TempDir()
	_ = os.Mkdir(filepath.Join(root, "config"), 0700)
	_ = os.WriteFile(filepath.Join(root, "config/SOUL.md"), []byte("original"), 0600)
	if err := Init(d, "me"); err != nil {
		t.Fatal(err)
	}
	s, _ := Read(filepath.Join(d, "settings.yaml"))
	s.Model = "test"
	s.ModelURL = "http://host.docker.internal:8317/v1"
	s.GoogleEmail = "me@example.org"
	s.DesktopURL = "http://host.docker.internal:8765/mcp"
	s.DraftsURL = "http://host.docker.internal:8766/mcp"
	for _, f := range Features {
		s.Features = append(s.Features, f.Name)
	}
	s.Features = nil
	for _, f := range Features {
		s.Features = append(s.Features, f.Name)
	}
	b, _ := yaml.Marshal(s)
	_ = os.WriteFile(filepath.Join(d, "settings.yaml"), b, 0600)
	if err := Render(d, root); err != nil {
		t.Fatal(err)
	}
	cfg := Config(s)
	servers := cfg["mcp_servers"].(M)
	if len(servers) != 9 {
		t.Fatalf("servers: %v", servers)
	}
	telegram := servers["telegram_user"].(M)["env"].(M)
	if !strings.HasPrefix(telegram["TELEGRAM_EXPOSED_TOOLS"].(string), "read-only+") {
		t.Fatal("write optin missing")
	}
	s.Features = []string{"telegram_user"}
	telegram = Config(s)["mcp_servers"].(M)["telegram_user"].(M)["env"].(M)
	if telegram["TELEGRAM_EXPOSED_TOOLS"] != "read-only" {
		t.Fatal("unsafe default")
	}
	s.Features = []string{"google"}
	googleArgs := Config(s)["mcp_servers"].(M)["google"].(M)["args"].([]string)
	if !slices.Contains(googleArgs, "--read-only") {
		t.Fatal("Google writes enabled by default")
	}
	s.Features = []string{"google", "google_write"}
	googleArgs = Config(s)["mcp_servers"].(M)["google"].(M)["args"].([]string)
	if slices.Contains(googleArgs, "--read-only") {
		t.Fatal("Google write opt-in ignored")
	}
	s.Features = []string{"gitlab"}
	s.GitLabHost = "gitlab.example.com"
	composeEnv := Compose(s, "/source", "/space")["services"].(M)["hermes-runtime"].(M)["environment"].(M)
	if composeEnv["GITLAB_HOST"] != "gitlab.example.com" {
		t.Fatal("GitLab host missing")
	}
	s.Features = []string{"atlassian"}
	atlassian := Config(s)["mcp_servers"].(M)["atlassian"].(M)
	if atlassian["auth"] != nil || atlassian["headers"].(M)["Authorization"] != "Basic ${ATLASSIAN_BASIC_AUTH}" {
		t.Fatal("Atlassian PAT config missing")
	}
	soul := filepath.Join(d, "SOUL.md")
	_ = os.WriteFile(soul, []byte("owner changes"), 0600)
	if err := Render(d, root); err != nil {
		t.Fatal(err)
	}
	got, _ := os.ReadFile(soul)
	if string(got) != "owner changes" {
		t.Fatal("overwrote memory")
	}
	compose, _ := os.ReadFile(filepath.Join(d, "generated", "compose.prod.yaml"))
	if strings.Contains(string(compose), "127.0.0.1:") || strings.Contains(string(compose), "docker.sock") {
		t.Fatal("network or mount boundary")
	}
}

func TestTelegramGatewayAndPersonalMCPAreIndependent(t *testing.T) {
	s := Settings{Schema: 1, Environment: "prod", User: "me", Timezone: "UTC", BrowserPort: 6080, OAuthPort: 8000}
	s.Features = []string{"telegram"}
	config := Config(s)
	if _, ok := config["mcp_servers"].(M)["telegram_user"]; ok {
		t.Fatal("bot gateway enabled personal Telegram MCP")
	}
	if Compose(s, "/source", "/space")["services"].(M)["hermes-runtime"].(M)["command"].([]string)[0] != "serve" {
		t.Fatal("bot gateway did not select gateway runtime")
	}
	s.Features = []string{"telegram_user"}
	config = Config(s)
	if _, ok := config["mcp_servers"].(M)["telegram_user"]; !ok {
		t.Fatal("personal Telegram MCP missing")
	}
	if Compose(s, "/source", "/space")["services"].(M)["hermes-runtime"].(M)["command"].([]string)[0] != "idle" {
		t.Fatal("personal Telegram MCP selected bot gateway runtime")
	}
}

func TestSelfEnvKeysIncludeCatalogConnectors(t *testing.T) {
	s := Settings{Features: []string{"telegram", "gitlab", "atlassian"}, MCP: map[string]MCPServer{"custom": {URL: "https://example.invalid/mcp", Headers: map[string]string{"Authorization": "Bearer ${CUSTOM_TOKEN}"}}}}
	keys := strings.Join(selfEnvKeys(s), ",")
	if !strings.Contains(keys, "OPENAI_API_KEY") || !strings.Contains(keys, "FIRECRAWL_API_KEY") || strings.Contains(keys, "TELEGRAM_BOT_TOKEN") || strings.Contains(keys, "TELEGRAM_ALLOWED_USERS") || !strings.Contains(keys, "GITLAB_TOKEN") || !strings.Contains(keys, "ATLASSIAN_EMAIL") || !strings.Contains(keys, "ATLASSIAN_API_TOKEN") || !strings.Contains(keys, "CUSTOM_TOKEN") || !strings.Contains(keys, "TELEGRAM_API_ID") {
		t.Fatalf("unexpected self-env keys: %s", keys)
	}
}

func TestServiceCatalogConfig(t *testing.T) {
	gitlab, ok := ServiceInfoByName("gitlab")
	if !ok || !gitlab.SelfService || len(gitlab.Requires) != 1 || gitlab.Requires[0] != "GITLAB_TOKEN" {
		t.Fatal(gitlab, ok)
	}
	server, config, ok, err := ServiceMCPConfig("atlassian")
	if err != nil || !ok || server != "atlassian" || config["headers"].(M)["Authorization"] != "Basic ${ATLASSIAN_BASIC_AUTH}" {
		t.Fatal(server, config, ok, err)
	}
	if _, _, _, err := ServiceMCPConfig("browser"); err == nil {
		t.Fatal("host-managed service accepted")
	}
	for _, name := range []string{"google", "google_write", "github", "slack", "telegram_user", "telegram_write", "hh", "gitlab"} {
		if _, _, _, err := ServiceMCPConfig(name); err != nil {
			t.Fatal(name, err)
		}
	}
	if _, _, _, err := ServiceMCPConfig("unknown"); err == nil {
		t.Fatal("unknown service accepted")
	}
	if _, ok := ServiceInfoByName("unknown"); ok {
		t.Fatal("unknown service found")
	}
}

func TestSecretParsing(t *testing.T) {
	for _, content := range []string{"TOKEN='quoted'\n", "TOKEN=a\nTOKEN=b\n", "BAD NAME=a\n"} {
		p := filepath.Join(t.TempDir(), "env")
		_ = os.WriteFile(p, []byte(content), 0600)
		if _, err := ReadSecrets(p); err == nil {
			t.Fatalf("accepted %q", content)
		}
	}
	p := filepath.Join(t.TempDir(), "env")
	_ = os.WriteFile(p, []byte("TOKEN=$literal#stillliteral\n"), 0600)
	s, err := ReadSecrets(p)
	if err != nil || s["TOKEN"] != "$literal#stillliteral" {
		t.Fatal(s, err)
	}
}
