package toolhub

import (
	"context"
	"fmt"
	"path/filepath"
	"strings"
	"testing"

	"github.com/letya999/hermes-hub/internal/credstore"
	"github.com/letya999/hermes-hub/internal/identity"
	"github.com/letya999/hermes-hub/internal/toolhub/api"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

func TestToolOutputWithholdsLoopbackOAuthURL(t *testing.T) {
	key, err := credstore.GenerateKey()
	if err != nil {
		t.Fatal(err)
	}
	secrets, err := credstore.Open(credstore.Options{Path: filepath.Join(t.TempDir(), "store.enc"), Key: key})
	if err != nil {
		t.Fatal(err)
	}
	store := NewStore()
	auth := identity.Envelope{Schema: identity.Schema, PrincipalID: "alice", ExternalIdentityID: "alice", ContextID: "alice", RuntimeID: "alice", ConversationID: "test", DeliveryTargetID: "test", PolicyVersion: "policy-1"}
	d := api.CalendarDefinition(false)
	if err := store.RegisterDefinition(d); err != nil {
		t.Fatal(err)
	}
	locator, _ := secrets.NewLocator()
	if err := secrets.Put(locator, "alice", map[string]string{"ACCESS_TOKEN": "alice-token", "OAUTH_SCOPE": api.CalendarReadScope}); err != nil {
		t.Fatal(err)
	}
	ref := api.CredentialReference{Schema: 1, CredentialRefID: api.CredentialReferenceID(d.DefinitionID, 1), ConnectionID: d.DefinitionID, Revision: 1, Backend: "local", Locator: locator, Keys: []string{"ACCESS_TOKEN", "OAUTH_SCOPE"}, Status: api.ActiveStatus}
	if err := store.PutCredentialReference(ref); err != nil {
		t.Fatal(err)
	}
	if err := store.PutConnection(api.Connection{Schema: 1, ConnectionID: d.DefinitionID, Owner: api.OwnerRef{Type: api.PrincipalOwner, ID: "alice"}, DefinitionID: d.DefinitionID, CredentialRefID: ref.CredentialRefID, Revision: 1, Status: api.ActiveStatus}); err != nil {
		t.Fatal(err)
	}
	if _, err := store.Enable(auth, d.DefinitionID, d.Version); err != nil {
		t.Fatal(err)
	}
	poisoned := "**ACTION REQUIRED: Google Authentication Needed**\n" +
		"1. Open this URL in your browser to authorize access:\n" +
		"   Authorization URL: https://accounts.google.com/o/oauth2/auth?response_type=code&client_id=abc.apps.googleusercontent.com&redirect_uri=http%3A%2F%2Flocalhost%3A8000%2Foauth2callback&scope=x&state=s\n" +
		"2. Or visit http://localhost:8000/oauth2callback?state=s directly."
	gateway := &Gateway{Store: store, Backend: backendFunc(func(context.Context, api.EffectiveBinding, api.ToolSpec, map[string]any) (api.BackendResult, error) {
		return api.BackendResult{Text: poisoned, Content: []mcp.Content{&mcp.TextContent{Text: poisoned}}}, nil
	}), Injector: func(_ context.Context, e api.EffectiveBinding) (api.CredentialInjection, error) {
		env, err := api.DecryptAuthorized(secrets, e)
		return api.CredentialInjection{Environment: env}, err
	}}
	result, err := gateway.CallAuthorized(context.Background(), auth, api.ProjectedToolName(d.DefinitionID, d.Version, "list"), map[string]any{"calendar_id": "primary"})
	if err != nil || result == nil {
		t.Fatal(err)
	}
	var text strings.Builder
	for _, content := range result.Content {
		if tc, ok := content.(*mcp.TextContent); ok {
			text.WriteString(tc.Text)
		}
	}
	out := text.String()
	if strings.Contains(out, "localhost") || strings.Contains(out, "oauth2callback") || strings.Contains(out, "accounts.google.com") {
		t.Fatalf("loopback OAuth URL reached caller: %s", out)
	}
	if !strings.Contains(out, "ACTION REQUIRED") {
		t.Fatalf("legitimate error text stripped: %s", out)
	}
}

func TestToolOutputErrorWithholdsLoopbackOAuthURL(t *testing.T) {
	key, err := credstore.GenerateKey()
	if err != nil {
		t.Fatal(err)
	}
	secrets, err := credstore.Open(credstore.Options{Path: filepath.Join(t.TempDir(), "store.enc"), Key: key})
	if err != nil {
		t.Fatal(err)
	}
	store := NewStore()
	auth := identity.Envelope{Schema: identity.Schema, PrincipalID: "alice", ExternalIdentityID: "alice", ContextID: "alice", RuntimeID: "alice", ConversationID: "test", DeliveryTargetID: "test", PolicyVersion: "policy-1"}
	d := api.CalendarDefinition(false)
	if err := store.RegisterDefinition(d); err != nil {
		t.Fatal(err)
	}
	locator, _ := secrets.NewLocator()
	if err := secrets.Put(locator, "alice", map[string]string{"ACCESS_TOKEN": "alice-token", "OAUTH_SCOPE": api.CalendarReadScope}); err != nil {
		t.Fatal(err)
	}
	ref := api.CredentialReference{Schema: 1, CredentialRefID: api.CredentialReferenceID(d.DefinitionID, 1), ConnectionID: d.DefinitionID, Revision: 1, Backend: "local", Locator: locator, Keys: []string{"ACCESS_TOKEN", "OAUTH_SCOPE"}, Status: api.ActiveStatus}
	if err := store.PutCredentialReference(ref); err != nil {
		t.Fatal(err)
	}
	if err := store.PutConnection(api.Connection{Schema: 1, ConnectionID: d.DefinitionID, Owner: api.OwnerRef{Type: api.PrincipalOwner, ID: "alice"}, DefinitionID: d.DefinitionID, CredentialRefID: ref.CredentialRefID, Revision: 1, Status: api.ActiveStatus}); err != nil {
		t.Fatal(err)
	}
	if _, err := store.Enable(auth, d.DefinitionID, d.Version); err != nil {
		t.Fatal(err)
	}
	gateway := &Gateway{Store: store, Backend: backendFunc(func(context.Context, api.EffectiveBinding, api.ToolSpec, map[string]any) (api.BackendResult, error) {
		return api.BackendResult{}, fmt.Errorf("backend call failed: authorize at https://accounts.google.com/o/oauth2/auth?redirect_uri=http%%3A%%2F%%2Flocalhost%%3A8000%%2Foauth2callback then http://localhost:8000/oauth2callback?state=s")
	}), Injector: func(_ context.Context, e api.EffectiveBinding) (api.CredentialInjection, error) {
		env, err := api.DecryptAuthorized(secrets, e)
		return api.CredentialInjection{Environment: env}, err
	}}
	_, err = gateway.CallAuthorized(context.Background(), auth, api.ProjectedToolName(d.DefinitionID, d.Version, "list"), map[string]any{"calendar_id": "primary"})
	if err == nil {
		t.Fatal("expected backend error")
	}
	if strings.Contains(err.Error(), "localhost") || strings.Contains(err.Error(), "oauth2callback") || strings.Contains(err.Error(), "accounts.google.com") {
		t.Fatalf("loopback OAuth URL reached caller through error path: %v", err)
	}
	if !strings.Contains(err.Error(), "backend call failed") {
		t.Fatalf("error text mangled: %v", err)
	}
}

func TestScrubLoopbackOAuthURL(t *testing.T) {
	for _, tc := range []struct {
		name string
		in   string
		want string
	}{
		{"ordinary output kept", "calendars: 11 items", "calendars: 11 items"},
		{"plain google url kept", "see https://accounts.google.com/o/oauth2/auth?client_id=x&redirect_uri=https%3A%2F%2Fhub.example%2Foauth%2Fcallback", "see https://accounts.google.com/o/oauth2/auth?client_id=x&redirect_uri=https%3A%2F%2Fhub.example%2Foauth%2Fcallback"},
		{"bare callback scrubbed", "open http://localhost:8000/oauth2callback?state=s now", "open " + loopbackOAuthPlaceholder + " now"},
		{"authorize with loopback redirect scrubbed", "open https://accounts.google.com/o/oauth2/auth?redirect_uri=http%3A%2F%2Flocalhost%3A8000%2Foauth2callback&state=s", "open " + loopbackOAuthPlaceholder},
		{"127.0.0.1 callback scrubbed", "open http://127.0.0.1:3500/oauth2callback?state=s", "open " + loopbackOAuthPlaceholder},
		{"host.docker.internal callback scrubbed", "open http://host.docker.internal:8000/oauth2callback?state=s", "open " + loopbackOAuthPlaceholder},
		{"non-callback loopback kept", "ping http://localhost:8000/health", "ping http://localhost:8000/health"},
		{"external callback kept", "open https://hub.example/oauth2callback?state=s", "open https://hub.example/oauth2callback?state=s"},
	} {
		if got := scrubLoopbackOAuthURL(tc.in); got != tc.want {
			t.Errorf("%s: got %q want %q", tc.name, got, tc.want)
		}
	}
}
