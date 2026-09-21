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

	"github.com/letya999/hermes-hub/internal/credstore"
	"github.com/letya999/hermes-hub/internal/identity"
	"github.com/letya999/hermes-hub/internal/toolhub"
)

type connectorFixtureTransport struct {
	endpoint string
	base     http.RoundTripper
}

func TestConnectorRejectedInputsAndAccountCleanup(t *testing.T) {
	ctx := context.Background()
	base := []string{"--user", "alice", "--toolhub-store", filepath.Join(t.TempDir(), "registry.json"), "--store", filepath.Join(t.TempDir(), "store.enc"), "--key-file", filepath.Join(t.TempDir(), "key"), "--connection", "calendar1"}
	badFile := filepath.Join(t.TempDir(), "bad.txt")
	if err := os.WriteFile(badFile, []byte("bad"), 0600); err != nil {
		t.Fatal(err)
	}
	for _, test := range []struct {
		op    string
		extra []string
	}{
		{"connect", nil}, {"connect", []string{"--account", "subject1", "--client-file", badFile}},
		{"connect", []string{"--account", "subject1", "--client-file", filepath.Join(t.TempDir(), "missing")}},
		{"call", []string{"--from-file", badFile}},
		{"call", []string{"--from-file", filepath.Join(t.TempDir(), "missing")}},
		{"revoke", nil}, {"refresh", nil}, {"unknown", nil},
	} {
		if err := runConnector(ctx, append([]string{test.op}, append(base, test.extra...)...)); err == nil {
			t.Fatal(test.op)
		}
	}
	for _, extra := range [][]string{{"--bad"}, {"extra"}, {"--user", "../bad"}, {"--connection", "../bad"}, {"--store", "relative"}} {
		if err := runConnector(ctx, append([]string{"connect"}, append(base, extra...)...)); err == nil {
			t.Fatal(extra)
		}
	}
	clientFile := filepath.Join(t.TempDir(), "client.txt")
	if err := os.WriteFile(clientFile, []byte("CLIENT_ID=fixture\nCLIENT_SECRET=fixture"), 0600); err != nil {
		t.Fatal(err)
	}
	cancelled, cancel := context.WithCancel(ctx)
	cancel()
	if err := runConnector(cancelled, append([]string{"connect"}, append(base, "--account", "subject1", "--client-file", clientFile)...)); err == nil {
		t.Fatal("cancelled connect")
	}
	key, _ := credstore.GenerateKey()
	backend, err := credstore.Open(credstore.Options{Path: filepath.Join(t.TempDir(), "store.enc"), Key: key})
	if err != nil {
		t.Fatal(err)
	}
	auth := identity.Envelope{Schema: identity.Schema, PrincipalID: "alice", ContextID: "alice", RuntimeID: "alice", ExternalIdentityID: "alice", ConversationID: "test", DeliveryTargetID: "test", PolicyVersion: "policy-1"}
	for _, test := range []struct {
		status      int
		body, scope string
		write       bool
	}{
		{403, `{}`, toolhub.CalendarReadScope, false},
		{200, `bad`, toolhub.CalendarReadScope, false},
		{200, `{"sub":"bob"}`, toolhub.CalendarReadScope, false},
		{200, `{"sub":"subject1"}`, "openid", false},
		{200, strings.Repeat("x", 65537), toolhub.CalendarReadScope, false},
		{200, `{"sub":"subject1"}`, toolhub.CalendarWriteScope, true},
	} {
		fixture := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			w.WriteHeader(test.status)
			_, _ = io.WriteString(w, test.body)
		}))
		old := http.DefaultTransport
		http.DefaultTransport = connectorFixtureTransport{fixture.URL, old}
		locator, _ := backend.NewLocator()
		if err := backend.Put(locator, "alice", map[string]string{"ACCESS_TOKEN": "fixture", "OAUTH_SCOPE": test.scope}); err != nil {
			t.Fatal(err)
		}
		err := bindGoogle(ctx, toolhub.NewStore(), backend, auth, "calendar1", "subject1", locator, test.write)
		http.DefaultTransport = old
		fixture.Close()
		if test.write {
			if err != nil {
				t.Fatal(err)
			}
		} else {
			if err == nil {
				t.Fatal("bad account accepted")
			}
			if _, err := backend.Get(locator, "alice"); err == nil {
				t.Fatal("rejected token retained")
			}
		}
	}
}

