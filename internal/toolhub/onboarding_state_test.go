package toolhub

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"

	"github.com/letya999/hermes-hub/internal/credentialbroker"
	"github.com/letya999/hermes-hub/internal/toolhub/api"
)

func TestOnboardingStateFencesLateAndForeignWrites(t *testing.T) {
	s := NewStore()
	auth := aliceAuth()
	o := m13P3Onboarding(auth, "fsm", "github", "https://github.com/github/github-mcp-server", api.PhaseAwaitingCreds)
	if err := s.PutOnboarding(o); err != nil {
		t.Fatal(err)
	}
	stale := o
	o.Phase, o.Revision = api.PhaseRetryScheduled, o.Revision+1
	if err := s.PutOnboarding(o); err != nil {
		t.Fatal(err)
	}
	stale.Phase, stale.Revision = api.PhaseAwaitingConfirm, stale.Revision+1
	if err := s.PutOnboarding(stale); !errors.Is(err, api.ErrStale) {
		t.Fatalf("late reply replaced winner: %v", err)
	}
	foreign := o
	foreign.PrincipalID = "bob"
	foreign.Revision++
	if err := s.PutOnboarding(foreign); !errors.Is(err, api.ErrUnauthorized) {
		t.Fatalf("owner change: %v", err)
	}
	o.Phase, o.Revision = api.PhaseRemoved, o.Revision+1
	if err := s.PutOnboarding(o); err != nil {
		t.Fatal(err)
	}
	for _, phase := range []string{api.PhaseAwaitingCreds, api.PhaseAwaitingConfirm, api.PhaseVerifying, api.PhaseEnabled, api.PhaseCleanupIncomplete} {
		next := o
		next.Phase = phase
		next.Revision++
		if err := s.PutOnboarding(next); !errors.Is(err, api.ErrRevoked) {
			t.Fatalf("removed -> %s accepted: %v", phase, err)
		}
	}
}

func TestOnboardingNoticeSurvivesCrashAndDoesNotInvalidateApproval(t *testing.T) {
	now := time.Now().UTC()
	auth := aliceAuth()
	s := NewStore()
	grantTestControlOperations(t, s, auth.PrincipalID, "confirm")
	o := m13P3Onboarding(auth, "outbox", "github", "https://github.com/github/github-mcp-server", api.PhaseAwaitingConfirm)
	o.Initiator, o.ConfirmationNonce, o.ConfirmationExpires = &auth, "test-proof", now.Add(time.Hour)
	if err := s.PutOnboarding(o); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(t.TempDir(), "store.db")
	if err := s.Save(path); err != nil {
		t.Fatal(err)
	}
	if err := s.CloseDB(); err != nil {
		t.Fatal(err)
	}
	// Crash after state commit, before any HTTP send.
	s, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = s.CloseDB() })
	var calls atomic.Int32
	var proof api.HumanConfirmation
	channel := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var payload struct {
			Confirmation api.HumanConfirmation `json:"confirmation"`
		}
		if err := json.NewDecoder(r.Body).Decode(&payload); err != nil {
			t.Error(err)
		}
		proof = payload.Confirmation
		if calls.Add(1) == 1 {
			w.WriteHeader(http.StatusServiceUnavailable)
			return
		}
		w.WriteHeader(http.StatusNoContent)
	}))
	defer channel.Close()
	t.Setenv("HUB_COMMUNICATION_CONTROL_URL", channel.URL)
	t.Setenv("HUB_COMMUNICATION_AUTH", "test-control")
	c := &ControlPlane{Store: s, Now: func() time.Time { return now }}
	if c.deliverOnboardingNotice(t.Context(), auth, o.OnboardingID) {
		t.Fatal("503 claimed queued")
	}
	pending, _ := s.OnboardingFor(auth, o.OnboardingID)
	if pending.NoticeVersion == 0 || pending.NoticeQueuedVersion == pending.NoticeVersion {
		t.Fatal("failed delivery lost durable intent")
	}
	now = now.Add(6 * time.Second)
	if !c.deliverOnboardingNotice(t.Context(), auth, o.OnboardingID) {
		t.Fatal("retry did not queue")
	}
	if c.deliverOnboardingNotice(t.Context(), auth, o.OnboardingID) {
		t.Fatal("acknowledged notice sent again")
	}
	if calls.Load() != 2 {
		t.Fatalf("HTTP attempts=%d", calls.Load())
	}
	if _, err := s.ConsumeHumanConfirmation(proof, now); err != nil {
		t.Fatalf("delivery metadata invalidated action: %v", err)
	}
	if _, err := s.ConsumeHumanConfirmation(proof, now); !errors.Is(err, api.ErrUnauthorized) {
		t.Fatal("approval replay accepted")
	}
}

