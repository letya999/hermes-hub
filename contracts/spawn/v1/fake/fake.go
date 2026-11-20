// Package fake is a foreign spawn-controller fixture, not a service
// implementation: it stores submissions in memory, walks the same state
// names as the real FSM, keeps the same base-CAS activation rule and
// fabricates digest-shaped effective rows, so contract tests run without
// the passport compiler. It passes the same conformance suite as the
// real owner.
package fake

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"sort"
	"sync"
	"time"

	spawn "github.com/letya999/hermes-hub/contracts/spawn/v1"
	"github.com/letya999/hermes-hub/shared/httpapi"
)

// Server holds test-supplied state. Services lists the consumer names
// PolicySlice answers; any other service is not_found.
type Server struct {
	Services []string

	mu        sync.Mutex
	seq       int
	spaces    map[string]spawn.Space
	revisions map[string][]*spawn.Revision
	docs      map[string]map[string]map[string]any
	effective map[string]fakeEffective
}

type fakeEffective struct {
	digest    string
	documents map[string]json.RawMessage
	layers    map[string]string
}

func spaceKey(t spawn.Tenant, space string) string { return t.Org + "/" + t.Owner + "/" + space }

func (s *Server) init() {
	if s.spaces == nil {
		s.spaces = map[string]spawn.Space{}
		s.revisions = map[string][]*spawn.Revision{}
		s.docs = map[string]map[string]map[string]any{}
		s.effective = map[string]fakeEffective{}
	}
}

