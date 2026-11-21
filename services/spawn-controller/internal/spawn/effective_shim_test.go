package spawn_test

import (
	"context"

	spawnv1 "github.com/letya999/hermes-hub/contracts/spawn/v1"
	"github.com/letya999/hermes-hub/services/spawn-controller/internal/spawn"
	"github.com/letya999/hermes-hub/shared/store"
)

// revReader adapts the store's revision service to the contract's
// EffectiveReader for tests that drive stack-side materialization (S7d —
// the stored documents ARE the published revision now).
type revReader struct{ revs *spawn.Revisions }

var testTenant = store.Tenant{Org: "local", Owner: "alice"}

func (r revReader) Effective(ctx context.Context, _ spawnv1.Tenant, space string) (spawnv1.EffectiveSpec, error) {
	spec, err := r.revs.Effective(ctx, testTenant, space)
	if err != nil {
		return spawnv1.EffectiveSpec{}, err
	}
	return spawnv1.EffectiveSpec{Space: spec.Space, Digest: spec.Digest, Documents: spec.Documents}, nil
}

func (r revReader) PolicySlice(ctx context.Context, _ spawnv1.Tenant, space, service, digest string) (spawnv1.PolicySlice, error) {
	docs, err := r.revs.PolicySlice(ctx, testTenant, space, service, digest)
	if err != nil {
		return spawnv1.PolicySlice{}, err
	}
	return spawnv1.PolicySlice{Space: space, Service: service, Digest: digest, Documents: docs}, nil
}
