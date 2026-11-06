package stack

import (
	"github.com/letya999/hermes-hub/internal/toolhub/api"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// Two independent managed spaces resolve their own published revisions:
// user-declared connectors come from each space's tools passport, gated by
// the same user_mcp section, and the rendered configs stay distinct.
func TestIndependentSpacesAndExternalMCP(t *testing.T) {
	root := t.TempDir()
	var configs []M
	for _, owner := range []string{"one", "two"} {
		for _, environment := range []string{"dev", "prod"} {
			d := filepath.Join(root, owner+environment)
			if err := InitEnvironment(d, owner, environment); err != nil {
				t.Fatal(err)
			}
			docs := managedDocs(owner)
			docs["tools"] += "extended:\n  user_mcp: {via: mcp-raw}\n" +
				"connectors:\n" +
				"  external: {url: \"https://service.invalid/mcp\", headers: {Authorization: \"Bearer ${EXTERNAL_TOKEN}\"}, auth: oauth}\n" +
				"  local: {command: node, args: [/workspace/custom.cjs], env: {KEY: \"${KEY}\"}, timeout: 42}\n"
			publishManagedDocs(t, d, owner, "r-0000000000000001", docs)
			seedGovernance(t, d, func(doc *api.Governance) {
				activeToolGrant(t, doc, owner, api.SectionUserMCP, time.Hour)
			})
			s, err := ReadEnvironment(d, environment)
			if err != nil {
				t.Fatal(err)
			}
			if _, ok := s.MCP["external"]; !ok || s.MCP["local"].Timeout != 42 {
				t.Fatalf("connectors not projected: %+v", s.MCP)
			}
			c := Config(s)
			model := c["model"].(M)
			if model["api_key"] != "${OPENAI_API_KEY}" {
				t.Fatal("model key is not environment-backed")
			}
			compose := Compose(s, "/source", d)
			configs = append(configs, compose)
			services := compose["services"].(M)
			if _, ok := services["communication-hub"]; ok {
				t.Fatal("gateway started without a messaging feature")
			}
			if strings.Contains(strings.Join(Doctor(s, map[string]string{}), " "), "EXTERNAL_TOKEN") == false {
				t.Fatal("missing connector secret not reported")
			}
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
		if _, err := ReadEffective(p, "prod"); err == nil {
			t.Fatal(body)
		}
	}
	for _, name := range []string{"google", "desktop", "drafts"} {
		s := Settings{Schema: 1, User: "me", Environment: "prod", Timezone: "UTC", BrowserPort: 6080, OAuthPort: 8000, Tools: testTools(name), Ingress: testIngress(name)}
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
	// A well-formed published revision still reads.
	fresh = t.TempDir()
	publishManagedRevision(t, fresh, "me", "r-0000000000000001")
	if _, err := ReadEffective(fresh, "prod"); err != nil {
		t.Fatal(err)
	}
}
