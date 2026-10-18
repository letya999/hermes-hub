package communication

import (
	"context"
	"errors"

	"github.com/letya999/hermes-hub/internal/identity"
	hubruntime "github.com/letya999/hermes-hub/internal/runtime"
)

// Channel is the provider seam of the communication plane: a registered
// channel sends outbound deliveries and checks routine ownership so the
// gateway never switches on provider names. A new channel is added by
// implementing this interface and registering it in channelPlugins.
type Channel interface {
	// ID is the Job.Channel/Delivery.Channel value ("telegram_bot", ...).
	ID() string
	// NormalizeDelivery applies provider identity rules at enqueue time:
	// transport/target defaults and field invariants.
	NormalizeDelivery(d *Delivery) error
	// Deliver sends one claimed outbox record. errors.Is(err, ErrUncertain)
	// marks the delivery uncertain; any other error retries then fails.
	Deliver(ctx context.Context, d *Delivery) error
	// AuthorizeOccurrence re-checks provider ownership at routine fire
	// time, after the shared owner/scope checks already passed.
	AuthorizeOccurrence(job Job, user User, configured bool) error
	// ValidateOccurrence checks provider envelope invariants on an
	// admitted occurrence record.
	ValidateOccurrence(o RoutineOccurrence, caller identity.Envelope) error
	// ValidateSchedule checks provider fields on a routine specification
	// (target format, thread syntax); shared rules live in the validator.
	ValidateSchedule(in Schedule) error
	// PrepareRoutine finishes a routine spec at admission: fill a provider
	// target, bind a provider envelope, or refuse. It runs before shared
	// draft validation; the returned envelope becomes the caller.
	PrepareRoutine(in *Schedule, caller identity.Envelope) (identity.Envelope, error)
	// RouteBlockedNotice retargets a blocked-routine notice to a safe
	// address — the schedule's own destination may be what caused the
	// block.
	RouteBlockedNotice(item Schedule, d *Delivery)
	// JobEnvelope builds the caller envelope for an inbound message on
	// this channel — provider identity mapping lives here.
	JobEnvelope(user User, chatID, sender int64) identity.Envelope
	// Caps reports what this channel can do — the gateway asks before
	// offering optional work instead of naming providers in conditionals.
	Caps() ChannelCaps
	// SendPresence signals progress to the user (e.g. typing). Only called
	// when Caps().Typing is true.
	SendPresence(ctx context.Context, chatID, topicID int64) error
	// DeliverArtifact sends one artifact ref; only called when
	// Caps().Artifacts is true. Blobs live in the spool — the channel reads
	// what it needs or answers a note.
	DeliverArtifact(ctx context.Context, d *Delivery, ref hubruntime.ArtifactRef) error
}

// ChannelCaps are optional provider features the communication plane can
// offer. A false flag makes the gateway skip the mechanism entirely —
// no provider names appear in core conditionals.
type ChannelCaps struct {
	Artifacts      bool // binary artifact delivery
	EditableStatus bool // one coalescing editable status message per job
	Typing         bool // presence/typing indicators while a job runs
	Destinations   bool // routine destinations bound to provider targets
	RoutineOrigin  bool // routine-origin jobs carry origin_job_id upstream
}

// channelPlugins is the registry: provider id → adapter constructor. The
// adapters are plain structs bound to their gateway at lookup time, so
// struct-literal Gateway fixtures in tests resolve channels without init.
var channelPlugins = map[string]func(*Gateway) Channel{
	"telegram_bot": func(g *Gateway) Channel { return telegramChannel{g} },
	"slack_app":    func(g *Gateway) Channel { return slackChannel{g} },
	"mock":         func(g *Gateway) Channel { return mockChannel{g} },
}

// channelFor resolves an adapter; g may be nil for the pure methods
// (ValidateOccurrence) which never touch the gateway.
func channelFor(g *Gateway, id string) Channel {
	if makeChannel, ok := channelPlugins[id]; ok {
		return makeChannel(g)
	}
	return nil
}

func (g *Gateway) channel(id string) Channel {
	return channelFor(g, id)
}

// errDeliveryRetried means Deliver already requeued the record itself
// (e.g. an edit throttle); the dispatcher returns without touching state.
var errDeliveryRetried = errors.New("delivery requeued")

// deliveryTargetVerifier is an optional Channel extension: a channel whose
// targets can be re-bound between enqueue and send (the mock link table
// works this way) verifies the owner before the send.
type deliveryTargetVerifier interface {
	VerifyDeliveryTarget(d *Delivery, ownerID string) error
}

// errRoutineDenied marks a channel refusing routine admission on ownership
// grounds — the HTTP surface maps it to 403.
var errRoutineDenied = errors.New("routine denied")
