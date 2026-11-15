package toolhub

import (
	"context"
	"time"

	"github.com/letya999/hermes-hub/internal/identity"
	"github.com/letya999/hermes-hub/internal/toolhub/api"
)

// The pending notice lives in the same durable row as the state transition.
// A lost HTTP acknowledgement is retried with the same action proof, so the
// gateway's durable outbox deduplication closes the commit -> send crash window.
func (c *ControlPlane) deliverOnboardingNotice(ctx context.Context, auth identity.Envelope, id string) bool {
	o, err := c.Store.OnboardingFor(auth, id)
	if err != nil || !noticeRequired(o) || o.NoticeVersion == 0 || o.NoticeVersion == o.NoticeQueuedVersion || c.now().Before(o.NoticeNextAttempt) {
		return false
	}
	url, queued := api.PostPrepareNoticeWithDelivery(ctx, auth, o, o.NoticeEvent, len(o.Permissions))
	c.Store.mu.Lock()
	defer c.Store.mu.Unlock()
	current, ok := c.Store.onboardings[id]
	if !ok || current.NoticeVersion != o.NoticeVersion {
		return false
	}
	if queued {
		current.NoticeQueuedVersion = o.NoticeVersion
		current.NoticeNextAttempt = time.Time{}
		if url != "" {
			current.ChannelAuthURL = url
		}
	} else {
		current.NoticeNextAttempt = c.now().Add(5 * time.Second)
	}
	return c.Store.commitOnboardingLocked(current) == nil && queued
}

func (s *Store) RequestOnboardingNotice(auth identity.Envelope, id string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	o, ok := s.onboardings[id]
	if !ok {
		return api.ErrNotFound
	}
	if auth.Validate(o.PrincipalID, o.ContextID, o.RuntimeID, o.PolicyVersion) != nil {
		return api.ErrUnauthorized
	}
	// An armed removal counts as a pending notice even outside setup phases:
	// the owner may ask for the remove link again on a failed record. A
	// consumed confirmation keeps its gate tombstone, so it still counts.
	awaiting := o.Awaiting(api.GateCredentials) || o.Awaiting(api.GateOAuth) || o.GateKind() == api.GateConfirmSetup
	armedRemoval := o.GateNonce(api.GateRemove) != "" && !o.Removed()
	if !awaiting && !armedRemoval {
		return api.ErrConflict
	}
	o.Revision++
	o.NoticeVersion++
	o.NoticeEvent, o.NoticeEventID = "link-resent", api.RandomNonce()
	o.NoticeNextAttempt = time.Time{}
	if o.Initiator == nil {
		o.Initiator = &auth
	}
	return s.putOnboardingLocked(o)
}
