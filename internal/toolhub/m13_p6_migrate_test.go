package toolhub

import (
	"context"
	"testing"

	brokerv1 "github.com/letya999/credential-broker/api/v1"
	"github.com/letya999/hermes-hub/internal/toolhub/api"
)

// S6.5: `hubctl credentials migrate` is a two-phase re-anchoring pass. On the
// first pass a local-backed credential reference whose definition is bound to
// a reviewed broker contract mints a broker request (the owner still submits
// through the reviewed link — secrets never travel over the control plane),
// and the reference keeps reading the legacy store until the request resolves.
func TestMigrateCredentialsMintsBrokerRequest(t *testing.T) {
	store := NewStore()
	definition := remoteDefinition()
	definition.CredentialContractID = "google-token"
	definition.CredentialContractRevision = 1
	definition.CredentialContractEnv = map[string]string{"GOOGLE_TOKEN": "GOOGLE_TOKEN"}
	if err := store.RegisterDefinition(definition); err != nil {
		t.Fatal(err)
	}
	reference := api.CredentialReference{Schema: api.SchemaVersion, CredentialRefID: api.CredentialReferenceID("google-work", 1), ConnectionID: "google-work", Revision: 1, Backend: "local", Locator: "local://alice/google/1", Keys: []string{"GOOGLE_TOKEN"}, Status: api.ActiveStatus}
	if err := store.PutCredentialReference(reference); err != nil {
		t.Fatal(err)
	}
	connection := api.Connection{Schema: api.SchemaVersion, ConnectionID: "google-work", Owner: api.OwnerRef{Type: api.PrincipalOwner, ID: "alice"}, DefinitionID: definition.DefinitionID, CredentialRefID: reference.CredentialRefID, Revision: 1, Status: api.ActiveStatus}
	if err := store.PutConnection(connection); err != nil {
		t.Fatal(err)
	}
	sink := &fakeMigrateBroker{request: brokerv1.Request{ID: "req-1", Status: "pending", AuthorizationURL: "https://broker/connect/req-1"}}
	report, err := MigrateCredentials(context.Background(), store, func(api.Connection) (MigrateBroker, error) { return sink, nil })
	if err != nil {
		t.Fatal(err)
	}
	if len(report.Entries) != 1 || report.Entries[0].Outcome != "request_pending" || report.Entries[0].BrokerRequestID != "req-1" {
		t.Fatalf("unexpected report: %+v", report.Entries)
	}
	migrated, ok := store.credentialFor(reference.CredentialRefID)
	if !ok || migrated.Backend != "local" || migrated.MigrateRequestID != "req-1" || migrated.Locator != reference.Locator {
		t.Fatalf("reference moved before the owner submitted: %+v", migrated)
	}
	if sink.created.ContractID != "google-token" || sink.created.ConnectionID != "google-work" {
		t.Fatalf("request not bound to the connection contract: %+v", sink.created)
	}
}

// A later pass after the owner submitted anchors the reference: the broker
// credential id replaces the locator, the legacy locator is preserved for
// rollback, and the projection picks up the new reference.
func TestMigrateCredentialsAnchorsReadyRequest(t *testing.T) {
	store := NewStore()
	definition := remoteDefinition()
	definition.CredentialContractID = "google-token"
	definition.CredentialContractRevision = 1
	definition.CredentialContractEnv = map[string]string{"GOOGLE_TOKEN": "GOOGLE_TOKEN"}
	if err := store.RegisterDefinition(definition); err != nil {
		t.Fatal(err)
	}
	reference := api.CredentialReference{Schema: api.SchemaVersion, CredentialRefID: api.CredentialReferenceID("google-work", 1), ConnectionID: "google-work", Revision: 1, Backend: "local", Locator: "local://alice/google/1", Keys: []string{"GOOGLE_TOKEN"}, Status: api.ActiveStatus}
	if err := store.PutCredentialReference(reference); err != nil {
		t.Fatal(err)
	}
	connection := api.Connection{Schema: api.SchemaVersion, ConnectionID: "google-work", Owner: api.OwnerRef{Type: api.PrincipalOwner, ID: "alice"}, DefinitionID: definition.DefinitionID, CredentialRefID: reference.CredentialRefID, Revision: 1, Status: api.ActiveStatus}
	if err := store.PutConnection(connection); err != nil {
		t.Fatal(err)
	}
	if err := store.setMigrateRequest(reference.CredentialRefID, "req-1"); err != nil {
		t.Fatal(err)
	}
	sink := &fakeMigrateBroker{request: brokerv1.Request{ID: "req-1", Status: "ready", CredentialID: "cred-broker-1"}}
	report, err := MigrateCredentials(context.Background(), store, func(api.Connection) (MigrateBroker, error) { return sink, nil })
	if err != nil {
		t.Fatal(err)
	}
	if len(report.Entries) != 1 || report.Entries[0].Outcome != "anchored" {
		t.Fatalf("unexpected report: %+v", report.Entries)
	}
	current, ok := store.connectionFor("google-work")
	if !ok || current.CredentialRefID == reference.CredentialRefID {
		t.Fatalf("connection did not re-anchor: %+v", current)
	}
	anchored, ok := store.credentialFor(current.CredentialRefID)
	if !ok || anchored.Backend != "credential-broker" || anchored.Locator != "cred-broker-1" || anchored.LegacyLocator != "local://alice/google/1" || anchored.BrokerContractID != "google-token" {
		t.Fatalf("anchored reference lost lineage: %+v", anchored)
	}
	old, ok := store.credentialFor(reference.CredentialRefID)
	if !ok || old.Status != api.RevokedStatus {
		t.Fatalf("legacy reference must be revoked: %+v", old)
	}
}

