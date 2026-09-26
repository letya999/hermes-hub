package stack

import (
	"gopkg.in/yaml.v3"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestIndependentSpacesAndExternalMCP(t *testing.T) {
	root := t.TempDir()
	var configs []M
	for _, owner := range []string{"one", "two"} {
		for _, environment := range []string{"dev", "prod"} {
			d := filepath.Join(root, owner+environment)
			if err := InitEnvironment(d, owner, environment); err != nil {
				t.Fatal(err)
			}
			s, _ := ReadEnvironment(d, environment)
			s.MCP = map[string]MCPServer{"external": {URL: "https://service.invalid/mcp", Headers: map[string]string{"Authorization": "Bearer ${EXTERNAL_TOKEN}"}, Auth: "oauth"}, "local": {Command: "node", Args: []string{"/workspace/custom.cjs"}, Env: map[string]string{"KEY": "${KEY}"}, Timeout: 42}}
			s.Hooks = map[string]any{"post_tool_call": []any{map[string]any{"command": "/workspace/hook.sh"}}}
			if err := s.Validate(); err != nil {
				t.Fatal(err)
			}
			c := Config(s)
			model := c["model"].(M)
			if model["api_key"] != "${OPENAI_API_KEY}" {
				t.Fatal("model key is not environment-backed")
			}
			servers := c["mcp_servers"].(M)
			// hub + two user servers + the two hub-owned browser profiles.
			if !c["memory"].(M)["memory_enabled"].(bool) || len(servers) != 5 {
				t.Fatal(c)
			}
			compose := Compose(s, "/source", d)
			configs = append(configs, compose)
			services := compose["services"].(M)
			if len(services) != 5 {
				t.Fatal("embedded service")
			}
			if _, ok := services["communication-hub"]; ok {
				t.Fatal("gateway started without a messaging feature")
			}
			// Exactly one build block must exist across services: all services
			// share the image, so repeated build sections make compose run the
			// same multi-stage build once per service.
			builds := 0
			for name, svc := range services {
				if b, ok := svc.(M)["build"]; ok {
					builds++
					if name != "toolhub" || b.(M)["target"] != environment {
						t.Fatal(name, b)
					}
				}
			}
			if builds != 1 {
				t.Fatal("expected exactly one build section, got", builds)
			}
			if strings.Contains(strings.Join(Doctor(s, map[string]string{}), " "), "EXTERNAL_TOKEN") == false {
				t.Fatal("missing connector secret not reported")
			}
			s.MCP["external"] = MCPServer{URL: "https://service.invalid/mcp"}
			_ = Config(s)
		}
	}
	seen := map[string]bool{}
	for _, c := range configs {
		n := c["name"].(string)
		if seen[n] {
			t.Fatal("shared identity")
		}
		seen[n] = true
	}
}
func TestInvalidMCPContracts(t *testing.T) {
	cases := []map[string]MCPServer{{"hub": {Command: "node"}}, {"bad name": {Command: "node"}}, {"x": {}}, {"x": {URL: "https://x", Command: "node"}}, {"x": {Command: "node", Timeout: 601}}, {"x": {URL: "https://x", Auth: "bad"}}, {"x": {URL: "https://user:pass@x"}}, {"x": {URL: "https://x", Args: []string{"a"}}}, {"x": {Command: "node", Headers: map[string]string{"x": "a"}}}, {"x": {URL: "https://x", Headers: map[string]string{"x": "a\nb"}}}}
	for _, c := range cases {
		if validateMCP(c) == nil {
			t.Fatal("accepted", c)
		}
	}
}
func TestConfigurationFailurePaths(t *testing.T) {
	d := t.TempDir()
	p := filepath.Join(d, "settings.yaml")
	for _, body := range []string{"schema: [bad", "unknown: true", "schema: 1\n---\nschema: 2"} {
		_ = os.WriteFile(p, []byte(body), 0600)
		if _, err := Read(p); err == nil {
			t.Fatal(body)
		}
	}
	for _, name := range []string{"google", "desktop", "drafts"} {
		s := Settings{Schema: 1, User: "me", Environment: "prod", Timezone: "UTC", BrowserPort: 6080, OAuthPort: 8000, Features: []string{name}}
		if s.Validate() == nil {
			t.Fatal(name)
		}
	}
	s := Settings{Schema: 1, User: "me", Environment: "unknown", Timezone: "UTC"}
	if s.Validate() == nil {
		t.Fatal("environment")
	}
	if _, err := ReadSecrets(filepath.Join(d, "missing")); err == nil {
		t.Fatal("missing")
	}
	if InitEnvironment(d, "me", "staging") == nil {
		t.Fatal("environment")
	}
	if Init(filepath.Join(p, "nested"), "me") == nil {
		t.Fatal("file used as dir")
	}
	if Render(d, "../..") == nil {
		t.Fatal("invalid settings")
	}
	fresh := t.TempDir()
	_ = Init(fresh, "me")
	if Render(fresh, "/no-such-project") == nil {
		t.Fatal("missing SOUL")
	}
	_ = os.Remove(filepath.Join(fresh, "secrets.prod.env"))
	if Render(fresh, "../..") == nil {
		t.Fatal("missing env")
	}
	if atomic(filepath.Join(d, "missing/file"), []byte("x")) == nil {
		t.Fatal("invalid parent")
	}
	target := filepath.Join(d, "dir")
	_ = os.Mkdir(target, 0700)
	if atomic(target, []byte("x")) == nil {
		t.Fatal("overwrite directory")
	}
	s = Settings{Schema: 1, User: "me", Environment: "prod", Timezone: "UTC", BrowserPort: 6080, OAuthPort: 8000}
	b, _ := yaml.Marshal(s)
	_ = os.WriteFile(p, b, 0600)
	if _, err := Read(p); err != nil {
		t.Fatal(err)
	}
}
