// Package channel owns the provider seam of the communication plane: the
// Channel contract, its capability flags and the registered adapters.
// Adapters never reach the gateway — every capability they need arrives
// through a Deps struct, so this package never imports communication and
// provider code stays compile-time isolated from core routines.
package channel

import (
	"context"
	"errors"

	"github.com/letya999/hermes-hub/internal/commodel"
	"github.com/letya999/hermes-hub/internal/identity"
	hubruntime "github.com/letya999/hermes-hub/internal/runtime"
)

// Channel is the provider seam of the communication plane: a registered
// channel sends outbound deliveries and checks routine ownership so the
// gateway never switches on provider names. A new channel is added by
// implementing this interface and registering it in the gateway's registry.
type Channel interface {
	// ID is the Job.Channel/Delivery.Channel value ("telegram_bot", ...).
	ID() string
	// NormalizeDelivery applies provider identity rules at enqueue time:
	// transport/target defaults and field invariants.
	NormalizeDelivery(d *commodel.Delivery) error
	// Deliver sends one claimed outbox record. errors.Is(err, ErrUncertain)
	// marks the delivery uncertain; any other error retries then fails.
	Deliver(ctx context.Context, d *commodel.Delivery) error
	// AuthorizeOccurrence re-checks provider ownership at routine fire
	// time, after the shared owner/scope checks already passed.
	AuthorizeOccurrence(job commodel.Job, user commodel.User, configured bool) error
	// ValidateOccurrence checks provider envelope invariants on an
	// admitted occurrence record.
	ValidateOccurrence(o commodel.RoutineOccurrence, caller identity.Envelope) error
	// ValidateSchedule checks provider fields on a routine specification
	// (target format, thread syntax); shared rules live in the validator.
	ValidateSchedule(in commodel.Schedule) error
	// PrepareRoutine finishes a routine spec at admission: fill a provider
	// target, bind a provider envelope, or refuse. It runs before shared
	// draft validation; the returned envelope becomes the caller.
	PrepareRoutine(in *commodel.Schedule, caller identity.Envelope) (identity.Envelope, error)
	// RouteBlockedNotice retargets a blocked-routine notice to a safe
	// address — the schedule's own destination may be what caused the
	// block.
	RouteBlockedNotice(item commodel.Schedule, d *commodel.Delivery)
	// JobEnvelope builds the caller envelope for an inbound message on
	// this channel — provider identity mapping lives here.
	JobEnvelope(user commodel.User, chatID, sender int64) identity.Envelope
	// Caps reports what this channel can do — the gateway asks before
	// offering optional work instead of naming providers in conditionals.
	Caps() Caps
	// SendPresence signals progress to the user (e.g. typing). Only called
	// when Caps().Typing is true.
	SendPresence(ctx context.Context, chatID, topicID int64) error
	// DeliverArtifact sends one artifact ref; only called when
	// Caps().Artifacts is true. Blobs live in the spool — the channel reads
	// what it needs or answers a note.
	DeliverArtifact(ctx context.Context, d *commodel.Delivery, ref hubruntime.ArtifactRef) error
}

// Caps are optional provider features the communication plane can offer. A
// false flag makes the gateway skip the mechanism entirely — no provider
// names appear in core conditionals.
type Caps struct {
	Artifacts      bool // binary artifact delivery
	EditableStatus bool // one coalescing editable status message per job
	Typing         bool // presence/typing indicators while a job runs
	Destinations   bool // routine destinations bound to provider targets
	RoutineOrigin  bool // routine-origin jobs carry origin_job_id upstream
	// Actions marks a channel that renders actionable affordances for human
	// action links; adapters without it receive the same notice with the
	// link as plain message text.
	Actions bool
}

// ErrUncertain marks a delivery whose outcome the provider did not
// confirm — the gateway records it as uncertain rather than retrying it
// into a duplicate.
var ErrUncertain = errors.New("runtime result uncertain")

// ErrDeliveryRetried means Deliver already requeued the record itself
// (e.g. an edit throttle); the dispatcher returns without touching state.
var ErrDeliveryRetried = errors.New("delivery requeued")

// ErrRoutineDenied marks a channel refusing routine admission on ownership
// grounds — the HTTP surface maps it to 403.
var ErrRoutineDenied = errors.New("routine denied")

// FormatMarkdown is the Delivery.Format value requesting provider markdown
// rendering instead of a plain-text send.
const FormatMarkdown = "markdown"

// TargetVerifier is an optional Channel extension: a channel whose targets
// can be re-bound between enqueue and send (the mock link table works this
// way) verifies the owner before the send.
type TargetVerifier interface {
	VerifyDeliveryTarget(d *commodel.Delivery, ownerID string) error
}
