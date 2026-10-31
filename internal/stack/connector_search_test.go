package stack

import (
	"strings"
	"testing"
)

func TestComposeEnablesGitHubConnectorSearch(t *testing.T) {
	s := Settings{Schema: 1, Environment: "dev", User: "alice", Timezone: "UTC", BrowserPort: 6080, OAuthPort: 8000, Tools: testTools("telegram"), Ingress: testIngress("telegram")}
	services := Compose(s, "/source", "/space")["services"].(M)
	env := services["toolhub"].(M)["environment"].(M)
	catalogs, _ := env["HUB_RECIPE_CATALOGS"].(string)
	if !strings.Contains(","+catalogs+",", ",github-search,") {
		t.Fatalf("connector search omits GitHub fallback: %q", catalogs)
	}
}

// ToolHub no longer resolves credential refs from its own self-env.
func TestComposeToolhubDoesNotCarrySelfEnvPolicy(t *testing.T) {
	s := Settings{Schema: 1, Environment: "dev", User: "alice", Timezone: "UTC", BrowserPort: 6080, OAuthPort: 8000, Tools: testTools("telegram"), Ingress: testIngress("telegram")}
	env := Compose(s, "/source", "/space")["services"].(M)["toolhub"].(M)["environment"].(M)
	if _, ok := env["HUB_SELF_ENV_KEYS"]; ok {
		t.Fatal("toolhub still carries the owner's self-env allowlist")
	}
	if _, ok := env["HUB_PROTECTED_ENV_KEYS"]; ok {
		t.Fatal("toolhub still carries the owner's protected-key list")
	}
}
