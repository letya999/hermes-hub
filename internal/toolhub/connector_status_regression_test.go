package toolhub

import (
	"errors"
	"strings"
	"sync"
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
		if result.State == api.ModelStateDone || !strings.Contains(result.Summary, "degraded") {
			t.Errorf("unhealthy connector reported ready: %+v", result)
		}
	}
}

func TestConnectorStatusAcceptsOperationIDInName(t *testing.T) {
	store, auth, _, profile := managedStore(t)
	profile.Revision++
	profile.Connectors = api.ConnectorsPersonal
	if err := putProfile(t, store, profile); err != nil {
		t.Fatal(err)
	}
	c := &ControlPlane{Store: store, Now: time.Now}
	op := c.newConnectorOperation(auth, api.OpIntentInstall, "example", connectorInstallSteps)
	if err := store.PutOperation(op); err != nil {
		t.Fatal(err)
	}
	result, err := c.InvokeConnector(t.Context(), auth, "connector_status", map[string]any{"name": op.OperationID})
	if err != nil || result.OperationID != op.OperationID || result.State != api.ModelStateWorking {
		t.Fatalf("status by operation id: result=%+v err=%v", result, err)
	}
}

func TestConnectorStatusReadsListedOldPolicyOnboarding(t *testing.T) {
	store, auth, _, profile := managedStore(t)
	profile.Revision++
	profile.Connectors = api.ConnectorsPersonal
	if err := putProfile(t, store, profile); err != nil {
		t.Fatal(err)
	}
	onboarding := m13P3Onboarding(auth, "old-policy-status", "github-mcp-server", "", api.PhaseAwaitingConfirm)
	onboarding.PolicyVersion = "policy-old"
	if err := store.PutOnboarding(onboarding); err != nil {
		t.Fatal(err)
	}
	c := &ControlPlane{Store: store, Now: time.Now}
	listed, err := c.InvokeConnector(t.Context(), auth, "connector_list", nil)
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, candidate := range listed.Candidates {
		found = found || candidate.ID == onboarding.OnboardingID
	}
	if !found {
		t.Fatal("old-policy onboarding absent from owner list")
	}
	status, err := c.InvokeConnector(t.Context(), auth, "connector_status", map[string]any{"name": onboarding.OnboardingID})
	if err != nil || status.State != api.ModelStateWaitingYou {
		t.Fatalf("listed onboarding unreadable: status=%+v err=%v", status, err)
	}
}

func TestConnectorRemoveWithoutCurrentOnboardingIsDefinitive(t *testing.T) {
	store, auth, _, _ := managedStore(t)
	c := &ControlPlane{Store: store, Now: time.Now}
	result, err := c.connectorRemove(t.Context(), auth, map[string]any{"target": "github-mcp-server"})
	if err != nil || result.State != api.ModelStateBroken || result.Code != api.OpCodeNotPermitted {
		t.Fatalf("missing onboarding was uncertain: result=%+v err=%v", result, err)
	}
	if _, exists := store.OperationForRequestKey(auth, api.OpIntentRemove, "github-mcp-server"); exists {
		t.Fatal("remove journal appeared without resolving a target")
	}
}

func TestConnectorRemoveReclaimsOldPolicyOwnedConnection(t *testing.T) {
	store, auth, _, _ := managedStore(t)
	onboarding := m13P3EnabledOnboarding(t, store, auth)
	onboarding.PolicyVersion = "old-policy"
	onboarding.Revision++
	// Import an old deployment record; normal writes cannot change ownership.
	store.mu.Lock()
	store.onboardings[onboarding.OnboardingID] = cloneOnboarding(onboarding)
	store.mu.Unlock()
	c := &ControlPlane{Store: store, Now: time.Now}
	result, err := c.connectorRemove(t.Context(), auth, map[string]any{"target": onboarding.ConnectionID})
	if err != nil || result.State != api.ModelStateWaitingYou || noticeDelivered(result) {
		t.Fatalf("owned stale connection did not arm owner confirmation: %+v %v", result, err)
	}
	claimed, err := store.OnboardingFor(auth, onboarding.OnboardingID)
	if err != nil || claimed.PolicyVersion != auth.PolicyVersion || claimed.GateNonce(api.GateRemove) == "" || claimed.Removed() {
		t.Fatalf("unsafe or incomplete claim: %+v %v", claimed, err)
	}
	connection, ok := store.connectionFor(onboarding.ConnectionID)
	if !ok || connection.Status != api.ActiveStatus {
		t.Fatalf("pre-confirmation connection changed: %+v", connection)
	}
}

