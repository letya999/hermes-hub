package communication

import (
	"context"
	"errors"

	"github.com/letya999/hermes-hub/internal/identity"
	hubruntime "github.com/letya999/hermes-hub/internal/runtime"
)

// mockChannel is the registered adapter for channel "mock" — the local
// test bench captures instead of sending: the durable record under
// spool/mock-sent is what /v1/mock/outbox reports.
type mockChannel struct {
	g *Gateway
}

func (c mockChannel) ID() string { return "mock" }

func (c mockChannel) NormalizeDelivery(d *Delivery) error {
	if d.ChatID == 0 {
		return errors.New("invalid delivery identity")
	}
	return nil
}

func (c mockChannel) Deliver(ctx context.Context, delivery *Delivery) error {
	return c.g.captureMock(delivery)
}

func (c mockChannel) ValidateSchedule(in Schedule) error { return nil }

// Mock deliveries verify the link-table owner at send time: a chat may have
// been claimed by another user between enqueue and send.
func (c mockChannel) VerifyDeliveryTarget(d *Delivery, ownerID string) error {
	owner, err := c.g.mockLinkOwner(d.ChatID)
	if err != nil || owner != ownerID {
		return errors.New("mock delivery target re-bound")
	}
	return nil
}

// The capture bench takes only what Deliver already records.
func (c mockChannel) Caps() ChannelCaps { return ChannelCaps{} }

func (c mockChannel) SendPresence(ctx context.Context, chatID, topicID int64) error {
	return nil
}

// captureMock already stores any staged artifact refs in the record.
func (c mockChannel) DeliverArtifact(ctx context.Context, delivery *Delivery, ref hubruntime.ArtifactRef) error {
	return nil
}

func (c mockChannel) RouteBlockedNotice(item Schedule, d *Delivery) {}

func (c mockChannel) JobEnvelope(user User, chatID, sender int64) identity.Envelope {
	return user.mockEnvelope(chatID)
}

func (c mockChannel) PrepareRoutine(in *Schedule, caller identity.Envelope) (identity.Envelope, error) {
	owner, err := c.g.mockLinkOwner(in.ChatID)
	if err != nil || owner != caller.PrincipalID {
		return caller, errRoutineDenied
	}
	// Mock-channel routines carry the mock envelope so occurrence
	// authorization and delivery targeting stay coherent end to end.
	in.Envelope = c.g.user(caller.PrincipalID).mockEnvelope(in.ChatID)
	return in.Envelope, nil
}

func (c mockChannel) AuthorizeOccurrence(job Job, user User, configured bool) error {
	owner, err := c.g.mockLinkOwner(job.ChatID)
	if err != nil || owner != user.ID || !c.g.ownsRoutineEnvelope(job, user.mockEnvelope(job.ChatID)) {
		return errors.New("routine policy changed")
	}
	return nil
}

func (c mockChannel) ValidateOccurrence(o RoutineOccurrence, caller identity.Envelope) error {
	if o.Job.ChatID <= 0 || caller != identity.MockEnvelope(o.Job.UserID, o.Job.ChatID, o.Job.RuntimeID, o.Job.PolicyVersion) {
		return errors.New("invalid occurrence ownership")
	}
	return nil
}
