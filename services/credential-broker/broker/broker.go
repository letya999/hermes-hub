package broker

import (
	"context"
	"encoding/json"
	"errors"
	"net/url"
	"strings"
	"sync"

	v1 "github.com/letya999/credential-broker/api/v1"
	"github.com/letya999/credential-broker/contract"
	"github.com/letya999/credential-broker/identity"
	"github.com/letya999/credential-broker/internal/journal"
	"github.com/letya999/credential-broker/internal/seal"
	"github.com/letya999/credential-broker/internal/strictjson"
	"github.com/letya999/credential-broker/provider"
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
	b := &Broker{cfg: cfg, catalog: map[string]contract.Contract{}, requests: map[string]requestRecord{}, sessions: map[string]sessionRecord{}, credentials: map[string]credentialRecord{}, grants: map[string]grantRecord{}, states: map[string]stateRecord{}, oauth: map[string]oauthRecord{}, leases: map[string]*leaseRecord{}, objects: map[string]objectRecord{}, pins: map[string]int{}, usePins: map[string]string{}, refreshLocks: map[string]*sync.Mutex{}}
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
	if cfg.Journal.Normalized() {
		b.normalized = true
		rows, err := cfg.Journal.StateRows()
		if err != nil {
			return nil, ErrUnavailable
		}
		for _, row := range rows {
			if err := b.applyStateRow(row); err != nil {
				return nil, err
			}
		}
	} else {
		for _, r := range cfg.Journal.Records() {
			var m mutation
			if r.Kind != "mutation.v1" || strictjson.Decode(r.Data, &m) != nil {
				return nil, ErrUnavailable
			}
			// A journaled dynamic contract must never silently shadow a
			// file-catalog contract under the same id@revision.
			if m.Contract != nil {
				ck := key(m.Contract.Contract.ID, m.Contract.Contract.Revision)
				if existing, ok := b.catalog[ck]; ok && existing.Digest() != m.Contract.Contract.Digest() {
					return nil, ErrConflict
				}
			}
			b.apply(m, r.Sequence)
		}
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

// NormalizeRecord derives normalized-table writes for one journaled mutation
// — the same derivation commit uses, so the normalize pass and live writes
// stay consistent.
func NormalizeRecord(r journal.Record) ([]journal.StateWrite, *journal.AuditWrite, error) {
	if r.Kind != "mutation.v1" {
		return nil, nil, nil
	}
	var m mutation
	if strictjson.Decode(r.Data, &m) != nil {
		return nil, nil, ErrUnavailable
	}
	aux, err := mutationWrites(m)
	if err != nil {
		return nil, nil, ErrUnavailable
	}
	return aux.States, aux.Audit, nil
}

// mutationWrites projects one mutation onto the normalized tables: every
// domain object becomes a keyed current-state upsert, the event an audit row.
func mutationWrites(m mutation) (*journal.AuxWrites, error) {
	aux := &journal.AuxWrites{}
	add := func(kind, id string, v any) error {
		body, err := json.Marshal(v)
		if err != nil {
			return err
		}
		aux.States = append(aux.States, journal.StateWrite{Kind: kind, ID: id, Body: body})
		return nil
	}
	// A mutation may carry several objects at once (enrollment commits
	// request+credential+session atomically) — project every set field.
	type field struct {
		kind, id string
		v        any
	}
	var fields []field
	if m.Request != nil {
		fields = append(fields, field{"request", m.Request.View.ID, *m.Request})
	}
	if m.Session != nil {
		fields = append(fields, field{"session", m.Session.Hash, *m.Session})
	}
	if m.Credential != nil {
		fields = append(fields, field{"credential", m.Credential.View.ID, *m.Credential})
	}
	if m.Grant != nil {
		fields = append(fields, field{"grant", m.Grant.View.ID, *m.Grant})
	}
	if m.State != nil {
		fields = append(fields, field{"state", m.State.Key, *m.State})
	}
	if m.OAuth != nil {
		fields = append(fields, field{"oauth", m.OAuth.StateHash, *m.OAuth})
	}
	if m.Contract != nil {
		fields = append(fields, field{"contract", key(m.Contract.Contract.ID, m.Contract.Contract.Revision), *m.Contract})
	}
	for i := range m.Objects {
		fields = append(fields, field{"object", m.Objects[i].ID, m.Objects[i]})
	}
	for _, f := range fields {
		if err := add(f.kind, f.id, f.v); err != nil {
			return nil, err
		}
	}
	body, err := json.Marshal(m.Audit)
	if err != nil {
		return nil, err
	}
	aux.Audit = &journal.AuditWrite{Kind: m.Audit.Kind, Principal: m.Audit.PrincipalID, Context: m.Audit.ContextID, Body: body}
	return aux, nil
}

// putObject persists a durable creation intent — carrying the exact
// locator — before the provider write whenever the provider honors reserved
// refs. A crash between write and activation then leaves a known owned
// object, never an unknown remote. Providers without reservable refs keep
// the post-write ordering (documented unknown-orphan window).
// Caller holds b.mu.
func (b *Broker) putObject(ctx context.Context, a identity.Actor, storage, ownerKind, ownerID, op string, values provider.Bundle) (provider.Ref, error) {
	if rw, ok := b.cfg.Providers[storage].(provider.RefWriter); ok {
		ref := provider.Ref{Provider: storage, Locator: seal.Random(), Version: "1"}
		intent := objectRecord{ID: objectID(ref), OwnerKind: ownerKind, OwnerID: ownerID, Ref: ref, State: "intent", Op: op, CreatedAt: b.now(), UpdatedAt: b.now()}
		ev := b.audit("object.intent", a)
		if _, e := b.commit(mutation{Audit: ev, Objects: []objectRecord{intent}}); e != nil {
			return provider.Ref{}, e
		}
		if e := rw.WriteRef(ctx, ref, values); e != nil {
			return provider.Ref{}, ErrUnavailable
		}
		return ref, nil
	}
	return b.cfg.Providers.Put(ctx, storage, values)
}

// applyStateRow decodes one normalized-table row back into the live maps.
// Unknown kinds fail closed — a future record this build cannot serve must
// not be silently dropped.
func (b *Broker) applyStateRow(row journal.StateRow) error {
	switch row.Kind {
	case "request":
		var r requestRecord
		if strictjson.Decode(row.Body, &r) != nil {
			return ErrUnavailable
		}
		b.requests[r.View.ID] = r
	case "session":
		var s sessionRecord
		if strictjson.Decode(row.Body, &s) != nil {
			return ErrUnavailable
		}
		b.sessions[s.Hash] = s
	case "credential":
		var c credentialRecord
		if strictjson.Decode(row.Body, &c) != nil {
			return ErrUnavailable
		}
		b.credentials[c.View.ID] = c
	case "grant":
		var g grantRecord
		if strictjson.Decode(row.Body, &g) != nil {
			return ErrUnavailable
		}
		b.grants[g.View.ID] = g
	case "state":
		var s stateRecord
		if strictjson.Decode(row.Body, &s) != nil {
			return ErrUnavailable
		}
		b.states[s.Key] = s
	case "oauth":
		var o oauthRecord
		if strictjson.Decode(row.Body, &o) != nil {
			return ErrUnavailable
		}
		b.oauth[o.StateHash] = o
	case "contract":
		var c contractRecord
		if strictjson.Decode(row.Body, &c) != nil {
			return ErrUnavailable
		}
		ck := key(c.Contract.ID, c.Contract.Revision)
		if existing, ok := b.catalog[ck]; ok && existing.Digest() != c.Contract.Digest() {
			return ErrConflict
		}
		b.catalog[ck] = c.Contract
	case "object":
		var o objectRecord
		if strictjson.Decode(row.Body, &o) != nil {
			return ErrUnavailable
		}
		b.objects[o.ID] = o
	default:
		return ErrUnavailable
	}
	return nil
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
	if m.Contract != nil {
		b.catalog[key(m.Contract.Contract.ID, m.Contract.Contract.Revision)] = m.Contract.Contract
	}
	for _, o := range m.Objects {
		b.objects[o.ID] = o
	}
	if !b.normalized {
		// Normalized stores serve events from the audit table — no RAM clone.
		m.Audit.Sequence = seq
		b.events = append(b.events, m.Audit)
	}
}
func (b *Broker) commit(m mutation) (uint64, error) {
	if !b.live() {
		return 0, ErrUnavailable
	}
	aux, err := mutationWrites(m)
	if err != nil {
		return 0, ErrUnavailable
	}
	seq, e := b.cfg.Journal.AppendWith("mutation.v1", m, aux)
	if e != nil {
		if errors.Is(e, journal.ErrFull) {
			return 0, ErrRetentionFull
		}
		return 0, ErrUnavailable
	}
	b.apply(m, seq)
	return seq, nil
}
func (b *Broker) Healthy() bool { b.mu.Lock(); defer b.mu.Unlock(); return b.live() }
func (b *Broker) RecoveryRefs() []provider.Ref {
	b.mu.Lock()
	defer b.mu.Unlock()
	var refs []provider.Ref
	for _, c := range b.credentials {
		if c.Managed && c.View.Status == "active" {
			refs = append(refs, c.Ref)
		}
	}
	for _, r := range b.requests {
		if r.DraftRef != nil && r.View.Status == "authorizing" {
			refs = append(refs, *r.DraftRef)
		}
	}
	for _, g := range b.grants {
		c := b.credentials[g.View.CredentialID]
		if !g.View.Active || c.View.Status != "active" {
			continue
		}
		key := seal.Digest(c.View.ID + "/" + g.View.ContextID + "/" + g.View.BindingID)
		if state, ok := b.states[key]; ok && state.Ref.Provider != "" {
			refs = append(refs, state.Ref)
		}
	}
	return refs
}
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
	if b.normalized {
		// The retained floor is the re-anchor point: a cursor below it means
		// the audit rows it points past were archived out, so the page reports
		// the gap explicitly instead of silently starting mid-history.
		if floor, err := b.cfg.Journal.AuditFloor(); err == nil {
			out.EarliestAvailable = floor
			out.Gap = after+1 < floor
		}
		rows, err := b.cfg.Journal.AuditPage(after, limit, journal.AuditScope{
			Broadcast: "broker.started", Principal: a.PrincipalID,
			Context: a.ContextID, AnyPrincipal: a.ContextManager,
		})
		if err != nil {
			return out
		}
		for _, row := range rows {
			var e v1.Event
			if strictjson.Decode(row.Body, &e) != nil {
				continue
			}
			e.Sequence = row.Seq
			out.NextCursor = row.Seq
			out.Events = append(out.Events, e)
		}
		return out
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
	e.ConsumerID = r.View.ConsumerID
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
