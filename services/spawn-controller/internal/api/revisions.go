package api

import (
	"context"

	spawnv1 "github.com/letya999/hermes-hub/contracts/spawn/v1"
	"github.com/letya999/hermes-hub/services/spawn-controller/internal/spawn"
	"github.com/letya999/hermes-hub/shared/store"
)

// Contract adapts the durable revision service to the spawn/v1 Client
// surface: contract DTOs in, store tenants and service calls out.
type Contract struct {
	Revisions *spawn.Revisions
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

// ActivateRevision cuts the revision in one transaction.
func (c Contract) ActivateRevision(ctx context.Context, space, id string, in spawnv1.RevisionRequest) (spawnv1.Revision, error) {
	out, err := c.Revisions.Activate(ctx, tenantOf(in.Org, in.Owner), space, id)
	return revisionOf(out), err
}

// Effective returns the space's active effective spec.
func (c Contract) Effective(ctx context.Context, t spawnv1.Tenant, space string) (spawnv1.EffectiveSpec, error) {
	out, err := c.Revisions.Effective(ctx, tenantOf(t.Org, t.Owner), space)
	if err != nil {
		return spawnv1.EffectiveSpec{}, err
	}
	return spawnv1.EffectiveSpec{Space: out.Space, Digest: out.Digest, Documents: out.Documents}, nil
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