func TestPendingRemovalDoesNotStarveOtherOnboardings(t *testing.T) {
	s := NewStore()
	auth := aliceAuth()
	for _, id := range []string{"blocked", "ready"} {
		o := m13P3Onboarding(auth, id, "missing", "", api.PhaseCleanupIncomplete)
		o.Finalizers = []api.Finalizer{{Name: api.FinalizerBrokerCredential, State: api.FinalizerPending}}
		if id == "blocked" {
			o.BrokerCredentialID = "unavailable-broker-credential"
		}
		if err := s.PutOnboarding(o); err != nil {
			t.Fatal(err)
		}
	}
	c := &ControlPlane{Store: s}
	if err := c.ResumeRemovals(t.Context()); err != nil {
		t.Fatal(err)
	}
	for _, id := range []string{"blocked", "ready"} {
		o, err := s.OnboardingFor(auth, m13P3Onboarding(auth, id, "missing", "", api.PhaseCleanupIncomplete).OnboardingID)
		if err != nil || (id == "ready" && o.Phase != api.PhaseRemoved) || (id == "blocked" && o.Phase != api.PhaseCleanupIncomplete) {
			t.Fatalf("cleanup %s: phase=%s err=%v", id, o.Phase, err)
		}
	}
}

func TestConnectorStatusIsReadOnlyWithoutProvider(t *testing.T) {
	s := NewStore()
	auth := aliceAuth()
	o := m13P3Onboarding(auth, "readonly", "github", "https://github.com/github/github-mcp-server", api.PhaseAwaitingCreds)
	o.BrokerRequestID = "request-unreachable"
	if err := s.PutOnboarding(o); err != nil {
		t.Fatal(err)
	}
	c := &ControlPlane{Store: s, Broker: &credentialbroker.Config{URL: "http://127.0.0.1:1", KeyFile: filepath.Join(t.TempDir(), "absent-key")}}
	before, _ := s.OnboardingFor(auth, o.OnboardingID)
	for range 100 {
		result, err := c.connectorStatus(context.Background(), auth, o.OnboardingID, "")
		if err != nil || result.State != api.OpStateAwaitingUser {
			t.Fatalf("saved status: %v %s", err, result.State)
		}
	}
	after, _ := s.OnboardingFor(auth, o.OnboardingID)
	if !api.RecordsEqual(before, after) {
		t.Fatal("status changed state")
	}
}

func TestBrokerAdmissionRetriesPreserveAcceptedCredential(t *testing.T) {
	now := time.Now().UTC()
	s, auth := NewStore(), aliceAuth()
	d := catalogReadDefinition()
	if err := s.RegisterDefinition(d); err != nil {
		t.Fatal(err)
	}
	o := m13P3Onboarding(auth, "retry", d.DefinitionID, "https://github.com/example/mcp", api.PhaseAwaitingCreds)
	o.DefinitionVersion, o.AdmissionPending, o.BrokerRequestID = d.Version, true, "request-retry"
	if err := s.PutOnboarding(o); err != nil {
		t.Fatal(err)
	}
	c := &ControlPlane{Store: s, Now: func() time.Time { return now }}
	for i := range 5 {
		if err := c.completeBrokerAdmission(t.Context(), auth, &o, "accepted-credential"); err != nil {
			t.Fatal(err)
		}
		o, _ = s.OnboardingFor(auth, o.OnboardingID)
		if o.BrokerCredentialID != "accepted-credential" || o.Locator != "accepted-credential" || o.AdmissionAttempts != i+1 {
			t.Fatal("transient backend error lost credential or retry count")
		}
		if i < 4 && o.Phase != api.PhaseRetryScheduled {
			t.Fatalf("temporary error asked for credentials: %s", o.Phase)
		}
		now = now.Add(2 * time.Minute)
	}
	if o.Phase != api.PhaseFailed {
		t.Fatalf("retry budget never terminates: %s", o.Phase)
	}
	if err := c.completeBrokerAdmission(t.Context(), auth, &o, "accepted-credential"); err != nil {
		t.Fatal(err)
	}
	if next, _ := s.OnboardingFor(auth, o.OnboardingID); next.AdmissionAttempts != 5 {
		t.Fatal("exhausted admission kept retrying")
	}
}

func TestOnboardingIdentitySeparatesPolicyEpochs(t *testing.T) {
	s, auth := NewStore(), aliceAuth()
	d := catalogReadDefinition()
	c := &ControlPlane{Store: s}
	before, err := c.newOnboarding(auth, api.OnboardingCatalog, "same-request", d, "", "")
	if err != nil {
		t.Fatal(err)
	}
	next := auth
	next.PolicyVersion = "policy-2"
	after, err := c.newOnboarding(next, api.OnboardingCatalog, "same-request", d, "", "")
	if err != nil {
		t.Fatal(err)
	}
	if before.OnboardingID == after.OnboardingID {
		t.Fatal("policy rotation reused old workflow identity")
	}
	if _, err := s.OnboardingFor(next, before.OnboardingID); !errors.Is(err, api.ErrUnauthorized) {
		t.Fatal("new epoch claimed old workflow")
	}
}