func TestConnectorRemoveReclaimsFailedOrphanOnboarding(t *testing.T) {
	store, auth, _, _ := managedStore(t)
	onboarding := m13P3Onboarding(auth, "orphan", "google-work", "", api.PhaseFailed)
	onboarding.PolicyVersion = "old-policy"
	if err := store.PutOnboarding(onboarding); err != nil {
		t.Fatal(err)
	}
	c := &ControlPlane{Store: store, Now: time.Now}
	result, err := c.connectorRemove(t.Context(), auth, map[string]any{"target": onboarding.OnboardingID})
	if err != nil || result.State != api.ModelStateWaitingYou {
		t.Fatalf("failed orphan not removable: %+v %v", result, err)
	}
	claimed, err := store.OnboardingFor(auth, onboarding.OnboardingID)
	if err != nil || claimed.PolicyVersion != auth.PolicyVersion {
		t.Fatalf("orphan not scoped to current owner: %+v %v", claimed, err)
	}
}

func TestConnectorRemoveCannotClaimForeignStaleOnboarding(t *testing.T) {
	store, auth, _, _ := managedStore(t)
	foreign := auth
	foreign.PrincipalID = "bob"
	onboarding := m13P3Onboarding(foreign, "foreign-orphan", "google-work", "", api.PhaseFailed)
	onboarding.PolicyVersion = "old-policy"
	if err := store.PutOnboarding(onboarding); err != nil {
		t.Fatal(err)
	}
	c := &ControlPlane{Store: store, Now: time.Now}
	result, err := c.connectorRemove(t.Context(), auth, map[string]any{"target": onboarding.OnboardingID})
	if err != nil || result.State != api.ModelStateBroken {
		t.Fatalf("foreign stale record was claimable: %+v %v", result, err)
	}
	if got := store.onboardings[onboarding.OnboardingID]; got.PolicyVersion != "old-policy" || got.GateNonce(api.GateRemove) != "" {
		t.Fatalf("foreign record changed: %+v", got)
	}
}

func TestConnectorListShowsOwnedOldPolicyOrphanForCleanup(t *testing.T) {
	store, auth, _, _ := managedStore(t)
	onboarding := m13P3Onboarding(auth, "orphan-visible", "google-work", "", api.PhaseFailed)
	onboarding.PolicyVersion = "old-policy"
	if err := store.PutOnboarding(onboarding); err != nil {
		t.Fatal(err)
	}
	c := &ControlPlane{Store: store, Now: time.Now}
	listed, err := c.connectorList(auth)
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, candidate := range listed.Candidates {
		if candidate.ID == onboarding.OnboardingID {
			found = candidate.State == api.ModelStateBroken
		}
	}
	if !found {
		t.Fatal("old-policy orphan missing from owner's inventory")
	}
	status, err := c.connectorStatus(t.Context(), auth, onboarding.OnboardingID, "")
	if err != nil || len(status.Candidates) != 1 || status.Candidates[0].ID != onboarding.OnboardingID {
		t.Fatalf("stale onboarding status: %+v %v", status, err)
	}
}

func TestRemoveOwnedConnectionRecordsWithoutBinding(t *testing.T) {
	store, auth, _, _ := managedStore(t)
	connection := api.Connection{Schema: api.SchemaVersion, ConnectionID: "orphan-connection", DefinitionID: "google-work",
		Owner: api.OwnerRef{Type: api.PrincipalOwner, ID: auth.PrincipalID}, Revision: 1, Status: api.ActiveStatus}
	if err := store.PutConnection(connection); err != nil {
		t.Fatal(err)
	}
	if err := store.RemoveOwnedConnectionRecords(auth, connection.ConnectionID); !errors.Is(err, api.ErrUnauthorized) {
		t.Fatalf("active connection removed without confirmation: %v", err)
	}
	connection.Status = api.DeletingStatus
	if err := store.SetConnectionStatus(connection.ConnectionID, api.DeletingStatus); err != nil {
		t.Fatal(err)
	}
	other := auth
	other.PrincipalID = "bob"
	if err := store.RemoveOwnedConnectionRecords(other, connection.ConnectionID); !errors.Is(err, api.ErrUnauthorized) {
		t.Fatalf("foreign principal removed connection: %v", err)
	}
	if err := store.RemoveOwnedConnectionRecords(auth, connection.ConnectionID); err != nil {
		t.Fatal(err)
	}
	if store.HasConnection(connection.ConnectionID) {
		t.Fatal("owner's bindingless connection survived confirmed cleanup")
	}
}

