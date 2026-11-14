package toolhub

import (
	"context"
	"errors"
	"time"

	"github.com/letya999/hermes-hub/internal/identity"
	"github.com/letya999/hermes-hub/internal/toolhub/api"
)

func brokerCursorKey(auth identity.Envelope, shared bool) string {
	mode := "personal"
	if shared {
		mode = "shared"
	}
	return api.DeterministicID("cursor", auth.PrincipalID, auth.ContextID, auth.RuntimeID, auth.PolicyVersion, mode)
}

// ReconcileBrokerEvents consumes Broker's existing durable, owner-filtered
// event stream. Commit the cursor only after every affected record settles;
// replay after a crash is deliberately at least once.
func (c *ControlPlane) ReconcileBrokerEvents(ctx context.Context, auth identity.Envelope, shared bool) error {
	if c.Broker == nil || !c.Broker.Enabled() {
		return nil
	}
	client, err := c.brokerControl(auth, shared)
	if err != nil {
		return err
	}
	key := brokerCursorKey(auth, shared)
	c.Store.mu.RLock()
	cursor := c.Store.brokerCursors[key]
	c.Store.mu.RUnlock()
	ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	page, err := client.Events(ctx, cursor)
	if err != nil {
		return err
	}
	if page.NextCursor < cursor {
		return api.ErrStale
	}
	if page.Gap && page.EarliestAvailable > 0 {
		page.NextCursor = max(page.NextCursor, page.EarliestAvailable-1)
	}
	requests := make(map[string]bool)
	for _, event := range page.Events {
		if event.ContextID == auth.ContextID && (event.PrincipalID == auth.PrincipalID || shared) && event.Kind == "credential.ready" {
			requests[event.RequestID] = true
		}
	}
	c.Store.mu.RLock()
	var pending []api.Onboarding
	for _, o := range c.Store.onboardings {
		if o.PrincipalID == auth.PrincipalID && o.ContextID == auth.ContextID && o.RuntimeID == auth.RuntimeID && o.PolicyVersion == auth.PolicyVersion && (o.Awaiting(api.GateCredentials) || o.AdmissionPending && o.Stage == api.StagePending && o.GateKind() == "") && (page.Gap || cursor == 0 || requests[o.BrokerRequestID]) {
			pending = append(pending, cloneOnboarding(o))
		}
	}
	c.Store.mu.RUnlock()
	var failures []error
	for _, o := range pending {
		if o.Initiator == nil {
			o.Initiator = &auth
		}
		if c.onboardingShared(o) != shared {
			continue
		}
		if err := c.reconcileBrokerReady(ctx, auth, &o); err != nil && !errors.Is(err, api.ErrStale) {
			failures = append(failures, err)
		}
	}
	if len(failures) > 0 {
		return errors.Join(failures...)
	}
	if page.NextCursor == cursor {
		return nil
	}
	c.Store.mu.Lock()
	defer c.Store.mu.Unlock()
	if c.Store.brokerCursors[key] != cursor {
		return api.ErrStale
	}
	c.Store.brokerCursors[key] = page.NextCursor
	if c.Store.path != "" {
		before := c.Store.diskDigest
		if err := c.Store.saveLocked(c.Store.path); err != nil {
			if before == c.Store.diskDigest {
				c.Store.brokerCursors[key] = cursor
			}
			return err
		}
	}
	return nil
}

func (c *ControlPlane) reconcileBrokerReady(ctx context.Context, auth identity.Envelope, o *api.Onboarding) error {
	if c.Broker == nil || !c.Broker.Enabled() {
		return api.ErrCredentialVisibilityUnknown
	}
	if o.BrokerRequestID == "" {
		return nil
	}
	client, err := c.brokerControl(auth, c.onboardingShared(*o))
	if err != nil {
		return err
	}
	request, err := client.Request(ctx, o.BrokerRequestID)
	if err != nil {
		return err
	}
	// Background observation never renews an expired form or asks for a new
	// credential. Only the owner's explicit resend/reauth may do that.
	if request.Status != "ready" || request.CredentialID == "" {
		return nil
	}
	if o.AdmissionPending {
		return c.deferBrokerAdmission(auth, *o, request.CredentialID)
	}
	return c.refreshBrokerRequest(ctx, auth, o)
}
