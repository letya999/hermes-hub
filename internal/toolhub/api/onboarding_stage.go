package api

import (
	"encoding/json"
	"fmt"
	"time"
)

// Lifecycle stage — the only durable state an onboarding carries (ADR-0099).
// The install pipeline's fine-grained progress lives in journal Steps and
// admission fields; the pending human action lives in Gate, never in Stage.
const (
	StagePending = "pending"
	StageLive    = "live"
	StageFailed  = "failed"
	StageGone    = "gone"
)

// Gate kind names the single pending human action on a record. Arming a gate
// replaces the previous one in the same write — at most one live action link
// can exist per onboarding at any time.
const (
	GateConfirmSetup = "confirm_setup"
	GateCredentials  = "credentials"
	GateOAuth        = "oauth"
	GateRemove       = "remove"
)

// Gate is the armed user-facing action: kind, its mutation nonce and expiry.
// The stable wizard link (WizardNonce) is the delivery URL for interactive
// kinds; Gate.Nonce gates the mutation the link drives.
type Gate struct {
	Kind    string    `json:"kind"`
	Nonce   string    `json:"nonce,omitempty"`
	Expires time.Time `json:"expires,omitempty"`
}

// ArmGate installs kind+nonce+expiry as the record's only pending action.
func (o *Onboarding) ArmGate(kind, nonce string, expires time.Time) {
	o.Gate = &Gate{Kind: kind, Nonce: nonce, Expires: expires}
}

// ClearGate disarms the pending action — consumed, superseded or dead links.
func (o *Onboarding) ClearGate() {
	o.Gate = nil
}

// ClearGateKind disarms only when the armed gate is that kind — other gates
// keep their slot. Used when a write retires one specific action (the dead
// form link on failure) without touching an unrelated armed gate.
func (o *Onboarding) ClearGateKind(kind string) {
	if o.Gate != nil && o.Gate.Kind == kind {
		o.Gate = nil
	}
}

// GateKind returns the pending action kind, "" when no gate is armed.
// A consumed confirmation keeps its gate as a tombstone: replays match the
// stored nonce and get the dead-link page instead of an opaque 401.
func (o Onboarding) GateKind() string {
	if o.Gate == nil {
		return ""
	}
	return o.Gate.Kind
}

// GateNonce returns the armed nonce for kind, "" when another or no gate is
// armed — the direct replacement for the removed per-kind nonce fields.
func (o Onboarding) GateNonce(kind string) string {
	if o.Gate == nil || o.Gate.Kind != kind {
		return ""
	}
	return o.Gate.Nonce
}

// GateExpiry returns the armed expiry for kind, zero when another or no gate
// is armed.
func (o Onboarding) GateExpiry(kind string) time.Time {
	if o.Gate == nil || o.Gate.Kind != kind {
		return time.Time{}
	}
	return o.Gate.Expires
}

// GateLive reports an armed, unexpired gate — the link a user can still act on.
func (o Onboarding) GateLive(now time.Time) bool {
	return o.Gate != nil && (o.Gate.Expires.IsZero() || now.Before(o.Gate.Expires))
}

// Awaiting reports whether the record waits on the named human action. A
// consumed setup confirmation is not awaited: the gate stays only as the
// spent-link tombstone.
func (o Onboarding) Awaiting(kind string) bool {
	if o.Stage != StagePending || o.Gate == nil || o.Gate.Kind != kind {
		return false
	}
	return !(kind == GateConfirmSetup && o.ConfirmationUsed)
}

// CleanupIncomplete reports a removal ladder that stopped mid-way: a failed
// stage carrying unfinished finalizers. It is a derivation, not a stage.
func (o Onboarding) CleanupIncomplete() bool {
	return o.Stage == StageFailed && len(o.Finalizers) > 0
}

// AdmissionScheduled reports the broker admission cycle engaged — a probe
// lease held or a retry queued. The two admission timestamps together are
// what the old verifying/retry-scheduled phases meant.
func (o Onboarding) AdmissionScheduled() bool {
	return !o.AdmissionNextAttempt.IsZero() || !o.AdmissionLeaseUntil.IsZero()
}

