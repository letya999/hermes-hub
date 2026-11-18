package toolhub

import (
	"context"
	"errors"
	"time"

	"github.com/letya999/hermes-hub/internal/identity"
	"github.com/letya999/hermes-hub/internal/toolhub/api"
)

// LifecyclePass starts bounded independent work. A slow provider cannot hold a
// status request or the admission of a different onboarding behind a global lock.
func (c *ControlPlane) LifecyclePass(ctx context.Context, enrolled []identity.Envelope) {
	for _, auth := range enrolled {
		for _, shared := range []bool{false, true} {
			key := brokerCursorKey(auth, shared)
			c.startLifecycleTask(ctx, key, func(ctx context.Context) {
				_ = c.ReconcileBrokerEvents(ctx, auth, shared)
			})
		}
	}
	c.Store.mu.RLock()
	var records []api.Onboarding
	for _, o := range c.Store.onboardings {
		records = append(records, cloneOnboarding(o))
	}
	c.Store.mu.RUnlock()
	for _, o := range records {
		var actor identity.Envelope
		for _, auth := range enrolled {
			if auth.Validate(o.PrincipalID, o.ContextID, o.RuntimeID, o.PolicyVersion) == nil {
				actor = auth
				if o.Initiator != nil {
					actor.ExternalIdentityID, actor.ConversationID, actor.DeliveryTargetID = o.Initiator.ExternalIdentityID, o.Initiator.ConversationID, o.Initiator.DeliveryTargetID
				}
				break
			}
		}
		if actor.PrincipalID == "" {
			continue
		}
		pending := noticeRequired(o) && o.NoticeVersion > o.NoticeQueuedVersion && !c.now().Before(o.NoticeNextAttempt)
		retry := o.AdmissionPending && o.BrokerCredentialID != "" && o.Stage == api.StagePending && o.GateKind() == "" && !c.now().Before(o.AdmissionNextAttempt) && !c.now().Before(o.AdmissionLeaseUntil)
		refresh := pending && o.Awaiting(api.GateConfirmSetup) && !c.now().Before(o.GateExpiry(api.GateConfirmSetup))
		repair := (o.Awaiting(api.GateCredentials) && o.BrokerRequestID != "" || o.Awaiting(api.GateOAuth)) && c.now().Sub(o.ObservedAt) >= 30*time.Second
		adopt := o.Initiator == nil && o.Awaiting(api.GateConfirmSetup) && c.now().Before(o.GateExpiry(api.GateConfirmSetup))
		if !pending && !retry && !refresh && !repair && !adopt {
			continue
		}
		c.startLifecycleTask(ctx, o.OnboardingID, func(ctx context.Context) {
			current, err := c.Store.OnboardingFor(actor, o.OnboardingID)
			if err != nil {
				return
			}
			if adopt {
				current.Initiator = &actor
				expected := current.Revision
				current.Revision++
				if err := c.Store.PutOnboardingIfRevision(current, expected); err != nil {
					return
				}
				current, _ = c.Store.OnboardingFor(actor, o.OnboardingID)
			}
			if repair {
				if current.Initiator == nil {
					current.Initiator = &actor
				}
				if current.Awaiting(api.GateOAuth) {
					definition, derr := c.definitionOf(current)
					if derr == nil {
						_, err = c.ensurePreparedOAuth(ctx, actor, current, definition, false)
					} else {
						err = derr
					}
				} else {
					err = c.reconcileBrokerReady(ctx, actor, &current)
				}
				c.Store.recordOnboardingObservation(actor, o.OnboardingID, c.now(), err != nil)
			}
			if retry {
				_ = c.completeBrokerAdmission(ctx, actor, &current, current.BrokerCredentialID)
			}
			if refresh {
				_ = c.refreshConfirmation(ctx, actor, &current)
			}
			c.deliverOnboardingNotice(ctx, actor, o.OnboardingID)
		})
	}
}

func (c *ControlPlane) startLifecycleTask(ctx context.Context, key string, work func(context.Context)) {
	if ctx.Err() != nil {
		return
	}
	c.lifecycleMu.Lock()
	if c.lifecycleTasks == nil {
		c.lifecycleTasks = make(map[string]bool)
	}
	if c.lifecycleTasks[key] || len(c.lifecycleTasks) >= 8 {
		c.lifecycleMu.Unlock()
		return
	}
	c.lifecycleTasks[key] = true
	c.lifecycleMu.Unlock()
	go func() {
		defer func() { c.lifecycleMu.Lock(); delete(c.lifecycleTasks, key); c.lifecycleMu.Unlock() }()
		ctx, cancel := context.WithTimeout(ctx, 45*time.Second)
		defer cancel()
		work(ctx)
	}()
}

func (c *ControlPlane) deferBrokerAdmission(auth identity.Envelope, o api.Onboarding, credential string) error {
	if o.BrokerCredentialID == credential && o.Stage == api.StagePending && o.GateKind() == "" {
		return nil
	}
	expected := o.Revision
	o.BrokerCredentialID, o.Locator = credential, credential
	o.ObservedAt, o.ObservationCode = c.now(), ""
	o.Stage = api.StagePending
	o.ClearGate()
	o.AdmissionNextAttempt, o.AdmissionLeaseUntil = time.Time{}, time.Time{}
	o.Revision++
	err := c.Store.PutOnboardingIfRevision(o, expected)
	if errors.Is(err, api.ErrStale) {
		return nil
	}
	return err
}

func (s *Store) recordOnboardingObservation(auth identity.Envelope, id string, at time.Time, failed bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	o, ok := s.onboardings[id]
	if !ok || auth.Validate(o.PrincipalID, o.ContextID, o.RuntimeID, o.PolicyVersion) != nil || o.DeadEnd() || o.CleanupIncomplete() || !at.After(o.ObservedAt) {
		return
	}
	previous := o.ObservationCode
	o.ObservedAt, o.ObservationCode = at, ""
	if failed {
		o.ObservationCode = "broker_unavailable"
	}
	s.onboardings[id] = o
	// ObservedAt is a heartbeat — memory-truth only; the reconciler re-derives
	// it after restart. The durable record commits only when the observation
	// verdict itself changes, so every 30s scan no longer writes the registry.
	if o.ObservationCode != previous {
		_ = s.commitOnboardingLocked(o)
	}
}
