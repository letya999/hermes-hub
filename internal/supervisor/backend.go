package supervisor

import (
	"context"
	"errors"
	"strings"
)

// ComputeObservation is deliberately free of raw provider messages and env.
type ComputeObservation struct {
	State      string
	ID         string
	Address    string
	Conditions []Condition
}

// Compute ownership lives on the Manager again: Spawn left in CHG-0336 S8
// (managed workload launch moved to spawn-controller and the
// kubernetes/firecracker implementations moved to
// services/spawn-controller/internal/backend as compile-only code), leaving
// exactly one docker compute path. Verify must precede adoption; Remove must
// fence the observed immutable ID.
func (m *Manager) VerifyOwnership(ctx context.Context, runtime Runtime) error {
	return m.verifyDockerOwnership(ctx, runtime)
}

func (m *Manager) removeOwnedRuntime(ctx context.Context, runtime Runtime) error {
	return m.removeDockerRuntime(ctx, runtime)
}

func (m *Manager) observeCompute(ctx context.Context, runtime Runtime) (ComputeObservation, error) {
	out, err := m.command(ctx, "inspect", "--format", "{{.State.Status}}", runtime.Container)
	observation := ComputeObservation{State: strings.TrimSpace(string(out)), Address: runtime.Address}
	if err == nil && observation.State == "" {
		err = errors.New("compute state unavailable")
	}
	return observation, err
}
