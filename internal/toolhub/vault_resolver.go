package toolhub

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"sync"

	spawnv1 "github.com/letya999/hermes-hub/contracts/spawn/v1"
	"github.com/letya999/hermes-hub/internal/identity"
	"github.com/letya999/hermes-hub/internal/toolhub/api"
	"github.com/letya999/hermes-hub/internal/vault"
)

// VaultResolver maps a verified (principal, environment) pair to that
// principal's vault (SPEC-0098 M1). The binding table is scanned once at
// startup from the server-rendered HUB_VAULTS_BY_PRINCIPAL mount view;
// principal and environment arrive only from the authenticated envelope —
// model arguments, URL paths and headers can never select a vault.
type VaultResolver struct {
	spawn    spawnv1.EffectiveReader
	bindings map[string]string
	mu       sync.Mutex
	users    map[string]*UserVault
}

// UserVault is one principal's opened vault. Operations run under Locked —
// the per-user serialization point, so a blocked operation on one user's
// vault never holds another user's lock.
type UserVault struct {
	root string
	mu   sync.Mutex
}

// Locked runs fn(root) holding this user's vault lock.
func (u *UserVault) Locked(fn func(root string) error) error {
	u.mu.Lock()
	defer u.mu.Unlock()
	return fn(u.root)
}

// NewVaultResolver scans the rendered bindings root into the per-principal
// binding table; the spawn contract client is the enrollment authority
// binding a principal to its environment (S7d — the published file root is
// gone).
func NewVaultResolver(root string, spawn spawnv1.EffectiveReader) (*VaultResolver, error) {
	if info, err := os.Lstat(root); err != nil || !info.IsDir() { // #nosec G703 -- server-rendered bindings mount, not request input
		return nil, fmt.Errorf("%w: vault bindings root", api.ErrInvalid)
	}
	entries, err := os.ReadDir(root)
	if err != nil {
		return nil, fmt.Errorf("%w: vault bindings root: %v", api.ErrInvalid, err)
	}
	v := &VaultResolver{spawn: spawn, bindings: map[string]string{}, users: map[string]*UserVault{}}
	for _, entry := range entries {
		if entry.IsDir() && identity.ValidID(entry.Name()) {
			v.bindings[entry.Name()] = filepath.Join(root, entry.Name())
		}
	}
	return v, nil
}

// Resolve opens the vault bound to (principal, environment). Every gate —
// identity shape, a rendered binding and the live published enrollment
// record — runs before the vault root is ever opened.
func (v *VaultResolver) Resolve(principal, environment string) (*UserVault, error) {
	if !identity.ValidID(principal) {
		return nil, fmt.Errorf("%w: vault principal", api.ErrUnauthorized)
	}
	if err := memoryEnrollment(context.Background(), v.spawn, principal, environment); err != nil {
		return nil, err
	}
	return v.openBound(principal)
}

// An already-consumed proposal may need to finish after memory is disabled.
// This route still requires the server binding and published user/environment;
// pending proposals are denied at the confirmation commit gate.
func (v *VaultResolver) resolveForProposal(ctx context.Context, principal, environment string) (*UserVault, error) {
	if !identity.ValidID(principal) || v.spawn == nil {
		return nil, fmt.Errorf("%w: vault principal", api.ErrUnauthorized)
	}
	eff, err := effectiveFor(ctx, v.spawn, principal)
	if err != nil || eff.Header.Metadata.Space != principal || eff.Header.Metadata.Environment != environment {
		return nil, fmt.Errorf("%w: vault enrollment", api.ErrUnauthorized)
	}
	return v.openBound(principal)
}

func (v *VaultResolver) openBound(principal string) (*UserVault, error) {
	dir, ok := v.bindings[principal]
	if !ok {
		return nil, fmt.Errorf("%w: vault binding", api.ErrUnauthorized)
	}
	v.mu.Lock()
	u := v.users[principal]
	if u == nil {
		u = &UserVault{root: dir}
		v.users[principal] = u
	}
	v.mu.Unlock()
	if err := u.Locked(func(root string) error { return vault.Ensure(root) }); err != nil {
		return nil, fmt.Errorf("vault open: %w", err)
	}
	return u, nil
}

// memoryEnrollment is the effective-spec memory gate: the contract's
// declared space, environment and memory.enabled flag must all match the
// verified identity. It is re-fetched per call so a revoked grant lands on
// the next resolve, not the next restart.
func memoryEnrollment(ctx context.Context, client spawnv1.EffectiveReader, principal, environment string) error {
	if client == nil || !identity.ValidID(principal) || environment == "" {
		return fmt.Errorf("%w: memory passport unavailable", api.ErrUnauthorized)
	}
	eff, err := effectiveFor(ctx, client, principal)
	if err != nil || eff.Header.Metadata.Space != principal || eff.Header.Metadata.Environment != environment || eff.Config.Memory.Enabled == nil || !*eff.Config.Memory.Enabled {
		return fmt.Errorf("%w: memory disabled", api.ErrUnauthorized)
	}
	return nil
}

// effectiveFor is the shared tenant-scoped effective read every ToolHub
// consumer uses: (local, principal) is the enrolled space identity — the
// contract answers only documents this tenant owns.
func effectiveFor(ctx context.Context, client spawnv1.EffectiveReader, principal string) (spawnv1.Effective, error) {
	spec, err := client.Effective(ctx, spawnv1.Tenant{Org: "local", Owner: principal}, principal)
	if err != nil {
		return spawnv1.Effective{}, err
	}
	return spawnv1.Assemble(spec.Documents)
}
