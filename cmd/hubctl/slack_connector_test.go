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

	"github.com/letya999/hermes-hub/internal/audit"
	"github.com/letya999/hermes-hub/internal/toolhub"
)

func TestSlackConnectorCLIUserOAuthWriteReceiptRefreshRevoke(t *testing.T) {
	fixture := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/oauth.v2.access":
			id, secret, ok := r.BasicAuth()
			if !ok || id != "fixture-client" || secret != "fixture-secret" {
				t.Error("basic OAuth client missing")
			}
			_, _ = io.WriteString(w, `{"ok":true,"access_token":"fixture-bot-must-not-capture","token_type":"bot","team":{"id":"T1"},"authed_user":{"id":"U1","scope":"chat:write","access_token":"fixture-user","refresh_token":"fixture-refresh","token_type":"user"}}`)
		case "/api/auth.test":
			if r.Header.Get("Authorization") != "Bearer fixture-user" {
				t.Error("bot token injected")
			}
			_, _ = io.WriteString(w, `{"ok":true,"team_id":"T1","user_id":"U1"}`)
		case "/api/chat.postMessage":
			_, _ = io.WriteString(w, `{"ok":true,"channel":"D1","ts":"1234567890.000001"}`)
		case "/api/auth.revoke":
			_, _ = io.WriteString(w, `{"ok":true,"revoked":true}`)
		default:
			http.NotFound(w, r)
		}
	}))
	defer fixture.Close()
	oldTransport := http.DefaultTransport
	http.DefaultTransport = connectorFixtureTransport{fixture.URL, oldTransport}
	defer func() { http.DefaultTransport = oldTransport }()
	client := filepath.Join(t.TempDir(), "client.txt")
	if err := os.WriteFile(client, []byte("CLIENT_ID=fixture-client\nCLIENT_SECRET=fixture-secret"), 0600); err != nil {
		t.Fatal(err)
	}
	storePath := filepath.Join(t.TempDir(), "store.enc")
	base := []string{"--user", "alice", "--provider", "slack", "--toolhub-store", filepath.Join(t.TempDir(), "registry.json"), "--store", storePath, "--key-file", filepath.Join(t.TempDir(), "key"), "--connection", "slack1"}
	reader, writer, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	oldOut := os.Stdout
	os.Stdout = writer
	defer func() { os.Stdout = oldOut; reader.Close(); writer.Close() }()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	finished := make(chan error, 1)
	go func() {
		finished <- run(ctx, append([]string{"connector", "connect"}, append(base, "--write", "--workspace", "T1", "--account", "U1", "--client-file", client)...))
	}()
	line, err := bufio.NewReader(reader).ReadString('\n')
	if err != nil {
		t.Fatal(err)
	}
	u, err := url.Parse(strings.TrimSpace(line))
	if err != nil {
		t.Fatal(err)
	}
	if u.Query().Get("user_scope") != "chat:write" || u.Query().Get("scope") != "" {
		t.Error("bot scope requested")
	}
	resp := oauthCallbackGet(t, u.Query().Get("redirect_uri")+"?state="+url.QueryEscape(u.Query().Get("state"))+"&code=fixture-code")
	resp.Body.Close()
	if err := <-finished; err != nil {
		t.Fatal(err)
	}
	os.Stdout = oldOut
	input := filepath.Join(t.TempDir(), "arguments.json")
	if err := os.WriteFile(input, []byte(`{"channel":"D1","text":"fixture"}`), 0600); err != nil {
		t.Fatal(err)
	}
	if err := run(ctx, append([]string{"connector", "call"}, append(base, "--tool", toolhub.ProjectedToolName("slack-data-write", "1.0.0", "send"), "--from-file", input)...)); err != nil {
		t.Fatal(err)
	}
	ledger, err := audit.Open(filepath.Join(filepath.Dir(storePath), "audit.jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	events, err := ledger.List("alice")
	if err != nil {
		t.Fatal(err)
	}
	if len(events) != 2 || events[1].Receipt != "T1:D1:1234567890.000001" {
		t.Fatal("missing provider audit receipt", events)
	}
	for _, op := range []string{"refresh", "revoke"} {
		if err := run(ctx, append([]string{"connector", op}, append(base, "--client-file", client)...)); err != nil {
			t.Fatal(op, err)
		}
	}
}
