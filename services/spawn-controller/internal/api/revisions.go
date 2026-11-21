package api

import (
	"context"

	spawnv1 "github.com/letya999/hermes-hub/contracts/spawn/v1"
	"github.com/letya999/hermes-hub/services/spawn-controller/internal/spawn"
	"github.com/letya999/hermes-hub/shared/store"
)

// Contract adapts the durable revision, runtime and worker services to
// the spawn/v1 Client surface: contract DTOs in, store tenants and
// service calls out. Workers/Runtimes are nil when the installation runs
// no cells (V5).
type Contract struct {
	Revisions *spawn.Revisions
	Workers   *spawn.Workers
	Runtimes  *spawn.Runtimes
}

func tenantOf(org, owner string) store.Tenant { return store.Tenant{Org: org, Owner: owner} }

func spaceOf(s spawn.Space) spawnv1.Space {
	return spawnv1.Space{Org: s.Org, Owner: s.Owner, ID: s.ID, Environment: s.Environment, CreatedAt: s.CreatedAt}
}

func revisionOf(r spawn.Revision) spawnv1.Revision {
	return spawnv1.Revision{
		ID: r.ID, Space: r.Space, Layer: r.Layer, State: r.State,
		Digest: r.Digest, Error: r.Error, Detail: r.Detail,
		CreatedAt: r.CreatedAt, BaseRevision: r.BaseRevision,
	}
}

// CreateSpace registers the tenant space; the idempotency key is implicit
// in the (org, owner, id) primary key.
func (c Contract) CreateSpace(ctx context.Context, in spawnv1.CreateSpaceRequest) (spawnv1.Space, error) {
	out, err := c.Revisions.CreateSpace(ctx, tenantOf(in.Org, in.Owner), in.ID, in.Environment)
	return spaceOf(out), err
}

// SubmitRevision stores one layer's documents as a draft revision.
func (c Contract) SubmitRevision(ctx context.Context, space, layer string, in spawnv1.SubmitRevisionRequest) (spawnv1.Revision, error) {
	out, err := c.Revisions.Submit(ctx, tenantOf(in.Org, in.Owner), space, layer, in.Documents)
	return revisionOf(out), err
}

// ValidateRevision compiles the draft against the other layers.
func (c Contract) ValidateRevision(ctx context.Context, space, id string, in spawnv1.RevisionRequest) (spawnv1.Revision, error) {
	out, err := c.Revisions.Validate(ctx, tenantOf(in.Org, in.Owner), space, id)
	return revisionOf(out), err
}

// ActivateRevisions cuts the listed revisions in one transaction.
func (c Contract) ActivateRevisions(ctx context.Context, space string, in spawnv1.ActivationRequest) ([]spawnv1.Revision, error) {
	var base *string
	if in.BaseDigest != "" {
		base = &in.BaseDigest
	}
	rows, err := c.Revisions.Activate(ctx, tenantOf(in.Org, in.Owner), space, in.Revisions, base)
	if err != nil {
		return nil, err
	}
	out := make([]spawnv1.Revision, len(rows))
	for i, row := range rows {
		out[i] = revisionOf(row)
	}
	return out, nil
}

// Apply is the passport-writer surface: submit + validate + compile the
// changed layers, then — without dry_run — activate on the digest CAS.
func (c Contract) Apply(ctx context.Context, space string, in spawnv1.ApplyRequest) (spawnv1.ApplyResponse, error) {
	out, err := c.Revisions.Apply(ctx, tenantOf(in.Org, in.Owner), space, in.Layers, in.BaseDigest, in.DryRun)
	if err != nil {
		return spawnv1.ApplyResponse{}, err
	}
	res := spawnv1.ApplyResponse{
		EffectiveDigest:   out.EffectiveDigest,
		PreviousDigest:    out.PreviousDigest,
		PreviousRevisions: out.PreviousRevisions,
		Documents:         out.Documents,
	}
	for _, r := range out.Revisions {
		res.Revisions = append(res.Revisions, revisionOf(r))
	}
	for _, ch := range out.Changes {
		res.Changes = append(res.Changes, spawnv1.Change{Path: ch.Path, Before: ch.Before, After: ch.After, Effect: string(ch.Effect), Consumer: ch.Consumer})
	}
	for _, a := range out.Actions {
		res.Actions = append(res.Actions, spawnv1.Action{Kind: string(a.Kind), Target: a.Target, Consumers: a.Consumers, Paths: a.Paths})
	}
	return res, nil
}

