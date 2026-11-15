package api

import (
	"encoding/json"
	"testing"
	"time"
)

func TestOnboardingGateAccessors(t *testing.T) {
	var o Onboarding
	if o.GateKind() != "" || o.GateNonce(GateCredentials) != "" || !o.GateExpiry(GateCredentials).IsZero() || o.GateLive(time.Now()) {
		t.Fatal("empty gate reported armed")
	}
	expiry := time.Now().Add(time.Hour)
	o.ArmGate(GateCredentials, "nonce-1", expiry)
	if o.GateKind() != GateCredentials || o.GateNonce(GateCredentials) != "nonce-1" || !o.GateExpiry(GateCredentials).Equal(expiry) || !o.GateLive(time.Now()) {
		t.Fatal("armed gate not visible")
	}
	if o.GateNonce(GateOAuth) != "" || !o.GateExpiry(GateOAuth).IsZero() {
		t.Fatal("gate leaked across kinds")
	}
	o.ClearGateKind(GateOAuth)
	if o.GateKind() != GateCredentials {
		t.Fatal("foreign kind cleared the gate")
	}
	o.ClearGateKind(GateCredentials)
	if o.Gate != nil {
		t.Fatal("matching kind did not clear")
	}
	o.ArmGate(GateConfirmSetup, "n", time.Now().Add(-time.Hour))
	if o.GateLive(time.Now()) {
		t.Fatal("expired gate reported live")
	}
	o.ArmGate(GateConfirmSetup, "n", time.Time{})
	if !o.GateLive(time.Now()) {
		t.Fatal("zero-expiry gate reported dead")
	}
	o.ClearGate()
	if o.Gate != nil {
		t.Fatal("clear left gate armed")
	}
}

func TestOnboardingDerivations(t *testing.T) {
	var o Onboarding
	if o.Awaiting(GateCredentials) {
		t.Fatal("pending action on ungated record")
	}
	o.Stage = StagePending
	o.ArmGate(GateCredentials, "n", time.Time{})
	if !o.Awaiting(GateCredentials) || o.Awaiting(GateOAuth) {
		t.Fatal("awaiting mismatch")
	}
	o.ArmGate(GateConfirmSetup, "n", time.Time{})
	if !o.Awaiting(GateConfirmSetup) {
		t.Fatal("confirm gate not awaited")
	}
	o.ConfirmationUsed = true
	if o.Awaiting(GateConfirmSetup) {
		t.Fatal("spent confirm still awaited")
	}
	o.Stage = StageFailed
	o.Finalizers = []Finalizer{{Name: "x"}}
	if !o.CleanupIncomplete() {
		t.Fatal("failed record with finalizers not flagged")
	}
	o.Finalizers = nil
	if o.CleanupIncomplete() {
		t.Fatal("clean failure flagged as incomplete")
	}
	var sched Onboarding
	if sched.AdmissionScheduled() {
		t.Fatal("unscheduled admission reported")
	}
	sched.AdmissionNextAttempt = time.Now()
	if !sched.AdmissionScheduled() {
		t.Fatal("queued retry not reported")
	}
	sched = Onboarding{}
	sched.AdmissionLeaseUntil = time.Now()
	if !sched.AdmissionScheduled() {
		t.Fatal("held lease not reported")
	}
	gone := Onboarding{Stage: StageGone, Tombstone: TombstoneRemoved}
	if !gone.Removed() || !gone.DeadEnd() || !gone.Terminal() || gone.Revoked() {
		t.Fatal("removed derivations wrong")
	}
	gone.Tombstone = TombstoneRevoked
	if !gone.Revoked() || gone.DeadEnd() || gone.Removed() {
		t.Fatal("revoked derivations wrong")
	}
	gone.Tombstone = ""
	if !gone.DeadEnd() || !gone.Terminal() {
		t.Fatal("superseded derivations wrong")
	}
}

