package toolhub

import (
	"errors"
	"testing"

	"github.com/letya999/hermes-hub/internal/identity"
	"github.com/letya999/hermes-hub/internal/toolhub/api"
)

// The operator-pinned contract at store level: the connection can only ever
// aim at the immutable Source endpoint — never at a different backend, not
// even a trivially rewritten one.
func TestOperatorPinnedConnectionEndpoint(t *testing.T) {
	store := NewStore()
	endpoint := "http://127.0.0.1:8080/mcp"
	definition, err := api.BackendMCPDefinition(endpoint, "corp-pinned", nil, []api.ToolSpec{{Name: "search", Effect: api.ReadEffect}}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if err := store.RegisterDefinition(definition); err != nil {
		t.Fatal(err)
	}
	put := func(connID, metadata string) error {
		return store.PutConnection(api.Connection{
			Schema: api.SchemaVersion, ConnectionID: connID,
			Owner:        api.OwnerRef{Type: api.PrincipalOwner, ID: "alice"},
			DefinitionID: definition.DefinitionID, Revision: 1,
			Status:   api.ActiveStatus,
			Metadata: map[string]string{"mcp_endpoint": metadata},
		})
	}
	if err := put("conn-mismatch", "http://10.9.9.9:8080/mcp"); !errors.Is(err, api.ErrInvalid) {
		t.Fatalf("different endpoint admitted: %v", err)
	}
	if err := put("conn-uncredentialed-mismatch", "http://user:pw@127.0.0.1:8080/mcp"); !errors.Is(err, api.ErrInvalid) {
		t.Fatalf("credentialed URL admitted: %v", err)
	}
	if err := put("conn-missing", ""); !errors.Is(err, api.ErrInvalid) {
		t.Fatalf("missing endpoint admitted: %v", err)
	}
	// The canonical fold — case-insensitive host — is the same endpoint, not a mismatch.
	if err := put("conn-match", "HTTP://127.0.0.1:8080/mcp"); err != nil {
		t.Fatalf("canonical-equal endpoint refused: %v", err)
	}
}

// A credential-less operator-pinned definition still binds its owner
// connection — the connection carries the pinned endpoint — while a second
// principal can neither see nor enable it.
func TestOperatorPinnedEnableCredentialless(t *testing.T) {
	store := NewStore()
	endpoint := "http://127.0.0.1:8080/mcp"
	definition, err := api.BackendMCPDefinition(endpoint, "corp-open", nil, []api.ToolSpec{{Name: "search", Effect: api.ReadEffect}}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if err := store.RegisterDefinition(definition); err != nil {
		t.Fatal(err)
	}
	if err := store.PutPublication(api.DefinitionPublication{DefinitionID: definition.DefinitionID, Version: definition.Version, Visibility: api.PublicationUser, OwnerPrincipalID: "alice"}); err != nil {
		t.Fatal(err)
	}
	alice := aliceAuth()
	bob := identity.TelegramEnvelope("bob", 8, "runtime", "policy-1")
	// Without an owner connection the enable still fails closed.
	if _, err := store.Enable(alice, definition.DefinitionID, definition.Version); !errors.Is(err, api.ErrUnauthorized) {
		t.Fatalf("connectionless enable: %v", err)
	}
	if err := store.PutConnection(api.Connection{
		Schema: api.SchemaVersion, ConnectionID: "conn-alice-corp",
		Owner:        api.OwnerRef{Type: api.PrincipalOwner, ID: "alice"},
		DefinitionID: definition.DefinitionID, Revision: 1,
		Status:   api.ActiveStatus,
		Metadata: map[string]string{"mcp_endpoint": endpoint},
	}); err != nil {
		t.Fatal(err)
	}
	// The user-scoped publication is invisible to anyone but its owner.
	if _, err := store.Enable(bob, definition.DefinitionID, definition.Version); !errors.Is(err, api.ErrUnauthorized) {
		t.Fatalf("cross-principal enable: %v", err)
	}
	binding, err := store.Enable(alice, definition.DefinitionID, definition.Version)
	if err != nil {
		t.Fatalf("owner enable: %v", err)
	}
	if binding.ConnectionID != "conn-alice-corp" || binding.CredentialRefID != "" {
		t.Fatalf("binding did not land the owner connection: %+v", binding)
	}
	effective, err := store.Resolve(alice, binding.ToolBindingID)
	if err != nil {
		t.Fatal(err)
	}
	if effective.Connection == nil || effective.Connection.Metadata["mcp_endpoint"] != endpoint {
		t.Fatalf("resolved binding lost the pinned endpoint: %+v", effective.Connection)
	}
	// Bob cannot resolve alice's binding either — ownership is enforced at resolve.
	if _, err := store.Resolve(bob, binding.ToolBindingID); err == nil {
		t.Fatal("cross-principal resolve succeeded")
	}
}
