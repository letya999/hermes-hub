package toolhub

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"

	"github.com/letya999/hermes-hub/internal/credstore"
	"github.com/letya999/hermes-hub/internal/identity"
)

type calendarFixtureTransport struct {
	endpoint string
	base     http.RoundTripper
}

func (f calendarFixtureTransport) RoundTrip(r *http.Request) (*http.Response, error) {
	copyRequest := r.Clone(r.Context())
	copyURL := *r.URL
	copyRequest.URL = &copyURL
	fixtureReq, _ := http.NewRequest(http.MethodGet, f.endpoint, nil)
	copyRequest.URL.Scheme, copyRequest.URL.Host = fixtureReq.URL.Scheme, fixtureReq.URL.Host
	return f.base.RoundTrip(copyRequest)
}

func TestCalendarShippedAuthorizeInjectAndHTTP(t *testing.T) {
	key, _ := credstore.GenerateKey()
	secrets, err := credstore.Open(credstore.Options{Path: filepath.Join(t.TempDir(), "store.enc"), Key: key})
	if err != nil {
		t.Fatal(err)
	}
	store := NewStore()
	auth := identity.Envelope{Schema: identity.Schema, PrincipalID: "alice", ExternalIdentityID: "alice", ContextID: "alice", RuntimeID: "alice", ConversationID: "test", DeliveryTargetID: "test", PolicyVersion: "policy-1"}
	calls := 0
	fixture := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		if r.Header.Get("Authorization") != "Bearer alice-token" {
			t.Error("wrong credential")
		}
		if !strings.HasPrefix(r.URL.Path, "/calendar/v3/calendars/primary/events") {
			t.Error(r.URL.Path)
		}
		if r.Method == http.MethodDelete {
			w.WriteHeader(204)
			return
		}
		if r.Method == http.MethodPatch {
			raw, _ := io.ReadAll(r.Body)
			if !strings.Contains(string(raw), "attendees") {
				t.Error(string(raw))
			}
		}
		_, _ = io.WriteString(w, `{"id":"event1","items":[]}`)
	}))
	defer fixture.Close()
	provider := CalendarBackend{HTTP: &http.Client{Transport: calendarFixtureTransport{fixture.URL, http.DefaultTransport}}}
	for _, write := range []bool{false, true} {
		d := CalendarDefinition(write)
		if err := store.RegisterDefinition(d); err != nil {
			t.Fatal(err)
		}
		locator, _ := secrets.NewLocator()
		scope := CalendarReadScope
		if write {
			scope = CalendarWriteScope
		}
		if err := secrets.Put(locator, "alice", map[string]string{"ACCESS_TOKEN": "alice-token", "OAUTH_SCOPE": scope}); err != nil {
			t.Fatal(err)
		}
		ref := CredentialReference{Schema: 1, CredentialRefID: CredentialReferenceID(d.DefinitionID, 1), ConnectionID: d.DefinitionID, Revision: 1, Backend: "local", Locator: locator, Keys: []string{"ACCESS_TOKEN", "OAUTH_SCOPE"}, Status: ActiveStatus}
		if err := store.PutCredentialReference(ref); err != nil {
			t.Fatal(err)
		}
		if err := store.PutConnection(Connection{Schema: 1, ConnectionID: d.DefinitionID, Owner: OwnerRef{Type: PrincipalOwner, ID: "alice"}, DefinitionID: d.DefinitionID, CredentialRefID: ref.CredentialRefID, Revision: 1, Status: ActiveStatus, Metadata: map[string]string{"google_sub": "subject1"}}); err != nil {
			t.Fatal(err)
		}
		if _, err := store.Enable(auth, d.DefinitionID, d.Version); err != nil {
			t.Fatal(err)
		}
	}
	gateway := &Gateway{Store: store, Backend: RoutingBackend{Provider: provider}, Injector: func(_ context.Context, e EffectiveBinding) (CredentialInjection, error) {
		env, err := DecryptAuthorized(secrets, e)
		return CredentialInjection{Environment: env}, err
	}}
	for _, name := range []string{"list", "get", "create", "update", "delete"} {
		write := name == "create" || name == "update" || name == "delete"
		d := CalendarDefinition(write)
		args := map[string]any{"calendar_id": "primary"}
		if name == "get" || name == "update" || name == "delete" {
			args["event_id"] = "event1"
		}
		if name == "create" || name == "update" {
			args["event_json"] = `{"attendees":[{"email":"fixture@example.invalid"}]}`
		}
		result, err := gateway.CallAuthorized(context.Background(), auth, ProjectedToolName(d.DefinitionID, d.Version, name), args)
		if err != nil || result == nil {
			t.Fatalf("%s: %v", name, err)
		}
	}
	name := ProjectedToolName("google-calendar-read", "1.0.0", "list")
	for _, args := range []map[string]any{{"calendar_id": "../bob"}, {"calendar_id": "primary", "connection_id": "bob"}, {"calendar_id": "primary", "extra": "evil"}} {
		if _, err := gateway.call(context.Background(), auth, name, args); err == nil {
			t.Fatal("malformed accepted")
		}
	}
	bob := auth
	bob.PrincipalID, bob.ContextID, bob.RuntimeID = "bob", "bob", "bob"
	if _, err := gateway.call(context.Background(), bob, name, map[string]any{"calendar_id": "primary"}); err == nil {
		t.Fatal("cross owner accepted")
	}
	if err := store.SetConnectionStatus("google-calendar-read", RevokedStatus); err != nil {
		t.Fatal(err)
	}
	if _, err := gateway.call(context.Background(), auth, name, map[string]any{"calendar_id": "primary"}); err == nil {
		t.Fatal("revoked accepted")
	}
	if calls != 5 {
		t.Fatalf("unexpected provider calls: %d", calls)
	}
	// A transport-shaped token never satisfies personal data inputs.
	d := CalendarDefinition(false)
	communication := NewStore()
	if err := communication.RegisterDefinition(d); err != nil {
		t.Fatal(err)
	}
	ref := CredentialReference{Schema: 1, CredentialRefID: CredentialReferenceID("bot-only", 1), ConnectionID: "bot-only", Revision: 1, Backend: "local", Locator: "fixture-bot", Keys: []string{"SLACK_BOT_TOKEN"}, Status: ActiveStatus}
	if err := communication.PutCredentialReference(ref); err != nil {
		t.Fatal(err)
	}
	if err := communication.PutConnection(Connection{Schema: 1, ConnectionID: "bot-only", Owner: OwnerRef{Type: PrincipalOwner, ID: "alice"}, DefinitionID: d.DefinitionID, CredentialRefID: ref.CredentialRefID, Revision: 1, Status: ActiveStatus}); err != nil {
		t.Fatal(err)
	}
	if _, err := communication.Enable(auth, d.DefinitionID, d.Version); err == nil {
		t.Fatal("communication credentials granted data tools")
	}
}

