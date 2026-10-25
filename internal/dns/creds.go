package dns

import (
	"context"
	"encoding/json"
	"fmt"
	"io/fs"
	"strings"

	brokerv1 "github.com/letya999/credential-broker/api/v1"
	"github.com/letya999/hermes-hub/internal/credentialbroker"
	"github.com/letya999/hermes-hub/internal/identity"
)

// Resolver turns a credential_ref into a materialized key map for one
// operation (SPEC-0076 R3).
type Resolver struct {
	Root   fs.FS
	Auth   identity.Envelope
	Broker credentialbroker.Config
}

// Resolve returns the credential map and a release func. file: reads a JSON
// env-map below Root; broker: acquires a lease, materializes it, and the
// returned func releases the lease.
func (r *Resolver) Resolve(ctx context.Context, ref string) (map[string]string, func(), error) {
	scheme, rest, _ := strings.Cut(ref, ":")
	if scheme == "file" {
		f, err := r.Root.Open(rest)
		if err != nil {
			return nil, nil, fmt.Errorf("%w: credential file %q: %v", ErrUnavailable, rest, err)
		}
		defer f.Close()
		var env map[string]string
		if err = json.NewDecoder(f).Decode(&env); err != nil {
			return nil, nil, fmt.Errorf("%w: credential file %q unreadable", ErrUnavailable, rest)
		}
		return env, func() {}, nil
	}
	return r.materialize(ctx, rest)
}

func (r *Resolver) materialize(ctx context.Context, grantID string) (map[string]string, func(), error) {
	control, err := r.Broker.NewForBinding(r.Auth, "broker:control", "dns", "hermes-runtime")
	if err != nil {
		return nil, nil, err
	}
	runtime, err := r.Broker.NewForBinding(r.Auth, "broker:runtime", "dns", "hermes-runtime")
	if err != nil {
		return nil, nil, err
	}
	lease, err := control.Acquire(ctx, brokerv1.AcquireLease{GrantID: grantID, TTLSeconds: 60})
	if err != nil {
		return nil, nil, err
	}
	release := func() {
		_ = runtime.Release(context.Background(), lease.ID, brokerv1.RuntimeRelease{})
	}
	materialized, err := runtime.Materialize(ctx, lease.ID)
	if err != nil {
		return nil, nil, err
	}
	return materialized.Env, release, nil
}
