package toolhub

import (
	"context"
	"errors"
	"fmt"
	"github.com/letya999/hermes-hub/internal/toolhub/api"
	"net/http"
	"net/url"
	"strings"
	"time"

	brokerv1 "github.com/letya999/credential-broker/api/v1"
	"github.com/letya999/hermes-hub/internal/credentialbroker"
	"github.com/letya999/hermes-hub/internal/credstore"
	"github.com/letya999/hermes-hub/internal/identity"
)

// admitWithBrokerCredential uses a short-lived owner-bound grant for the
// tools/list probe. The permanent workload grant is created only after the
// confirmed tool contract is bound to this owner.
func (c *ControlPlane) admitWithBrokerCredential(ctx context.Context, auth identity.Envelope, onboarding api.Onboarding, definition api.ToolDefinition, credentialID string) (admitted api.ToolDefinition, resultErr error) {
	if c.Broker == nil || !c.Broker.Enabled() || c.BrokerRuntime == nil || !c.BrokerRuntime.Enabled() || c.AdmitWithCredentials == nil || credentialID == "" {
		return api.ToolDefinition{}, fmt.Errorf("%w: Broker admission unavailable", api.ErrIsolation)
	}
	control, err := c.Broker.New(auth, "broker:control")
	if err != nil {
		return api.ToolDefinition{}, err
	}
	bindingID := api.DeterministicID("admission", onboarding.OnboardingID, credentialID)
	workloadID := api.DeterministicID("probe", onboarding.OnboardingID, api.RandomNonce())
	grant, err := control.Grant(ctx, credentialID, brokerv1.GrantRequest{
		ContractID: definition.CredentialContractID, ContractRevision: definition.CredentialContractRevision,
		PrincipalID: auth.PrincipalID, ContextID: auth.ContextID, RuntimeID: auth.RuntimeID,
		BindingID: bindingID, WorkloadID: workloadID, Execution: "dedicated", IdempotencyKey: api.RandomNonce(),
	})
	if err != nil {
		return api.ToolDefinition{}, err
	}
	defer func() {
		cleanupCtx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
		defer cancel()
		if err := control.Do(cleanupCtx, http.MethodPost, "/v1/grants/"+url.PathEscape(grant.ID)+"/revoke", nil, nil); err != nil {
			resultErr = errors.Join(resultErr, fmt.Errorf("%w: temporary Broker grant revoke: %v", api.ErrIsolation, err))
		}
	}()
	effective := api.EffectiveBinding{
		Binding:    api.ToolBinding{ToolBindingID: bindingID, PrincipalID: auth.PrincipalID, ContextID: auth.ContextID, RuntimeID: auth.RuntimeID, PolicyVersion: auth.PolicyVersion},
		WorkloadID: workloadID, Definition: definition,
		Credential: &api.CredentialReference{Backend: "credential-broker", BrokerGrantID: grant.ID, BrokerEnv: api.CloneMap(definition.CredentialContractEnv), Keys: requiredCredentialNames(definition)},
	}
	injection, err := brokerRuntimeInjector(c.Broker, c.BrokerRuntime)(ctx, effective)
	if err != nil {
		return api.ToolDefinition{}, err
	}
	defer func() {
		if err := injection.Cleanup(); err != nil {
			resultErr = errors.Join(resultErr, fmt.Errorf("%w: temporary Broker lease release: %v", api.ErrIsolation, err))
		}
	}()
	if len(injection.Mounts) != 0 {
		return api.ToolDefinition{}, fmt.Errorf("%w: credentialed tools/list does not support Broker file mounts", api.ErrIsolation)
	}
	admitted, err = c.AdmitWithCredentials(ctx, definition, injection.Environment)
	if err != nil {
		// The upstream error can echo a submitted StringSession. Keep it out of
		// onboarding state and channel responses.
		return api.ToolDefinition{}, fmt.Errorf("%w: authenticated tools/list failed", api.ErrIsolation)
	}
	return admitted, nil
}

