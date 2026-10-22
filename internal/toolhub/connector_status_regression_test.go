package toolhub

import (
	"strings"
	"testing"
	"time"

	"github.com/letya999/hermes-hub/internal/toolhub/api"
)

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
