package supervisor

// Managed-runtime claiming (CHG-0336 S7c): spawn-controller owns the
// workload lifecycle — the supervisor resolves the identity material,
// claims the runtime through contracts/spawn/v1, and forwards to the
// claimed endpoint exactly as it did for a container it started itself.
// The claim's lease is the runtime's serving lifetime: the supervisor
// holds it while its entry serves (busy, the 2h hold bound), renews it on
// reuse so the warm-pool sweeper never drains a serving workload, and
// releases it when the entry is reaped — a released row goes warm and its
// own idle TTL drains it, which is the warm-pool contract.

import (
	"context"
	"errors"
	"fmt"

	identityv1 "github.com/letya999/hermes-hub/contracts/identity/v1"
	spawnv1 "github.com/letya999/hermes-hub/contracts/spawn/v1"
)

// spawnClaimer is the slice of the spawn contract the runtime lifecycle
// needs — tests answer it with their own runtime endpoint.
type spawnClaimer interface {
	ClaimRuntime(context.Context, string, spawnv1.ClaimRuntimeRequest) (spawnv1.ClaimedRuntime, error)
	RenewLease(context.Context, string, spawnv1.LeaseRequest) (spawnv1.Runtime, error)
	ReleaseLease(context.Context, string, spawnv1.LeaseRequest) error
	DrainRuntimes(context.Context, string, spawnv1.DrainRuntimesRequest) (int, error)
	PolicySlice(context.Context, spawnv1.Tenant, string, string, string) (spawnv1.PolicySlice, error)
	// Effective is the passport read slice (S7d): admission checks resolve
	// the space's active bundle and digest through the contract, never the
	// deleted file mirror.
	Effective(context.Context, spawnv1.Tenant, string) (spawnv1.EffectiveSpec, error)
}

// claimAgent is the claim's agent key: one runtime row per
// (principal, context, mode) — the same key the supervisor hashes its
// items map by, so the v0 per-binding runtime semantics carry over.
func claimAgent(binding Binding) string {
	return containerName(runtimeKey(binding))
}

// claimTenant is the managed-space tenant convention (Decision 13): org
// "local", owner the space itself.
func claimTenant(binding Binding) spawnv1.Tenant {
	return spawnv1.Tenant{Org: "local", Owner: binding.ContextID}
}

// claimManaged resolves the binding's identity template and claims the
// managed runtime through the spawn contract: the row binds the space's
// stored digests, the routing fields and the resolved identity revision,
// and the returned runtime carries the endpoint the supervisor forwards
// to. The lease stays held — the caller stores it on the entry and owns
// its release.
func (m *Manager) claimManaged(ctx context.Context, binding Binding) (spawnv1.ClaimedRuntime, error) {
	client := m.cfg.Spawn
	if client == nil {
		return spawnv1.ClaimedRuntime{}, errors.New("spawn controller claim client unavailable")
	}
	template, revision, err := m.identityTemplate(ctx, binding)
	if err != nil {
		return spawnv1.ClaimedRuntime{}, err
	}
	if binding.IdentityRevision != "" && revision != binding.IdentityRevision {
		return spawnv1.ClaimedRuntime{}, identityv1.ErrConflict
	}
	tenant := claimTenant(binding)
	claimed, err := client.ClaimRuntime(ctx, binding.ContextID, spawnv1.ClaimRuntimeRequest{
		Org:              tenant.Org,
		Owner:            tenant.Owner,
		Agent:            claimAgent(binding),
		RuntimeID:        binding.RuntimeID,
		RuntimeMode:      binding.RuntimeMode,
		UserID:           binding.UserID,
		OrganizationID:   binding.OrganizationID,
		PolicyVersion:    binding.PolicyVersion,
		IdentityRevision: revision,
		Restrictions:     &template.Restrictions,
	})
	if err != nil {
		return spawnv1.ClaimedRuntime{}, err
	}
	if claimed.Runtime.Address == "" || claimed.Lease == "" {
		return spawnv1.ClaimedRuntime{}, fmt.Errorf("spawn claim returned no runtime endpoint")
	}
	return claimed, nil
}

// renewClaim re-arms the busy hold of the runtime an entry serves —
// called wherever an existing entry is reused, so a serving workload's
// hold bound never lapses. A lease the service no longer knows (drain,
// restart) reports not-found so the caller can re-claim.
func (m *Manager) renewClaim(ctx context.Context, lease, contextID string) error {
	if m.cfg.Spawn == nil || lease == "" {
		return nil
	}
	tenant := spawnv1.Tenant{Org: "local", Owner: contextID}
	_, err := m.cfg.Spawn.RenewLease(ctx, lease, spawnv1.LeaseRequest{Org: tenant.Org, Owner: tenant.Owner})
	return err
}

// releaseClaim drops the entry's spawn hold; the row goes back to warm and
// its own idle TTL drains it. Releasing is always safe — a lease the
// service already forgot is a no-op. The field is lock-guarded, so the
// claim hand-off happens under m.mu here.
func (m *Manager) releaseClaim(ctx context.Context, entry *runtimeEntry) {
	if m.cfg.Spawn == nil || entry == nil {
		return
	}
	m.mu.Lock()
	lease := entry.spawnLease
	entry.spawnLease = ""
	m.mu.Unlock()
	if lease == "" {
		return
	}
	_ = m.cfg.Spawn.ReleaseLease(ctx, lease, spawnv1.LeaseRequest{Org: "local", Owner: entry.Runtime.ContextID})
}

// releaseWorkload tears down whatever backs the entry: a claimed runtime
// releases its spawn lease, a locally-spawned one (compile-only backends)
// removes the owned container.
func (m *Manager) releaseWorkload(ctx context.Context, entry *runtimeEntry) error {
	if entry == nil {
		return nil
	}
	m.mu.Lock()
	claimed := entry.spawnLease != ""
	m.mu.Unlock()
	if claimed {
		m.releaseClaim(ctx, entry)
		return nil
	}
	return m.removeOwnedRuntime(ctx, entry.Runtime)
}

// drainClaim forces the binding's runtime lane to drain — the respawn
// primitive: the next claim starts a fresh generation.
func (m *Manager) drainClaim(ctx context.Context, binding Binding) error {
	if m.cfg.Spawn == nil {
		return errors.New("spawn controller claim client unavailable")
	}
	tenant := claimTenant(binding)
	_, err := m.cfg.Spawn.DrainRuntimes(ctx, binding.ContextID, spawnv1.DrainRuntimesRequest{
		Org: tenant.Org, Owner: tenant.Owner, Agent: claimAgent(binding),
	})
	return err
}
