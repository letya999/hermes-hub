// Firecracker and Kubernetes launch backends — compile-only stubs
// (CHG-0336 S7c, decision 13): the managed-runtime launch authority is
// moving here from the v0 supervisor's firecracker.go and
// kubernetes_plan.go, but neither backend is selectable until M40.15.
// They implement Backend so the interface surface and the managed Spec
// shape compile against them; every call refuses, so they can never
// silently run a workload.
package backend

import (
	"context"
	"errors"
)

// errBackendNotSelectable is the single refusal every compile-only
// backend returns — the service's backend selection refuses them
// earlier, so reaching this is a wiring bug.
var errBackendNotSelectable = errors.New("backend: not selectable until M40.15")

// Firecracker is the compile-only microVM backend: the Spec it would
// apply carries the same managed mount/env contract the docker backend
// runs — the guest image maps Spec.Mounts to virtiofs shares and Spec.Env
// to the guest environment.
type Firecracker struct{}

// Kubernetes is the compile-only pod backend: the managed Spec maps to a
// pod spec (Mounts -> volumes, EnvFiles -> projected envFrom, Relay ->
// the dual-container relay sidecar) the same way the docker branch
// applies it.
type Kubernetes struct{}

func (Firecracker) Start(context.Context, Spec) (Runtime, error) {
	return Runtime{}, errBackendNotSelectable
}
func (Firecracker) Stop(context.Context, string) error { return errBackendNotSelectable }
func (Firecracker) Inspect(context.Context, string) (Runtime, error) {
	return Runtime{}, errBackendNotSelectable
}
func (Firecracker) List(context.Context) ([]Runtime, error) { return nil, errBackendNotSelectable }
func (Firecracker) Exec(context.Context, string, ExecSpec) (ExecResult, error) {
	return ExecResult{}, errBackendNotSelectable
}
func (Firecracker) VerifyWorker(context.Context, string, Spec) error {
	return errBackendNotSelectable
}

func (Kubernetes) Start(context.Context, Spec) (Runtime, error) {
	return Runtime{}, errBackendNotSelectable
}
func (Kubernetes) Stop(context.Context, string) error { return errBackendNotSelectable }
func (Kubernetes) Inspect(context.Context, string) (Runtime, error) {
	return Runtime{}, errBackendNotSelectable
}
func (Kubernetes) List(context.Context) ([]Runtime, error) { return nil, errBackendNotSelectable }
func (Kubernetes) Exec(context.Context, string, ExecSpec) (ExecResult, error) {
	return ExecResult{}, errBackendNotSelectable
}
func (Kubernetes) VerifyWorker(context.Context, string, Spec) error {
	return errBackendNotSelectable
}
