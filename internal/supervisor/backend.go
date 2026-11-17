package supervisor

import (
	"context"
	"errors"
	"fmt"
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

func (d dockerBackend) Spawn(ctx context.Context, request SpawnRequest) (ComputeObservation, error) {
	m, binding, rt := d.m, request.Binding, request.Runtime
	if rt.Managed {
		preparation := binding
		preparation.RuntimeMode = "spawn-files"
		lock := m.lockFor(runtimeKey(preparation))
		lock.Lock()
		defer lock.Unlock()
	}
	args, err := m.runArgsWithGeneration(binding, rt.Container, request.Port, rt.Generation)
	if err != nil {
		return ComputeObservation{}, fmt.Errorf("prepare runtime launch: %w", err)
	}
	if rt.Managed {
		if err := m.ensureManagedInfra(ctx, binding, request.Settings, rt.Container, request.Port, rt.Generation); err != nil {
			return ComputeObservation{}, fmt.Errorf("managed topology: %w", err)
		}
	}
	if out, err := m.command(ctx, args...); err != nil {
		return ComputeObservation{}, fmt.Errorf("start runtime: %w: %s", err, firstLine(string(out)))
	}
	if request.Settings.Has("browser") || request.Settings.Has("meet") {
		if err := m.ensureBrowserSidecar(ctx, binding.UserID); err != nil {
			return ComputeObservation{}, err
		}
		if !rt.Managed {
			network := "hermes-hub-" + binding.UserID + "-" + m.environment() + "_default"
			if out, err := m.command(ctx, "network", "connect", network, rt.Container); err != nil {
				return ComputeObservation{}, fmt.Errorf("attach browser network: %w: %s", err, firstLine(string(out)))
			}
		}
	}
	return ComputeObservation{Address: rt.Address}, nil
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
