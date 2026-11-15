package broker

import (
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"sort"

	v1 "github.com/letya999/credential-broker/api/v1"
	"github.com/letya999/credential-broker/contract"
	"github.com/letya999/credential-broker/identity"
	"github.com/letya999/credential-broker/provider"
)

// Use is an authorized, cancellable use of one exact grant and credential epoch.
// It is only for the trusted runtime adapter and the in-process credential proxy.
type Use struct {
	Context      context.Context
	Contract     contract.Contract
	CredentialID string
	Ref          provider.Ref
	Finish       func()
	Valid        func() bool
}

func (b *Broker) BeginUse(ctx context.Context, a identity.Actor, id string) (Use, error) {
	if !identity.ValidID(a.BindingID) || !identity.ValidID(a.WorkloadID) {
		return Use{}, ErrDenied
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	l, g, c, e := b.lease(a, id)
	if e != nil {
		return Use{}, e
	}
	if len(l.Active) >= 16 {
		return Use{}, ErrUnavailable
	}
	ev := b.audit("lease.used", a)
	ev.LeaseID = id
	ev.GrantID = g.View.ID
	ev.CredentialID = c.View.ID
	ev.BindingID = g.View.BindingID
	ev.WorkloadID = g.View.WorkloadID
	if _, e := b.commit(mutation{Audit: ev}); e != nil {
		return Use{}, e
	}
	op := newID("u")
	ctx, cancel := context.WithTimeout(ctx, l.View.ExpiresAt.Sub(b.now()))
	l.Active[op] = cancel
	oid := objectID(c.Ref)
	b.pins[oid]++
	b.usePins[op] = oid
	use := Use{Context: ctx, Contract: requestContract(b.catalog[g.ContractKey], c.Extra, demotedSet(c.Demoted)), CredentialID: c.View.ID, Ref: c.Ref}
	use.Finish = func() {
		cancel()
		b.mu.Lock()
		defer b.mu.Unlock()
		b.unpinOp(op)
		if cur, ok := b.leases[id]; ok {
			delete(cur.Active, op)
		}
	}
	use.Valid = func() bool {
		b.mu.Lock()
		defer b.mu.Unlock()
		_, _, _, e := b.lease(a, id)
		return e == nil && ctx.Err() == nil
	}
	return use, nil
}
func (b *Broker) Resolve(ctx context.Context, u Use) (provider.Bundle, error) {
	if !u.Valid() {
		return nil, ErrDenied
	}
	value, e := b.cfg.Providers.Resolve(ctx, u.Ref)
	if e != nil {
		return nil, ErrUnavailable
	}
	// OAuth fields are reserved and not part of user input validation.
	clean := value.Clone()
	for k := range clean {
		if k != "" && k[0] == '$' {
			delete(clean, k)
		}
	}
	err := u.Contract.ValidateValues(clean)
	clean.Wipe()
	if err != nil || !u.Valid() {
		value.Wipe()
		return nil, ErrDenied
	}
	return value, nil
}
func copyMaterialized(m v1.Materialized) v1.Materialized {
	raw, _ := json.Marshal(m)
	var out v1.Materialized
	_ = json.Unmarshal(raw, &out)
	return out
}
func (b *Broker) Materialize(ctx context.Context, a identity.Actor, id string) (v1.Materialized, error) {
	use, e := b.BeginUse(ctx, a, id)
	if e != nil {
		return v1.Materialized{}, e
	}
	defer use.Finish()
	b.mu.Lock()
	l, _, _, e := b.lease(a, id)
	if e != nil {
		b.mu.Unlock()
		return v1.Materialized{}, e
	}
	if l.Materialized != nil {
		out := copyMaterialized(*l.Materialized)
		b.mu.Unlock()
		return out, nil
	}
	stateKey := l.StateKey
	stateRec := b.states[stateKey]
	expires := l.View.ExpiresAt
	b.mu.Unlock()
	values, e := b.Resolve(use.Context, use)
	if e != nil {
		return v1.Materialized{}, e
	}
	defer values.Wipe()
	// Only declared input fields go to env/file materialization; refresh tokens stay
	// broker-side unless an explicit future token-export adapter is approved.
	for k := range values {
		if k != "" && k[0] == '$' {
			clear(values[k])
			delete(values, k)
		}
	}
	state := provider.Bundle{}
	if stateRec.Ref.Provider != "" {
		state, e = b.cfg.Providers.Resolve(use.Context, stateRec.Ref)
		if e != nil {
			return v1.Materialized{}, ErrUnavailable
		}
		defer state.Wipe()
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	l, _, _, e = b.lease(a, id)
	if e != nil {
		return v1.Materialized{}, e
	}
	if l.Materialized != nil {
		return copyMaterialized(*l.Materialized), nil
	}
	out, e := b.cfg.Materializer.Prepare(id, expires, use.Contract, values, state)
	if e != nil {
		return v1.Materialized{}, ErrUnavailable
	}
	ev := b.audit("lease.materialized", a)
	ev.LeaseID = id
	ev.CredentialID = use.CredentialID
	if _, e = b.commit(mutation{Audit: ev}); e != nil {
		_ = b.cfg.Materializer.Remove(id)
		return v1.Materialized{}, e
	}
	if stateRec.Ref.Provider != "" {
		l.PinnedObj = objectID(stateRec.Ref)
		b.pins[l.PinnedObj]++
	}
	l.Materialized = &out
	return copyMaterialized(out), nil
}

// unpinOp releases one use's hold on its credential object version. The
// durable row transitions at the next lifecycle mutation — pins are a
// live-count gate, not a stored flag.
func (b *Broker) unpinOp(op string) {
	b.unpin(b.usePins[op])
	delete(b.usePins, op)
}

// unpin decrements a live pin count.
func (b *Broker) unpin(oid string) {
	if oid == "" {
		return
	}
	if b.pins[oid] > 1 {
		b.pins[oid]--
	} else {
		delete(b.pins, oid)
	}
}
func (b *Broker) Release(ctx context.Context, a identity.Actor, id string, in v1.RuntimeRelease) error {
	b.mu.Lock()
	defer b.mu.Unlock()
	l, g, c, e := b.lease(a, id)
	if e != nil {
		return e
	}
	if in.Checkpoint {
		if !in.Quiesced || l.StateKey == "" || l.Materialized == nil {
			return ErrInvalid
		}
		values, e := b.cfg.Materializer.Snapshot(id, b.catalog[g.ContractKey])
		if e != nil {
			return ErrInvalid
		}
		defer values.Wipe()
		storage := b.catalog[g.ContractKey].Storage
		prev := b.states[l.StateKey]
		digest := bundleDigest(values)
		var ref provider.Ref
		var objs []objectRecord
		now := b.now()
		if prev.Digest != "" && prev.Digest == digest && prev.Ref.Provider != "" {
			// Digest-equal snapshot — reuse the existing version instead of
			// minting an identical object (SPEC-0087 R4).
			ref = prev.Ref
		} else {
			ref, e = b.putObject(ctx, a, storage, "state", l.StateKey, "state.checkpoint", values)
			if e != nil {
				return ErrUnavailable
			}
			objs = []objectRecord{{ID: objectID(ref), OwnerKind: "state", OwnerID: l.StateKey, Ref: ref, State: "current", Op: "state.checkpoint", CreatedAt: now, UpdatedAt: now}}
			if o, ok := b.objects[objectID(prev.Ref)]; ok && o.State == "current" {
				o.State = "retired"
				o.UpdatedAt = now
				objs = append(objs, o)
			}
		}
		sr := stateRecord{Key: l.StateKey, Ref: ref, Revision: prev.Revision + 1, OwnedRefs: prev.OwnedRefs, Digest: digest}
		ev := b.audit("state.checkpointed", a)
		ev.CredentialID = c.View.ID
		ev.LeaseID = id
		ev.BindingID = g.View.BindingID
		if _, e = b.commit(mutation{Audit: ev, State: &sr, Objects: objs}); e != nil {
			return e
		}
	}
	ev := b.audit("lease.released", a)
	ev.LeaseID = id
	ev.CredentialID = c.View.ID
	if _, e = b.commit(mutation{Audit: ev}); e != nil {
		return e
	}
	b.dropLease(id)
	return nil
}

// bundleDigest is the canonical content digest of a snapshot bundle —
// order-independent over keys so identical content always dedupes.
func bundleDigest(b provider.Bundle) string {
	keys := make([]string, 0, len(b))
	for k := range b {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	h := sha256.New()
	for _, k := range keys {
		h.Write([]byte(k))
		h.Write([]byte{0})
		h.Write(b[k])
		h.Write([]byte{0})
	}
	return base64.RawURLEncoding.EncodeToString(h.Sum(nil))
}

func cloneContract(c contract.Contract) contract.Contract {
	raw, _ := json.Marshal(c)
	var out contract.Contract
	_ = json.Unmarshal(raw, &out)
	return out
}
func cloneLease(l v1.Lease) v1.Lease {
	raw, _ := json.Marshal(l)
	var out v1.Lease
	_ = json.Unmarshal(raw, &out)
	return out
}