func TestConnectorRemoveBindinglessOwnedConnectionEndToEnd(t *testing.T) {
	store, auth, _, _ := managedStore(t)
	connection := api.Connection{Schema: api.SchemaVersion, ConnectionID: "lost-binding-connection", DefinitionID: "google-work",
		Owner: api.OwnerRef{Type: api.PrincipalOwner, ID: auth.PrincipalID}, Revision: 1, Status: api.ActiveStatus}
	if err := store.PutConnection(connection); err != nil {
		t.Fatal(err)
	}
	c := &ControlPlane{Store: store, Now: time.Now, WorkloadRoot: t.TempDir()}
	result, err := c.connectorRemove(t.Context(), auth, map[string]any{"target": connection.ConnectionID})
	if err != nil || result.State != api.ModelStateWaitingYou {
		t.Fatalf("bindingless owner cannot arm cleanup: %+v %v", result, err)
	}
	op, ok := store.OperationForRequestKey(auth, api.OpIntentRemove, connection.ConnectionID)
	if !ok || op.OnboardingID == "" {
		t.Fatalf("cleanup operation missing: %+v", op)
	}
	onboarding, err := store.OnboardingFor(auth, op.OnboardingID)
	if err != nil || onboarding.GateNonce(api.GateRemove) == "" {
		t.Fatalf("protected confirmation missing: %+v %v", onboarding, err)
	}
	if _, err := c.remove(t.Context(), auth, map[string]any{"onboarding_id": onboarding.OnboardingID, "remove_nonce": onboarding.GateNonce(api.GateRemove)}); err != nil {
		t.Fatal(err)
	}
	onboarding, err = store.OnboardingFor(auth, op.OnboardingID)
	if err != nil || !onboarding.Removed() || store.HasConnection(connection.ConnectionID) {
		t.Fatalf("bindingless cleanup incomplete: %+v %v", onboarding, err)
	}
}

func TestConnectorRemoveRefreshesExpiredOwnerLink(t *testing.T) {
	store, auth, _, _ := managedStore(t)
	onboarding := m13P3EnabledOnboarding(t, store, auth)
	clock := time.Now().UTC()
	c := &ControlPlane{Store: store, Now: func() time.Time { return clock }, ConfirmationTTL: time.Minute}
	if _, err := c.connectorRemove(t.Context(), auth, map[string]any{"target": onboarding.ConnectionID}); err != nil {
		t.Fatal(err)
	}
	first, err := store.OnboardingFor(auth, onboarding.OnboardingID)
	if err != nil {
		t.Fatal(err)
	}
	clock = clock.Add(2 * time.Minute)
	if _, err := c.connectorRemove(t.Context(), auth, map[string]any{"target": onboarding.ConnectionID}); err != nil {
		t.Fatal(err)
	}
	second, err := store.OnboardingFor(auth, onboarding.OnboardingID)
	if err != nil || first.GateNonce(api.GateRemove) == second.GateNonce(api.GateRemove) || second.GateNonce(api.GateRemove) == "" {
		t.Fatalf("expired owner link was not refreshed: %v", err)
	}
	if _, err := c.remove(t.Context(), auth, map[string]any{"onboarding_id": onboarding.OnboardingID, "remove_nonce": first.GateNonce(api.GateRemove)}); !errors.Is(err, api.ErrUnauthorized) {
		t.Fatalf("old remove nonce accepted: %v", err)
	}
}

