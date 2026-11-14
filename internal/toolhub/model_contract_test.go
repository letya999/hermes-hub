package toolhub

import (
	"encoding/json"
	"path/filepath"
	"testing"
	"time"

	"github.com/letya999/hermes-hub/internal/toolhub/api"
)

// noticeDelivered reports whether the uniform result says the notice reached
// the channel — queued, confirmed-sent, or attempted-uncertain — the post-step4
// reading of what link_sent used to claim.
func noticeDelivered(result api.OperationResult) bool {
	switch result.Delivery {
	case api.DeliveryQueued, api.DeliverySent, api.DeliveryUncertain:
		return true
	}
	return false
}

func TestModelContractDerivation(t *testing.T) {
	now := time.Now()
	future, past := now.Add(time.Hour), now.Add(-time.Hour)
	cases := []struct {
		name     string
		record   api.Onboarding
		disabled bool
		state    string
		youMust  string
	}{
		{"live enabled", api.Onboarding{Stage: api.StageLive}, false, api.ModelStateReady, ""},
		{"live disabled binding", api.Onboarding{Stage: api.StageLive}, true, api.ModelStatePaused, ""},
		{"pending confirm", api.Onboarding{Stage: api.StagePending, Gate: &api.Gate{Kind: api.GateConfirmSetup, Expires: future}}, false, api.ModelStateWaitingYou, api.YouMustClickLink},
		{"pending credentials", api.Onboarding{Stage: api.StagePending, Gate: &api.Gate{Kind: api.GateCredentials, Expires: future}}, false, api.ModelStateWaitingYou, api.YouMustClickLink},
		{"pending oauth", api.Onboarding{Stage: api.StagePending, Gate: &api.Gate{Kind: api.GateOAuth, Expires: future}}, false, api.ModelStateWaitingYou, api.YouMustClickLink},
		{"pending working", api.Onboarding{Stage: api.StagePending}, false, api.ModelStateWorking, api.YouMustWait},
		{"pending confirmed", api.Onboarding{Stage: api.StagePending, ConfirmationUsed: true}, false, api.ModelStateWorking, api.YouMustEnable},
		{"armed remove", api.Onboarding{Stage: api.StageLive, Gate: &api.Gate{Kind: api.GateRemove, Expires: future}}, false, api.ModelStateWaitingYou, api.YouMustClickLink},
		{"expired remove", api.Onboarding{Stage: api.StageLive, Gate: &api.Gate{Kind: api.GateRemove, Expires: past}}, false, api.ModelStateWaitingYou, api.YouMustRetry},
		{"failed", api.Onboarding{Stage: api.StageFailed}, false, api.ModelStateBroken, api.YouMustRetry},
		{"cleanup incomplete", api.Onboarding{Stage: api.StageFailed, Finalizers: []api.Finalizer{{Name: "revoke", State: api.FinalizerPending}}}, false, api.ModelStateBroken, api.YouMustRemove},
		{"removed", api.Onboarding{Stage: api.StageGone, Tombstone: api.TombstoneRemoved}, false, api.ModelStateDone, ""},
		{"revoked", api.Onboarding{Stage: api.StageGone, Tombstone: api.TombstoneRevoked}, false, api.ModelStateDone, api.YouMustRemove},
		{"spent gate on live", api.Onboarding{Stage: api.StageLive, Gate: &api.Gate{Kind: api.GateConfirmSetup, Expires: past}}, false, api.ModelStateReady, ""},
		{"spent gate on failed", api.Onboarding{Stage: api.StageFailed, Gate: &api.Gate{Kind: api.GateConfirmSetup, Expires: past}}, false, api.ModelStateBroken, api.YouMustRetry},
	}
	for _, tc := range cases {
		if got := tc.record.ModelState(tc.disabled); got != tc.state {
			t.Errorf("%s: state=%q want %q", tc.name, got, tc.state)
		}
		if got := tc.record.YouMust(now); got != tc.youMust {
			t.Errorf("%s: you_must=%q want %q", tc.name, got, tc.youMust)
		}
	}
}

