package toolhub

import (
	"slices"
	"time"

	"github.com/letya999/hermes-hub/internal/toolhub/api"
)

func prepareOnboardingWrite(before, after api.Onboarding) api.Onboarding {
	if before.BrokerRequestID != after.BrokerRequestID && after.Awaiting(api.GateCredentials) {
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
	if before.OnboardingID == "" || before.GateNonce(api.GateConfirmSetup) != after.GateNonce(api.GateConfirmSetup) || api.ConfirmationActionDigest(before) != api.ConfirmationActionDigest(after) {
		after.ActionRevision = 0 // The current domain revision already names this action.
	} else {
		after.ActionRevision = api.ConfirmationRevision(before)
	}
	after.NoticeVersion = max(before.NoticeVersion, after.NoticeVersion)
	after.NoticeQueuedVersion = max(before.NoticeQueuedVersion, after.NoticeQueuedVersion)
	if after.NoticeVersion == before.NoticeVersion && after.NoticeQueuedVersion < after.NoticeVersion && after.NoticeNextAttempt.Before(before.NoticeNextAttempt) {
		after.NoticeNextAttempt = before.NoticeNextAttempt
	}
	// A human-action notice fires only once its link exists: posting earlier
	// produced "the form link arrives in the next message" texts whose follow-up
	// could be lost downstream. Terminal stages always notify — they carry a
	// result, not a link. An action URL appearing under an unchanged gate
	// signature (wizard mint, broker URL) is itself a bump-worthy change.
	actionURL := api.NoticeActionURL(after)
	actionable := after.Stage == api.StageLive || after.Stage == api.StageFailed || actionURL != ""
	if after.Initiator != nil && noticeRequired(after) && actionable && (before.Initiator == nil || gateSignature(before) != gateSignature(after) || before.Stage != after.Stage || api.NoticeActionURL(before) != actionURL) {
		after.NoticeVersion++
		after.NoticeEvent, after.NoticeEventID = "", ""
		after.NoticeNextAttempt = time.Time{}
	}
	return after
}

// gateSignature identifies the armed action link: kind+nonce+expiry is the
// one tuple a notice bump cares about — any change means a new link for the
// owner.
func gateSignature(o api.Onboarding) string {
	if o.Gate == nil {
		return ""
	}
	return o.Gate.Kind + "\x00" + o.Gate.Nonce + "\x00" + o.Gate.Expires.UTC().Format(time.RFC3339Nano)
}

// Install progress is a projection of the canonical onboarding, never a
// second independently advanced workflow.
func installProgress(op api.Operation, o api.Onboarding) api.Operation {
	if op.Intent != api.OpIntentInstall && op.Intent != api.OpIntentReauth || op.Intent == api.OpIntentReauth && op.State == api.OpStateDone {
		return op
	}
	op.Steps = slices.Clone(op.Steps)
	op.State, op.ErrorCode = connectorOpStateFromOnboarding(o), o.ErrorCode
	if o.CredsCollected() {
		markStep(&op, api.OpStepCollectCredentials, api.OpStepDone)
	} else {
		markStep(&op, api.OpStepCollectCredentials, api.OpStepPending)
	}
	if !o.AdmissionPending && (o.GateKind() == api.GateConfirmSetup || o.Stage == api.StageLive) {
		markStep(&op, api.OpStepAdmitProbe, api.OpStepDone)
	}
	if o.Stage == api.StageLive {
		markStep(&op, api.OpStepBind, api.OpStepDone)
		markStep(&op, api.OpStepProject, api.OpStepDone)
		// Reaching live settles the outcome notification too — the step
		// cannot stay wedged on whether a channel notice was queued.
		markStep(&op, api.OpStepNotify, api.OpStepDone)
	}
	if o.NoticeVersion > 0 && o.NoticeQueuedVersion == o.NoticeVersion {
		markStep(&op, api.OpStepNotify, api.OpStepDone)
	}
	return op
}

// noticeRequired mirrors the old noticePhase set: settled live/failed
// outcomes and every armed human action on a non-terminal record.
func noticeRequired(o api.Onboarding) bool {
	if o.Stage == api.StageLive || o.Stage == api.StageFailed {
		return true
	}
	return o.Stage != api.StageGone && o.Gate != nil
}
