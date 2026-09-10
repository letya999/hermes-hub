package stack

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestPeerScopeHomesAndCollisionRules(t *testing.T) {
	root := t.TempDir()
	spaces := filepath.Join(root, "spaces")
	org := filepath.Join(spaces, "acme")
	user := filepath.Join(spaces, "artem")
	if err := InitOrganization(org, "acme", "artem"); err != nil {
		t.Fatal(err)
	}
	if err := InitEnvironmentWithOrganization(user, "artem", "prod", "acme"); err != nil {
		t.Fatal(err)
	}
	for _, check := range []struct {
		path string
		kind string
		id   string
	}{
		{org, "organization", "acme"},
		{user, "user", "artem"},
	} {
		body, err := os.ReadFile(filepath.Join(check.path, "scope.yaml"))
		if err != nil || !strings.Contains(string(body), "kind: "+check.kind) || !strings.Contains(string(body), "id: "+check.id) {
			t.Fatal(check, err, string(body))
		}
	}
	for _, name := range []string{"hermes/memories", "hermes/skills", "hermes/sessions", "connections", "workspace", "archive", "generated"} {
		if info, err := os.Stat(filepath.Join(user, name)); err != nil || !info.IsDir() {
			t.Fatal(name, err)
		}
	}
	if err := InitEnvironment(filepath.Join(spaces, "acme"), "acme", "prod"); err == nil {
		t.Fatal("user initialization crossed an organization scope")
	}
	if err := InitOrganization(filepath.Join(spaces, "artem"), "artem", "artem"); err == nil {
		t.Fatal("organization initialization crossed a user scope")
	}
	if err := InitEnvironment(spaces+string(filepath.Separator)+".."+string(filepath.Separator)+"escape", "escape", "prod"); err == nil {
		t.Fatal("traversal path accepted")
	}
}

func TestRenderedServiceBoundaries(t *testing.T) {
	s := Settings{Schema: 1, Environment: "prod", User: "artem", Organization: "acme", Features: []string{"workspace", "telegram"}, OrganizationDir: "/host/spaces/acme", OrganizationDocsDir: "/host/spaces/acme/docs", OrganizationSkillsDir: "/host/spaces/acme/hermes/skills", Timezone: "UTC", BrowserPort: 6080, OAuthPort: 8000}
	config := Config(s)
	if got := Config(s)["skills"].(M)["external_dirs"].([]string); len(got) != 1 || got[0] != "/org/hermes/skills" {
		t.Fatal(got)
	}
	services := Compose(s, "/source", "/host/spaces/artem")["services"].(M)
	if _, ok := services["communication-hub"]; !ok {
		t.Fatal("communication-hub missing")
	}
	runtimeService := services["hermes-runtime"].(M)
	communication := services["communication-hub"].(M)
	if len(communication["volumes"].([]any)) != 1 || len(runtimeService["volumes"].([]any)) == 0 {
		t.Fatal("unexpected volume boundary")
	}
	if _, ok := communication["environment"].(M)["TELEGRAM_BOT_TOKEN"]; ok {
		// The bot token is supplied by the gateway-only env file, never runtime env.
		t.Fatal("bot token should not be duplicated in communication environment map")
	}
	if _, ok := runtimeService["environment"].(M)["TELEGRAM_BOT_TOKEN"]; ok {
		t.Fatal("bot token leaked to runtime")
	}
	if strings.Contains(strings.Join(runtimeService["ports"].([]string), ","), ":8080") {
		t.Fatal("runtime port published")
	}
	if config["model"].(M)["api_key"] != "${OPENAI_API_KEY}" {
		t.Fatal("provider credential was embedded")
	}
}

func TestScopeValidationAndSingleDocument(t *testing.T) {
	for _, scope := range []Scope{
		{Kind: "unknown", ID: "alice"},
		{Kind: UserScope, ID: "../alice"},
		{Kind: UserScope, ID: "alice", Organization: "../acme"},
		{Kind: OrganizationScope, ID: "acme", Organization: "other"},
	} {
		if scope.Validate() == nil {
			t.Fatalf("invalid scope accepted: %+v", scope)
		}
	}
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "scope.yaml"), []byte("kind: user\nid: alice\n---\nkind: user\nid: bob\n"), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := ReadScope(dir); err == nil {
		t.Fatal("multiple YAML documents accepted")
	}
}