func TestConnectorRemoveDeniesTeamConnectionBeforeConfirmation(t *testing.T) {
	store, auth, _, _ := managedStore(t)
	connection := api.Connection{Schema: api.SchemaVersion, ConnectionID: "team-connection", DefinitionID: "google-work",
		Owner: api.OwnerRef{Type: api.TeamOwner, ID: "team-one"}, Revision: 1, Status: api.ActiveStatus}
	if err := store.PutConnection(connection); err != nil {
		t.Fatal(err)
	}
	onboarding := m13P3Onboarding(auth, "team-remove", "google-work", "", api.PhaseEnabled)
	onboarding.ConnectionID = connection.ConnectionID
	if err := store.PutOnboarding(onboarding); err != nil {
		t.Fatal(err)
	}
	c := &ControlPlane{Store: store, Now: time.Now}
	result, err := c.connectorRemove(t.Context(), auth, map[string]any{"target": onboarding.OnboardingID})
	if err != nil || result.Code != api.OpCodeNotPermitted || noticeDelivered(result) {
		t.Fatalf("team connection cleanup armed: %+v %v", result, err)
	}
	got, _ := store.OnboardingFor(auth, onboarding.OnboardingID)
	if got.GateNonce(api.GateRemove) != "" || !store.HasConnection(connection.ConnectionID) {
		t.Fatal("team connection changed before owner authorization")
	}
}

func TestClaimRemovalTargetConcurrentRetriesUseOneRecord(t *testing.T) {
	store, auth, _, _ := managedStore(t)
	connection := api.Connection{Schema: api.SchemaVersion, ConnectionID: "concurrent-cleanup", DefinitionID: "google-work",
		Owner: api.OwnerRef{Type: api.PrincipalOwner, ID: auth.PrincipalID}, Revision: 1, Status: api.ActiveStatus}
	if err := store.PutConnection(connection); err != nil {
		t.Fatal(err)
	}
	ids := make(chan string, 12)
	errCh := make(chan error, 12)
	var group sync.WaitGroup
	for range 12 {
		group.Add(1)
		go func() {
			defer group.Done()
			resolved, err := store.claimRemovalTarget(auth, connection.ConnectionID)
			if err != nil {
				errCh <- err
				return
			}
			ids <- resolved.OnboardingID
		}()
	}
	group.Wait()
	close(ids)
	close(errCh)
	for err := range errCh {
		t.Fatal(err)
	}
	first := ""
	for id := range ids {
		if first == "" {
			first = id
		} else if id != first {
			t.Fatalf("concurrent cleanup selected multiple records: %s, %s", first, id)
		}
	}
	if first == "" || len(store.onboardings) != 1 {
		t.Fatal("cleanup claim was not durable and unique")
	}
}

func TestLegacyRemoveCannotPurgeForeignPersonalConnection(t *testing.T) {
	store, auth, _, _ := managedStore(t)
	connection := api.Connection{Schema: api.SchemaVersion, ConnectionID: "foreign-personal-connection", DefinitionID: "google-work",
		Owner: api.OwnerRef{Type: api.PrincipalOwner, ID: "bob"}, Revision: 1, Status: api.ActiveStatus}
	if err := store.PutConnection(connection); err != nil {
		t.Fatal(err)
	}
	onboarding := m13P3Onboarding(auth, "foreign-personal-remove", "google-work", "", api.PhaseEnabled)
	onboarding.ConnectionID = connection.ConnectionID
	if err := store.PutOnboarding(onboarding); err != nil {
		t.Fatal(err)
	}
	if !store.sharedForeignConnection(auth.PrincipalID, connection.ConnectionID) {
		t.Fatal("foreign personal connection not fenced from member cleanup")
	}
	c := &ControlPlane{Store: store, Now: time.Now, WorkloadRoot: t.TempDir()}
	if _, err := c.remove(t.Context(), auth, map[string]any{"onboarding_id": onboarding.OnboardingID}); err != nil {
		t.Fatal(err)
	}
	armed, err := store.OnboardingFor(auth, onboarding.OnboardingID)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := c.remove(t.Context(), auth, map[string]any{"onboarding_id": onboarding.OnboardingID, "remove_nonce": armed.GateNonce(api.GateRemove)}); err != nil {
		t.Fatal(err)
	}
	got, ok := store.connectionFor(connection.ConnectionID)
	if !ok || got.Status != api.ActiveStatus {
		t.Fatalf("legacy member remove changed foreign connection: %+v", got)
	}
}

