package main

import (
	"bufio"
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

func TestOfficialWorkspaceCLIConnectOAuthEmail(t *testing.T) {
	server := mcp.NewServer(&mcp.Implementation{Name: "Google-shaped", Version: "1.0.0"}, nil)
	server.AddTool(&mcp.Tool{Name: "gmail.get_thread", InputSchema: map[string]any{"type": "object"}}, func(context.Context, *mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		return &mcp.CallToolResult{Content: []mcp.Content{&mcp.TextContent{Text: "thread"}}}, nil
	})
	handler := mcp.NewStreamableHTTPHandler(func(*http.Request) *mcp.Server { return server }, &mcp.StreamableHTTPOptions{Stateless: true, JSONResponse: true, DisableLocalhostProtection: true})
	fixture := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/token":
			_, _ = io.WriteString(w, `{"access_token":"fixture-access","refresh_token":"fixture-refresh","scope":"openid email https://www.googleapis.com/auth/gmail.readonly","token_type":"Bearer"}`)
		case "/v1/userinfo":
			_, _ = io.WriteString(w, `{"sub":"subject","email":"fixture@example.invalid","email_verified":true}`)
		default:
			handler.ServeHTTP(w, r)
		}
	}))
	defer fixture.Close()
	previous := http.DefaultTransport
	http.DefaultTransport = connectorFixtureTransport{fixture.URL, previous}
	defer func() { http.DefaultTransport = previous }()
	client := filepath.Join(t.TempDir(), "client.txt")
	_ = os.WriteFile(client, []byte("CLIENT_ID=fixture-client\nCLIENT_SECRET=fixture-secret"), 0600)
	base := []string{"--user", "alice", "--account", "fixture@example.invalid", "--client-file", client, "--official-mcp", "--product", "gmail", "--toolhub-store", filepath.Join(t.TempDir(), "registry.json"), "--store", filepath.Join(t.TempDir(), "store.enc"), "--key-file", filepath.Join(t.TempDir(), "key"), "--connection", "gmail1"}
	reader, writer, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	oldOut := os.Stdout
	os.Stdout = writer
	defer func() { os.Stdout = oldOut; reader.Close(); writer.Close() }()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	done := make(chan error, 1)
	go func() { done <- run(ctx, append([]string{"connector", "connect"}, base...)) }()
	line, err := bufio.NewReader(reader).ReadString('\n')
	if err != nil {
		t.Fatal(err)
	}
	u, err := url.Parse(strings.TrimSpace(line))
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(u.Query().Get("scope"), "calendar") || !strings.Contains(u.Query().Get("scope"), "gmail.readonly") {
		t.Fatal("wrong scopes")
	}
	response := oauthCallbackGet(t, u.Query().Get("redirect_uri")+"?state="+url.QueryEscape(u.Query().Get("state"))+"&code=fixture-code")
	response.Body.Close()
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	os.Stdout = oldOut
	for _, override := range [][]string{{"--write"}, {"--product", "unknown"}, {"--callback-port", "-1"}, {"--provider", "slack"}} {
		args := append(append([]string{}, base...), override...)
		if err := run(ctx, append([]string{"connector", "connect"}, args...)); err == nil {
			t.Fatal("invalid official MCP grant")
		}
	}
}
