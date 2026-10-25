package toolhub

import (
	"testing"
	"time"

	"github.com/letya999/hermes-hub/internal/toolhub/api"
)

func TestReconcileOnboardingRejectsUnrelatedHistory(t *testing.T) {
	for _, tc := range []struct {
		name   string
		change func(*api.Onboarding)
	}{
		{"wrong-principal", func(o *api.Onboarding) { o.PrincipalID = "bob" }},
		{"old-credential", func(o *api.Onboarding) { o.CredentialRefID = "credential-old" }},
		{"superseded", func(o *api.Onboarding) { o.SupersededBy = "onboard-new" }},
		{"removed", func(o *api.Onboarding) { o.Phase = api.PhaseRemoved }},
		{"revoked", func(o *api.Onboarding) { o.Phase = api.PhaseRevoked }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s := NewStore()
			s.connections["connection-a"] = api.Connection{ConnectionID: "connection-a", Owner: api.OwnerRef{Type: api.PrincipalOwner, ID: "alice"}, CredentialRefID: "credential-current"}
			o := api.Onboarding{OnboardingID: "onboard-a", PrincipalID: "alice", ContextID: "context-a", ConnectionID: "connection-a", CredentialRefID: "credential-current", Phase: api.PhaseEnabled}
			tc.change(&o)
			s.onboardings[o.OnboardingID] = o
			if _, ok := s.onboardingForConnection("connection-a"); ok {
				t.Fatal("historical or foreign onboarding selected for credential reconciliation")
			}
		})
	}
}

func TestReconcileOnboardingSelectsCurrentReferenceBeforeLegacy(t *testing.T) {
	s := NewStore()
	s.connections["connection-a"] = api.Connection{ConnectionID: "connection-a", Owner: api.OwnerRef{Type: api.PrincipalOwner, ID: "alice"}, CredentialRefID: "credential-current"}
	now := time.Date(2026, 10, 23, 0, 0, 0, 0, time.UTC)
	current := api.Onboarding{OnboardingID: "onboard-current", PrincipalID: "alice", ConnectionID: "connection-a", CredentialRefID: "credential-current", Phase: api.PhaseEnabled, CreatedAt: now}
	legacy := current
	legacy.OnboardingID, legacy.CredentialRefID, legacy.CreatedAt = "onboard-legacy", "", now.Add(time.Hour)
	s.onboardings[current.OnboardingID], s.onboardings[legacy.OnboardingID] = current, legacy
	for range 64 {
		got, ok := s.onboardingForConnection("connection-a")
		if !ok || got.OnboardingID != current.OnboardingID {
			t.Fatalf("current credential reference not selected: got %q, found=%v", got.OnboardingID, ok)
		}
	}
}

func TestReconcileOnboardingUsesStableLatestRecord(t *testing.T) {
	for _, sameTime := range []bool{false, true} {
		s := NewStore()
		s.connections["connection-a"] = api.Connection{ConnectionID: "connection-a", Owner: api.OwnerRef{Type: api.PrincipalOwner, ID: "alice"}}
		older := api.Onboarding{OnboardingID: "onboard-a", PrincipalID: "alice", ConnectionID: "connection-a", Phase: api.PhaseEnabled, CreatedAt: time.Date(2026, 10, 23, 0, 0, 0, 0, time.UTC)}
		newer := older
		newer.OnboardingID = "onboard-z"
		if !sameTime {
			newer.CreatedAt = older.CreatedAt.Add(time.Hour)
		}
		s.onboardings[older.OnboardingID], s.onboardings[newer.OnboardingID] = older, newer
		for range 64 {
			got, ok := s.onboardingForConnection("connection-a")
			if !ok || got.OnboardingID != newer.OnboardingID {
				t.Fatalf("sameTime=%v: got %q, found=%v; want stable latest %q", sameTime, got.OnboardingID, ok, newer.OnboardingID)
			}
		}
	}
}

func TestReconcileOnboardingContextOwnerAndMissingConnection(t *testing.T) {
	s := NewStore()
	s.connections["connection-a"] = api.Connection{ConnectionID: "connection-a", Owner: api.OwnerRef{Type: api.ContextOwner, ID: "context-a"}}
	o := api.Onboarding{OnboardingID: "onboard-a", ConnectionID: "connection-a", ContextID: "context-b", Phase: api.PhaseEnabled}
	s.onboardings[o.OnboardingID] = o
	if _, ok := s.onboardingForConnection("connection-a"); ok {
		t.Fatal("foreign context accepted")
	}
	o.ContextID = "context-a"
	s.onboardings[o.OnboardingID] = o
	if _, ok := s.onboardingForConnection("connection-a"); !ok {
		t.Fatal("owning context rejected")
	}
	delete(s.connections, "connection-a")
	if _, ok := s.onboardingForConnection("connection-a"); ok {
		t.Fatal("orphan onboarding accepted without its connection")
	}
}
