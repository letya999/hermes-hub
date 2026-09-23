package toolhub

import (
	"context"
	"fmt"
	"strings"

	brokerv1 "github.com/letya999/credential-broker/api/v1"
	"github.com/letya999/hermes-hub/internal/credentialbroker"
	"github.com/letya999/hermes-hub/internal/identity"
)

func brokerRuntimeInjector(controlConfig, runtimeConfig *credentialbroker.Config) CredentialInjector {
	return func(ctx context.Context, effective EffectiveBinding) (CredentialInjection, error) {
		if effective.Credential == nil || effective.Credential.Backend != "credential-broker" {
			return CredentialInjection{Environment: map[string]string{}}, nil
		}
		if controlConfig == nil || runtimeConfig == nil || effective.Credential.BrokerGrantID == "" {
			return CredentialInjection{}, fmt.Errorf("%w: credential broker runtime is not configured", ErrIsolation)
		}
		if effective.WorkloadID == "" {
			return CredentialInjection{}, fmt.Errorf("%w: workload identity", ErrIsolation)
		}
		auth := effectiveBrokerEnvelope(effective)
		control, err := controlConfig.NewForBinding(auth, "broker:control", effective.Binding.ToolBindingID, effective.WorkloadID)
		if err != nil {
			return CredentialInjection{}, err
		}
		runtime, err := runtimeConfig.NewForBinding(auth, "broker:runtime", effective.Binding.ToolBindingID, effective.WorkloadID)
		if err != nil {
			return CredentialInjection{}, err
		}
		lease, err := control.Acquire(ctx, brokerv1.AcquireLease{GrantID: effective.Credential.BrokerGrantID, TTLSeconds: 120})
		if err != nil {
			return CredentialInjection{}, err
		}
		released := false
		release := func(checkpoint bool) error {
			if released {
				return nil
			}
			err := runtime.Release(context.Background(), lease.ID, brokerv1.RuntimeRelease{Quiesced: true, Checkpoint: checkpoint})
			released = err == nil
			return err
		}
		materialized, err := runtime.Materialize(ctx, lease.ID)
		if err != nil {
			_ = release(false)
			return CredentialInjection{}, err
		}
		environment := map[string]string{}
		for input, target := range effective.Credential.BrokerEnv {
			if value := materialized.Env[target]; value != "" {
				environment[input] = value
			}
		}
		for _, input := range effective.Credential.Keys {
			if environment[input] == "" && len(materialized.Mounts) == 0 {
				_ = release(false)
				return CredentialInjection{}, fmt.Errorf("%w: broker delivery %s", ErrIsolation, input)
			}
		}
		mounts := make([]Mount, 0, len(materialized.Mounts))
		var checkpoint func() error
		for _, mount := range materialized.Mounts {
			mounts = append(mounts, Mount{Source: mount.Source, Target: mount.Target, ReadOnly: mount.ReadOnly})
			if !mount.ReadOnly {
				checkpoint = func() error { return release(true) }
			}
		}
		return CredentialInjection{Environment: environment, Mounts: mounts, Checkpoint: checkpoint, Cleanup: func() error {
			clear(environment)
			clear(materialized.Env)
			return release(false)
		}}, nil
	}
}

func effectiveBrokerEnvelope(effective EffectiveBinding) identity.Envelope {
	return identity.Envelope{
		Schema: identity.Schema, PrincipalID: effective.Binding.PrincipalID,
		ExternalIdentityID: effective.Binding.PrincipalID, ContextID: effective.Binding.ContextID,
		RuntimeID: effective.Binding.RuntimeID, ConversationID: effective.Binding.ContextID,
		DeliveryTargetID: effective.Binding.ContextID, PolicyVersion: effective.Binding.PolicyVersion,
	}
}

func mergeCredentialInjectors(local, broker CredentialInjector, brokerOnly bool) CredentialInjector {
	if brokerOnly {
		return func(ctx context.Context, effective EffectiveBinding) (CredentialInjection, error) {
			if effective.Credential == nil || effective.Credential.Backend != "credential-broker" {
				return CredentialInjection{}, fmt.Errorf("%w: credential reference must be migrated to Credential Broker", ErrUnauthorized)
			}
			return broker(ctx, effective)
		}
	}
	if local == nil {
		return broker
	}
	if broker == nil {
		return local
	}
	return func(ctx context.Context, effective EffectiveBinding) (CredentialInjection, error) {
		if effective.Credential != nil && strings.EqualFold(effective.Credential.Backend, "credential-broker") {
			return broker(ctx, effective)
		}
		return local(ctx, effective)
	}
}