func TestOnboardingRetireAndCreds(t *testing.T) {
	o := Onboarding{Stage: StagePending}
	o.ArmGate(GateCredentials, "n", time.Time{})
	o.Retire(TombstoneRemoved)
	if o.Stage != StageGone || o.Tombstone != TombstoneRemoved || o.Gate != nil {
		t.Fatal("retire kept non-remove gate")
	}
	o = Onboarding{Stage: StagePending}
	o.ArmGate(GateRemove, "n", time.Time{})
	o.Retire(TombstoneRemoved)
	if o.Gate == nil || o.Gate.Kind != GateRemove {
		t.Fatal("retire dropped remove tombstone")
	}
	// CredsCollected: live records count as collected; pending records hinge
	// on gates and delivery evidence.
	if !(Onboarding{Stage: StageLive}).CredsCollected() {
		t.Fatal("live record without creds")
	}
	pending := Onboarding{Stage: StagePending}
	if pending.CredsCollected() {
		t.Fatal("fresh pending record reported collected")
	}
	pending.ArmGate(GateCredentials, "n", time.Time{})
	if pending.CredsCollected() {
		t.Fatal("awaiting creds reported collected")
	}
	pending.ClearGate()
	pending.ConfirmationUsed = true
	if !pending.CredsCollected() {
		t.Fatal("confirmed record not collected")
	}
	pending.ConfirmationUsed = false
	pending.Locator = "loc"
	if !pending.CredsCollected() {
		t.Fatal("located credential not collected")
	}
	pending.Locator = ""
	pending.BrokerCredentialID = "bc-1"
	if !pending.CredsCollected() {
		t.Fatal("broker credential not collected")
	}
	pending.ArmGate(GateOAuth, "n", time.Time{})
	if pending.CredsCollected() {
		t.Fatal("awaiting oauth reported collected")
	}
}

func TestNoticeWirePhase(t *testing.T) {
	cases := []struct {
		name string
		o    Onboarding
		want string
	}{
		{"gate-creds", Onboarding{Stage: StagePending, Gate: &Gate{Kind: GateCredentials}}, PhaseAwaitingCreds},
		{"gate-oauth", Onboarding{Stage: StagePending, Gate: &Gate{Kind: GateOAuth}}, PhaseAwaitingOAuth},
		{"gate-confirm", Onboarding{Stage: StagePending, Gate: &Gate{Kind: GateConfirmSetup}}, PhaseAwaitingConfirm},
		{"gate-confirm-spent", Onboarding{Stage: StagePending, Gate: &Gate{Kind: GateConfirmSetup}, ConfirmationUsed: true}, PhaseConfirmed},
		{"gate-remove-pending", Onboarding{Stage: StagePending, Gate: &Gate{Kind: GateRemove}}, PhaseAwaitingConfirm},
		{"live", Onboarding{Stage: StageLive}, PhaseEnabled},
		{"live-spent-gate", Onboarding{Stage: StageLive, Gate: &Gate{Kind: GateCredentials}}, PhaseEnabled},
		{"failed", Onboarding{Stage: StageFailed}, PhaseFailed},
		{"cleanup", Onboarding{Stage: StageFailed, Finalizers: []Finalizer{{Name: "x"}}}, PhaseCleanupIncomplete},
		{"removed", Onboarding{Stage: StageGone, Tombstone: TombstoneRemoved}, PhaseRemoved},
		{"revoked", Onboarding{Stage: StageGone, Tombstone: TombstoneRevoked}, PhaseRevoked},
		{"gone-other", Onboarding{Stage: StageGone}, PhaseRevoked},
		{"confirmed", Onboarding{ConfirmationUsed: true}, PhaseConfirmed},
		{"preparing", Onboarding{}, PhasePreparing},
	}
	for _, tc := range cases {
		if got := tc.o.NoticePhase(); got != tc.want {
			t.Fatalf("%s: %q want %q", tc.name, got, tc.want)
		}
	}
}

