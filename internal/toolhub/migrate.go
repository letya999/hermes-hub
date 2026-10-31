package toolhub

import (
	"errors"
	"fmt"
	"github.com/letya999/hermes-hub/internal/toolhub/api"

	"github.com/letya999/hermes-hub/internal/identity"
)

// bindingChange is the persisted audit record for one binding's policy-version
// migration (SPEC-0053 R1.2): who moved, from which hash to which, at which
// binding revision.
type bindingChange struct {
	BindingID  string `json:"binding_id"`
	FromPolicy string `json:"from_policy"`
	ToPolicy   string `json:"to_policy"`
	Revision   uint64 `json:"revision"`
}

// MigrateBindings moves every active binding of one (principal, context,
// runtime) projection from policy version `from` to `to` (SPEC-0053 R1.2).
// Disabled and revoked bindings never move — a revoked grant must not be
// resurrected by a policy roll — and bindings already at `to` are skipped, so
// the call is idempotent. Each migrated binding gets a new revision and a
// bumped projection revision.
// migrateMaxAttempts bounds stale-writer retries: a concurrent store writer
// invalidates our snapshot between the scan and Save, so each attempt
// re-reads the registry before rewriting it.
const migrateMaxAttempts = 8

func MigrateBindings(store *Store, principalID, contextID, runtimeID, from, to string) (int, error) {
	if !identity.ValidID(principalID) || !identity.ValidID(contextID) || !identity.ValidID(runtimeID) {
		return 0, fmt.Errorf("%w: migration target", api.ErrInvalid)
	}
	if !identity.ValidID(from) || !identity.ValidID(to) || from == to {
		return 0, fmt.Errorf("%w: migration policy versions", api.ErrInvalid)
	}
	var lastErr error
	for attempt := 0; attempt < migrateMaxAttempts; attempt++ {
		moved, err := store.migrateOnce(principalID, contextID, runtimeID, from, to)
		if err == nil {
			return moved, nil
		}
		lastErr = err
		if !errors.Is(err, api.ErrConflict) && !errors.Is(err, api.ErrStale) {
			return 0, err
		}
		// A concurrent writer committed between our snapshot and Save. Reload
		// restores the freshest registry; the next attempt re-scans it.
		if reloadErr := store.Reload(); reloadErr != nil {
			return 0, fmt.Errorf("%w: reload after stale write: %v", err, reloadErr)
		}
	}
	return 0, fmt.Errorf("%w: migrate gave up after %d stale writes: %v", api.ErrConflict, migrateMaxAttempts, lastErr)
}

// ProjectionMigration reports what a policy-version roll moved and what
// stayed pinned for an operator decision.
type ProjectionMigration struct {
	BindingsMoved   int
	ProfilesBlocked []string
}

// MigrateProjection rolls every active binding of one (principal, context,
// runtime) projection onto policy version `to`. Unlike MigrateBindings it
// scans live state rather than trusting one `from` version: envelopes are
// minted at the rendered policy hash, so anything pinned at a version no
// manifest recorded still bricks the projection unless it moves.
// Disabled/revoked bindings never move.
// Capability profiles are confirmation-signed: rewriting PolicyVersion would
// invalidate the operator's review digest and brick every later store Load,
// so a stale active profile is never rewritten — it is reported in
// ProfilesBlocked for the operator to republish under the new policy.
func MigrateProjection(store *Store, principalID, contextID, runtimeID, to string) (ProjectionMigration, error) {
	var lastErr error
	for attempt := 0; attempt < migrateMaxAttempts; attempt++ {
		report, err := store.migrateProjectionOnce(principalID, contextID, runtimeID, to)
		if err == nil {
			return report, nil
		}
		lastErr = err
		if !errors.Is(err, api.ErrConflict) && !errors.Is(err, api.ErrStale) {
			return ProjectionMigration{}, err
		}
		if reloadErr := store.Reload(); reloadErr != nil {
			return ProjectionMigration{}, fmt.Errorf("%w: reload after stale write: %v", err, reloadErr)
		}
	}
	return ProjectionMigration{}, fmt.Errorf("%w: migrate gave up after %d stale writes: %v", api.ErrConflict, migrateMaxAttempts, lastErr)
}

func (s *Store) migrateProjectionOnce(principalID, contextID, runtimeID, to string) (ProjectionMigration, error) {
	var report ProjectionMigration
	s.mu.Lock()
	for id, binding := range s.bindings {
		if binding.PrincipalID != principalID || binding.ContextID != contextID || binding.RuntimeID != runtimeID {
			continue
		}
		if binding.Status != api.ActiveStatus || binding.PolicyVersion == to {
			continue
		}
		from := binding.PolicyVersion
		binding.PolicyVersion = to
		binding.Revision++
		s.touchProjectionLocked(&binding)
		s.bindings[id] = binding
		s.bindingChanges = append(s.bindingChanges, bindingChange{
			BindingID:  binding.ToolBindingID,
			FromPolicy: from,
			ToPolicy:   to,
			Revision:   binding.Revision,
		})
		report.BindingsMoved++
	}
	for id, profile := range s.capabilityProfiles {
		if profile.PrincipalID != principalID || profile.ContextID != contextID || profile.RuntimeID != runtimeID {
			continue
		}
		if profile.Status != api.ActiveStatus || profile.PolicyVersion == to {
			continue
		}
		report.ProfilesBlocked = append(report.ProfilesBlocked, id)
	}
	s.mu.Unlock()
	if report.BindingsMoved == 0 {
		return report, nil
	}
	if err := s.persist(); err != nil {
		_ = s.Reload()
		return ProjectionMigration{}, err
	}
	return report, nil
}

func (s *Store) migrateOnce(principalID, contextID, runtimeID, from, to string) (int, error) {
	s.mu.Lock()
	moved := 0
	for id, binding := range s.bindings {
		if binding.PrincipalID != principalID || binding.ContextID != contextID || binding.RuntimeID != runtimeID {
			continue
		}
		if binding.Status != api.ActiveStatus || binding.PolicyVersion != from {
			continue
		}
		binding.PolicyVersion = to
		binding.Revision++
		s.touchProjectionLocked(&binding)
		s.bindings[id] = binding
		s.bindingChanges = append(s.bindingChanges, bindingChange{
			BindingID:  binding.ToolBindingID,
			FromPolicy: from,
			ToPolicy:   to,
			Revision:   binding.Revision,
		})
		moved++
	}
	s.mu.Unlock()
	if err := s.persist(); err != nil {
		// The in-memory mutations may not have landed on disk; reload so the
		// registry cannot diverge silently before the caller sees the error.
		_ = s.Reload()
		return 0, err
	}
	return moved, nil
}