// GetSettings reads the space's reviewed self-settings leaves.
func (c Contract) GetSettings(ctx context.Context, t spawnv1.Tenant, space, key string) (spawnv1.SettingsResult, error) {
	out, err := c.Revisions.GetSettings(ctx, tenantOf(t.Org, t.Owner), space, key)
	if err != nil {
		return spawnv1.SettingsResult{}, err
	}
	return spawnv1.SettingsResult{
		PublishedRevision:    out.PublishedRevision,
		EffectiveRevision:    out.EffectiveRevision,
		PublishedValues:      out.PublishedValues,
		ManageableKeys:       out.ManageableKeys,
		SecretValuesIncluded: out.SecretValuesIncluded,
	}, nil
}

// EditSettings applies one reviewed config leaf edit on the digest CAS.
func (c Contract) EditSettings(ctx context.Context, space string, in spawnv1.SettingsEditRequest) (spawnv1.SettingsEditResult, error) {
	out, err := c.Revisions.EditSettings(ctx, tenantOf(in.Org, in.Owner), space, in.Key, in.Value, in.BaseRevision)
	if err != nil {
		return spawnv1.SettingsEditResult{}, err
	}
	return spawnv1.SettingsEditResult{PublishedRevision: out.PublishedRevision, PreviousRevision: out.PreviousRevision, Changed: out.Changed}, nil
}

// Effective returns the space's active effective spec.
func (c Contract) Effective(ctx context.Context, t spawnv1.Tenant, space string) (spawnv1.EffectiveSpec, error) {
	out, err := c.Revisions.Effective(ctx, tenantOf(t.Org, t.Owner), space)
	if err != nil {
		return spawnv1.EffectiveSpec{}, err
	}
	return spawnv1.EffectiveSpec{Space: out.Space, Digest: out.Digest, Documents: out.Documents}, nil
}

// Explain returns the space's per-leaf layer provenance.
func (c Contract) Explain(ctx context.Context, t spawnv1.Tenant, space string) (spawnv1.ExplainReport, error) {
	out, err := c.Revisions.Explain(ctx, tenantOf(t.Org, t.Owner), space)
	if err != nil {
		return spawnv1.ExplainReport{}, err
	}
	fields := make([]spawnv1.ExplainField, len(out.Fields))
	for i, f := range out.Fields {
		fields[i] = spawnv1.ExplainField{Doc: f.Doc, Path: f.Path, Value: f.Value, Layer: f.Layer}
	}
	return spawnv1.ExplainReport{Space: out.Space, Digest: out.Digest, Fields: fields}, nil
}

// PolicySlice projects one consumer's share of an effective spec.
func (c Contract) PolicySlice(ctx context.Context, t spawnv1.Tenant, space, service, digest string) (spawnv1.PolicySlice, error) {
	docs, err := c.Revisions.PolicySlice(ctx, tenantOf(t.Org, t.Owner), space, service, digest)
	if err != nil {
		return spawnv1.PolicySlice{}, err
	}
	return spawnv1.PolicySlice{Space: space, Service: service, Digest: digest, Documents: docs}, nil
}

// ListRevisions returns the space's revisions, optionally by layer.
func (c Contract) ListRevisions(ctx context.Context, t spawnv1.Tenant, space, layer string) ([]spawnv1.Revision, error) {
	rows, err := c.Revisions.ListRevisions(ctx, tenantOf(t.Org, t.Owner), space, layer)
	if err != nil {
		return nil, err
	}
	out := make([]spawnv1.Revision, len(rows))
	for i, row := range rows {
		out[i] = revisionOf(row)
	}
	return out, nil
}
