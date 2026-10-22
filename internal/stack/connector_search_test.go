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