// CreateSpace registers the space; the same id with the same environment
// returns the stored row.
func (s *Server) CreateSpace(ctx context.Context, in spawn.CreateSpaceRequest) (spawn.Space, error) {
	if err := ctx.Err(); err != nil {
		return spawn.Space{}, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.init()
	key := spaceKey(spawn.Tenant{Org: in.Org, Owner: in.Owner}, in.ID)
	if sp, ok := s.spaces[key]; ok {
		if sp.Environment != in.Environment {
			return spawn.Space{}, fmt.Errorf("%w: space exists", spawn.ErrConflict)
		}
		return httpapi.Copy(sp)
	}
	sp := spawn.Space{Org: in.Org, Owner: in.Owner, ID: in.ID, Environment: in.Environment, CreatedAt: time.Now().Unix()}
	s.spaces[key] = sp
	return httpapi.Copy(sp)
}

// SubmitRevision stores a draft; identical documents return the existing
// non-pruned row (the service dedups on the layer digest).
func (s *Server) SubmitRevision(ctx context.Context, space, layer string, in spawn.SubmitRevisionRequest) (spawn.Revision, error) {
	if err := ctx.Err(); err != nil {
		return spawn.Revision{}, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.init()
	t := spawn.Tenant{Org: in.Org, Owner: in.Owner}
	key := spaceKey(t, space)
	if _, ok := s.spaces[key]; !ok {
		return spawn.Revision{}, spawn.ErrNotFound
	}
	raw, _ := json.Marshal(in.Documents)
	sum := sha256.Sum256(raw)
	digest := "sha256:" + hex.EncodeToString(sum[:])
	for _, rev := range s.revisions[key] {
		if rev.Layer == layer && rev.Digest == digest && rev.State != "pruned" {
			return httpapi.Copy(*rev)
		}
	}
	s.seq++
	rev := &spawn.Revision{
		ID:        fmt.Sprintf("rev-%016x", s.seq),
		Space:     space,
		Layer:     layer,
		State:     "draft",
		Digest:    digest,
		CreatedAt: time.Now().Unix(),
	}
	s.revisions[key] = append(s.revisions[key], rev)
	if s.docs[rev.ID] == nil {
		s.docs[rev.ID] = map[string]map[string]any{}
	}
	for d, body := range in.Documents {
		s.docs[rev.ID][d] = body
	}
	return httpapi.Copy(*rev)
}

func (s *Server) find(t spawn.Tenant, space, id string) (*spawn.Revision, error) {
	for _, rev := range s.revisions[spaceKey(t, space)] {
		if rev.ID == id {
			return rev, nil
		}
	}
	return nil, spawn.ErrNotFound
}

// ValidateRevision moves draft to validated and anchors the CAS base to
// the layer's current active revision; the fake compiles nothing.
func (s *Server) ValidateRevision(ctx context.Context, space, id string, in spawn.RevisionRequest) (spawn.Revision, error) {
	if err := ctx.Err(); err != nil {
		return spawn.Revision{}, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.init()
	t := spawn.Tenant{Org: in.Org, Owner: in.Owner}
	key := spaceKey(t, space)
	rev, err := s.find(t, space, id)
	if err != nil {
		return spawn.Revision{}, err
	}
	if rev.State != "draft" && rev.State != "validated" {
		return spawn.Revision{}, fmt.Errorf("%w: revision is %s", spawn.ErrConflict, rev.State)
	}
	rev.State = "validated"
	rev.BaseRevision = ""
	for _, other := range s.revisions[key] {
		if other != rev && other.Layer == rev.Layer && other.State == "active" {
			rev.BaseRevision = other.ID
		}
	}
	return httpapi.Copy(*rev)
}

// ActivateRevisions runs the same atomic set cut as the service: every
// listed row must exist in a movable state, at most one per layer, and
// each non-active row's base must still be the layer's active row — all
// checked before anything moves. Then the old rows are superseded and
// re-anchored, the targets go active and a fresh effective digest lands.
func (s *Server) ActivateRevisions(ctx context.Context, space string, in spawn.ActivationRequest) ([]spawn.Revision, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.init()
	t := spawn.Tenant{Org: in.Org, Owner: in.Owner}
	key := spaceKey(t, space)
	targets := map[string]*spawn.Revision{}
	for _, id := range in.Revisions {
		rev, err := s.find(t, space, id)
		if err != nil {
			return nil, err
		}
		switch rev.State {
		case "validated", "superseded", "active":
		default:
			return nil, fmt.Errorf("%w: revision %q is %s", spawn.ErrConflict, id, rev.State)
		}
		if _, dup := targets[rev.Layer]; dup {
			return nil, fmt.Errorf("%w: two revisions for layer %q", spawn.ErrInvalid, rev.Layer)
		}
		targets[rev.Layer] = rev
	}
	currents := map[string]*spawn.Revision{}
	for _, other := range s.revisions[key] {
		if other.State == "active" {
			currents[other.Layer] = other
		}
	}
	for layer, rev := range targets {
		if rev.State == "active" {
			continue
		}
		current := currents[layer]
		if (current == nil) != (rev.BaseRevision == "") || (current != nil && current.ID != rev.BaseRevision) {
			return nil, fmt.Errorf("%w: activation base moved", spawn.ErrConflict)
		}
	}
	for layer, rev := range targets {
		if rev.State == "active" {
			continue
		}
		if current := currents[layer]; current != nil {
			current.State = "superseded"
			current.BaseRevision = rev.ID
		}
		rev.State = "active"
	}
	// Fabricate the effective row: digest of every active layer digest,
	// documents merged from the active revisions' submissions. The fake
	// runs no compiler — each document's leaves are attributed to the
	// layer that submitted it so Explain stays contract-shaped.
	h := sha256.New()
	docs := map[string]json.RawMessage{}
	docLayers := map[string]string{}
	for _, other := range s.revisions[key] {
		if other.State != "active" {
			continue
		}
		h.Write([]byte(other.Layer + "\x00" + other.Digest + "\x00"))
		for d, body := range s.docs[other.ID] {
			raw, err := json.Marshal(body)
			if err == nil {
				docs[d] = raw
				docLayers[d] = other.Layer
			}
		}
	}
	s.effective[key] = fakeEffective{digest: "sha256:" + hex.EncodeToString(h.Sum(nil)), documents: docs, layers: docLayers}
	out := make([]spawn.Revision, 0, len(in.Revisions))
	for _, id := range in.Revisions {
		rev, _ := s.find(t, space, id)
		out = append(out, *rev)
	}
	return httpapi.Copy(out)
}

// Effective returns the fabricated effective row of the space.
func (s *Server) Effective(ctx context.Context, t spawn.Tenant, space string) (spawn.EffectiveSpec, error) {
	if err := ctx.Err(); err != nil {
		return spawn.EffectiveSpec{}, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.init()
	eff, ok := s.effective[spaceKey(t, space)]
	if !ok {
		return spawn.EffectiveSpec{}, spawn.ErrNotFound
	}
	return httpapi.Copy(spawn.EffectiveSpec{Space: space, Digest: eff.digest, Documents: eff.documents})
}

// PolicySlice returns the fabricated documents for a known service.
func (s *Server) PolicySlice(ctx context.Context, t spawn.Tenant, space, service, digest string) (spawn.PolicySlice, error) {
	if err := ctx.Err(); err != nil {
		return spawn.PolicySlice{}, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.init()
	eff, ok := s.effective[spaceKey(t, space)]
	if !ok || eff.digest != digest {
		return spawn.PolicySlice{}, spawn.ErrNotFound
	}
	for _, known := range s.Services {
		if known == service {
			return httpapi.Copy(spawn.PolicySlice{Space: space, Service: service, Digest: digest, Documents: eff.documents})
		}
	}
	return spawn.PolicySlice{}, spawn.ErrNotFound
}

// Explain flattens the fabricated effective row: each leaf reports the
// layer whose active revision submitted the document.
func (s *Server) Explain(ctx context.Context, t spawn.Tenant, space string) (spawn.ExplainReport, error) {
	if err := ctx.Err(); err != nil {
		return spawn.ExplainReport{}, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.init()
	eff, ok := s.effective[spaceKey(t, space)]
	if !ok {
		return spawn.ExplainReport{}, spawn.ErrNotFound
	}
	fields := []spawn.ExplainField{}
	for _, doc := range []string{"stack", "config", "models", "workspace", "tools", "grants"} {
		raw, ok := eff.documents[doc]
		if !ok {
			continue
		}
		var body map[string]any
		if err := json.Unmarshal(raw, &body); err != nil {
			return spawn.ExplainReport{}, err
		}
		flat := map[string]any{}
		flattenFake(body, "", flat)
		layer := eff.layers[doc]
		paths := make([]string, 0, len(flat))
		for p := range flat {
			paths = append(paths, p)
		}
		sort.Strings(paths)
		for _, p := range paths {
			fields = append(fields, spawn.ExplainField{Doc: doc, Path: p, Value: flat[p], Layer: layer})
		}
	}
	return httpapi.Copy(spawn.ExplainReport{Space: space, Digest: eff.digest, Fields: fields})
}

func flattenFake(node map[string]any, prefix string, out map[string]any) {
	for k, v := range node {
		path := k
		if prefix != "" {
			path = prefix + "." + k
		}
		if m, ok := v.(map[string]any); ok {
			flattenFake(m, path, out)
			continue
		}
		out[path] = v
	}
}

// ListRevisions returns the space's rows, newest first, optionally by layer.
func (s *Server) ListRevisions(ctx context.Context, t spawn.Tenant, space, layer string) ([]spawn.Revision, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.init()
	key := spaceKey(t, space)
	if _, ok := s.spaces[key]; !ok {
		return nil, spawn.ErrNotFound
	}
	rows := s.revisions[key]
	out := make([]spawn.Revision, 0, len(rows))
	for i := len(rows) - 1; i >= 0; i-- {
		if layer == "" || rows[i].Layer == layer {
			out = append(out, *rows[i])
		}
	}
	return httpapi.Copy(out)
}
