package broker

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base32"
	"encoding/json"
	"strings"
	"time"

	v1 "github.com/letya999/credential-broker/api/v1"
	"github.com/letya999/credential-broker/identity"
	"github.com/letya999/credential-broker/internal/seal"
	"github.com/letya999/credential-broker/provider"
)

func (b *Broker) CreateRequest(ctx context.Context, a identity.Actor, in v1.CreateRequest) (v1.Request, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	if !b.live() {
		return v1.Request{}, ErrUnavailable
	}
	if !a.Valid() || !identity.ValidID(in.ConnectionID) || !identity.ValidID(in.OnboardingID) || !validIdempotency(in.IdempotencyKey) || in.OwnerKind != "user" && in.OwnerKind != "context" || in.OwnerKind == "context" && !a.ContextManager {
		return v1.Request{}, ErrInvalid
	}
	ck := key(in.ContractID, in.ContractRevision)
	c, ok := b.catalog[ck]
	if !ok {
		return v1.Request{}, ErrInvalid
	}
	if in.ExternalAlias == "" && !b.cfg.Providers[c.Storage].Capabilities().Write {
		return v1.Request{}, ErrInvalid
	}
	raw, _ := json.Marshal(in)
	fingerprint := seal.Digest(string(raw))
	idem := requestKey(a, in.IdempotencyKey)
	for _, r := range b.requests {
		if r.Idempotency == idem {
			if r.Fingerprint != fingerprint {
				return v1.Request{}, ErrConflict
			}
			return r.View, nil
		}
	}
	if len(b.requests) >= 10000 {
		return v1.Request{}, ErrUnavailable
	}
	if in.RotateCredentialID != "" {
		existing, ok := b.credentials[in.RotateCredentialID]
		if !ok || !managedBy(a, existing) || existing.View.Status == "deleted" || existing.ContractKey != ck || existing.View.ConnectionID != in.ConnectionID || existing.View.OwnerKind != in.OwnerKind {
			return v1.Request{}, ErrDenied
		}
	} else {
		for _, existing := range b.credentials {
			if existing.View.ContextID == a.ContextID && existing.View.ConnectionID == in.ConnectionID && existing.View.Status != "deleted" {
				return v1.Request{}, ErrConflict
			}
		}
	}
	for _, prior := range b.requests {
		if prior.View.ConnectionID == in.ConnectionID && prior.Actor.ContextID == a.ContextID && (prior.View.Status == "pending" || prior.View.Status == "authorizing" || prior.View.Status == "exchanging") && b.now().Before(prior.View.ExpiresAt) {
			return v1.Request{}, ErrConflict
		}
	}
	id := newID("r")
	view := v1.Request{ID: id, ContractID: c.ID, ContractRevision: c.Revision, ConnectionID: in.ConnectionID, OnboardingID: in.OnboardingID, Status: "pending", AuthorizationURL: b.cfg.PublicOrigin + "/connect/" + id, ExpiresAt: b.now().Add(15 * time.Minute)}
	r := requestRecord{View: view, Actor: a, OwnerKind: in.OwnerKind, ContractKey: ck, ContractDigest: c.Digest(), Idempotency: idem, Fingerprint: fingerprint, RotateID: in.RotateCredentialID, ExternalAlias: in.ExternalAlias}
	if in.RotateCredentialID != "" {
		r.ExpectedRevision = b.credentials[in.RotateCredentialID].View.Revision
	}
	if in.ExternalAlias != "" {
		alias, ok := b.cfg.ExternalAliases[in.ExternalAlias]
		if !ok || alias.PrincipalID != a.PrincipalID || alias.ContextID != a.ContextID || alias.ContractKey != ck || c.OAuthProvider != "" {
			return v1.Request{}, ErrDenied
		}
		value, e := b.cfg.Providers.Resolve(ctx, alias.Ref)
		if e != nil {
			return v1.Request{}, ErrUnavailable
		}
		defer value.Wipe()
		if c.ValidateValues(value) != nil {
			return v1.Request{}, ErrInvalid
		}
		r.DraftRef = &alias.Ref
		_, e = b.ready(&r, alias.Ref, false, nil)
		if e != nil {
			return v1.Request{}, e
		}
		return b.requests[r.View.ID].View, nil
	}
	e := b.audit("request.created", a)
	e.RequestID = id
	e.ConnectionID = in.ConnectionID
	e.OnboardingID = in.OnboardingID
	if _, err := b.commit(mutation{Audit: e, Request: &r}); err != nil {
		return v1.Request{}, err
	}
	return view, nil
}

