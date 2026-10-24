package toolhub

import (
	"strings"
	"testing"
	"time"

	"github.com/letya999/hermes-hub/internal/toolhub/api"
)

func TestLegacyStatusKeepsMissingAndForeignConnectionStateUnknown(t *testing.T) {
	store, auth, _, _ := managedStore(t)
	onboarding := m13P3EnabledOnboarding(t, store, auth)
	foreign := api.Connection{Schema: api.SchemaVersion, ConnectionID: "foreign-connection", DefinitionID: onboarding.DefinitionID,
		Owner: api.OwnerRef{Type: api.PrincipalOwner, ID: "bob"}, Revision: 1, Status: api.DegradedStatus,
		CredentialVisibility: "unknown", HealthCause: "credential_visibility_unknown", CredentialCheckedAt: time.Now().UTC()}
	if err := store.PutConnection(foreign); err != nil {
		t.Fatal(err)
	}
	for _, id := range []string{"missing-connection", foreign.ConnectionID} {
		onboarding.ConnectionID = id
		body := (&ControlPlane{Store: store}).statusBody(onboarding, false)
		if _, present := body["connection_status"]; present {
			t.Fatalf("unavailable connection state disclosed or invented for %s", id)
		}
		if _, present := body["credential_visibility"]; present {
			t.Fatalf("foreign credential evidence disclosed for %s", id)
		}
		if body["next_action"] == "ready" {
			t.Fatalf("unavailable connection %s reported ready", id)
		}
	}
}

func TestLegacyEnabledStatusDoesNotClaimProviderReadiness(t *testing.T) {
	for _, state := range []api.Status{api.ActiveStatus, api.DegradedStatus, api.DisabledStatus, api.RevokedStatus, api.ReauthStatus, api.DeletingStatus} {
		store, auth, _, _ := managedStore(t)
		onboarding := m13P3EnabledOnboarding(t, store, auth)
		var err error
		if state == api.DegradedStatus {
			err = store.MarkDegraded(onboarding.ConnectionID)
		} else {
			err = store.SetConnectionStatus(onboarding.ConnectionID, state)
		}
		if err != nil {
			t.Fatal(err)
		}
		body := (&ControlPlane{Store: store}).statusBody(onboarding, false)
		if body["phase"] != api.PhaseEnabled || body["connection_status"] != string(state) {
			t.Errorf("state=%s: onboarding and connection facts conflated: phase=%v connection_status=%v", state, body["phase"], body["connection_status"])
		}
		if body["next_action"] != "diagnostics" {
			t.Errorf("state=%s: enabled onboarding is not evidence of provider readiness: next_action=%v", state, body["next_action"])
		}
	}
}

func TestLegacyStatusCarriesScopedCredentialEvidence(t *testing.T) {
	store, auth, _, _ := managedStore(t)
	onboarding := m13P3EnabledOnboarding(t, store, auth)
	connection := store.connections[onboarding.ConnectionID]
	connection.CredentialVisibility = "unknown"
	connection.HealthCause = "credential_visibility_unknown"
	connection.CredentialCheckedAt = time.Date(2026, 10, 23, 12, 0, 0, 0, time.UTC)
	store.connections[connection.ConnectionID] = connection
	body := (&ControlPlane{Store: store}).statusBody(onboarding, false)
	if body["credential_visibility"] != connection.CredentialVisibility || body["health_cause"] != connection.HealthCause || body["credential_checked_at"] != connection.CredentialCheckedAt {
		t.Fatalf("legacy status omitted credential evidence: %+v", body)
	}
}

func TestConnectorStatusReflectsDegradedConnection(t *testing.T) {
	store, auth, _, profile := managedStore(t)
	profile.Revision++
	profile.Connectors = api.ConnectorsPersonal
	if err := putProfile(t, store, profile); err != nil {
		t.Fatal(err)
	}
	onboarding := m13P3Onboarding(auth, "degraded-status", "google-work", "", api.PhaseEnabled)
	onboarding.ConnectionID = "google-work"
	if err := store.PutOnboarding(onboarding); err != nil {
		t.Fatal(err)
	}
	if err := store.MarkDegraded(onboarding.ConnectionID); err != nil {
		t.Fatal(err)
	}
	c := &ControlPlane{Store: store, Now: time.Now}
	op := c.newConnectorOperation(auth, api.OpIntentInstall, "google-work", connectorInstallSteps)
	op.OnboardingID, op.State = onboarding.OnboardingID, api.OpStateDone
	if err := store.PutOperation(op); err != nil {
		t.Fatal(err)
	}
	for _, args := range []map[string]any{{"target": "google-work"}, {"operation_id": op.OperationID}} {
		result, err := c.InvokeConnector(t.Context(), auth, "connector_status", args)
		if err != nil {
			t.Fatal(err)
		}
		if result.State == api.OpStateDone || !strings.Contains(result.Summary, "degraded") {
			t.Errorf("unhealthy connector reported ready: %+v", result)
		}
	}
}