func TestConnectorManageVerbsReportStalePolicy(t *testing.T) {
	// Old-epoch records are dead states, not missing records: manage verbs
	// must report the stable stale_policy code and point at removal instead
	// of a bare not-found that invites blind retries.
	store, auth, _, profile := managedStore(t)
	profile.Revision++
	profile.Connectors = api.ConnectorsPersonal
	if err := putProfile(t, store, profile); err != nil {
		t.Fatal(err)
	}
	onboarding := m13P3EnabledOnboarding(t, store, auth)
	onboarding.PolicyVersion = "old-policy"
	onboarding.Revision++
	store.mu.Lock()
	store.onboardings[onboarding.OnboardingID] = cloneOnboarding(onboarding)
	store.mu.Unlock()
	c := &ControlPlane{Store: store, Now: time.Now}
	for _, tc := range []struct {
		tool string
		args map[string]any
	}{
		{"connector_disable", map[string]any{"target": onboarding.ConnectionID}},
		{"connector_reauth", map[string]any{"target": onboarding.ConnectionID}},
		{"connector_allow_writes", map[string]any{"target": onboarding.ConnectionID, "on": true}},
		{"connector_edit", map[string]any{"action": "resend", "target": onboarding.ConnectionID}},
		{"connector_edit", map[string]any{"action": "disable", "target": onboarding.OnboardingID}},
		{"connector_reauth", map[string]any{"target": onboarding.OnboardingID}},
	} {
		result, err := c.InvokeConnector(t.Context(), auth, tc.tool, tc.args)
		if err != nil || result.State != api.ModelStateBroken || result.Code != api.OpCodeStalePolicy || result.YouMust != api.YouMustRemove {
			t.Fatalf("%s on stale target: result=%+v err=%v", tc.tool, result, err)
		}
	}
}

func TestConnectorResendArmedRemovalReissuesRemoveLink(t *testing.T) {
	// An armed removal is the pending action: resend must re-issue the remove
	// link, not arm a setup confirmation that would undo the cleanup — and
	// once the nonce expired, resend re-mints it through the remove arm.
	store, auth, _, profile := managedStore(t)
	profile.Revision++
	profile.Connectors = api.ConnectorsPersonal
	if err := putProfile(t, store, profile); err != nil {
		t.Fatal(err)
	}
	m13P3EnabledOnboarding(t, store, auth)
	c := &ControlPlane{Store: store, Now: time.Now}
	failed := m13P3Onboarding(auth, "pw", "playwright-mcp", "https://github.com/microsoft/playwright-mcp", api.PhaseFailed)
	failed.ArmGate(api.GateRemove, api.RandomNonce(), time.Now().Add(time.Hour))
	owner := auth
	failed.Initiator = &owner
	if err := store.PutOnboarding(failed); err != nil {
		t.Fatal(err)
	}
	result, err := c.InvokeConnector(t.Context(), auth, "connector_edit", map[string]any{"action": "resend", "target": failed.OnboardingID})
	if err != nil || result.State != api.ModelStateWaitingYou || result.YouMust != api.YouMustClickLink {
		t.Fatalf("armed-removal resend: %+v err=%v", result, err)
	}
	current, err := store.OnboardingFor(auth, failed.OnboardingID)
	if err != nil || current.GateNonce(api.GateConfirmSetup) != "" || current.GateNonce(api.GateRemove) != failed.GateNonce(api.GateRemove) {
		t.Fatalf("resend armed the wrong pending action: %+v err=%v", current, err)
	}
	current.ArmGate(api.GateRemove, "", time.Now().Add(-time.Minute))
	current.Revision++
	store.mu.Lock()
	store.onboardings[current.OnboardingID] = cloneOnboarding(current)
	store.mu.Unlock()
	result, err = c.InvokeConnector(t.Context(), auth, "connector_edit", map[string]any{"action": "resend", "target": failed.OnboardingID})
	if err != nil || result.State != api.ModelStateWaitingYou || result.YouMust != api.YouMustClickLink {
		t.Fatalf("expired-removal resend: %+v err=%v", result, err)
	}
	// The re-mint rewrites the record under the store's current epoch — read
	// the row directly; OnboardingFor would refuse the pv mismatch.
	store.mu.RLock()
	renewed := store.onboardings[failed.OnboardingID]
	store.mu.RUnlock()
	if renewed.GateNonce(api.GateRemove) == failed.GateNonce(api.GateRemove) || renewed.GateExpiry(api.GateRemove).Before(time.Now()) {
		t.Fatalf("expired removal not re-minted: %+v", renewed)
	}
}

