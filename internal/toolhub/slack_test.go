package toolhub

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"

	"github.com/letya999/hermes-hub/internal/credstore"
	"github.com/letya999/hermes-hub/internal/identity"
)

func TestSlackShippedAuthorizeInjectReceiptAndDenials(t *testing.T) {
	key, _ := credstore.GenerateKey()
	secrets, err := credstore.Open(credstore.Options{Path: filepath.Join(t.TempDir(), "store.enc"), Key: key})
	if err != nil {
		t.Fatal(err)
	}
	store := NewStore()
	auth := identity.Envelope{Schema: identity.Schema, PrincipalID: "alice", ContextID: "alice", RuntimeID: "alice", ExternalIdentityID: "alice", ConversationID: "test", DeliveryTargetID: "test", PolicyVersion: "policy-1"}
	team, user, bot := "T1", "U1", false
	mutations := 0
	fixture := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer alice-user" {
			t.Error("wrong credential")
		}
		if r.URL.Path == "/api/auth.test" {
			data := map[string]any{"ok": true, "team_id": team, "user_id": user}
			if bot {
				data["bot_id"] = "B1"
			}
			_ = json.NewEncoder(w).Encode(data)
			return
		}
		if r.URL.Path == "/api/auth.revoke" {
			_, _ = io.WriteString(w, `{"ok":true,"revoked":true}`)
			return
		}
		_ = r.ParseForm()
		if strings.Contains(r.URL.Path, "chat.") {
			mutations++
			if r.Method != http.MethodPost {
				t.Error("wrong mutation method")
			}
			if r.Form.Get("text") != "fixture" || r.Form.Get("channel") != "D1" {
				t.Error("bad mutation resource")
			}
			_, _ = io.WriteString(w, `{"ok":true,"channel":"D1","ts":"1234567890.000001"}`)
			return
		}
		if r.URL.Path == "/api/search.messages" {
			_, _ = io.WriteString(w, `{"ok":true,"messages":{"matches":[]}}`)
			return
		}
		_, _ = io.WriteString(w, `{"ok":true,"messages":[]}`)
	}))
	defer fixture.Close()
	provider := SlackBackend{HTTP: &http.Client{Transport: calendarFixtureTransport{fixture.URL, http.DefaultTransport}}}
	for _, write := range []bool{false, true} {
		d := SlackDefinition(write)
		if err := store.RegisterDefinition(d); err != nil {
			t.Fatal(err)
		}
		locator, _ := secrets.NewLocator()
		if err := secrets.Put(locator, "alice", map[string]string{"ACCESS_TOKEN": "alice-user", "OAUTH_SCOPE": strings.Join(SlackScopes(write), ",")}); err != nil {
			t.Fatal(err)
		}
		ref := CredentialReference{Schema: 1, CredentialRefID: CredentialReferenceID(d.DefinitionID, 1), ConnectionID: d.DefinitionID, Revision: 1, Backend: "local", Locator: locator, Keys: []string{"ACCESS_TOKEN", "OAUTH_SCOPE"}, Status: ActiveStatus}
		if err := store.PutCredentialReference(ref); err != nil {
			t.Fatal(err)
		}
		if err := store.PutConnection(Connection{Schema: 1, ConnectionID: d.DefinitionID, Owner: OwnerRef{Type: PrincipalOwner, ID: "alice"}, DefinitionID: d.DefinitionID, CredentialRefID: ref.CredentialRefID, Revision: 1, Status: ActiveStatus, Metadata: map[string]string{"slack_team": "T1", "slack_user": "U1"}}); err != nil {
			t.Fatal(err)
		}
		if _, err := store.Enable(auth, d.DefinitionID, d.Version); err != nil {
			t.Fatal(err)
		}
	}
	receipts := 0
	gateway := &Gateway{Store: store, Backend: RoutingBackend{Provider: provider}, Injector: func(_ context.Context, e EffectiveBinding) (CredentialInjection, error) {
		env, err := DecryptAuthorized(secrets, e)
		return CredentialInjection{Environment: env}, err
	}, AuditWrite: func(_ string, fields map[string]string) error {
		if fields["receipt"] != "" {
			receipts++
		}
		return nil
	}}
	for _, write := range []bool{false, true} {
		d := SlackDefinition(write)
		for _, tool := range d.Tools {
			args := map[string]any{}
			for _, arg := range tool.Arguments {
				switch arg.Name {
				case "channel":
					args[arg.Name] = "D1"
				case "query", "text":
					args[arg.Name] = "fixture"
				case "ts":
					args[arg.Name] = "1234567890.000001"
				case "cursor":
					args[arg.Name] = "next"
				}
			}
			if _, err := gateway.CallAuthorized(context.Background(), auth, ProjectedToolName(d.DefinitionID, d.Version, tool.Name), args); err != nil {
				t.Fatal(tool.Name, err)
			}
		}
	}
	name := ProjectedToolName("slack-data-write", "1.0.0", "send")
	valid := map[string]any{"channel": "D1", "text": "fixture"}
	for _, change := range []func(){func() { team = "T2" }, func() { team = "T1"; user = "U2" }, func() { user = "U1"; bot = true }} {
		change()
		if _, err := gateway.CallAuthorized(context.Background(), auth, name, valid); err == nil {
			t.Fatal("identity mismatch accepted")
		}
	}
	bot = false
	for _, args := range []map[string]any{{"channel": "../evil", "text": "fixture"}, {"channel": "D1", "text": "fixture", "connection_id": "bob"}, {"channel": "D1", "text": true}, {"channel": "D1", "text": "fixture", "unexpected": "evil"}, {"channel": "D1"}} {
		if _, err := gateway.CallAuthorized(context.Background(), auth, name, args); err == nil {
			t.Fatal("invalid accepted")
		}
	}
	bob := auth
	bob.PrincipalID, bob.ContextID, bob.RuntimeID = "bob", "bob", "bob"
	if _, err := gateway.CallAuthorized(context.Background(), bob, name, valid); err == nil {
		t.Fatal("cross owner accepted")
	}
	if err := store.SetConnectionStatus("slack-data-write", RevokedStatus); err != nil {
		t.Fatal(err)
	}
	if _, err := gateway.CallAuthorized(context.Background(), auth, name, valid); err == nil {
		t.Fatal("revoked accepted")
	}
	if mutations != 3 || receipts != 3 {
		t.Fatal(mutations, receipts)
	}
	if err := provider.VerifyAccount(context.Background(), "alice-user", "T1", "U1"); err != nil {
		t.Fatal(err)
	}
	if err := provider.VerifyAccount(context.Background(), "alice-user", "C1", "U1"); err == nil {
		t.Fatal("invalid workspace type")
	}
	if err := provider.Revoke(context.Background(), "alice-user"); err != nil {
		t.Fatal(err)
	}
	read := SlackDefinition(false)
	e := EffectiveBinding{Definition: read, Connection: &Connection{Metadata: map[string]string{"slack_team": "T1", "slack_user": "U1"}}}
	if _, err := provider.CallEnv(context.Background(), e, SlackDefinition(true).Tools[0], valid, map[string]string{"ACCESS_TOKEN": "alice-user", "OAUTH_SCOPE": strings.Join(SlackScopes(false), ",")}); err == nil {
		t.Fatal("read connection posted")
	}
	if _, err := provider.Call(context.Background(), e, read.Tools[0], nil); err == nil {
		t.Fatal("missing credentials")
	}
	unknown := read.Tools[0]
	unknown.Name = "send"
	if _, err := provider.CallEnv(context.Background(), e, unknown, valid, map[string]string{"ACCESS_TOKEN": "alice-user", "OAUTH_SCOPE": strings.Join(SlackScopes(false), ",")}); err == nil {
		t.Fatal("forged read effect posted")
	}
}