func TestModelContractResultHasNoLegacyFields(t *testing.T) {
	result := api.OperationResult{State: api.ModelStateWaitingYou, YouMust: api.YouMustClickLink, Delivery: api.DeliveryQueued, Channel: "telegram_bot"}
	encoded, err := json.Marshal(result)
	if err != nil {
		t.Fatal(err)
	}
	var fields map[string]any
	if err := json.Unmarshal(encoded, &fields); err != nil {
		t.Fatal(err)
	}
	for _, key := range []string{"next_action", "link_sent", "delivery_status", "onboarding_phase", "pending_action"} {
		if _, present := fields[key]; present {
			t.Errorf("legacy field %q leaked into the model contract", key)
		}
	}
	for key, want := range map[string]string{"state": api.ModelStateWaitingYou, "you_must": api.YouMustClickLink, "delivery": api.DeliveryQueued, "channel": "telegram_bot"} {
		if fields[key] != want {
			t.Errorf("%s=%v want %q", key, fields[key], want)
		}
	}
}

// The 2026-11-13 incident root: the model addressed a returned record ID via
// `id`/`onboarding_id` and got not_found while the record existed. Both keys
// are `name` aliases on every connector_* tool.
func TestConnectorArgsAcceptIDAliases(t *testing.T) {
	store, auth, _, profile := managedStore(t)
	profile.Revision++
	profile.Connectors = api.ConnectorsPersonal
	if err := putProfile(t, store, profile); err != nil {
		t.Fatal(err)
	}
	c := &ControlPlane{Store: store, Now: time.Now}
	installed, err := c.InvokeConnector(t.Context(), auth, "connector_install", map[string]any{"id": "google-work", "version": "2.0.0"})
	if err != nil || installed.OperationID == "" {
		t.Fatalf("install via id alias: %+v %v", installed, err)
	}
	op, err := store.OperationFor(auth, installed.OperationID)
	if err != nil {
		t.Fatal(err)
	}
	want, err := c.InvokeConnector(t.Context(), auth, "connector_status", map[string]any{"name": op.OnboardingID})
	if err != nil {
		t.Fatal(err)
	}
	for _, key := range []string{"id", "onboarding_id"} {
		got, err := c.InvokeConnector(t.Context(), auth, "connector_status", map[string]any{key: op.OnboardingID})
		if err != nil || got.State != want.State {
			t.Fatalf("status via %s: %+v %v (want state %q)", key, got, err, want.State)
		}
	}
}

// The 2026-11-13 incident state: six sibling live records for two connectors
// persisted before the slot existed. Load quarantines every loser with
// superseded_by=winner so no second lifecycle stays addressable.
func TestLoadQuarantinesDuplicateLiveRecords(t *testing.T) {
	auth := aliceAuth()
	s := NewStore()
	older := m13P3Onboarding(auth, "dup-a", "github-mcp", "https://github.com/github/github-mcp-server", api.PhaseEnabled)
	older.CreatedAt = time.Now().Add(-time.Hour)
	newer := m13P3Onboarding(auth, "dup-b", "github-mcp", "https://github.com/github/github-mcp-server", api.PhaseEnabled)
	for _, o := range []api.Onboarding{older, newer} {
		if err := s.PutOnboarding(o); err != nil {
			t.Fatal(err)
		}
	}
	path := filepath.Join(t.TempDir(), "store.db")
	if err := s.Save(path); err != nil {
		t.Fatal(err)
	}
	if err := s.CloseDB(); err != nil {
		t.Fatal(err)
	}
	loaded, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = loaded.CloseDB() })
	winner, found := loaded.OnboardingSlot(auth, "github-mcp")
	if !found || winner.OnboardingID != newer.OnboardingID {
		t.Fatalf("slot winner = %+v found=%t; want %s", winner, found, newer.OnboardingID)
	}
	loser, err := loaded.OnboardingFor(auth, older.OnboardingID)
	if err != nil {
		t.Fatal(err)
	}
	if loser.Stage != api.StageGone || loser.SupersededBy != newer.OnboardingID {
		t.Fatalf("loser not quarantined: stage=%q superseded_by=%q", loser.Stage, loser.SupersededBy)
	}
	if _, err := loaded.ResolveConnectorTarget(auth, older.OnboardingID); err == nil {
		t.Fatal("quarantined record still addressable")
	}
}
