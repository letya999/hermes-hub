package broker

import (
	"context"
	"time"

	"github.com/letya999/credential-broker/identity"
)

// SweepObjects is the bounded, idempotent cleanup worker for provider
// objects (SPEC-0087 R4.6).
func (b *Broker) SweepObjects(ctx context.Context, a identity.Actor, limit int) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.sweepObjectsLocked(ctx, a, limit)
}

// Caller holds b.mu.
func (b *Broker) sweepObjectsLocked(ctx context.Context, a identity.Actor, limit int) (int, error) {
	var queue []objectRecord
	for _, o := range b.objects {
		if len(queue) == limit {
			break
		}
		switch o.State {
		case "retired":
			o.State = "pending-delete"
			o.UpdatedAt = b.now()
			queue = append(queue, o)
		case "intent":
			// Abandoned drafts/verifiers are freed only through their
			// ownership row: the owning request must already be terminal.
			if o.OwnerKind == "request" && b.requestTerminal(o.OwnerID) {
				o.State = "pending-delete"
				o.UpdatedAt = b.now()
				queue = append(queue, o)
			}
		case "pending-delete":
			if o.NextAt.After(b.now()) {
				continue
			}
			queue = append(queue, o)
		}
	}
	if len(queue) == 0 {
		return 0, nil
	}
	if _, e := b.commit(mutation{Audit: b.audit("object.sweep", a), Objects: queue}); e != nil {
		return 0, e
	}
	processed := 0
	for _, o := range queue {
		processed++
		if e := b.cfg.Providers[o.Ref.Provider].Delete(ctx, o.Ref); e != nil {
			o.NextAt = b.now().Add(30 * time.Second)
			o.UpdatedAt = b.now()
			if _, ce := b.commit(mutation{Audit: b.audit("object.sweep", a), Objects: []objectRecord{o}}); ce != nil {
				return processed, ce
			}
			return processed, ErrUnavailable
		}
		o.State = "deleted"
		o.UpdatedAt = b.now()
		ev := b.audit("object.deleted", a)
		if _, ce := b.commit(mutation{Audit: ev, Objects: []objectRecord{o}}); ce != nil {
			return processed, ce
		}
	}
	return processed, nil
}

func (b *Broker) requestTerminal(id string) bool {
	r, ok := b.requests[id]
	return ok && (r.View.Status == "canceled" || !b.now().Before(r.View.ExpiresAt))
}
