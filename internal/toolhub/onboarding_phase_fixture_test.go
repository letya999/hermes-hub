package toolhub

import (
	"time"

	"github.com/letya999/hermes-hub/internal/toolhub/api"
)

// applyOnboardingPhase folds the retired phase vocabulary into Stage+Gate for
// parametric fixtures that still iterate over legacy phase names. It mirrors
// api.migrateLegacyStage: awaiting-* arms an empty gate a later nonce write
// replaces, gone keeps the tombstone, pipeline sub-states collapse to pending.
// Retired names absent from the wire constants (removed, disabled, review,
// verifying, retry-scheduled) are plain string literals here.
func applyOnboardingPhase(o *api.Onboarding, phase string) {
	switch phase {
	case api.PhaseAwaitingCreds:
		o.Stage = api.StagePending
		o.ArmGate(api.GateCredentials, "", time.Time{})
	case api.PhaseAwaitingConfirm:
		o.Stage = api.StagePending
		o.ArmGate(api.GateConfirmSetup, "", time.Time{})
	case api.PhaseAwaitingOAuth:
		o.Stage = api.StagePending
		o.ArmGate(api.GateOAuth, "", time.Time{})
	case api.PhaseEnabled, "disabled":
		o.Stage = api.StageLive
	case api.PhaseFailed, api.PhaseCleanupIncomplete:
		o.Stage = api.StageFailed
	case "removed":
		o.Retire(api.TombstoneRemoved)
	case api.PhaseRevoked:
		o.Retire(api.TombstoneRevoked)
	case api.PhasePreparing:
		o.Stage = api.StagePending
		o.PreparingStub = true
	case api.PhaseConfirmed:
		o.Stage = api.StagePending
		o.ArmGate(api.GateConfirmSetup, "", time.Time{})
		o.ConfirmationUsed = true
	default:
		o.Stage = api.StagePending
	}
}