// Removed reports the record died by owner removal or supersede — the old
// model wrote phase=removed for both.
func (o Onboarding) Removed() bool {
	return o.Stage == StageGone && o.Tombstone == TombstoneRemoved
}

// Revoked reports the record died by authorization cut — still resolvable as
// a target so reauth can reopen the same record.
func (o Onboarding) Revoked() bool {
	return o.Stage == StageGone && o.Tombstone == TombstoneRevoked
}

// DeadEnd reports a terminal record lifecycle operations can no longer
// address: removed and superseded records are history; only a revoked
// tombstone stays resolvable for reauth.
func (o Onboarding) DeadEnd() bool {
	return o.Stage == StageGone && o.Tombstone != TombstoneRevoked
}

// Retire lands the record in the absorbing stage. A gone record keeps only
// an armed remove link as the 410 tombstone — every other pending action
// dies with it.
func (o *Onboarding) Retire(tombstone string) {
	o.Stage = StageGone
	o.Tombstone = tombstone
	if o.Gate != nil && o.Gate.Kind != GateRemove {
		o.Gate = nil
	}
}

// Terminal reports the absorbing stage: the record is history, never a target.
func (o Onboarding) Terminal() bool {
	return o.Stage == StageGone
}

// CredsCollected projects the legacy pipeline boundary between "waiting for
// submitted credentials" and "credentials accepted": collected once the
// record reached confirmation or later, still open while a credential/OAuth
// gate is armed or nothing was delivered yet.
func (o Onboarding) CredsCollected() bool {
	if o.Stage != StagePending {
		return true
	}
	if o.Awaiting(GateCredentials) || o.Awaiting(GateOAuth) {
		return false
	}
	return o.ConfirmationUsed || o.Locator != "" || o.BrokerCredentialID != ""
}

// noticeWirePhase maps (stage, gate) onto the stable channel vocabulary the
// communication plane renders. The wire strings are the channel's rendering
// keys — not a second lifecycle model. Gates label only pending records:
// a gated action (reauth, allow_writes) always drops the record to pending
// first, so a gate surviving on live/failed/gone is a spent tombstone.
func (o Onboarding) noticeWirePhase() string {
	if o.Gate != nil && (o.Stage == StagePending || o.Stage == "") {
		switch o.Gate.Kind {
		case GateCredentials:
			return PhaseAwaitingCreds
		case GateOAuth:
			return PhaseAwaitingOAuth
		case GateConfirmSetup:
			if !o.ConfirmationUsed {
				return PhaseAwaitingConfirm
			}
		case GateRemove:
			if o.Stage == StagePending {
				return PhaseAwaitingConfirm
			}
		}
	}
	switch o.Stage {
	case StageLive:
		return PhaseEnabled
	case StageFailed:
		if len(o.Finalizers) > 0 {
			return PhaseCleanupIncomplete
		}
		return PhaseFailed
	case StageGone:
		if o.Tombstone == TombstoneRemoved {
			return PhaseRemoved
		}
		return PhaseRevoked
	}
	if o.ConfirmationUsed {
		return PhaseConfirmed
	}
	return PhasePreparing
}

// NoticePhase is the channel-facing settlement label for this record.
func (o Onboarding) NoticePhase() string { return o.noticeWirePhase() }

// onboardingLegacy mirrors the pre-Stage persisted shape. These fields are
// read exactly once at decode and folded into Stage/Gate by migrate; they are
// never serialized back.
type onboardingLegacy struct {
	Phase               string    `json:"phase"`
	ConfirmationNonce   string    `json:"confirmation_nonce"`
	ConfirmationExpires time.Time `json:"confirmation_expires"`
	FormNonce           string    `json:"form_nonce"`
	FormExpires         time.Time `json:"form_expires"`
	RemoveNonce         string    `json:"remove_nonce"`
	RemoveExpires       time.Time `json:"remove_expires"`
}

// Tombstone names why a gone record died: "removed" is dead for good, while
// "revoked" stays resolvable so reauth can reopen the same record. Superseded
// records carry SupersededBy instead.
const (
	TombstoneRemoved = "removed"
	TombstoneRevoked = "revoked"
)

