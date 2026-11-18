package toolhub

import (
	"context"
	"errors"
	"log"
	"maps"
	"path/filepath"
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
	cursors := c.cursorStore()
	cursor := cursors.Get(key)
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
	if !cursors.CompareAndAdvance(key, cursor, page.NextCursor) {
		return api.ErrStale
	}
	// Durability is batched: the sidecar flushes at most once per
	// cursorFlushInterval, so a crash replays at most a minute of broker
	// events — already covered by the at-least-once contract above.
	if err := cursors.Flush(c.now()); err != nil {
		log.Printf("toolhub: cursor flush deferred: %v", err)
	}
	return nil
}

// cursorStore lazily binds the sidecar to the store's directory and seeds it
// with the registry's persisted brokerCursors — the one-time migration off
// kvmap rows written before CHG-0308. Constructed ControlPlanes that never
// reconcile broker events never touch the file.
//
// The write lock is paid once — at bind. Taking s.mu.Lock() on every pass
// made each reconcile a waiting writer, and a queued writer parks every
// /v1/rights/decide reader behind it (RWMutex writer preference) — the same
// starvation the sidecar exists to remove.
func (c *ControlPlane) cursorStore() *CursorStore {
	c.Store.mu.RLock()
	cs := c.Cursors
	c.Store.mu.RUnlock()
	if cs != nil {
		return cs
	}
	c.Store.mu.Lock()
	defer c.Store.mu.Unlock()
	if c.Cursors == nil {
		path := ""
		if c.Store.path != "" {
			path = filepath.Join(filepath.Dir(c.Store.path), "cursors.json")
		}
		loaded, err := LoadCursorStore(path, maps.Clone(c.Store.brokerCursors))
		if err != nil {
			log.Printf("toolhub: cursor sidecar unreadable, seeding from registry: %v", err)
			loaded = &CursorStore{path: path, cursors: maps.Clone(c.Store.brokerCursors)}
		}
		c.Cursors = loaded
	}
	return c.Cursors
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
		// A still-pending request keeps its live connect link, so the armed
		// credential notice must reach the channel — the record cannot say a
		// link was sent while no message ever left. Dead links stay silent
		// until the owner's resend re-mints the request.
		if o.Awaiting(api.GateCredentials) && !c.brokerRequestDead(request) {
			c.deliverOutcomeNotice(ctx, auth, o)
		}
		return nil
	}
	if o.AdmissionPending {
		return c.deferBrokerAdmission(auth, *o, request.CredentialID)
	}
	return c.refreshBrokerRequest(ctx, auth, o)
}
