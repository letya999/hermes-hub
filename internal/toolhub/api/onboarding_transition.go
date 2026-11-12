package api

import "fmt"

// ValidateOnboardingTransition is a pure guard shared by all persistence paths.
// Removal is absorbing: a late provider reply can never restore authority.
func ValidateOnboardingTransition(before, after Onboarding) error {
	if before.PrincipalID != after.PrincipalID || before.ContextID != after.ContextID || before.RuntimeID != after.RuntimeID || before.PolicyVersion != after.PolicyVersion || before.Mode != after.Mode {
		return fmt.Errorf("%w: onboarding owner is immutable", ErrUnauthorized)
	}
	if after.Revision <= before.Revision {
		return ErrStale
	}
	if len(before.Finalizers) > 0 && after.Phase != PhaseRemoved && after.Phase != PhaseCleanupIncomplete {
		return ErrRevoked
	}
	if before.Phase == after.Phase {
		return nil
	}
	if before.Phase == PhaseRemoved {
		return ErrRevoked
	}
	if after.Phase == PhaseRemoved || after.Phase == PhaseCleanupIncomplete || after.Phase == PhaseRevoked {
		return nil
	}
	allowed := map[string][]string{
		PhasePreparing:       {PhaseReview, PhaseAwaitingCreds, PhaseAwaitingConfirm, PhaseAwaitingOAuth, PhaseFailed},
		PhaseReview:          {PhaseAwaitingCreds, PhaseAwaitingConfirm, PhaseAwaitingOAuth, PhaseFailed},
		PhaseAwaitingCreds:   {PhaseVerifying, PhaseRetryScheduled, PhaseAwaitingConfirm, PhaseAwaitingOAuth, PhaseFailed},
		PhaseVerifying:       {PhaseRetryScheduled, PhaseAwaitingCreds, PhaseAwaitingConfirm, PhaseFailed},
		PhaseRetryScheduled:  {PhaseVerifying, PhaseAwaitingCreds, PhaseAwaitingConfirm, PhaseFailed},
		PhaseAwaitingOAuth:   {PhaseAwaitingCreds, PhaseAwaitingConfirm, PhaseConfirmed, PhaseEnabled, PhaseFailed},
		PhaseAwaitingConfirm: {PhaseAwaitingCreds, PhaseAwaitingOAuth, PhaseConfirmed, PhaseEnabled, PhaseDisabled, PhaseFailed},
		PhaseConfirmed:       {PhaseEnabled, PhaseDisabled, PhaseAwaitingOAuth, PhaseAwaitingCreds, PhaseFailed},
		PhaseEnabled:         {PhaseDisabled, PhaseAwaitingConfirm, PhaseAwaitingCreds, PhaseAwaitingOAuth, PhaseFailed},
		PhaseDisabled:        {PhaseEnabled, PhaseAwaitingConfirm, PhaseAwaitingCreds, PhaseAwaitingOAuth, PhaseFailed},
		PhaseRevoked:         {PhaseAwaitingCreds, PhaseAwaitingOAuth},
		PhaseFailed:          {PhasePreparing, PhaseAwaitingCreds, PhaseAwaitingConfirm, PhaseAwaitingOAuth},
	}
	for _, phase := range allowed[before.Phase] {
		if phase == after.Phase {
			return nil
		}
	}
	return fmt.Errorf("%w: illegal onboarding transition %s -> %s", ErrConflict, before.Phase, after.Phase)
}

func ConfirmationRevision(o Onboarding) uint64 {
	if o.ActionRevision != 0 {
		return o.ActionRevision
	}
	return o.Revision
}
