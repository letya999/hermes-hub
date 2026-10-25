package credentialbroker

import (
	"context"
	"errors"
	"time"

	brokerv1 "github.com/letya999/credential-broker/api/v1"
	"github.com/letya999/hermes-hub/internal/identity"
)

// ResolveEnv keeps a provider credential inside the authenticated service. It
// never returns the materialized DTO or retains a lease after resolution.
func (c Config) ResolveEnv(ctx context.Context, actor identity.Envelope, binding, workload, grant, target string) (string, error) {
	control, err := c.NewForBinding(actor, "broker:control", binding, workload)
	if err != nil {
		return "", errors.New("credential broker control client unavailable")
	}
	runtime, err := c.NewForBinding(actor, "broker:runtime", binding, workload)
	if err != nil {
		return "", errors.New("credential broker runtime client unavailable")
	}
	lease, err := control.Acquire(ctx, brokerv1.AcquireLease{GrantID: grant, TTLSeconds: 60})
	if err != nil {
		return "", errors.New("credential grant unavailable")
	}
	materialized, materializeErr := runtime.Materialize(ctx, lease.ID)
	defer clear(materialized.Env)
	releaseContext, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
	defer cancel()
	releaseErr := runtime.Release(releaseContext, lease.ID, brokerv1.RuntimeRelease{})
	if materializeErr != nil {
		return "", errors.New("credential materialization failed")
	}
	if releaseErr != nil {
		return "", errors.New("credential lease release failed")
	}
	value := materialized.Env[target]
	if value == "" {
		return "", errors.New("credential delivery target missing")
	}
	return value, nil
}