func TestConnectorListMarksOldPolicyRecordsStale(t *testing.T) {
	// Inventory keeps old-epoch records visible — marked stale_policy —
	// because removal is the one transition still legal on them: the
	// conn-bound row and the conn-less orphan both carry the badge.
	store, auth, _, _ := managedStore(t)
	onboarding := m13P3EnabledOnboarding(t, store, auth)
	onboarding.PolicyVersion = "old-policy"
	onboarding.Revision++
	store.mu.Lock()
	store.onboardings[onboarding.OnboardingID] = cloneOnboarding(onboarding)
	store.mu.Unlock()
	ghost := m13P3Onboarding(auth, "ghost", "github-mcp-server", "", api.PhaseAwaitingConfirm)
	ghost.PolicyVersion = "old-policy"
	if err := store.PutOnboarding(ghost); err != nil {
		t.Fatal(err)
	}
	c := &ControlPlane{Store: store, Now: time.Now}
	listed, err := c.connectorList(auth)
	if err != nil {
		t.Fatal(err)
	}
	var connRow, ghostRow *api.OperationCandidate
	for i := range listed.Candidates {
		if listed.Candidates[i].ConnectionID == onboarding.ConnectionID {
			connRow = &listed.Candidates[i]
		}
		if listed.Candidates[i].ID == ghost.OnboardingID {
			ghostRow = &listed.Candidates[i]
		}
	}
	if connRow == nil || connRow.Badge != api.OpCodeStalePolicy {
		t.Fatalf("stale-epoch connection row unmarked: %+v", listed.Candidates)
	}
	if ghostRow == nil || ghostRow.Badge != api.OpCodeStalePolicy {
		t.Fatalf("stale-epoch orphan unmarked: %+v", listed.Candidates)
	}
}

func TestOwnsConnectorTargetSpansPolicyEpochs(t *testing.T) {
	store, auth, _, _ := managedStore(t)
	onboarding := m13P3EnabledOnboarding(t, store, auth)
	onboarding.PolicyVersion = "old-policy"
	onboarding.Revision++
	store.mu.Lock()
	store.onboardings[onboarding.OnboardingID] = cloneOnboarding(onboarding)
	store.mu.Unlock()
	for _, target := range []string{onboarding.OnboardingID, onboarding.ConnectionID, onboarding.DefinitionID} {
		if !store.ownsConnectorTarget(auth, target) {
			t.Errorf("owned stale target %q reported missing", target)
		}
	}
	foreign := auth
	foreign.PrincipalID = "bob"
	if store.ownsConnectorTarget(foreign, onboarding.ConnectionID) {
		t.Fatal("foreign record reported owned")
	}
	if store.ownsConnectorTarget(auth, "never-existed") {
		t.Fatal("unknown target reported owned")
	}
}

func TestConnectorVerbDomainErrorsAreToolResults(t *testing.T) {
	// Typed domain failures must surface as failed results — a completed
	// round trip — never as protocol errors that strike the client circuit
	// breaker and read as backend unreachability.
	store, auth, _, profile := managedStore(t)
	profile.Revision++
	profile.Connectors = api.ConnectorsPersonal
	if err := putProfile(t, store, profile); err != nil {
		t.Fatal(err)
	}
	onboarding := m13P3EnabledOnboarding(t, store, auth)
	onboarding.PolicyVersion = "old-policy"
	onboarding.Revision++
	store.mu.Lock()
	store.onboardings[onboarding.OnboardingID] = cloneOnboarding(onboarding)
	store.mu.Unlock()
	g := &Gateway{Control: &ControlPlane{Store: store, Now: time.Now}}
	spec := api.ToolSpec{Name: "connector_reauth", Executor: "runtime:connectors"}
	for _, tc := range []struct {
		name string
		args map[string]any
		code string
	}{
		{"stale epoch", map[string]any{"target": onboarding.ConnectionID}, api.OpCodeStalePolicy},
		{"missing target", map[string]any{}, api.OpCodeNotPermitted},
		{"unknown target", map[string]any{"target": "never-existed"}, api.OpCodeNotFound},
	} {
		out, err := g.execRemote(t.Context(), auth, api.EffectiveBinding{}, spec, tc.args)
		if err != nil {
			t.Fatalf("%s: domain failure escaped as protocol error: %v", tc.name, err)
		}
		if !out.IsError {
			t.Fatalf("%s: failure not flagged on the result: %+v", tc.name, out)
		}
		body, _ := out.Structured.(map[string]any)
		if got, _ := body["code"].(string); got != tc.code {
			t.Fatalf("%s: code=%q want %q: %+v", tc.name, got, tc.code, out.Structured)
		}
	}
}