func TestSlackHTTPBoundsCancellationAndReceiptContract(t *testing.T) {
	d := SlackDefinition(true)
	e := EffectiveBinding{Definition: d, Connection: &Connection{Metadata: map[string]string{"slack_team": "T1", "slack_user": "U1"}}}
	env := map[string]string{"ACCESS_TOKEN": "fixture", "OAUTH_SCOPE": "chat:write"}
	args := map[string]any{"channel": "D1", "text": "fixture"}
	for _, response := range []struct {
		status int
		body   string
	}{
		{http.StatusTooManyRequests, `{}`}, {http.StatusOK, `{"ok":false}`}, {http.StatusOK, `null`}, {http.StatusOK, `bad`}, {http.StatusOK, strings.Repeat("x", 65537)},
		{http.StatusOK, `{"ok":true}`}, {http.StatusOK, `{"ok":true,"channel":"D2","ts":"1234567890.000001"}`}, {http.StatusOK, `{"ok":true,"channel":"D1","ts":"bad"}`},
	} {
		fixture := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if r.URL.Path == "/api/auth.test" {
				_, _ = io.WriteString(w, `{"ok":true,"team_id":"T1","user_id":"U1"}`)
				return
			}
			w.WriteHeader(response.status)
			_, _ = io.WriteString(w, response.body)
		}))
		provider := SlackBackend{HTTP: &http.Client{Transport: calendarFixtureTransport{fixture.URL, http.DefaultTransport}}}
		if _, err := provider.CallEnv(context.Background(), e, d.Tools[0], args, env); err == nil {
			t.Fatal("bad mutation response accepted")
		}
		fixture.Close()
	}
	fixture := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, "https://other.example.invalid", http.StatusFound)
	}))
	defer fixture.Close()
	provider := SlackBackend{HTTP: &http.Client{Transport: calendarFixtureTransport{fixture.URL, http.DefaultTransport}}}
	if _, err := provider.CallEnv(context.Background(), e, d.Tools[0], args, env); err == nil {
		t.Fatal("redirect accepted")
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := provider.CallEnv(ctx, e, d.Tools[0], args, env); err == nil {
		t.Fatal("cancel accepted")
	}
	for _, invalid := range []map[string]any{{"channel": "D1", "text": strings.Repeat("x", 4097)}, {"channel": "D1", "text": "x\x00"}, {"channel": "D1", "text": "fixture", "ts": "bad"}} {
		if _, err := provider.CallEnv(context.Background(), e, d.Tools[1], invalid, env); err == nil {
			t.Fatal("invalid resource")
		}
	}
	if err := provider.VerifyAccount(context.Background(), "fixture", "T1", "U1"); err == nil {
		t.Fatal("redirect account verify")
	}
	if err := provider.Revoke(context.Background(), "fixture"); err == nil {
		t.Fatal("redirect revoke")
	}
}