// Rollback keeps the legacy reader: an anchored reference flips back to the
// preserved legacy locator and backend, and the legacy record itself is never
// deleted by migration.
func TestRollbackCredentialsRestoresLegacyLocator(t *testing.T) {
	store := NewStore()
	definition := remoteDefinition()
	definition.CredentialContractID = "google-token"
	definition.CredentialContractRevision = 1
	definition.CredentialContractEnv = map[string]string{"GOOGLE_TOKEN": "GOOGLE_TOKEN"}
	if err := store.RegisterDefinition(definition); err != nil {
		t.Fatal(err)
	}
	reference := api.CredentialReference{Schema: api.SchemaVersion, CredentialRefID: api.CredentialReferenceID("google-work", 1), ConnectionID: "google-work", Revision: 1, Backend: "local", Locator: "local://alice/google/1", Keys: []string{"GOOGLE_TOKEN"}, Status: api.ActiveStatus}
	if err := store.PutCredentialReference(reference); err != nil {
		t.Fatal(err)
	}
	connection := api.Connection{Schema: api.SchemaVersion, ConnectionID: "google-work", Owner: api.OwnerRef{Type: api.PrincipalOwner, ID: "alice"}, DefinitionID: definition.DefinitionID, CredentialRefID: reference.CredentialRefID, Revision: 1, Status: api.ActiveStatus}
	if err := store.PutConnection(connection); err != nil {
		t.Fatal(err)
	}
	sink := &fakeMigrateBroker{request: brokerv1.Request{ID: "req-1", Status: "ready", CredentialID: "cred-broker-1"}}
	if _, err := MigrateCredentials(context.Background(), store, func(api.Connection) (MigrateBroker, error) { return sink, nil }); err != nil {
		t.Fatal(err)
	}
	if _, err := MigrateCredentials(context.Background(), store, func(api.Connection) (MigrateBroker, error) { return sink, nil }); err != nil {
		t.Fatal(err)
	}
	report, err := RollbackCredentials(context.Background(), store)
	if err != nil {
		t.Fatal(err)
	}
	if len(report.Entries) != 1 || report.Entries[0].Outcome != "rolled_back" {
		t.Fatalf("unexpected report: %+v", report.Entries)
	}
	current, ok := store.connectionFor("google-work")
	if !ok {
		t.Fatal("connection gone")
	}
	restored, ok := store.credentialFor(current.CredentialRefID)
	if !ok || restored.Backend != "local" || restored.Locator != "local://alice/google/1" {
		t.Fatalf("rollback did not restore the legacy locator: %+v", restored)
	}
}

type fakeMigrateBroker struct {
	created    brokerv1.CreateRequest
	request    brokerv1.Request
	requestErr error
}

func (f *fakeMigrateBroker) CreateRequest(_ context.Context, in brokerv1.CreateRequest) (brokerv1.Request, error) {
	f.created = in
	return f.request, nil
}

func (f *fakeMigrateBroker) Request(_ context.Context, _ string) (brokerv1.Request, error) {
	return f.request, f.requestErr
}
