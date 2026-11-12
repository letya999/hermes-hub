package toolhub

import (
	"slices"
	"time"

	"github.com/letya999/hermes-hub/internal/toolhub/api"
)

func prepareOnboardingWrite(before, after api.Onboarding) api.Onboarding {
	if before.BrokerRequestID != after.BrokerRequestID && after.Phase == api.PhaseAwaitingCreds {
		after.AdmissionAttempts = 0
		after.AdmissionLeaseUntil, after.AdmissionNextAttempt = time.Time{}, time.Time{}
	}
	if after.ObservedAt.Before(before.ObservedAt) {
		after.ObservedAt, after.ObservationCode = before.ObservedAt, before.ObservationCode
	}
	if after.ChannelAuthURL == "" && before.ChannelAuthURL != "" && after.BrokerRequestID == before.BrokerRequestID {
		after.ChannelAuthURL = before.ChannelAuthURL
	}
	if after.Initiator == nil {
		after.Initiator = before.Initiator
	}
	if before.OnboardingID == "" || before.ConfirmationNonce != after.ConfirmationNonce || api.ConfirmationActionDigest(before) != api.ConfirmationActionDigest(after) {
		after.ActionRevision = 0 // The current domain revision already names this action.
	} else {
		after.ActionRevision = api.ConfirmationRevision(before)
	}
	after.NoticeVersion = max(before.NoticeVersion, after.NoticeVersion)
	after.NoticeQueuedVersion = max(before.NoticeQueuedVersion, after.NoticeQueuedVersion)
	if after.NoticeVersion == before.NoticeVersion && after.NoticeQueuedVersion < after.NoticeVersion && after.NoticeNextAttempt.Before(before.NoticeNextAttempt) {
		after.NoticeNextAttempt = before.NoticeNextAttempt
	}
	if after.Initiator != nil && noticeRequired(after) && (before.Initiator == nil || before.RemoveNonce != after.RemoveNonce || before.Phase != after.Phase || before.ConfirmationNonce != after.ConfirmationNonce || before.FormNonce != after.FormNonce || before.BrokerAuthorizationURL != after.BrokerAuthorizationURL) {
		after.NoticeVersion++
		after.NoticeEvent, after.NoticeEventID = "", ""
		after.NoticeNextAttempt = time.Time{}
	}
	return after
}

func noticePhase(phase string) bool {
	switch phase {
	case api.PhaseAwaitingCreds, api.PhaseAwaitingConfirm, api.PhaseAwaitingOAuth, api.PhaseEnabled, api.PhaseFailed, api.PhaseCleanupIncomplete:
		return true
	}
	return false
}

// Install progress is a projection of the canonical onboarding, never a
// second independently advanced workflow.
func installProgress(op api.Operation, o api.Onboarding) api.Operation {
	if op.Intent != api.OpIntentInstall && op.Intent != api.OpIntentReauth || op.Intent == api.OpIntentReauth && op.State == api.OpStateDone {
		return op
	}
	op.Steps = slices.Clone(op.Steps)
	op.State, op.ErrorCode = connectorOnboardingOperationState(o), o.ErrorCode
	collected := o.Phase != api.PhasePreparing && o.Phase != api.PhaseReview && o.Phase != api.PhaseAwaitingCreds && o.Phase != api.PhaseAwaitingOAuth
	if collected {
		markStep(&op, api.OpStepCollectCredentials, api.OpStepDone)
	} else {
		markStep(&op, api.OpStepCollectCredentials, api.OpStepPending)
	}
	if !o.AdmissionPending && (o.Phase == api.PhaseAwaitingConfirm || o.Phase == api.PhaseConfirmed || o.Phase == api.PhaseEnabled) {
		markStep(&op, api.OpStepAdmitProbe, api.OpStepDone)
	}
	if o.Phase == api.PhaseEnabled {
		markStep(&op, api.OpStepBind, api.OpStepDone)
		markStep(&op, api.OpStepProject, api.OpStepDone)
	}
	if o.NoticeVersion > 0 && o.NoticeQueuedVersion == o.NoticeVersion {
		markStep(&op, api.OpStepNotify, api.OpStepDone)
	}
	return op
}

func noticeRequired(o api.Onboarding) bool {
	return noticePhase(o.Phase) || o.RemoveNonce != "" && o.Phase != api.PhaseRemoved
}