func (f connectorFixtureTransport) RoundTrip(r *http.Request) (*http.Response, error) {
	if r.URL.Scheme != "https" {
		return f.base.RoundTrip(r)
	}
	clone := r.Clone(r.Context())
	u := *r.URL
	clone.URL = &u
	target, _ := url.Parse(f.endpoint)
	clone.URL.Host, clone.URL.Scheme = target.Host, target.Scheme
	return f.base.RoundTrip(clone)
}

// oauthCallbackGet hits the loopback OAuth callback listener with a fresh
// transport: reusing a pooled connection left by a recently closed fixture
// server whose ephemeral port the listener recycled surfaces as a flaky EOF.
func oauthCallbackGet(t *testing.T, rawURL string) *http.Response {
	t.Helper()
	resp, err := (&http.Client{Transport: &http.Transport{DisableKeepAlives: true}}).Get(rawURL)
	if err != nil {
		t.Fatal(err)
	}
	return resp
}

func TestConnectorCLIConnectCallRefreshRevoke(t *testing.T) {
	fixture := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/token":
			_ = r.ParseForm()
			if r.Form.Get("client_id") != "fixture-client" || r.Form.Get("client_secret") != "fixture-secret" {
				t.Error("OAuth client missing")
			}
			_, _ = io.WriteString(w, `{"access_token":"fixture-access","refresh_token":"fixture-refresh","scope":"openid https://www.googleapis.com/auth/calendar.events.readonly","token_type":"Bearer"}`)
		case "/v1/userinfo":
			_, _ = io.WriteString(w, `{"sub":"subject1"}`)
		case "/revoke":
			_, _ = io.WriteString(w, "")
		default:
			_, _ = io.WriteString(w, `{"items":[]}`)
		}
	}))
	defer fixture.Close()
	previous := http.DefaultTransport
	http.DefaultTransport = connectorFixtureTransport{fixture.URL, previous}
	defer func() { http.DefaultTransport = previous }()
	clientFile := filepath.Join(t.TempDir(), "client.txt")
	if err := os.WriteFile(clientFile, []byte("CLIENT_ID=fixture-client\nCLIENT_SECRET=fixture-secret"), 0600); err != nil {
		t.Fatal(err)
	}
	base := []string{"--user", "alice", "--toolhub-store", filepath.Join(t.TempDir(), "registry.json"), "--store", filepath.Join(t.TempDir(), "store.enc"), "--key-file", filepath.Join(t.TempDir(), "key"), "--connection", "calendar1"}
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
		finished <- run(ctx, append([]string{"connector", "connect"}, append(base, "--account", "subject1", "--client-file", clientFile)...))
	}()
	line, err := bufio.NewReader(reader).ReadString('\n')
	if err != nil {
		t.Fatal(err)
	}
	u, err := url.Parse(strings.TrimSpace(line))
	if err != nil {
		t.Fatal(err)
	}
	redirect := u.Query().Get("redirect_uri")
	resp := oauthCallbackGet(t, redirect+"?state="+url.QueryEscape(u.Query().Get("state"))+"&code=fixture-code")
	resp.Body.Close()
	if err := <-finished; err != nil {
		t.Fatal(err)
	}
	os.Stdout = oldOut
	badClient := filepath.Join(t.TempDir(), "bad-client.txt")
	if err := os.WriteFile(badClient, []byte("bad"), 0600); err != nil {
		t.Fatal(err)
	}
	for _, extra := range [][]string{nil, {"--client-file", badClient}, {"--client-file", filepath.Join(t.TempDir(), "missing")}} {
		if err := run(ctx, append([]string{"connector", "refresh"}, append(base, extra...)...)); err == nil {
			t.Fatal("invalid refresh client accepted")
		}
	}
	input := filepath.Join(t.TempDir(), "arguments.json")
	if err := os.WriteFile(input, []byte(`{"calendar_id":"primary"}`), 0600); err != nil {
		t.Fatal(err)
	}
	for _, op := range []string{"status", "call", "refresh", "revoke"} {
		extra := []string{}
		if op == "call" {
			extra = []string{"--tool", toolhub.ProjectedToolName("google-calendar-read", "1.0.0", "list"), "--from-file", input}
		}
		if op == "refresh" {
			extra = []string{"--client-file", clientFile}
		}
		if err := run(ctx, append([]string{"connector", op}, append(base, extra...)...)); err != nil {
			t.Fatalf("%s: %v", op, err)
		}
	}
	if err := run(ctx, append([]string{"connector", "refresh"}, append(base, "--client-file", clientFile)...)); err == nil {
		t.Fatal("revoked refresh accepted")
	}
	for _, args := range [][]string{nil, {"status"}, {"connect"}, {"invalid"}} {
		if err := runConnector(ctx, args); err == nil {
			t.Fatal("invalid command accepted")
		}
	}
}
