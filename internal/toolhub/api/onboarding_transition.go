package api

import "fmt"

// ValidateOnboardingTransition is a pure guard shared by all persistence paths.
// The four-stage edge table is the whole lifecycle law: pending pipelines reach
// live, fail, or die; live records re-enter pending on reauth, fail on a stuck
// cleanup, or die on removal; failed records retry or die; gone is absorbing —
// a late provider reply can never restore authority.
func ValidateOnboardingTransition(before, after Onboarding) error {
	if before.PrincipalID != after.PrincipalID || before.ContextID != after.ContextID || before.RuntimeID != after.RuntimeID || before.PolicyVersion != after.PolicyVersion || before.Mode != after.Mode {
		return fmt.Errorf("%w: onboarding owner is immutable", ErrUnauthorized)
	}
	if after.Revision <= before.Revision {
		return ErrStale
	}
	if len(before.Finalizers) > 0 && after.Stage != StageGone && after.Stage != StageFailed {
		return ErrRevoked
	}
	if before.Stage == after.Stage {
		return nil
	}
	if before.Stage == StageGone {
		// A revoked tombstone is the one resumable death: reauth reopens the
		// record's credential intake, undoing the cut. Removed and superseded
		// records never come back.
		if before.Tombstone == TombstoneRevoked && after.Stage == StagePending {
			return nil
		}
		return ErrRevoked
	}
	if validStageEdge(before.Stage, after.Stage) {
		return nil
	}
	return fmt.Errorf("%w: illegal onboarding transition %s -> %s", ErrConflict, before.Stage, after.Stage)
}

func ConfirmationRevision(o Onboarding) uint64 {
	if o.ActionRevision != 0 {
		return o.ActionRevision
	}
	return o.Revision
}