func TestOnboardingLegacyMigration(t *testing.T) {
	expiry := "2027-01-01T00:00:00Z"
	decode := func(phase string, extra string) Onboarding {
		t.Helper()
		body := `{"onboarding_id":"o1","phase":"` + phase + `"` + extra + `}`
		var o Onboarding
		if err := json.Unmarshal([]byte(body), &o); err != nil {
			t.Fatalf("%s: %v", phase, err)
		}
		return o
	}
	o := decode("awaiting-credentials", `,"form_nonce":"fn","form_expires":"`+expiry+`"`)
	if o.Stage != StagePending || o.GateKind() != GateCredentials || o.GateNonce(GateCredentials) != "fn" {
		t.Fatalf("credentials migration: %+v", o)
	}
	o = decode("awaiting-confirm", `,"confirmation_nonce":"cn"`)
	if o.Stage != StagePending || o.GateKind() != GateConfirmSetup || o.GateNonce(GateConfirmSetup) != "cn" {
		t.Fatalf("confirm migration: %+v", o)
	}
	o = decode("awaiting-oauth", "")
	if o.Stage != StagePending || o.GateKind() != GateOAuth {
		t.Fatalf("oauth migration: %+v", o)
	}
	for _, phase := range []string{"enabled", "disabled"} {
		if o = decode(phase, ""); o.Stage != StageLive {
			t.Fatalf("%s: %s", phase, o.Stage)
		}
	}
	for _, phase := range []string{"failed", "cleanup_incomplete"} {
		if o = decode(phase, ""); o.Stage != StageFailed {
			t.Fatalf("%s: %s", phase, o.Stage)
		}
	}
	if o = decode("removed", ""); o.Stage != StageGone || o.Tombstone != TombstoneRemoved {
		t.Fatalf("removed: %+v", o)
	}
	if o = decode("revoked", ""); o.Stage != StageGone || o.Tombstone != TombstoneRevoked {
		t.Fatalf("revoked: %+v", o)
	}
	if o = decode("preparing", ""); o.Stage != StagePending || !o.PreparingStub {
		t.Fatalf("preparing: %+v", o)
	}
	for _, phase := range []string{"verifying", "retry-scheduled", "confirmed", "review", "unknown-x"} {
		if o = decode(phase, ""); o.Stage != StagePending {
			t.Fatalf("%s: %s", phase, o.Stage)
		}
	}
	// An armed remove nonce outranks the pipeline phase.
	o = decode("enabled", `,"remove_nonce":"rn","remove_expires":"`+expiry+`"`)
	if o.Stage != StageLive || o.GateKind() != GateRemove {
		t.Fatalf("remove precedence: %+v", o)
	}
	// Superseded records land in gone and shed any non-remove gate.
	o = decode("awaiting-credentials", `,"superseded_by":"o2","form_nonce":"fn"`)
	if o.Stage != StageGone || o.Gate != nil {
		t.Fatalf("supersede: %+v", o)
	}
	// New-schema records ignore the legacy phase entirely.
	o = decode("enabled", `,"stage":"failed"`)
	if o.Stage != StageFailed {
		t.Fatalf("new schema folded: %+v", o)
	}
}

func TestOnboardingStageValidation(t *testing.T) {
	if err := (Onboarding{Stage: "bogus"}).validateStage(); err == nil {
		t.Fatal("bad stage admitted")
	}
	if err := (Onboarding{Stage: StagePending, Gate: &Gate{Kind: "bogus"}}).validateStage(); err == nil {
		t.Fatal("bad gate admitted")
	}
	if err := (Onboarding{Stage: StageGone, Gate: &Gate{Kind: GateCredentials}}).validateStage(); err == nil {
		t.Fatal("non-remove gate on gone admitted")
	}
	if err := (Onboarding{Stage: StageGone, Gate: &Gate{Kind: GateRemove}}).validateStage(); err != nil {
		t.Fatalf("remove tombstone denied: %v", err)
	}
	edges := map[string]map[string]bool{
		StagePending: {StageLive: true, StageFailed: true, StageGone: true, StagePending: false},
		StageLive:    {StagePending: true, StageFailed: true, StageGone: true, StageLive: false},
		StageFailed:  {StagePending: true, StageGone: true, StageLive: false, StageFailed: false},
		StageGone:    {StagePending: false, StageLive: false, StageFailed: false, StageGone: false},
	}
	for from, tos := range edges {
		for to, want := range tos {
			if got := validStageEdge(from, to); got != want {
				t.Fatalf("edge %s→%s: %v want %v", from, to, got, want)
			}
		}
	}
}
