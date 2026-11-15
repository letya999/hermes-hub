package api

import "time"

// Model-facing connector vocabulary (ADR-0099 decision 4). These are the only
// lifecycle words the agent sees — next_action, onboarding_phase, link_sent
// and pending_action collapsed here. Everything is derived from (stage, gate,
// gate expiry, tombstone, binding status); nothing is stored.
const (
	ModelStateWorking    = "working"
	ModelStateWaitingYou = "waiting_you"
	ModelStateReady      = "ready"
	ModelStatePaused     = "paused"
	ModelStateBroken     = "broken"
	ModelStateDone       = "done"
)

// you_must names the single action that moves the record forward. An absent
// value means nothing is required — a ready or finished record needs no verb.
const (
	YouMustClickLink = "click_link"
	YouMustWait      = "wait"
	YouMustRetry     = "retry"
	YouMustRemove    = "remove"
	YouMustEnable    = "enable"
)

// delivery is the honest notice truth: not_sent means nothing was handed to
// the channel, sent means the channel confirmed transmission — never that the
// user read or clicked.
const (
	DeliveryNotSent   = "not_sent"
	DeliveryQueued    = "queued"
	DeliverySent      = "sent"
	DeliveryFailed    = "failed"
	DeliveryUncertain = "uncertain"
)

// ModelState projects the record onto the model's five-word lifecycle.
// disabled is the binding's truth — a live record with a disabled binding is
// paused, never a separate stored state. An armed owner-confirmed removal
// lives on a live record, so its gate outranks the stage; a leftover gate on
// a terminal record is a spent tombstone and the stage wins.
func (o Onboarding) ModelState(disabled bool) string {
	switch o.Stage {
	case StageGone:
		return ModelStateDone
	case StageFailed:
		return ModelStateBroken
	case StageLive:
		if o.GateKind() == GateRemove {
			return ModelStateWaitingYou
		}
		if disabled {
			return ModelStatePaused
		}
		return ModelStateReady
	}
	if o.Awaiting(GateConfirmSetup) || o.Awaiting(GateCredentials) || o.Awaiting(GateOAuth) || o.GateKind() == GateRemove {
		return ModelStateWaitingYou
	}
	return ModelStateWorking
}

// YouMust derives the one action the record waits for, from (stage, gate,
// gate expiry) alone — an armed remove link outranks setup, and an expired
// link asks for a reissue rather than a click. Terminal stages keep their own
// verbs regardless of a leftover gate.
func (o Onboarding) YouMust(now time.Time) string {
	switch o.Stage {
	case StageGone:
		if o.Tombstone == TombstoneRevoked {
			return YouMustRemove
		}
		return ""
	case StageFailed:
		if o.CleanupIncomplete() {
			return YouMustRemove
		}
		return YouMustRetry
	}
	if o.GateKind() == GateRemove {
		if o.GateExpiry(GateRemove).IsZero() || !now.Before(o.GateExpiry(GateRemove)) {
			return YouMustRetry
		}
		return YouMustClickLink
	}
	if o.Stage == StageLive {
		return ""
	}
	switch {
	case o.Awaiting(GateConfirmSetup), o.Awaiting(GateCredentials), o.Awaiting(GateOAuth):
		return YouMustClickLink
	case o.ConfirmationUsed:
		return YouMustEnable
	}
	return YouMustWait
}

// RecordDelivery is the record-local delivery truth: the notice exists and
// reached the queue (queued) or was never emitted (not_sent). Channel-confirmed
// sent/failed/uncertain arrives from the channel's durable status read.
func (o Onboarding) RecordDelivery() string {
	if o.NoticeVersion > 0 && o.NoticeQueuedVersion >= o.NoticeVersion {
		return DeliveryQueued
	}
	return DeliveryNotSent
}

// ContractDelivery folds the channel's durable prepare-delivery vocabulary
// onto the model's — the wire words unknown/uncertain/delivered map onto
// uncertain/sent without inventing success.
func ContractDelivery(status string) string {
	switch status {
	case PrepareDeliveryQueued:
		return DeliveryQueued
	case PrepareDeliveryDelivered:
		return DeliverySent
	case PrepareDeliveryUncertain:
		return DeliveryUncertain
	case PrepareDeliveryNotQueued:
		return DeliveryNotSent
	default:
		return DeliveryUncertain
	}
}
