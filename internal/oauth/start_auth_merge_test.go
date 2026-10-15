package oauth

import (
	"net/url"
	"testing"
)

// StartAuth builds the authorize URL with PKCE, honors optional parameters,
// routes Slack scopes to user_scope, and accepts a redirect that only adds
// an onboarding_id query to an allowed redirect.
func TestStartAuthBranches(t *testing.T) {
	fixture := NewFixture()
	defer fixture.Close()
	broker := testBroker(t, fixture, testSecrets(t))

	start, err := broker.StartAuth(AuthRequest{
		Principal: "alice", Context: "alice", Connection: "work", Provider: "fixture",
		Redirect: "http://127.0.0.1/callback", Scopes: []string{"files:read"},
		AccessType: "offline", Prompt: "consent", IncludeGrantedScopes: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	q, _ := url.Parse(start.AuthorizeURL)
	query := q.Query()
	if query.Get("code_challenge") == "" || query.Get("access_type") != "offline" ||
		query.Get("prompt") != "consent" || query.Get("include_granted_scopes") != "true" ||
		query.Get("scope") != "files:read" {
		t.Fatalf("authorize url: %s", start.AuthorizeURL)
	}

	// Slack uses user_scope so the flow can never mint a bot token.
	slack, err := broker.StartAuth(AuthRequest{
		Principal: "alice", Context: "alice", Connection: "slack-work", Provider: "slack",
		Redirect: "http://127.0.0.1/callback", Scopes: []string{"channels:read"},
	})
	if err != nil {
		t.Fatal(err)
	}
	sq, _ := url.Parse(slack.AuthorizeURL)
	if sq.Query().Get("user_scope") != "channels:read" || sq.Query().Get("scope") != "" {
		t.Fatalf("slack authorize url: %s", slack.AuthorizeURL)
	}

	// Redirect allowlist: an added onboarding_id query is accepted, other
	// query or host changes are denied.
	ok, err := broker.StartAuth(AuthRequest{
		Principal: "alice", Context: "alice", Connection: "work", Provider: "fixture",
		Redirect: "http://127.0.0.1/callback?onboarding_id=abc123",
	})
	if err != nil || ok.State == "" {
		t.Fatalf("onboarding redirect: %v", err)
	}
	if _, err := broker.StartAuth(AuthRequest{
		Principal: "alice", Context: "alice", Connection: "work", Provider: "fixture",
		Redirect: "http://127.0.0.1/callback?other=1",
	}); err == nil {
		t.Fatal("foreign query redirect accepted")
	}
	if _, err := broker.StartAuth(AuthRequest{
		Principal: "alice", Context: "alice", Connection: "work", Provider: "fixture",
		Redirect: "https://evil.example/callback",
	}); err == nil {
		t.Fatal("foreign host redirect accepted")
	}
	if _, err := broker.StartAuth(AuthRequest{
		Principal: "alice", Context: "alice", Connection: "work", Provider: "missing",
		Redirect: "http://127.0.0.1/callback",
	}); err == nil {
		t.Fatal("unknown provider accepted")
	}
	if start.State == "" {
		t.Fatal("empty state")
	}
}