func brokerRuntimeInjector(controlConfig, runtimeConfig *credentialbroker.Config) api.CredentialInjector {
	return func(ctx context.Context, effective api.EffectiveBinding) (api.CredentialInjection, error) {
		if effective.Credential == nil || effective.Credential.Backend != "credential-broker" {
			return api.CredentialInjection{Environment: map[string]string{}}, nil
		}
		if controlConfig == nil || runtimeConfig == nil || effective.Credential.BrokerGrantID == "" {
			return api.CredentialInjection{}, fmt.Errorf("%w: credential broker runtime is not configured", api.ErrIsolation)
		}
		if effective.WorkloadID == "" {
			return api.CredentialInjection{}, fmt.Errorf("%w: workload identity", api.ErrIsolation)
		}
		auth := effectiveBrokerEnvelope(effective)
		control, err := controlConfig.NewForBinding(auth, "broker:control", effective.Binding.ToolBindingID, effective.WorkloadID)
		if err != nil {
			return api.CredentialInjection{}, err
		}
		runtime, err := runtimeConfig.NewForBinding(auth, "broker:runtime", effective.Binding.ToolBindingID, effective.WorkloadID)
		if err != nil {
			return api.CredentialInjection{}, err
		}
		lease, err := control.Acquire(ctx, brokerv1.AcquireLease{GrantID: effective.Credential.BrokerGrantID, TTLSeconds: 120})
		if err != nil {
			return api.CredentialInjection{}, err
		}
		released := false
		release := func(checkpoint, quiesced bool) error {
			if released {
				return nil
			}
			err := runtime.Release(context.Background(), lease.ID, brokerv1.RuntimeRelease{Quiesced: quiesced, Checkpoint: checkpoint})
			released = err == nil
			return err
		}
		materialized, err := runtime.Materialize(ctx, lease.ID)
		if err != nil {
			_ = release(false, false)
			return api.CredentialInjection{}, err
		}
		environment := map[string]string{}
		for input, target := range effective.Credential.BrokerEnv {
			if value := materialized.Env[target]; value != "" {
				environment[input] = value
			}
		}
		// The contract may deliver alternative credential groups (e.g. a Jira
		// block or a Confluence block): a complete group satisfies the member
		// keys of the other alternatives, but never missing standalone keys.
		if !effective.Definition.CredentialsSatisfied(func(name string) bool { return environment[name] != "" }) {
			_ = release(false, false)
			return api.CredentialInjection{}, fmt.Errorf("%w: broker delivery does not satisfy %s", api.ErrIsolation, effective.Definition.DefinitionID)
		}
		for _, input := range effective.Credential.Keys {
			if environment[input] == "" && !api.GroupedCredentialInput(effective.Definition, input) {
				_ = release(false, false)
				return api.CredentialInjection{}, fmt.Errorf("%w: broker delivery %s", api.ErrIsolation, input)
			}
		}
		mounts := make([]api.Mount, 0, len(materialized.Mounts))
		var checkpoint func() error
		for _, mount := range materialized.Mounts {
			mounts = append(mounts, api.Mount{Source: mount.Source, Target: mount.Target, ReadOnly: mount.ReadOnly})
			if !mount.ReadOnly {
				checkpoint = func() error { return release(true, true) }
			}
		}
		return api.CredentialInjection{Environment: environment, Mounts: mounts, Checkpoint: checkpoint, Cleanup: func() error {
			clear(environment)
			clear(materialized.Env)
			return release(false, false)
		}}, nil
	}
}

func effectiveBrokerEnvelope(effective api.EffectiveBinding) identity.Envelope {
	return identity.Envelope{
		Schema: identity.Schema, PrincipalID: effective.Binding.PrincipalID,
		ExternalIdentityID: effective.Binding.PrincipalID, ContextID: effective.Binding.ContextID,
		RuntimeID: effective.Binding.RuntimeID, ConversationID: effective.Binding.ContextID,
		DeliveryTargetID: effective.Binding.ContextID, PolicyVersion: effective.Binding.PolicyVersion,
	}
}

func mergeCredentialInjectors(local, broker api.CredentialInjector, brokerOnly bool) api.CredentialInjector {
	if brokerOnly {
		return func(ctx context.Context, effective api.EffectiveBinding) (api.CredentialInjection, error) {
			if effective.Credential == nil {
				if len(effective.Definition.Credentials) == 0 {
					return api.CredentialInjection{Environment: map[string]string{}}, nil
				}
				return api.CredentialInjection{}, fmt.Errorf("%w: credential reference must be migrated to Credential Broker", api.ErrUnauthorized)
			}
			if strings.EqualFold(effective.Credential.Backend, "credential-broker") {
				return broker(ctx, effective)
			}
			// A credential-gate admission has no reviewed contract. Its loopback
			// secret stays in the local store; a contracted definition does not.
			if effective.Definition.CredentialContractID == "" && local != nil && strings.EqualFold(effective.Credential.Backend, credstore.BackendLocal) {
				return local(ctx, effective)
			}
			return api.CredentialInjection{}, fmt.Errorf("%w: credential reference must be migrated to Credential Broker", api.ErrUnauthorized)
		}
	}
	if local == nil {
		return broker
	}
	if broker == nil {
		return local
	}
	return func(ctx context.Context, effective api.EffectiveBinding) (api.CredentialInjection, error) {
		if effective.Credential != nil && strings.EqualFold(effective.Credential.Backend, "credential-broker") {
			return broker(ctx, effective)
		}
		return local(ctx, effective)
	}
}
