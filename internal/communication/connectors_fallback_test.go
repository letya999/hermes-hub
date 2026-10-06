package communication

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/letya999/hermes-hub/internal/identity"
)

// /connections falls back to ToolHub's /v1/connectors when the local
// registry file is unavailable (broker-approve keeps the store inside the
// ToolHub container). The sibling token pins the caller's principal; without
// one the control bearer carries X-Hub-Principal.
func TestConnectionsFallsBackToToolHubHTTP(t *testing.T) {
	user := User{ID: "alice", Enabled: true, TelegramIDs: []int64{11}}
	var gotAuth, gotPrincipal string
	api := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/connectors" {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		gotAuth = r.Header.Get("Authorization")
		gotPrincipal = r.Header.Get("X-Hub-Principal")
		_ = json.NewEncoder(w).Encode(map[string]any{"connectors": []map[string]any{
			{"definition_id": "github-mcp", "transport": "container-mcp", "tools": []string{"github-mcp_1.0.0_get_me", "github-mcp_1.0.0_search_code"}},
		}})
	}))
	defer api.Close()
	g := &Gateway{config: Config{ToolHubURL: api.URL, RuntimeAuth: "supervisor-bearer", Supervised: true}, users: map[int64]User{11: user}, now: time.Now}
	got := g.connectionsCommand(user, 11)
	if gotAuth != "Bearer supervisor-bearer" || gotPrincipal != "alice" {
		t.Fatalf("toolhub call auth=%q principal=%q", gotAuth, gotPrincipal)
	}
	if !strings.Contains(got, "github-mcp") || !strings.Contains(got, "get_me") {
		t.Fatalf("toolhub inventory not rendered: %q", got)
	}

	// Unsupervised the same bearer is the owner's own token: ToolHub would
	// resolve the owner's envelope regardless of the header, so the call must
	// not be made at all for a sibling principal.
	g.config.Supervised = false
	if out := g.connectionsCommand(user, 11); !strings.Contains(out, "реестр недоступен") {
		t.Fatalf("unsupervised must not borrow the owner token: %q", out)
	}
	g.config.Supervised = true

	// A sibling token enrolled in the tokens file wins over the control
	// bearer and needs no header.
	tokensFile := filepath.Join(t.TempDir(), "toolhub-tokens.json")
	entries := map[string]identity.Envelope{"alice-runtime-token-000000000000": identity.TelegramEnvelope("alice", 11, "alice", "policy-1")}
	body, err := json.Marshal(entries)
	if err != nil || os.WriteFile(tokensFile, body, 0600) != nil {
		t.Fatal(err)
	}
	gotAuth, gotPrincipal = "", ""
	g.config.ControlTokensFile = tokensFile
	if out := g.connectionsCommand(user, 11); !strings.Contains(out, "github-mcp") {
		t.Fatalf("sibling-token fallback failed: %q", out)
	}
	if gotAuth != "Bearer alice-runtime-token-000000000000" || gotPrincipal != "" {
		t.Fatalf("sibling token should pin the principal: auth=%q principal=%q", gotAuth, gotPrincipal)
	}
}

// Projected tool names carry a hub-<definition>- prefix and a dedup hash;
// /connections renders them readable and collapses long inventories.
func TestConnectorLineCompactsProjectedNames(t *testing.T) {
	names := []string{
		"hub-github-mcp-server-add-issue-comment-9e3e990e",
		"hub-github-mcp-server-create-pull-request-679e6b49",
		"hub-github-mcp-server-get-me-0ec3302d",
		"hub-github-mcp-server-list-issues-7884a2f6",
		"hub-github-mcp-server-merge-pull-request-91da6585",
		"hub-github-mcp-server-search-code-5eea4643",
		"hub-github-mcp-server-search-issues-bfd1c434",
	}
	got := connectorLine("github-mcp-server", "container-mcp", names)
	if !strings.Contains(got, "github-mcp-server [container-mcp] — 7 инструментов") {
		t.Fatalf("line=%q", got)
	}
	if !strings.Contains(got, "add-issue-comment") || strings.Contains(got, "9e3e990e") || strings.Contains(got, "hub-github") {
		t.Fatalf("names not stripped: %q", got)
	}
	if strings.Contains(got, "search-issues") {
		t.Fatalf("long inventory must collapse after 3 examples: %q", got)
	}
	short := connectorLine("hub-agent-tools", "agent-tools", []string{"file_read", "file_write"})
	if !strings.Contains(short, "2 инструментов: file_read, file_write") {
		t.Fatalf("short line=%q", short)
	}
}

// Without ToolHubURL or any usable credential the command still reports the
// registry as unavailable instead of failing open.
func TestConnectionsWithoutToolHubStillReportsUnavailable(t *testing.T) {
	user := User{ID: "alice", Enabled: true, TelegramIDs: []int64{11}}
	g := &Gateway{config: Config{}, users: map[int64]User{11: user}, now: time.Now}
	if got := g.connectionsCommand(user, 11); !strings.Contains(got, "реестр недоступен") {
		t.Fatalf("missing fallback path must stay honest: %q", got)
	}
}
