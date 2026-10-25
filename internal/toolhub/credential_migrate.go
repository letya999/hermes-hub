package toolhub

import (
	"context"
	"fmt"

	brokerv1 "github.com/letya999/credential-broker/api/v1"
	"github.com/letya999/hermes-hub/internal/credstore"
	"github.com/letya999/hermes-hub/internal/toolhub/api"
)

// MigrateBroker is the broker control surface a migration run needs: mint a
// credential request and later poll it. The owner still submits secret values
// through the reviewed connect link — the control plane never moves secrets.
type MigrateBroker interface {
	CreateRequest(ctx context.Context, in brokerv1.CreateRequest) (brokerv1.Request, error)
	Request(ctx context.Context, id string) (brokerv1.Request, error)
}

// MigrateBrokerFor builds the owner-scoped control client for a connection.
type MigrateBrokerFor func(conn api.Connection) (MigrateBroker, error)

// MigrateEntry is one per-reference migration outcome in a run report.
type MigrateEntry struct {
	ConnectionID     string `json:"connection_id"`
	CredentialRefID  string `json:"credential_ref"`
	Outcome          string `json:"outcome"`
	BrokerRequestID  string `json:"broker_request_id,omitempty"`
	AuthorizationURL string `json:"authorization_url,omitempty"`
}

// MigrateReport summarizes one migrate pass; the CLI prints it as JSON.
type MigrateReport struct {
	Entries []MigrateEntry `json:"entries"`
}

// MigrateCredentials re-anchors local credential references onto Credential
// Broker credentials for contract-bound definitions. Phase one mints a broker
// request per reference (`request_pending`); a later pass rewrites the
// reference once the owner submitted through the reviewed link.
func MigrateCredentials(ctx context.Context, s *Store, brokerFor MigrateBrokerFor) (MigrateReport, error) {
	report := MigrateReport{}
	for _, connection := range s.Connections() {
		if connection.CredentialRefID == "" {
			continue
		}
		reference, ok := s.credentialFor(connection.CredentialRefID)
		if !ok || reference.Backend != credstore.BackendLocal {
			continue
		}
		definition, err := s.definitionForConnection(connection)
		if err != nil || definition.CredentialContractID == "" {
			continue
		}
		broker, err := brokerFor(connection)
		if err != nil {
			return MigrateReport{}, err
		}
		if reference.MigrateRequestID != "" {
			entry, err := s.pollMigrateRequest(ctx, connection, reference, definition, broker)
			if err != nil {
				return MigrateReport{}, err
			}
			report.Entries = append(report.Entries, entry)
			continue
		}
		request, err := broker.CreateRequest(ctx, brokerv1.CreateRequest{
			ContractID: definition.CredentialContractID, ContractRevision: definition.CredentialContractRevision,
			ConnectionID: connection.ConnectionID, ConsumerID: definition.DefinitionID,
			OwnerKind: "user", IdempotencyKey: api.DeterministicID("migrate", reference.CredentialRefID, fmt.Sprint(reference.Revision)),
		})
		if err != nil {
			return MigrateReport{}, err
		}
		if err := s.setMigrateRequest(reference.CredentialRefID, request.ID); err != nil {
			return MigrateReport{}, err
		}
		report.Entries = append(report.Entries, MigrateEntry{ConnectionID: connection.ConnectionID, CredentialRefID: reference.CredentialRefID, Outcome: "request_pending", BrokerRequestID: request.ID, AuthorizationURL: request.AuthorizationURL})
	}
	return report, nil
}

// pollMigrateRequest resolves a pending broker request. A ready request
// anchors the connection on a fresh credential reference bound to the minted
// broker credential, with the legacy locator preserved for rollback.
func (s *Store) pollMigrateRequest(ctx context.Context, connection api.Connection, reference api.CredentialReference, definition api.ToolDefinition, broker MigrateBroker) (MigrateEntry, error) {
	entry := MigrateEntry{ConnectionID: connection.ConnectionID, CredentialRefID: reference.CredentialRefID, Outcome: "request_pending", BrokerRequestID: reference.MigrateRequestID}
	request, err := broker.Request(ctx, reference.MigrateRequestID)
	if err != nil {
		return entry, err
	}
	if request.Status != "ready" || request.CredentialID == "" {
		return entry, nil
	}
	next := connection.Revision + 1
	anchored := api.CredentialReference{
		Schema: api.SchemaVersion, CredentialRefID: api.CredentialReferenceID(connection.ConnectionID, next),
		ConnectionID: connection.ConnectionID, Revision: next,
		Backend: "credential-broker", Locator: request.CredentialID, Keys: append([]string(nil), reference.Keys...),
		BrokerContractID: definition.CredentialContractID, BrokerContractRevision: definition.CredentialContractRevision,
		BrokerEnv: api.CloneMap(definition.CredentialContractEnv), LegacyLocator: reference.Locator, Status: api.ActiveStatus,
	}
	if _, err := s.RotateCredentialRecord(anchored); err != nil {
		return entry, err
	}
	entry.CredentialRefID = anchored.CredentialRefID
	entry.Outcome = "anchored"
	return entry, nil
}

// RollbackCredentials flips every broker-anchored migrated reference back to
// its preserved legacy locator — the legacy record is never deleted by
// migration, so the legacy reader keeps working after the flip.
func RollbackCredentials(ctx context.Context, s *Store) (MigrateReport, error) {
	report := MigrateReport{}
	for _, connection := range s.Connections() {
		if connection.CredentialRefID == "" {
			continue
		}
		reference, ok := s.credentialFor(connection.CredentialRefID)
		if !ok || reference.Backend != "credential-broker" || reference.LegacyLocator == "" {
			continue
		}
		next := connection.Revision + 1
		restored := api.CredentialReference{
			Schema: api.SchemaVersion, CredentialRefID: api.CredentialReferenceID(connection.ConnectionID, next),
			ConnectionID: connection.ConnectionID, Revision: next,
			Backend: credstore.BackendLocal, Locator: reference.LegacyLocator,
			Keys: append([]string(nil), reference.Keys...), Status: api.ActiveStatus,
		}
		if _, err := s.RotateCredentialRecord(restored); err != nil {
			return MigrateReport{}, err
		}
		report.Entries = append(report.Entries, MigrateEntry{ConnectionID: connection.ConnectionID, CredentialRefID: restored.CredentialRefID, Outcome: "rolled_back"})
	}
	return report, nil
}
