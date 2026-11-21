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
	"strings"
	"sync"
	"time"

	spawn "github.com/letya999/hermes-hub/contracts/spawn/v1"
	"github.com/letya999/hermes-hub/internal/selfsettings"
	"github.com/letya999/hermes-hub/shared/httpapi"
)

// Server holds test-supplied state. Services lists the consumer names
// PolicySlice answers; any other service is not_found. CellsOff makes
// ClaimWorker answer unavailable — the V5 "spawn without cells" fixture.
type Server struct {
	Services []string
	// ExecFunc optionally answers ExecWorker; the default echoes argv on
	// stdout with exit code 0 so DTO plumbing is exercised on both legs.
	ExecFunc func(spawn.WorkerExecRequest) (spawn.WorkerExecResult, error)
	CellsOff bool

	mu        sync.Mutex
	seq       int
	spaces    map[string]spawn.Space
	revisions map[string][]*spawn.Revision
	docs      map[string]map[string]map[string]any
	effective map[string]fakeEffective
	workers   map[string]*fakeWorker
	runtimes  map[string]*fakeRuntime
}

// fakeWorker is the in-memory worker row plus its live lease ids.
type fakeWorker struct {
	worker  spawn.Worker
	claim   spawn.ClaimWorkerRequest
	leases  map[string]bool
	binding string
	job     string
}

