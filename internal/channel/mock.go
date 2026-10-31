package channel

import (
	"context"
	"errors"

	"github.com/letya999/hermes-hub/internal/commodel"
	"github.com/letya999/hermes-hub/internal/identity"
	hubruntime "github.com/letya999/hermes-hub/internal/runtime"
)

// MockDeps declares the gateway capabilities the mock adapter borrows: the
// capture bench and the link table that maps chats to their claimed owner.
type MockDeps struct {
	Capture             func(d *commodel.Delivery) error
	LinkOwner           func(chatID int64) (string, error)
	UserByPrincipal     func(id string) commodel.User
	OwnsRoutineEnvelope func(job commodel.Job, canonical identity.Envelope) bool
}

// mock is the registered adapter for channel "mock" — the local test bench
// captures instead of sending: the durable record under spool/mock-sent is
// what /v1/mock/outbox reports.
type mock struct {
	d MockDeps
}

func NewMock(deps MockDeps) Channel { return mock{d: deps} }

func (c mock) ID() string { return "mock" }

func (c mock) NormalizeDelivery(d *commodel.Delivery) error {
	if d.ChatID == 0 {
		return errors.New("invalid delivery identity")
	}
	return nil
}

func (c mock) Deliver(ctx context.Context, delivery *commodel.Delivery) error {
	return c.d.Capture(delivery)
}

func (c mock) ValidateSchedule(in commodel.Schedule) error { return nil }

// Mock deliveries verify the link-table owner at send time: a chat may have
// been claimed by another user between enqueue and send.
func (c mock) VerifyDeliveryTarget(d *commodel.Delivery, ownerID string) error {
	owner, err := c.d.LinkOwner(d.ChatID)
	if err != nil || owner != ownerID {
		return errors.New("mock delivery target re-bound")
	}
	return nil
}

// The capture bench takes only what Deliver already records.
func (c mock) Caps() Caps { return Caps{} }

func (c mock) SendPresence(ctx context.Context, chatID, topicID int64) error {
	return nil
}

// The capture already stores any staged artifact refs in the record.
func (c mock) DeliverArtifact(ctx context.Context, delivery *commodel.Delivery, ref hubruntime.ArtifactRef) error {
	return nil
}

func (c mock) RouteBlockedNotice(item commodel.Schedule, d *commodel.Delivery) {}

func (c mock) JobEnvelope(user commodel.User, chatID, sender int64) identity.Envelope {
	return user.MockEnvelope(chatID)
}

func (c mock) PrepareRoutine(in *commodel.Schedule, caller identity.Envelope) (identity.Envelope, error) {
	owner, err := c.d.LinkOwner(in.ChatID)
	if err != nil || owner != caller.PrincipalID {
		return caller, ErrRoutineDenied
	}
	// Mock-channel routines carry the mock envelope so occurrence
	// authorization and delivery targeting stay coherent end to end.
	in.Envelope = c.d.UserByPrincipal(caller.PrincipalID).MockEnvelope(in.ChatID)
	return in.Envelope, nil
}

func (c mock) AuthorizeOccurrence(job commodel.Job, user commodel.User, configured bool) error {
	owner, err := c.d.LinkOwner(job.ChatID)
	if err != nil || owner != user.ID || !c.d.OwnsRoutineEnvelope(job, user.MockEnvelope(job.ChatID)) {
		return errors.New("routine policy changed")
	}
	return nil
}

func (c mock) ValidateOccurrence(o commodel.RoutineOccurrence, caller identity.Envelope) error {
	if o.Job.ChatID <= 0 || !caller.Equal(identity.MockEnvelope(o.Job.UserID, o.Job.ChatID, o.Job.RuntimeID, o.Job.PolicyVersion)) {
		return errors.New("invalid occurrence ownership")
	}
	return nil
}