func TestCalendarTrustBoundaryFailures(t *testing.T) {
	d := CalendarDefinition(true)
	e := EffectiveBinding{Definition: d, Connection: &Connection{Metadata: map[string]string{"google_sub": "subject1"}}}
	env := map[string]string{"ACCESS_TOKEN": "fixture", "OAUTH_SCOPE": CalendarWriteScope}
	var tool ToolSpec
	for _, spec := range d.Tools {
		if spec.Name == "update" {
			tool = spec
		}
	}
	valid := map[string]any{"calendar_id": "primary", "event_id": "event1", "event_json": `{"summary":"fixture"}`}
	for _, args := range []map[string]any{
		{"calendar_id": "primary", "event_id": "event1", "event_json": "null"},
		{"calendar_id": "primary", "event_id": "event1", "event_json": "{}"},
		{"calendar_id": "primary", "event_id": "event1", "event_json": strings.Repeat("x", 16385)},
		{"calendar_id": "primary", "event_id": "/bad", "event_json": `{"summary":"fixture"}`},
		{"calendar_id": false}, {"owner_id": "bob"}, {"calendar_id": "primary", "x": true},
	} {
		if _, err := (CalendarBackend{}).CallEnv(context.Background(), e, tool, args, env); err == nil {
			t.Fatal("invalid accepted")
		}
	}
	if _, err := (CalendarBackend{}).Call(context.Background(), e, tool, valid); err == nil {
		t.Fatal("missing credential accepted")
	}
	if _, err := (CalendarBackend{}).CallEnv(context.Background(), e, tool, valid, map[string]string{"ACCESS_TOKEN": "fixture", "OAUTH_SCOPE": CalendarReadScope}); err == nil {
		t.Fatal("read token writes")
	}
	unknown := tool
	unknown.Name = "unknown"
	if _, err := (CalendarBackend{}).CallEnv(context.Background(), e, unknown, valid, env); err == nil {
		t.Fatal("unknown tool")
	}
	for _, response := range []struct {
		status int
		body   string
	}{
		{403, `{"error":"denied"}`}, {200, "null"}, {200, "bad"}, {200, "{}"}, {200, `{"id":4}`}, {200, `{"id":"/evil"}`}, {200, strings.Repeat("x", 65537)},
	} {
		fixture := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			w.WriteHeader(response.status)
			_, _ = io.WriteString(w, response.body)
		}))
		provider := CalendarBackend{HTTP: &http.Client{Transport: calendarFixtureTransport{fixture.URL, http.DefaultTransport}}}
		if _, err := provider.CallEnv(context.Background(), e, tool, valid, env); err == nil {
			t.Fatal("bad response accepted")
		}
		fixture.Close()
	}
	fixture := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, "https://other.example.invalid", http.StatusFound)
	}))
	defer fixture.Close()
	provider := CalendarBackend{HTTP: &http.Client{Transport: calendarFixtureTransport{fixture.URL, http.DefaultTransport}}}
	if _, err := provider.CallEnv(context.Background(), e, tool, valid, env); err == nil {
		t.Fatal("redirect accepted")
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := provider.CallEnv(ctx, e, tool, valid, env); err == nil {
		t.Fatal("cancelled request accepted")
	}
	for _, value := range []string{"", ".", "..", strings.Repeat("x", 129), "a\\b", "x\x00"} {
		if calendarResource(value) {
			t.Fatal(value)
		}
	}
	read := CalendarDefinition(false)
	readEffective := EffectiveBinding{Definition: read, Connection: &Connection{Metadata: map[string]string{"google_sub": "subject1"}}}
	for _, token := range []any{true, strings.Repeat("x", 2049)} {
		if _, err := provider.CallEnv(context.Background(), readEffective, read.Tools[0], map[string]any{"calendar_id": "primary", "page_token": token}, map[string]string{"ACCESS_TOKEN": "fixture", "OAUTH_SCOPE": CalendarReadScope}); err == nil {
			t.Fatal("bad pagination accepted")
		}
	}
	if _, err := provider.CallEnv(context.Background(), readEffective, read.Tools[0], map[string]any{"calendar_id": "primary", "page_token": "next"}, map[string]string{"ACCESS_TOKEN": "fixture", "OAUTH_SCOPE": CalendarReadScope}); err == nil {
		t.Fatal("redirect pagination accepted")
	}
}