// fakeRuntime is the in-memory runtime row plus its live lease ids.
type fakeRuntime struct {
	runtime spawn.Runtime
	leases  map[string]bool
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
		s.workers = map[string]*fakeWorker{}
		s.runtimes = map[string]*fakeRuntime{}
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
	if in.BaseDigest != "" && s.effective[key].digest != in.BaseDigest {
		return nil, fmt.Errorf("%w: activation base moved", spawn.ErrConflict)
	}
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

// Apply mirrors the service apply: every listed layer is submitted as a
// draft, the would-be set fabricates a candidate spec, and without
// dry_run the same atomic cut ActivateRevisions runs lands on the
// base_digest CAS. Changes/actions stay empty — the fake runs no diff.
func (s *Server) Apply(ctx context.Context, space string, in spawn.ApplyRequest) (spawn.ApplyResponse, error) {
	if err := ctx.Err(); err != nil {
		return spawn.ApplyResponse{}, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.init()
	t := spawn.Tenant{Org: in.Org, Owner: in.Owner}
	key := spaceKey(t, space)
	if _, ok := s.spaces[key]; !ok {
		return spawn.ApplyResponse{}, spawn.ErrNotFound
	}
	eff := s.effective[key]
	currents := map[string]*fakeRevRef{}
	targets := map[string]*spawn.Revision{}
	previous := []string{}
	for _, other := range s.revisions[key] {
		if other.State == "active" {
			currents[other.Layer] = &fakeRevRef{rev: other}
			previous = append(previous, other.ID)
		}
	}
	if eff.digest != in.BaseDigest {
		return spawn.ApplyResponse{}, fmt.Errorf("%w: activation base moved", spawn.ErrConflict)
	}
	out := spawn.ApplyResponse{PreviousDigest: eff.digest, PreviousRevisions: previous}
	rows := []spawn.Revision{}
	layers := make([]string, 0, len(in.Layers))
	for layer := range in.Layers {
		layers = append(layers, layer)
	}
	sort.Strings(layers)
	for _, layer := range layers {
		docs := in.Layers[layer]
		raw, _ := json.Marshal(docs)
		sum := sha256.Sum256(raw)
		digest := "sha256:" + hex.EncodeToString(sum[:])
		var rev *spawn.Revision
		for _, cand := range s.revisions[key] {
			if cand.Layer == layer && cand.Digest == digest && cand.State != "pruned" {
				rev = cand
			}
		}
		if rev == nil {
			s.seq++
			rev = &spawn.Revision{
				ID: fmt.Sprintf("rev-%016x", s.seq), Space: space, Layer: layer,
				State: "draft", Digest: digest, CreatedAt: time.Now().Unix(),
			}
			s.revisions[key] = append(s.revisions[key], rev)
			if s.docs[rev.ID] == nil {
				s.docs[rev.ID] = map[string]map[string]any{}
			}
			for d, body := range docs {
				s.docs[rev.ID][d] = body
			}
		}
		if rev.State == "draft" {
			rev.State = "validated"
			rev.BaseRevision = ""
			if cur := currents[layer]; cur != nil {
				rev.BaseRevision = cur.rev.ID
			}
		}
		targets[layer] = rev
		rows = append(rows, *rev)
	}
	out.Revisions = rows
	for layer, ref := range currents {
		targets[layer] = ref.rev
	}
	// Fabricate the candidate digest exactly as ActivateRevisions would:
	// the revisions slice's insertion order — never a map, whose order is
	// random and would leave dry-run and confirm digests diverging.
	h := sha256.New()
	candDocs := map[string]json.RawMessage{}
	candLayers := map[string]string{}
	for _, other := range s.revisions[key] {
		if targets[other.Layer] != other {
			continue
		}
		h.Write([]byte(other.Layer + "\x00" + other.Digest + "\x00"))
		for d, body := range s.docs[other.ID] {
			raw, err := json.Marshal(body)
			if err == nil {
				candDocs[d] = raw
				candLayers[d] = other.Layer
			}
		}
	}
	out.EffectiveDigest = "sha256:" + hex.EncodeToString(h.Sum(nil))
	out.Documents = candDocs
	if in.DryRun {
		return httpapi.Copy(out)
	}
	for layer, rev := range targets {
		if rev.State == "active" {
			continue
		}
		cur := currents[layer]
		curID := ""
		if cur != nil {
			curID = cur.rev.ID
		}
		if curID != rev.BaseRevision {
			return spawn.ApplyResponse{}, fmt.Errorf("%w: activation base moved", spawn.ErrConflict)
		}
	}
	for layer, rev := range targets {
		if rev.State == "active" {
			continue
		}
		if cur := currents[layer]; cur != nil {
			cur.rev.State = "superseded"
			cur.rev.BaseRevision = rev.ID
		}
		rev.State = "active"
	}
	s.effective[key] = fakeEffective{digest: out.EffectiveDigest, documents: candDocs, layers: candLayers}
	return httpapi.Copy(out)
}

// fakeRevRef names an active row the apply candidate replaces.
type fakeRevRef struct{ rev *spawn.Revision }

// GetSettings flattens the fabricated config document's scalar leaves —
// the real owner filters through the reviewed allowlist.
func (s *Server) GetSettings(ctx context.Context, t spawn.Tenant, space, key string) (spawn.SettingsResult, error) {
	if err := ctx.Err(); err != nil {
		return spawn.SettingsResult{}, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.init()
	eff, ok := s.effective[spaceKey(t, space)]
	if !ok {
		return spawn.SettingsResult{}, spawn.ErrNotFound
	}
	// The contract key is the published tool key; the service maps it to
	// the config doc's leaf path through the reviewed allowlist — the fake
	// walks the same mapping so both transports answer identically.
	pathFilter := ""
	if key != "" {
		path, ok := selfsettings.PublishedPath(key)
		if !ok {
			return spawn.SettingsResult{}, fmt.Errorf("%w: settings key %q", spawn.ErrInvalid, key)
		}
		pathFilter = path
	}
	values := map[string]any{}
	keys := []string{}
	if raw, ok := eff.documents["config"]; ok {
		var body map[string]any
		if err := json.Unmarshal(raw, &body); err != nil {
			return spawn.SettingsResult{}, err
		}
		delete(body, "apiVersion")
		delete(body, "kind")
		delete(body, "metadata")
		flat := map[string]any{}
		flattenFake(body, "", flat)
		for _, candidate := range selfsettings.AllowedKeys() {
			path, _ := selfsettings.PublishedPath(candidate)
			v, present := flat[path]
			if !present {
				continue
			}
			if pathFilter != "" && path != pathFilter {
				continue
			}
			values[candidate] = v
		}
		if key == "" {
			keys = append(keys, selfsettings.AllowedKeys()...)
		} else {
			keys = []string{key}
		}
	}
	return httpapi.Copy(spawn.SettingsResult{
		PublishedRevision: eff.digest, EffectiveRevision: eff.digest,
		PublishedValues: values, ManageableKeys: keys,
	})
}

// EditSettings applies the key as a config-doc leaf on the fabricated
// effective row under the digest CAS — the real owner re-publishes the
// user layer through the compiler.
func (s *Server) EditSettings(ctx context.Context, space string, in spawn.SettingsEditRequest) (spawn.SettingsEditResult, error) {
	if err := ctx.Err(); err != nil {
		return spawn.SettingsEditResult{}, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.init()
	t := spawn.Tenant{Org: in.Org, Owner: in.Owner}
	key := spaceKey(t, space)
	eff, ok := s.effective[key]
	if !ok {
		return spawn.SettingsEditResult{}, spawn.ErrNotFound
	}
	if eff.digest != in.BaseRevision {
		return spawn.SettingsEditResult{}, fmt.Errorf("%w: activation base moved", spawn.ErrConflict)
	}
	docs := map[string]json.RawMessage{}
	for d, raw := range eff.documents {
		docs[d] = raw
	}
	var config map[string]any
	if raw, ok := docs["config"]; ok {
		if err := json.Unmarshal(raw, &config); err != nil {
			return spawn.SettingsEditResult{}, err
		}
	}
	if config == nil {
		config = map[string]any{}
	}
	path, ok := selfsettings.PublishedPath(in.Key)
	if !ok {
		return spawn.SettingsEditResult{}, fmt.Errorf("%w: settings key %q", spawn.ErrInvalid, in.Key)
	}
	node := config
	parts := strings.Split(path, ".")
	for _, part := range parts[:len(parts)-1] {
		next, _ := node[part].(map[string]any)
		if next == nil {
			next = map[string]any{}
			node[part] = next
		}
		node = next
	}
	leaf := parts[len(parts)-1]
	if in.Value == nil {
		delete(node, leaf)
	} else {
		node[leaf] = *in.Value
	}
	raw, err := json.Marshal(config)
	if err != nil {
		return spawn.SettingsEditResult{}, err
	}
	docs["config"] = raw
	h := sha256.New()
	names := make([]string, 0, len(docs))
	for d := range docs {
		names = append(names, d)
	}
	sort.Strings(names)
	for _, d := range names {
		h.Write([]byte(d + "\x00" + string(docs[d]) + "\x00"))
	}
	digest := "sha256:" + hex.EncodeToString(h.Sum(nil))
	s.effective[key] = fakeEffective{digest: digest, documents: docs, layers: eff.layers}
	return httpapi.Copy(spawn.SettingsEditResult{
		PublishedRevision: digest, PreviousRevision: eff.digest,
		Changed: digest != eff.digest,
	})
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

// workerKey indexes live workers on their claim key — the exact
// (tenant, space, cell key, image) tuple the service binds at claim.
func workerKey(t spawn.Tenant, space, cellKey, image string) string {
	return t.Org + "/" + t.Owner + "/" + space + "/" + cellKey + "/" + image
}

// workerIDKey finds a live worker by row id under a tenant.
func (s *Server) workerIDKey(t spawn.Tenant, id string) string {
	for key, wk := range s.workers {
		if wk.worker.ID == id && strings.HasPrefix(key, t.Org+"/"+t.Owner+"/") {
			return key
		}
	}
	return ""
}

// ClaimWorker mirrors the service claim: an exact-key live worker is
// reused, anything else claims a fresh generation row that is busy at
// readiness — cells never warm and never cross tenant or space.
func (s *Server) ClaimWorker(ctx context.Context, space string, in spawn.ClaimWorkerRequest) (spawn.ClaimedWorker, error) {
	if err := ctx.Err(); err != nil {
		return spawn.ClaimedWorker{}, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.init()
	if s.CellsOff {
		return spawn.ClaimedWorker{}, spawn.ErrUnavailable
	}
	t := spawn.Tenant{Org: in.Org, Owner: in.Owner}
	key := workerKey(t, space, in.CellKey, in.Image)
	var wk *fakeWorker
	if live, ok := s.workers[key]; ok && live.worker.State == "busy" {
		wk = live
	} else {
		s.seq++
		gen := int64(1)
		for _, other := range s.workers {
			if other.worker.Space == space && other.worker.CellKey == in.CellKey && other.worker.ImageDigest == in.Image {
				gen = other.worker.Generation + 1
			}
		}
		wk = &fakeWorker{
			worker: spawn.Worker{
				Org: t.Org, Owner: t.Owner, ID: fmt.Sprintf("wk-%016x", s.seq),
				Space: space, Agent: in.Agent, Generation: gen, State: "busy",
				CellKey: in.CellKey, ImageDigest: in.Image,
			},
			claim:   in,
			leases:  map[string]bool{},
			binding: in.BindingID,
			job:     in.JobID,
		}
		s.workers[key] = wk
	}
	s.seq++
	lease := fmt.Sprintf("wl-%016x", s.seq)
	wk.leases[lease] = true
	out := spawn.ClaimedWorker{Worker: wk.worker, Lease: lease}
	egress := false
	for _, host := range in.Egress {
		h := strings.ToLower(strings.TrimSpace(host))
		if h != "" && h != "127.0.0.1" && h != "localhost" && h != "::1" {
			egress = true
		}
	}
	name := "hermes-hub-rt-" + wk.worker.ID
	if egress {
		out.Proxy = "http://" + name + "-proxy:3128"
	}
	if len(in.Brokered) > 0 {
		out.Cred = name + "-cred"
	}
	return httpapi.Copy(out)
}

// RenewWorker re-arms the busy bound while the lease still holds.
func (s *Server) RenewWorker(ctx context.Context, id string, in spawn.WorkerLeaseRequest) (spawn.Worker, error) {
	if err := ctx.Err(); err != nil {
		return spawn.Worker{}, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.init()
	key := s.workerIDKey(spawn.Tenant{Org: in.Org, Owner: in.Owner}, id)
	wk := s.workers[key]
	if wk == nil || !wk.leases[in.Lease] || wk.worker.State != "busy" {
		return spawn.Worker{}, spawn.ErrNotFound
	}
	return httpapi.Copy(wk.worker)
}

// ReleaseWorker drops the lease; the last hold drains the worker — a fake
// drain is immediately absent, matching the confirmed-stop fast path.
func (s *Server) ReleaseWorker(ctx context.Context, id string, in spawn.WorkerLeaseRequest) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.init()
	key := s.workerIDKey(spawn.Tenant{Org: in.Org, Owner: in.Owner}, id)
	wk := s.workers[key]
	if wk == nil || !wk.leases[in.Lease] {
		return spawn.ErrNotFound
	}
	delete(wk.leases, in.Lease)
	if len(wk.leases) == 0 {
		delete(s.workers, key)
	}
	return nil
}

// DrainWorkers kills every live worker of the space matching the claim
// identity fields; the count mirrors the service's answer.
func (s *Server) DrainWorkers(ctx context.Context, space string, in spawn.DrainWorkersRequest) (int, error) {
	if err := ctx.Err(); err != nil {
		return 0, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.init()
	prefix := in.Org + "/" + in.Owner + "/" + space + "/"
	n := 0
	for key, wk := range s.workers {
		if !strings.HasPrefix(key, prefix) {
			continue
		}
		if in.BindingID != "" && wk.binding != in.BindingID {
			continue
		}
		if in.JobID != "" && wk.job != in.JobID {
			continue
		}
		delete(s.workers, key)
		n++
	}
	return n, nil
}

// ExecWorker answers through the test's ExecFunc or the argv echo; the
// lease must still hold and the worker still be busy.
func (s *Server) ExecWorker(ctx context.Context, id string, in spawn.WorkerExecRequest) (spawn.WorkerExecResult, error) {
	if err := ctx.Err(); err != nil {
		return spawn.WorkerExecResult{}, err
	}
	s.mu.Lock()
	wk := s.workers[s.workerIDKey(spawn.Tenant{Org: in.Org, Owner: in.Owner}, id)]
	if wk == nil || !wk.leases[in.Lease] || wk.worker.State != "busy" {
		s.mu.Unlock()
		return spawn.WorkerExecResult{}, spawn.ErrNotFound
	}
	s.mu.Unlock()
	if s.ExecFunc != nil {
		return s.ExecFunc(in)
	}
	return spawn.WorkerExecResult{ExitCode: 0, Stdout: []byte(strings.Join(in.Argv, " "))}, nil
}

// runtimeKey scopes a runtime row to tenant + space + agent — the
// service's exact reuse key.
func runtimeKey(t spawn.Tenant, space, agent string) string {
	return t.Org + "/" + t.Owner + "/" + space + "/" + agent
}

// runtimeLeaseOwner finds the runtime that holds id under the tenant —
// leases never resolve across tenants.
func (s *Server) runtimeLeaseOwner(t spawn.Tenant, id string) *fakeRuntime {
	prefix := t.Org + "/" + t.Owner + "/"
	for k, rt := range s.runtimes {
		if strings.HasPrefix(k, prefix) && rt.leases[id] {
			return rt
		}
	}
	return nil
}

// ClaimRuntime mirrors the service's warm-pool claim: the space must
// exist under the tenant (its stored digests are what the claim binds),
// the row is reused on its exact key, and each claim mints a fresh lease
// that leaves the runtime busy.
func (s *Server) ClaimRuntime(ctx context.Context, space string, in spawn.ClaimRuntimeRequest) (spawn.ClaimedRuntime, error) {
	if err := ctx.Err(); err != nil {
		return spawn.ClaimedRuntime{}, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.init()
	t := spawn.Tenant{Org: in.Org, Owner: in.Owner}
	if _, ok := s.spaces[spaceKey(t, space)]; !ok {
		return spawn.ClaimedRuntime{}, fmt.Errorf("%w: space %q has no stored spec", spawn.ErrUnavailable, space)
	}
	k := runtimeKey(t, space, in.Agent)
	rt, ok := s.runtimes[k]
	if !ok {
		sum := sha256.Sum256([]byte(k))
		id := "rt-" + hex.EncodeToString(sum[:8])
		// Generations bump when content rotates the row — the fake never
		// rotates, so a fresh row starts at 1 and reuse keeps it.
		gen := int64(1)
		prefix := t.Org + "/" + t.Owner + "/" + space + "/"
		for k, other := range s.runtimes {
			if strings.HasPrefix(k, prefix) && k != runtimeKey(t, space, in.Agent) && other.runtime.Agent == in.Agent && other.runtime.Generation >= gen {
				gen = other.runtime.Generation + 1
			}
		}
		rt = &fakeRuntime{
			runtime: spawn.Runtime{
				ID: id, Space: space, Agent: in.Agent,
				Generation:   gen,
				State:        "warm",
				PolicyDigest: "sha256:fake-policy",
				ImageDigest:  "sha256:fake-image",
				Address:      "http://" + id + ":8080",
			},
			leases: map[string]bool{},
		}
		s.runtimes[k] = rt
	}
	s.seq++
	lease := fmt.Sprintf("ls-%016x", s.seq)
	rt.leases[lease] = true
	rt.runtime.State = "busy"
	out := spawn.ClaimedRuntime{Runtime: rt.runtime, Lease: lease}
	out.Runtime.Org, out.Runtime.Owner = in.Org, in.Owner
	return httpapi.Copy(out)
}

// RenewLease re-arms the held lease; a lease id that is not the tenant's
// is not_found, like the service's scoped lookup.
func (s *Server) RenewLease(ctx context.Context, id string, in spawn.LeaseRequest) (spawn.Runtime, error) {
	if err := ctx.Err(); err != nil {
		return spawn.Runtime{}, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.init()
	rt := s.runtimeLeaseOwner(spawn.Tenant{Org: in.Org, Owner: in.Owner}, id)
	if rt == nil {
		return spawn.Runtime{}, spawn.ErrNotFound
	}
	out := rt.runtime
	out.Org, out.Owner = in.Org, in.Owner
	return httpapi.Copy(out)
}

// ReleaseLease drops the hold; the last lease moves the runtime back to
// warm. Releasing a lease that is not the tenant's is a no-op — releasing
// is always safe.
func (s *Server) ReleaseLease(ctx context.Context, id string, in spawn.LeaseRequest) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.init()
	rt := s.runtimeLeaseOwner(spawn.Tenant{Org: in.Org, Owner: in.Owner}, id)
	if rt == nil {
		return nil
	}
	delete(rt.leases, id)
	if len(rt.leases) == 0 && rt.runtime.State == "busy" {
		rt.runtime.State = "warm"
	}
	return nil
}

// ListRuntimes serves the tenant's runtime inventory.
func (s *Server) ListRuntimes(ctx context.Context, t spawn.Tenant) ([]spawn.Runtime, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.init()
	prefix := t.Org + "/" + t.Owner + "/"
	out := []spawn.Runtime{}
	for k, rt := range s.runtimes {
		if !strings.HasPrefix(k, prefix) {
			continue
		}
		row := rt.runtime
		row.Org, row.Owner = t.Org, t.Owner
		out = append(out, row)
	}
	return httpapi.Copy(out)
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
