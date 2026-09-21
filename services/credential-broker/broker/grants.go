package broker

import (
	"context"
	"encoding/json"
	"time"

	v1 "github.com/letya999/credential-broker/api/v1"
	"github.com/letya999/credential-broker/contract"
	"github.com/letya999/credential-broker/identity"
	"github.com/letya999/credential-broker/internal/seal"
)

func (b *Broker) CreateGrant(a identity.Actor, credentialID string, in v1.GrantRequest) (v1.Grant, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	if !b.live() {
		return v1.Grant{}, ErrUnavailable
	}
	c, ok := b.credentials[credentialID]
	if !ok || !managedBy(a, c) {
		return v1.Grant{}, ErrNotFound
	}
	if c.View.Status != "active" {
		return v1.Grant{}, ErrDenied
	}
	for _, id := range []string{in.PrincipalID, in.ContextID, in.RuntimeID, in.BindingID, in.WorkloadID} {
		if !identity.ValidID(id) {
			return v1.Grant{}, ErrInvalid
		}
	}
	if !validIdempotency(in.IdempotencyKey) || in.Execution != "dedicated" && in.Execution != "shared" || in.ContextID != c.View.ContextID {
		return v1.Grant{}, ErrInvalid
	}
	if c.View.OwnerKind == "user" && in.PrincipalID != c.View.PrincipalID {
		return v1.Grant{}, ErrDenied
	}
	ck := key(in.ContractID, in.ContractRevision)
	target, ok := b.catalog[ck]
	if !ok || !contract.Compatible(b.catalog[c.ContractKey], target) {
		return v1.Grant{}, ErrInvalid
	}
	if in.Execution == "shared" && !target.AllowShared {
		return v1.Grant{}, ErrDenied
	}
	raw, _ := json.Marshal(in)
	fp := seal.Digest(string(raw))
	idem := requestKey(a, "grant/"+credentialID+"/"+in.IdempotencyKey)
	for _, g := range b.grants {
		if g.Idempotency == idem {
			if g.Fingerprint != fp {
				return v1.Grant{}, ErrConflict
			}
			return g.View, nil
		}
		if !g.View.Active {
			continue
		}
		if g.View.BindingID == in.BindingID && g.View.ContextID == in.ContextID && g.View.PrincipalID == in.PrincipalID {
			return v1.Grant{}, ErrConflict
		}
		if g.View.WorkloadID == in.WorkloadID {
			if in.Execution != "shared" || g.View.Execution != "shared" {
				return v1.Grant{}, ErrDenied
			}
			oldContract := b.catalog[g.ContractKey]
			processScoped := len(target.Deliveries) > 0 || target.State != nil || len(oldContract.Deliveries) > 0 || oldContract.State != nil
			if processScoped && (g.View.CredentialID != credentialID || g.ContractKey != ck) {
				return v1.Grant{}, ErrDenied
			}
		}
	}
	if len(b.grants) >= 50000 {
		return v1.Grant{}, ErrUnavailable
	}
	g := grantRecord{View: v1.Grant{ID: newID("g"), CredentialID: credentialID, PrincipalID: in.PrincipalID, ContextID: in.ContextID, RuntimeID: in.RuntimeID, BindingID: in.BindingID, WorkloadID: in.WorkloadID, Execution: in.Execution, ContractID: target.ID, ContractRevision: target.Revision, PolicyVersion: a.PolicyVersion, Active: true}, ContractKey: ck, ContractDigest: target.Digest(), Idempotency: idem, Fingerprint: fp}
	ev := b.audit("grant.created", a)
	ev.CredentialID = credentialID
	ev.GrantID = g.View.ID
	ev.BindingID = in.BindingID
	ev.WorkloadID = in.WorkloadID
	_, e := b.commit(mutation{Audit: ev, Grant: &g})
	return g.View, e
}
func grantMatches(a identity.Actor, g grantRecord) bool {
	return g.View.Active && a.PrincipalID == g.View.PrincipalID && a.ContextID == g.View.ContextID && a.RuntimeID == g.View.RuntimeID && a.PolicyVersion == g.View.PolicyVersion && (a.BindingID == "" || a.BindingID == g.View.BindingID) && (a.WorkloadID == "" || a.WorkloadID == g.View.WorkloadID)
}
func (b *Broker) Acquire(a identity.Actor, in v1.AcquireLease) (v1.Lease, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	if !b.live() {
		return v1.Lease{}, ErrUnavailable
	}
	b.sweepLocked()
	g, ok := b.grants[in.GrantID]
	if !ok || !grantMatches(a, g) {
		return v1.Lease{}, ErrDenied
	}
	c, ok := b.credentials[g.View.CredentialID]
	if !ok || c.View.Status != "active" {
		return v1.Lease{}, ErrDenied
	}
	if in.TTLSeconds == 0 {
		in.TTLSeconds = 60
	}
	if in.TTLSeconds < 1 || in.TTLSeconds > 300 {
		return v1.Lease{}, ErrInvalid
	}
	if len(b.leases) >= 4096 {
		return v1.Lease{}, ErrUnavailable
	}
	cc := b.catalog[g.ContractKey]
	stateKey := ""
	if cc.State != nil {
		stateKey = seal.Digest(c.View.ID + "/" + g.View.ContextID + "/" + g.View.BindingID)
		for _, l := range b.leases {
			if l.StateKey == stateKey {
				return v1.Lease{}, ErrConflict
			}
		}
	}
	view := v1.Lease{ID: newID("l"), GrantID: g.View.ID, CredentialID: c.View.ID, Revision: c.View.Revision, ExpiresAt: b.now().Add(time.Duration(in.TTLSeconds) * time.Second), Deliveries: cc.Deliveries, State: cc.State}
	for _, r := range cc.Routes {
		view.Routes = append(view.Routes, r.ID)
	}
	ev := b.audit("lease.acquired", a)
	ev.CredentialID = c.View.ID
	ev.GrantID = g.View.ID
	ev.LeaseID = view.ID
	ev.BindingID = g.View.BindingID
	ev.WorkloadID = g.View.WorkloadID
	if _, e := b.commit(mutation{Audit: ev}); e != nil {
		return v1.Lease{}, e
	}
	b.leases[view.ID] = &leaseRecord{View: view, StateKey: stateKey, Active: map[string]context.CancelFunc{}}
	return cloneLease(view), nil
}
func (b *Broker) lease(a identity.Actor, id string) (*leaseRecord, grantRecord, credentialRecord, error) {
	if !b.live() {
		return nil, grantRecord{}, credentialRecord{}, ErrUnavailable
	}
	l, ok := b.leases[id]
	if !ok {
		return nil, grantRecord{}, credentialRecord{}, ErrDenied
	}
	g, ok := b.grants[l.View.GrantID]
	if !ok || !grantMatches(a, g) {
		return nil, g, credentialRecord{}, ErrDenied
	}
	c, ok := b.credentials[l.View.CredentialID]
	if !ok || c.View.Status != "active" || c.View.Revision != l.View.Revision || !b.now().Before(l.View.ExpiresAt) {
		return nil, g, c, ErrExpired
	}
	return l, g, c, nil
}
func (b *Broker) dropLease(id string) {
	l, ok := b.leases[id]
	if !ok {
		return
	}
	for _, cancel := range l.Active {
		cancel()
	}
	_ = b.cfg.Materializer.Remove(id)
	// Every request is rejected after this removal, even if OS cleanup failed.
	delete(b.leases, id)
}
func (b *Broker) invalidateCredential(id string) {
	for lid, l := range b.leases {
		if l.View.CredentialID == id {
			b.dropLease(lid)
		}
	}
}
func (b *Broker) sweepLocked() {
	for id, l := range b.leases {
		if !b.now().Before(l.View.ExpiresAt) {
			b.dropLease(id)
		}
	}
}
func (b *Broker) Sweep() { b.mu.Lock(); defer b.mu.Unlock(); b.sweepLocked() }
func (b *Broker) RevokeGrant(a identity.Actor, id string) error {
	b.mu.Lock()
	defer b.mu.Unlock()
	g, ok := b.grants[id]
	if !ok {
		return ErrNotFound
	}
	c, ok := b.credentials[g.View.CredentialID]
	if !ok || !managedBy(a, c) {
		return ErrNotFound
	}
	if !g.View.Active {
		return nil
	}
	g.View.Active = false
	ev := b.audit("grant.revoked", a)
	ev.CredentialID = c.View.ID
	ev.GrantID = id
	ev.BindingID = g.View.BindingID
	ev.WorkloadID = g.View.WorkloadID
	if _, e := b.commit(mutation{Audit: ev, Grant: &g}); e != nil {
		return e
	}
	for lid, l := range b.leases {
		if l.View.GrantID == id {
			b.dropLease(lid)
		}
	}
	return nil
}
func (b *Broker) RevokeCredential(a identity.Actor, id string) error {
	b.mu.Lock()
	defer b.mu.Unlock()
	c, ok := b.credentials[id]
	if !ok || !managedBy(a, c) {
		return ErrNotFound
	}
	if c.View.Status == "revoked" || c.View.Status == "deleted" {
		return nil
	}
	c.View.Status = "revoked"
	c.View.Revision++
	ev := b.audit("credential.revoked", a)
	ev.CredentialID = id
	ev.ConnectionID = c.View.ConnectionID
	if _, e := b.commit(mutation{Audit: ev, Credential: &c}); e != nil {
		return e
	}
	b.invalidateCredential(id)
	return nil
}