// UnmarshalJSON folds the legacy Phase/nonce shape into Stage+Gate. The store
// decoder uses DisallowUnknownFields, but a type with a custom unmarshaler
// decodes itself — legacy keys land here instead of erroring.
func (o *Onboarding) UnmarshalJSON(body []byte) error {
	type alias Onboarding
	var raw struct {
		alias `json:",inline"`
		onboardingLegacy
	}
	if err := json.Unmarshal(body, &raw); err != nil {
		return err
	}
	*o = Onboarding(raw.alias)
	o.migrateLegacyStage(raw.onboardingLegacy)
	return nil
}

// migrateLegacyStage maps the retired 14-phase vocabulary onto Stage+Gate.
// An armed remove nonce outranked the setup phase in the old model; it keeps
// that precedence here. Gate-less pipeline phases collapse to bare pending.
func (o *Onboarding) migrateLegacyStage(legacy onboardingLegacy) {
	if o.Stage != "" {
		return // already new-schema
	}
	switch legacy.Phase {
	case "awaiting-credentials":
		o.Stage = StagePending
		o.ArmGate(GateCredentials, legacy.FormNonce, legacy.FormExpires)
	case "awaiting-confirm":
		o.Stage = StagePending
		// The gate arms even when ConfirmationUsed: it is the spent-link
		// tombstone a replayed click matches against.
		o.ArmGate(GateConfirmSetup, legacy.ConfirmationNonce, legacy.ConfirmationExpires)
	case "awaiting-oauth":
		o.Stage = StagePending
		o.ArmGate(GateOAuth, "", time.Time{})
	case "enabled", "disabled":
		o.Stage = StageLive
	case "failed", "cleanup_incomplete":
		o.Stage = StageFailed
	case "removed":
		o.Stage, o.Tombstone = StageGone, TombstoneRemoved
	case "revoked":
		o.Stage, o.Tombstone = StageGone, TombstoneRevoked
	case "preparing":
		o.Stage, o.PreparingStub = StagePending, true
	default:
		// review, verifying, retry-scheduled, confirmed — and any
		// unrecognized value — all fold into the working stage.
		o.Stage = StagePending
	}
	if legacy.RemoveNonce != "" {
		o.ArmGate(GateRemove, legacy.RemoveNonce, legacy.RemoveExpires)
	}
	if o.SupersededBy != "" {
		o.Stage = StageGone
	}
	if o.Stage == StageGone && o.Gate != nil && o.Gate.Kind != GateRemove {
		o.Gate = nil
	}
}

// validStages is the closed stage set for Validate.
func validStage(stage string) bool {
	switch stage {
	case StagePending, StageLive, StageFailed, StageGone:
		return true
	}
	return false
}

func validGateKind(kind string) bool {
	switch kind {
	case GateConfirmSetup, GateCredentials, GateOAuth, GateRemove:
		return true
	}
	return false
}

var stageEdges = map[string][]string{
	// reauth reopens the pipeline on a live record; a stuck cleanup keeps
	// failed reachable; retry re-enters pending from failed.
	StagePending: {StageLive, StageFailed, StageGone},
	StageLive:    {StagePending, StageFailed, StageGone},
	StageFailed:  {StagePending, StageGone},
	StageGone:    {},
}

func validStageEdge(from, to string) bool {
	for _, allowed := range stageEdges[from] {
		if allowed == to {
			return true
		}
	}
	return false
}

func (o Onboarding) validateStage() error {
	if !validStage(o.Stage) {
		return fmt.Errorf("%w: onboarding stage", ErrInvalid)
	}
	if o.Gate != nil {
		if !validGateKind(o.Gate.Kind) {
			return fmt.Errorf("%w: onboarding gate", ErrInvalid)
		}
		// A gone record keeps only a spent remove link as the 410 tombstone;
		// every other pending action dies with the record.
		if o.Stage == StageGone && o.Gate.Kind != GateRemove {
			return fmt.Errorf("%w: gate on terminal onboarding", ErrInvalid)
		}
	}
	return nil
}
