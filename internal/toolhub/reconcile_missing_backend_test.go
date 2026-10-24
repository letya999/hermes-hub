package toolhub

import (
	"context"
	"testing"

	"github.com/letya999/hermes-hub/internal/toolhub/api"
)

func TestReconcilerDoesNotClaimCredentialActiveWithoutBackend(t *testing.T) {
	s := NewStore()
	s.connections["live"] = api.Connection{ConnectionID: "live", CredentialRefID: "ref", Status: api.ActiveStatus}
	r := (&ControlPlane{Store: s}).NewReconciler()
	if err := r.ScanOnce(t.Context()); err == nil {
		t.Fatal("missing credential backend was not reported")
	}
	got := s.connections["live"]
	if got.CredentialVisibility != "unknown" || got.HealthCause != "credential_check_unavailable" {
		t.Fatalf("missing credential backend was reported as active: %+v", got)
	}
}

func TestReconcilerSkipsCredentialProbeWithoutReference(t *testing.T) {
	s := NewStore()
	s.connections["credentialless"] = api.Connection{ConnectionID: "credentialless", Status: api.ActiveStatus}
	checked := false
	r := &Reconciler{Store: s, CredAlive: func(context.Context, string) (bool, error) {
		checked = true
		return false, nil
	}, Notify: func(context.Context, string, string, map[string]any) error { return nil }}
	if err := r.ScanOnce(t.Context()); err != nil {
		t.Fatal(err)
	}
	if checked || s.connections["credentialless"].CredentialVisibility != "" {
		t.Fatal("credentialless connection received a credential failure")
	}
}