// Delete unlinks managed secret versions, never externally imported secrets.
// It is retryable: a backend failure leaves the credential unusable as "revoked".
func (b *Broker) DeleteCredential(ctx context.Context, a identity.Actor, id string) error {
	if e := b.RevokeCredential(a, id); e != nil {
		return e
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	c := b.credentials[id]
	if c.View.Status == "deleted" {
		return nil
	}
	for _, ref := range c.OwnedRefs {
		p := b.cfg.Providers[ref.Provider]
		if p == nil || !p.Capabilities().Delete || p.Delete(ctx, ref) != nil {
			return ErrUnavailable
		}
	}
	// State snapshots have their own lifecycle. Their current versions are removed.
	for sk, s := range b.states {
		belongs := false
		for _, g := range b.grants {
			if g.View.CredentialID == id && seal.Digest(id+"/"+g.View.ContextID+"/"+g.View.BindingID) == sk {
				belongs = true
			}
		}
		if belongs {
			p := b.cfg.Providers[s.Ref.Provider]
			if p == nil {
				return ErrUnavailable
			}
			for _, ref := range s.OwnedRefs {
				if p.Delete(ctx, ref) != nil {
					return ErrUnavailable
				}
			}
		}
	}
	c.View.Status = "deleted"
	ev := b.audit("credential.deleted", a)
	ev.CredentialID = id
	_, e := b.commit(mutation{Audit: ev, Credential: &c})
	return e
}

func (b *Broker) InspectLease(a identity.Actor, id string) (v1.Lease, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	l, _, _, e := b.lease(a, id)
	if e != nil {
		return v1.Lease{}, e
	}
	return cloneLease(l.View), nil
}
func (b *Broker) Renew(a identity.Actor, id string, seconds int) (v1.Lease, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	l, g, c, e := b.lease(a, id)
	if e != nil {
		return v1.Lease{}, e
	}
	if seconds < 1 || seconds > 300 {
		return v1.Lease{}, ErrInvalid
	}
	expires := b.now().Add(time.Duration(seconds) * time.Second)
	if expires.Before(l.View.ExpiresAt) {
		return v1.Lease{}, ErrInvalid
	}
	ev := b.audit("lease.renewed", a)
	ev.LeaseID = id
	ev.CredentialID = c.View.ID
	ev.GrantID = g.View.ID
	if _, e = b.commit(mutation{Audit: ev}); e != nil {
		return v1.Lease{}, e
	}
	l.View.ExpiresAt = expires
	if l.Materialized != nil {
		l.Materialized.ExpiresAt = expires
	}
	return cloneLease(l.View), nil
}
