package supervisor

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/letya999/hermes-hub/internal/stack"
)

// The acquisition lock spans replacement; generation and lease ownership commit
// before creating compute so a lost API acknowledgement remains recoverable.
func (m *Manager) restartKubernetesLocked(ctx context.Context, snapshot Runtime) (bool, error) {
	if err := ctx.Err(); err != nil {
		return false, err
	}
	ctx, cancel := context.WithTimeout(context.Background(), 4*time.Minute)
	defer cancel()
	key := runtimeKey(Binding{PrincipalID: snapshot.PrincipalID, ContextID: snapshot.ContextID, RuntimeMode: snapshot.RuntimeMode})
	if err := m.removeOwnedRuntime(ctx, snapshot); err != nil {
		return false, err
	}
	m.mu.Lock()
	entry := m.items[key]
	if entry == nil || entry.Generation != snapshot.Generation {
		m.mu.Unlock()
		return false, errors.New("runtime changed before replacement")
	}
	m.gen++
	entry.Generation = fmt.Sprintf("gen-%d", m.gen)
	entry.ComputeID, entry.ComputeConditions, entry.State, entry.restored = "", nil, Starting, true
	entry.Restarts++
	for id, lease := range entry.leases {
		lease.Generation = entry.Generation
		entry.leases[id] = lease
	}
	runtime, binding := entry.Runtime, entry.binding
	if err := m.persistLocked(); err != nil {
		entry.State = Degraded
		m.mu.Unlock()
		return false, err
	}
	m.mu.Unlock()
	settings, err := stack.ReadEffectiveSpace(binding.ContextRoot)
	if err != nil {
		m.markDegraded(key)
		return false, errors.New("runtime settings unavailable")
	}
	observed, err := m.backend.Spawn(ctx, SpawnRequest{Binding: binding, Runtime: runtime, Settings: settings, Port: m.cfg.RuntimePort})
	if err == nil {
		err = m.ready(ctx, observed.Address, binding.runtimeAuth)
	}
	if err != nil {
		_ = m.removeOwnedRuntime(ctx, runtime)
		m.markDegraded(key)
		return false, err
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	entry = m.items[key]
	entry.Address, entry.ComputeID, entry.ComputeConditions = observed.Address, observed.ID, observed.Conditions
	entry.State, entry.restored, entry.PendingRestart = snapshot.State, false, nil
	entry.RuntimeHealth, entry.HermesReadiness, entry.HealthCheckedAt = observed.State, "ready", m.cfg.Now()
	return true, m.persistLocked()
}
