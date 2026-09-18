package broker

import (
	"encoding/json"
	"net/url"
	"strings"
	"sync"

	v1 "github.com/letya999/credential-broker/api/v1"
	"github.com/letya999/credential-broker/contract"
	"github.com/letya999/credential-broker/identity"
	"github.com/letya999/credential-broker/internal/strictjson"
)

func New(cfg Config) (*Broker, error) {
	u, e := url.Parse(cfg.PublicOrigin)
	if e != nil || u.Hostname() == "" || u.User != nil || u.RawQuery != "" || u.Fragment != "" || u.Path != "" || u.Scheme != "https" && u.Scheme != "http" || cfg.Journal == nil || cfg.Materializer == nil || len(cfg.SessionKey) != 32 || len(cfg.Providers) == 0 {
		return nil, ErrInvalid
	}
	// HTTP is only useful for an explicit loopback development deployment.
	if u.Scheme == "http" && u.Hostname() != "127.0.0.1" && u.Hostname() != "localhost" && u.Hostname() != "::1" {
		return nil, ErrInvalid
	}
	b := &Broker{cfg: cfg, catalog: map[string]contract.Contract{}, requests: map[string]requestRecord{}, sessions: map[string]sessionRecord{}, credentials: map[string]credentialRecord{}, grants: map[string]grantRecord{}, states: map[string]stateRecord{}, oauth: map[string]oauthRecord{}, leases: map[string]*leaseRecord{}, refreshLocks: map[string]*sync.Mutex{}}
	for _, c := range cfg.Contracts {
		k := key(c.ID, c.Revision)
		if c.Validate() != nil || b.catalog[k].ID != "" || cfg.Providers[c.Storage] == nil {
			return nil, ErrInvalid
		}
		if (c.State != nil || c.OAuthProvider != "") && !cfg.Providers[c.Storage].Capabilities().Write {
			return nil, ErrInvalid
		}
		if c.OAuthProvider != "" {
			p := cfg.OAuth[c.OAuthProvider]
			if p == nil || p.Validate() != nil {
				return nil, ErrInvalid
			}
		}
		raw, _ := json.Marshal(c)
		var copy contract.Contract
		if strictjson.Decode(raw, &copy) != nil {
			return nil, ErrInvalid
		}
		b.catalog[k] = copy
	}
	if len(b.catalog) == 0 {
		return nil, ErrInvalid
	}
	for _, r := range cfg.Journal.Records() {
		var m mutation
		if r.Kind != "mutation.v1" || strictjson.Decode(r.Data, &m) != nil {
			return nil, ErrUnavailable
		}
		b.apply(m, r.Sequence)
	}
	// Reviewed contract revisions are immutable. Configuration drift cannot silently
	// change where an existing credential will be sent or mounted.
	for _, r := range b.requests {
		if c, ok := b.catalog[r.ContractKey]; !ok || c.Digest() != r.ContractDigest {
			return nil, ErrConflict
		}
	}
	for _, g := range b.grants {
		if c, ok := b.catalog[g.ContractKey]; !ok || c.Digest() != g.ContractDigest {
			return nil, ErrConflict
		}
	}
	for _, r := range b.requests {
		if r.View.Status == "exchanging" {
			r.View.Status = "authorizing"
			r.OAuthStateHash = ""
			ev := b.audit("oauth.interrupted", r.Actor)
			ev.RequestID = r.View.ID
			if _, e = b.commit(mutation{Audit: ev, Request: &r}); e != nil {
				return nil, e
			}
		}
	}
	if _, e = b.commit(mutation{Audit: b.audit("broker.started", identity.Actor{})}); e != nil {
		return nil, e
	}
	return b, nil
}
func (b *Broker) apply(m mutation, seq uint64) {
	if m.Request != nil {
		b.requests[m.Request.View.ID] = *m.Request
	}
	if m.Session != nil {
		b.sessions[m.Session.Hash] = *m.Session
	}
	if m.Credential != nil {
		b.credentials[m.Credential.View.ID] = *m.Credential
	}
	if m.Grant != nil {
		b.grants[m.Grant.View.ID] = *m.Grant
	}
	if m.State != nil {
		b.states[m.State.Key] = *m.State
	}
	if m.OAuth != nil {
		b.oauth[m.OAuth.StateHash] = *m.OAuth
	}
	m.Audit.Sequence = seq
	b.events = append(b.events, m.Audit)
}
func (b *Broker) commit(m mutation) (uint64, error) {
	if !b.live() {
		return 0, ErrUnavailable
	}
	seq, e := b.cfg.Journal.Append("mutation.v1", m)
	if e != nil {
		return 0, ErrUnavailable
	}
	b.apply(m, seq)
	return seq, nil
}
func (b *Broker) Healthy() bool { b.mu.Lock(); defer b.mu.Unlock(); return b.live() }
func (b *Broker) Close() error {
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.closed {
		return nil
	}
	b.closed = true
	for _, l := range b.leases {
		for _, cancel := range l.Active {
			cancel()
		}
	}
	e := b.cfg.Materializer.Close()
	je := b.cfg.Journal.Close()
	if e != nil {
		return e
	}
	return je
}
func (b *Broker) GetRequest(a identity.Actor, id string) (v1.Request, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	r, ok := b.requests[id]
	if !b.live() {
		return v1.Request{}, ErrUnavailable
	}
	if !ok || !requestBy(a, r) {
		return v1.Request{}, ErrNotFound
	}
	out := r.View
	if !b.now().Before(out.ExpiresAt) && (out.Status == "pending" || out.Status == "authorizing") {
		out.Status = "expired"
	}
	return out, nil
}
func (b *Broker) GetCredential(a identity.Actor, id string) (v1.Credential, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	c, ok := b.credentials[id]
	if !b.live() {
		return v1.Credential{}, ErrUnavailable
	}
	if !ok || !managedBy(a, c) {
		return v1.Credential{}, ErrNotFound
	}
	return c.View, nil
}
func (b *Broker) Events(a identity.Actor, after uint64, limit int) v1.Events {
	b.mu.Lock()
	defer b.mu.Unlock()
	out := v1.Events{Events: []v1.Event{}, NextCursor: after}
	if limit < 1 || limit > 100 {
		limit = 100
	}
	for _, e := range b.events {
		if e.Sequence <= after {
			continue
		}
		out.NextCursor = e.Sequence
		if e.Kind == "broker.started" || e.ContextID == a.ContextID && (e.PrincipalID == a.PrincipalID || a.ContextManager) {
			out.Events = append(out.Events, e)
		}
		if len(out.Events) >= limit {
			break
		}
	}
	return out
}
func (b *Broker) CancelRequest(a identity.Actor, id string) error {
	b.mu.Lock()
	defer b.mu.Unlock()
	r, ok := b.requests[id]
	if !ok || !requestBy(a, r) {
		return ErrNotFound
	}
	if r.View.Status == "canceled" {
		return nil
	}
	if r.View.Status == "ready" {
		return ErrConflict
	}
	r.View.Status = "canceled"
	e := b.audit("request.canceled", a)
	e.RequestID = id
	e.ConnectionID = r.View.ConnectionID
	_, err := b.commit(mutation{Audit: e, Request: &r})
	return err
}
func (b *Broker) Catalog() []contract.Contract {
	b.mu.Lock()
	defer b.mu.Unlock()
	out := []contract.Contract{}
	for _, c := range b.catalog {
		raw, _ := json.Marshal(c)
		var cp contract.Contract
		_ = json.Unmarshal(raw, &cp)
		out = append(out, cp)
	}
	return out
}
func (b *Broker) PublicOrigin() string { return b.cfg.PublicOrigin }
func validIdempotency(s string) bool {
	return len(s) >= 8 && len(s) <= 128 && !strings.ContainsAny(s, "\r\n\x00")
}