func TestPersonalProviderReadRejectsEmptySuccess(t *testing.T) {
	for _, test := range []struct {
		slack      bool
		name, body string
		allow      bool
	}{
		{false, "list", `{}`, false}, {false, "list", `{"items":null}`, false}, {false, "get", `{}`, false}, {false, "list", `{"kind":"calendar#events"}`, true},
		{true, "search", `{"ok":true,"messages":[]}`, false}, {true, "search", `{"ok":true,"messages":{}}`, false}, {true, "history", `{"ok":true}`, false},
	} {
		fixture := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if r.URL.Path == "/api/auth.test" {
				_, _ = io.WriteString(w, `{"ok":true,"team_id":"T1","user_id":"U1"}`)
				return
			}
			_, _ = io.WriteString(w, test.body)
		}))
		client := &http.Client{Transport: calendarFixtureTransport{fixture.URL, http.DefaultTransport}}
		d := CalendarDefinition(false)
		metadata := map[string]string{"google_sub": "subject1"}
		env := map[string]string{"ACCESS_TOKEN": "fixture", "OAUTH_SCOPE": CalendarReadScope}
		args := map[string]any{"calendar_id": "primary"}
		if test.name == "get" {
			args["event_id"] = "event1"
		}
		var backend envBackend = CalendarBackend{HTTP: client}
		if test.slack {
			d = SlackDefinition(false)
			metadata = map[string]string{"slack_team": "T1", "slack_user": "U1"}
			env["OAUTH_SCOPE"] = strings.Join(SlackScopes(false), ",")
			args = map[string]any{"channel": "D1"}
			if test.name == "search" {
				args = map[string]any{"query": "fixture"}
			}
			backend = SlackBackend{HTTP: client}
		}
		var tool ToolSpec
		for _, spec := range d.Tools {
			if spec.Name == test.name {
				tool = spec
			}
		}
		_, err := backend.CallEnv(context.Background(), EffectiveBinding{Definition: d, Connection: &Connection{Metadata: metadata}}, tool, args, env)
		fixture.Close()
		if (err == nil) != test.allow {
			t.Fatal(test.name, test.body, err)
		}
	}
	for _, id := range []string{"google-calendar-read", "slack-data-read", "unknown"} {
		d := CalendarDefinition(false)
		if id == "slack-data-read" {
			d = SlackDefinition(false)
		}
		d.DefinitionID = id
		if _, err := (PersonalProviderBackend{}).Call(context.Background(), EffectiveBinding{Definition: d}, d.Tools[0], nil); err == nil {
			t.Fatal("unauthorized provider routed")
		}
	}
}