func TestOwnedConnectionLifecycleLookupIsolation(t *testing.T) {
	store, auth, _ := seededStore(t)
	c, r, err := store.OwnedConnection(auth, "google-work")
	if err != nil || c.ConnectionID != "google-work" || r.ConnectionID != c.ConnectionID {
		t.Fatal(c, r, err)
	}
	if c.Metadata != nil {
		c.Metadata["fixture"] = "changed"
	}
	r.Keys[0] = "CHANGED"
	_, again, err := store.OwnedConnection(auth, "google-work")
	if err != nil || again.Keys[0] == "CHANGED" {
		t.Fatal("mutable alias")
	}
	bob := auth
	bob.PrincipalID, bob.ContextID = "bob", "bob"
	if _, _, err := store.OwnedConnection(bob, "google-work"); err == nil {
		t.Fatal("cross-owner lifecycle lookup")
	}
	if _, _, err := store.OwnedConnection(auth, "missing"); err == nil {
		t.Fatal("missing lookup")
	}
	if _, _, err := store.OwnedConnection(identity.Envelope{}, "google-work"); err == nil {
		t.Fatal("invalid identity")
	}
	store.mu.Lock()
	store.credentials = map[string]CredentialReference{}
	store.mu.Unlock()
	if _, _, err := store.OwnedConnection(auth, "google-work"); err == nil {
		t.Fatal("missing credential accepted")
	}
}

func TestProviderAPIManifestValidation(t *testing.T) {
	for _, mutate := range []func(*ToolDefinition){
		func(d *ToolDefinition) { d.Workload.Stateful = true },
		func(d *ToolDefinition) { d.Credentials = nil },
		func(d *ToolDefinition) { d.Execution.Egress = []string{"other.example.invalid"} },
		func(d *ToolDefinition) { d.Tools[0].Arguments[0].Flag = "--evil" },
		func(d *ToolDefinition) { d.Source.URL = "http://www.googleapis.com" },
		func(d *ToolDefinition) { d.Source.TLSMode = "" },
		func(d *ToolDefinition) { d.Health.Kind = "exec" },
	} {
		d := CalendarDefinition(false)
		mutate(&d)
		if err := d.Validate(); err == nil {
			t.Fatal("unsafe provider manifest accepted")
		}
	}
	e := EffectiveBinding{Definition: CalendarDefinition(false)}
	if _, err := (RoutingBackend{}).Call(context.Background(), e, e.Definition.Tools[0], nil); err == nil {
		t.Fatal("missing backend accepted")
	}
}

func TestProviderReceiptControlCharactersAreNotAuditMetadata(t *testing.T) {
	for _, value := range []string{"a\tb", "a\x1bb", "a\u0085b", "a\x00b", "a\nb"} {
		if SafeReceipt(value) != "" {
			t.Fatal("control receipt accepted")
		}
	}
	if SafeReceipt(" T1:D1:1234567890.000001 ") != "T1:D1:1234567890.000001" {
		t.Fatal("valid receipt dropped")
	}
}

func TestRegistryConcurrentSnapshotDoesNotLoseRevocation(t *testing.T) {
	store, auth, _ := seededStore(t)
	path := filepath.Join(t.TempDir(), "registry.json")
	if err := store.Save(path); err != nil {
		t.Fatal(err)
	}
	first, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	second, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := first.SetConnectionStatus("google-work", RevokedStatus); err != nil {
		t.Fatal(err)
	}
	if err := second.SetTerminalExposure("google-work", true); err == nil {
		t.Fatal("stale snapshot overwrote revoke")
	}
	fresh, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	c, _, err := fresh.OwnedConnection(auth, "google-work")
	if err != nil || c.Status != RevokedStatus {
		t.Fatal("revocation lost", err)
	}
	if err := NewStore().Save(path); err == nil {
		t.Fatal("unloaded snapshot overwrite")
	}
}
