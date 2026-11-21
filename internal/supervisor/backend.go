package supervisor

import (
	"context"
	"errors"
	"strings"

	"github.com/letya999/hermes-hub/internal/stack"
)

// ComputeBackend owns compute, never admission, leases or persistent user data.
// Verify must precede adoption; Remove must fence the observed immutable ID.
type ComputeBackend interface {
	Spawn(context.Context, SpawnRequest) (ComputeObservation, error)
	Observe(context.Context, Runtime) (ComputeObservation, error)
	Verify(context.Context, Runtime) error
	Remove(context.Context, Runtime) error
}

type SpawnRequest struct {
	Binding  Binding
	Runtime  Runtime
	Settings stack.Settings
	Port     int
}

// ComputeObservation is deliberately free of raw provider messages and env.
type ComputeObservation struct {
	State      string
	ID         string
	Address    string
	Conditions []Condition
}

type dockerBackend struct{ m *Manager }

func (d dockerBackend) Verify(ctx context.Context, runtime Runtime) error {
	return d.m.verifyDockerOwnership(ctx, runtime)
}

func (d dockerBackend) Remove(ctx context.Context, runtime Runtime) error {
	return d.m.removeDockerRuntime(ctx, runtime)
}

func (d dockerBackend) Observe(ctx context.Context, runtime Runtime) (ComputeObservation, error) {
	out, err := d.m.command(ctx, "inspect", "--format", "{{.State.Status}}", runtime.Container)
	return ComputeObservation{State: strings.TrimSpace(string(out)), Address: runtime.Address}, err
}

// Managed workload launch moved to spawn-controller (CHG-0336 S7c): the
// supervisor claims runtimes through contracts/spawn/v1 and never runs
// containers itself. Spawn stays on the interface for the compile-only
// firecracker/kubernetes backends; the docker backend refuses.
func (d dockerBackend) Spawn(context.Context, SpawnRequest) (ComputeObservation, error) {
	return ComputeObservation{}, errors.New("managed workload launch moved to spawn-controller")
}

func (m *Manager) usingKubernetes() bool { return m.cfg.ExecutionBackend == "kubernetes" }

func (m *Manager) VerifyOwnership(ctx context.Context, runtime Runtime) error {
	return m.backend.Verify(ctx, runtime)
}

func (m *Manager) removeOwnedRuntime(ctx context.Context, runtime Runtime) error {
	return m.backend.Remove(ctx, runtime)
}

func (m *Manager) observeCompute(ctx context.Context, runtime Runtime) (ComputeObservation, error) {
	observation, err := m.backend.Observe(ctx, runtime)
	if err == nil && observation.State == "" {
		err = errors.New("compute state unavailable")
	}
	return observation, err
}