func (b *Broker) sessionCode(raw string) string {
	h := hmac.New(sha256.New, b.cfg.SessionKey)
	_, _ = h.Write([]byte("approval/v1/" + raw))
	return base32.StdEncoding.WithPadding(base32.NoPadding).EncodeToString(h.Sum(nil))[:16]
}
func (b *Broker) CSRF(raw, request string) string {
	h := hmac.New(sha256.New, b.cfg.SessionKey)
	_, _ = h.Write([]byte("csrf/v1/" + request + "/" + raw))
	return base32.StdEncoding.WithPadding(base32.NoPadding).EncodeToString(h.Sum(nil))
}
func (b *Broker) validSession(raw, id string, approved bool) (sessionRecord, requestRecord, error) {
	s, ok := b.sessions[seal.Digest(raw)]
	r, rok := b.requests[id]
	if !ok || !rok || s.RequestID != id || s.Consumed || !b.now().Before(s.ExpiresAt) || !b.now().Before(r.View.ExpiresAt) || r.View.Status == "canceled" || r.View.Status == "ready" {
		return s, r, ErrExpired
	}
	if approved && !s.Approved {
		return s, r, ErrDenied
	}
	return s, r, nil
}

// OpenSession never authenticates link possession. The browser must separately
// prove its principal by an approval sent through the authenticated Hub
// channel, unless Config.DirectForm pre-approves every session for personal
// loopback deployments.
func (b *Broker) OpenSession(id, raw string) (v1.SessionView, string, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	if !b.live() {
		return v1.SessionView{}, "", ErrUnavailable
	}
	r, ok := b.requests[id]
	if !ok {
		return v1.SessionView{}, "", ErrNotFound
	}
	if !b.now().Before(r.View.ExpiresAt) || r.View.Status == "canceled" || r.View.Status == "ready" {
		return v1.SessionView{}, "", ErrExpired
	}
	var s sessionRecord
	if raw != "" {
		s, _, _ = b.validSession(raw, id, false)
	}
	if s.Hash == "" || s.Consumed || s.RequestID != id || !b.now().Before(s.ExpiresAt) {
		active := 0
		for _, existing := range b.sessions {
			if existing.RequestID == id && !existing.Consumed && b.now().Before(existing.ExpiresAt) {
				active++
			}
		}
		if active >= 8 || len(b.sessions) >= 50000 {
			return v1.SessionView{}, "", ErrConflict
		}
		raw = seal.Random()
		s = sessionRecord{Hash: seal.Digest(raw), CodeHash: seal.Digest(b.sessionCode(raw)), RequestID: id, ExpiresAt: r.View.ExpiresAt, Approved: b.cfg.DirectForm}
		e := b.audit("session.opened", r.Actor)
		e.RequestID = id
		if _, err := b.commit(mutation{Audit: e, Session: &s}); err != nil {
			return v1.SessionView{}, "", err
		}
	}
	c := b.catalog[r.ContractKey]
	view := v1.SessionView{RequestID: id, Title: c.Title, Status: r.View.Status, Code: b.sessionCode(raw), Approved: s.Approved, OAuth: c.OAuthProvider != ""}
	if s.Approved {
		view.Fields = append(view.Fields, c.Fields...)
	}
	return view, raw, nil
}
func (b *Broker) Approve(a identity.Actor, id, code string) error {
	b.mu.Lock()
	defer b.mu.Unlock()
	r, ok := b.requests[id]
	if !ok || !requestBy(a, r) {
		return ErrNotFound
	}
	if !b.live() {
		return ErrUnavailable
	}
	if !b.now().Before(r.View.ExpiresAt) || r.View.Status != "pending" {
		return ErrExpired
	}
	code = strings.ToUpper(strings.ReplaceAll(strings.TrimSpace(code), "-", ""))
	if len(code) != 16 {
		return ErrDenied
	}
	for _, s := range b.sessions {
		if s.RequestID == id && !s.Consumed && !s.Approved && b.now().Before(s.ExpiresAt) && seal.Equal(s.CodeHash, seal.Digest(code)) {
			// Only one browser can be approved. A copied link cannot displace that browser.
			for _, other := range b.sessions {
				if other.RequestID == id && other.Approved && !other.Consumed {
					return ErrConflict
				}
			}
			s.Approved = true
			e := b.audit("session.approved", a)
			e.RequestID = id
			_, err := b.commit(mutation{Audit: e, Session: &s})
			return err
		}
	}
	return ErrDenied
}
func (b *Broker) Submit(ctx context.Context, id, raw, csrf string, values provider.Bundle) error {
	b.mu.Lock()
	defer b.mu.Unlock()
	if !b.live() {
		return ErrUnavailable
	}
	s, r, e := b.validSession(raw, id, true)
	if e != nil {
		return e
	}
	if !seal.Equal(csrf, b.CSRF(raw, id)) || r.View.Status != "pending" {
		return ErrDenied
	}
	c := b.catalog[r.ContractKey]
	if c.ValidateValues(values) != nil {
		return ErrInvalid
	}
	ref, e := b.cfg.Providers.Put(ctx, c.Storage, values)
	if e != nil {
		return ErrUnavailable
	}
	if c.OAuthProvider != "" {
		r.DraftRef = &ref
		r.View.Status = "authorizing"
		ev := b.audit("request.authorizing", r.Actor)
		ev.RequestID = id
		if _, e = b.commit(mutation{Audit: ev, Request: &r}); e != nil {
			return e
		}
		return nil
	}
	_, e = b.ready(&r, ref, true, &s)
	return e
}
func (b *Broker) ready(r *requestRecord, ref provider.Ref, managed bool, s *sessionRecord) (credentialRecord, error) {
	id := r.RotateID
	rev := uint64(1)
	if id == "" {
		id = newID("c")
	} else {
		old, ok := b.credentials[id]
		if !ok || old.View.Status == "deleted" || old.View.Revision != r.ExpectedRevision {
			return credentialRecord{}, ErrConflict
		}
		rev = old.View.Revision + 1
	}
	c := credentialRecord{View: v1.Credential{ID: id, OwnerKind: r.OwnerKind, PrincipalID: r.Actor.PrincipalID, ContextID: r.Actor.ContextID, ConnectionID: r.View.ConnectionID, Revision: rev, Status: "active", Provider: ref.Provider}, ContractKey: r.ContractKey, ContractDigest: r.ContractDigest, Ref: ref, Managed: managed, TokenRevision: 1}
	if old, ok := b.credentials[id]; ok {
		c.OwnedRefs = append(c.OwnedRefs, old.OwnedRefs...)
	}
	if managed && r.DraftRef != nil {
		c.OwnedRefs = append(c.OwnedRefs, *r.DraftRef)
	}
	if managed {
		c.OwnedRefs = append(c.OwnedRefs, ref)
	}
	r.View.CredentialID = id
	r.View.Revision = rev
	r.View.Status = "ready"
	if s != nil {
		s.Consumed = true
	}
	ev := b.audit("credential.ready", r.Actor)
	ev.RequestID = r.View.ID
	ev.ConnectionID = r.View.ConnectionID
	ev.OnboardingID = r.View.OnboardingID
	ev.CredentialID = id
	if _, e := b.commit(mutation{Audit: ev, Request: r, Credential: &c, Session: s}); e != nil {
		return credentialRecord{}, e
	}
	b.invalidateCredential(id)
	return c, nil
}
