package toolhub

import (
	"context"
	"errors"
	"sync/atomic"
	"testing"
	"time"

	"github.com/letya999/hermes-hub/internal/identity"
	"github.com/letya999/hermes-hub/internal/toolhub/api"
)

// A slow provider call must never pin the registry fence: while owner A's
// backend is in flight, a write mutation lands and an independent call by
// owner B completes. Before post-admission dispatch the read lock was held
// through the backend, so the pending writer starved every new admission.
func TestInFlightBackendDoesNotStarveRevocationOrOtherOwners(t *testing.T) {
	store, auth, binding := seededStore(t)
	bob := identity.TelegramEnvelope("bob", 8, "runtime-bob", "policy-1")
	credential := api.CredentialReference{Schema: api.SchemaVersion, CredentialRefID: api.CredentialReferenceID("google-work", 2), ConnectionID: "bob-work", Revision: 1, Backend: "local", Locator: "local://bob/google/1", Keys: []string{"GOOGLE_TOKEN"}, Status: api.ActiveStatus}
	if err := store.PutCredentialReference(credential); err != nil {
		t.Fatal(err)
	}
	connection := api.Connection{Schema: api.SchemaVersion, ConnectionID: "bob-work", Owner: api.OwnerRef{Type: api.PrincipalOwner, ID: "bob"}, DefinitionID: "google-work", CredentialRefID: credential.CredentialRefID, Revision: 1, Status: api.ActiveStatus, Metadata: map[string]string{"account": "bob@example.com"}}
	if err := store.PutConnection(connection); err != nil {
		t.Fatal(err)
	}
	if err := putGrant(t, store, api.OperatorGrant(api.GrantDefinition, bob.PrincipalID, "google-work", "1.0.0")); err != nil {
		t.Fatal(err)
	}
	bobBinding := api.ToolBinding{Schema: api.SchemaVersion, PrincipalID: "bob", ContextID: "bob", RuntimeID: "runtime-bob", DefinitionID: "google-work", DefinitionVersion: "1.0.0", ConnectionID: "bob-work", ConnectionRevision: 1, CredentialRefID: credential.CredentialRefID, CredentialRevision: 1, PolicyVersion: bob.PolicyVersion, WorkloadClass: api.PerUser, Status: api.ActiveStatus, Revision: 1, ProjectionRevision: 1}
	if err := store.PutBinding(bobBinding); err != nil {
		t.Fatal(err)
	}

	entered := make(chan struct{})
	release := make(chan struct{})
	var calls atomic.Int32
	g := &Gateway{Store: store, AuditWrite: testAuditWriter(t), Backend: backendFunc(func(context.Context, api.EffectiveBinding, api.ToolSpec, map[string]any) (api.BackendResult, error) {
		if calls.Add(1) == 1 {
			close(entered)
			<-release
		}
		return api.BackendResult{Text: "sent"}, nil
	})}
	name := api.ProjectedToolName("google-work", "1.0.0", "send")
	first := make(chan error, 1)
	go func() {
		_, err := g.call(context.Background(), auth, name, map[string]any{"message": "a"})
		first <- err
	}()
	<-entered
	// A revocation lands while A's effect is still in flight.
	revoked := make(chan error, 1)
	go func() { revoked <- store.SetBindingStatus(binding.ToolBindingID, api.RevokedStatus) }()
	select {
	case err := <-revoked:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("revocation queued behind in-flight backend")
	}
	// B's independent admission and call complete on their own binding.
	second := make(chan error, 1)
	go func() {
		_, err := g.call(context.Background(), bob, name, map[string]any{"message": "b"})
		second <- err
	}()
	select {
	case err := <-second:
		if err != nil {
			t.Fatalf("independent call: %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("independent call starved by in-flight backend")
	}
	close(release)
	if err := <-first; err != nil {
		t.Fatal(err)
	}
}

// The dispatch-time recheck is the last gate before the audit admission: a
// binding revoked between the projected admission and execution fails closed.
func TestRevocationBetweenAdmissionAndDispatchDenied(t *testing.T) {
	store, auth, binding := seededStore(t)
	effective, err := store.Resolve(auth, binding.ToolBindingID)
	if err != nil {
		t.Fatal(err)
	}
	if err := store.SetBindingStatus(binding.ToolBindingID, api.RevokedStatus); err != nil {
		t.Fatal(err)
	}
	if err := store.ReverifyEffective(effective); !errors.Is(err, api.ErrUnauthorized) {
		t.Fatalf("stale authority dispatched: %v", err)
	}
}
