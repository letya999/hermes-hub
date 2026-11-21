package api

import (
	"context"

	spawnv1 "github.com/letya999/hermes-hub/contracts/spawn/v1"
	"github.com/letya999/hermes-hub/services/spawn-controller/internal/spawn"
)

// runtimeOf maps the durable row onto the contract DTO; the tenant
// fields land on the outer method like workerOf's.
func runtimeOf(r spawn.Runtime) spawnv1.Runtime {
	return spawnv1.Runtime{
		ID: r.ID, Space: r.Space, Agent: r.Agent, Generation: r.Generation,
		State: r.State, PolicyDigest: r.PolicyDigest, ImageDigest: r.ImageDigest,
		Address: r.Address, HostPort: r.HostPort,
	}
}

// ClaimRuntime resolves one claim to a ready runtime plus a fresh lease:
// the claim binds the space's stored digests, the lease minted here is
// the hold the caller renews and releases around its forwards. The
// request's routing and identity fields bind to the started generation.
func (c Contract) ClaimRuntime(ctx context.Context, space string, in spawnv1.ClaimRuntimeRequest) (spawnv1.ClaimedRuntime, error) {
	if c.Runtimes == nil {
		return spawnv1.ClaimedRuntime{}, spawnv1.ErrUnavailable
	}
	t := tenantOf(in.Org, in.Owner)
	rt, err := c.Runtimes.ClaimManaged(ctx, t, space, spawn.ClaimRequest{
		Agent:            in.Agent,
		RuntimeID:        in.RuntimeID,
		RuntimeMode:      in.RuntimeMode,
		UserID:           in.UserID,
		OrganizationID:   in.OrganizationID,
		PolicyVersion:    in.PolicyVersion,
		IdentityRevision: in.IdentityRevision,
		Restrictions:     in.Restrictions,
	})
	if err != nil {
		return spawnv1.ClaimedRuntime{}, err
	}
	lease := leaseID()
	if err := c.Runtimes.Acquire(ctx, t, rt.ID, lease); err != nil {
		return spawnv1.ClaimedRuntime{}, err
	}
	out := spawnv1.ClaimedRuntime{Runtime: runtimeOf(rt), Lease: lease}
	// The claim answer reports the post-acquire truth: this lease holds
	// the runtime busy for the caller.
	out.Runtime.State = "busy"
	out.Runtime.Org, out.Runtime.Owner = in.Org, in.Owner
	return out, nil
}

// RenewLease re-arms a held runtime's busy bound; the lease id resolves
// the runtime inside the service transaction.
func (c Contract) RenewLease(ctx context.Context, id string, in spawnv1.LeaseRequest) (spawnv1.Runtime, error) {
	if c.Runtimes == nil {
		return spawnv1.Runtime{}, spawnv1.ErrUnavailable
	}
	rt, err := c.Runtimes.Renew(ctx, tenantOf(in.Org, in.Owner), id)
	out := runtimeOf(rt)
	out.Org, out.Owner = in.Org, in.Owner
	return out, err
}

// ReleaseLease drops the hold; the last lease moves the runtime back to
// warm.
func (c Contract) ReleaseLease(ctx context.Context, id string, in spawnv1.LeaseRequest) error {
	if c.Runtimes == nil {
		return spawnv1.ErrUnavailable
	}
	return c.Runtimes.ReleaseLease(ctx, tenantOf(in.Org, in.Owner), id)
}

// ListRuntimes serves the tenant-scoped inventory.
func (c Contract) ListRuntimes(ctx context.Context, t spawnv1.Tenant) ([]spawnv1.Runtime, error) {
	if c.Runtimes == nil {
		return nil, spawnv1.ErrUnavailable
	}
	rows, err := c.Runtimes.List(ctx, tenantOf(t.Org, t.Owner))
	out := make([]spawnv1.Runtime, 0, len(rows))
	for _, r := range rows {
		dto := runtimeOf(r)
		dto.Org, dto.Owner = t.Org, t.Owner
		out = append(out, dto)
	}
	return out, err
}

// DrainRuntimes drains a space's live runtimes, optionally scoped to one
// agent lane — the respawn/replace primitive.
func (c Contract) DrainRuntimes(ctx context.Context, space string, in spawnv1.DrainRuntimesRequest) (int, error) {
	if c.Runtimes == nil {
		return 0, spawnv1.ErrUnavailable
	}
	return c.Runtimes.Drain(ctx, tenantOf(in.Org, in.Owner), space, in.Agent)
}
