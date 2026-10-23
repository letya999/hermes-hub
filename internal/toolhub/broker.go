package toolhub

import (
	"context"
	"errors"
	"fmt"
	"github.com/letya999/hermes-hub/internal/toolhub/api"
	"net/http"
	"net/url"
	"slices"
	"strings"
	"time"

	brokerv1 "github.com/letya999/credential-broker/api/v1"
	brokerclient "github.com/letya999/credential-broker/client"
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
	control, err := c.brokerControl(auth, definition.Workload.Class == api.Shared)
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
		if err := control.Do(cleanupCtx, http.MethodPost, "/v1/grants/"+url.PathEscape(grant.ID)+"/revoke", nil, nil); err != nil && !brokerclient.IsNotFound(err) {
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
	ctx = api.WithProbeOptions(ctx, api.ProbeOptions{Mounts: injection.Mounts})
	if definition.Source.Digest == "" && onboarding.Recipe != nil && onboarding.Recipe.Connection.AuthBeforeToolsList {
		if c.Reviewer == nil {
			return api.ToolDefinition{}, fmt.Errorf("%w: deferred source reviewer unavailable", api.ErrIsolation)
		}
		recipe := onboarding.Recipe.Connection
		recipe.AuthBeforeToolsList = false
		ctx = context.WithValue(ctx, api.ConnectionRecipeContextKey{}, recipe)
		ctx = api.WithProbeOptions(ctx, api.ProbeOptions{Values: injection.Environment, Mounts: injection.Mounts, AllowNetwork: true})
		var review api.SourceReview
		review, err = c.Reviewer(ctx, onboarding.Recipe.Source, nil)
		admitted = review.Definition
		admitted.CredentialContractID = definition.CredentialContractID
		admitted.CredentialContractRevision = definition.CredentialContractRevision
		admitted.CredentialContractEnv = api.CloneMap(definition.CredentialContractEnv)
	} else {
		admitted, err = c.AdmitWithCredentials(ctx, definition, injection.Environment)
	}
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
		// Request-scoped extras deliver under the input name itself: a value the
		// owner filled on the form reaches the workload without a contract edit.
		for _, input := range effective.Definition.Credentials {
			if input.PerRequest {
				continue
			}
			if _, mapped := environment[input.Name]; !mapped {
				if value := materialized.Env[input.Name]; value != "" {
					environment[input.Name] = value
				}
			}
		}
		// The contract may deliver alternative credential groups (e.g. a Jira
		// block or a Confluence block): a complete group satisfies the member
		// keys of the other alternatives, but never missing standalone keys.
		if !effective.Definition.CredentialsSatisfied(func(name string) bool { return environment[name] != "" }) {
			var missing []string
			for _, input := range effective.Definition.Credentials {
				if input.Required && environment[input.Name] == "" {
					missing = append(missing, input.Name)
				}
			}
			_ = release(false, false)
			return api.CredentialInjection{}, fmt.Errorf("%w: %w: broker delivery does not satisfy %s; not delivered: %s", api.ErrIsolation, api.ErrCredentialDelivery, effective.Definition.DefinitionID, strings.Join(missing, ", "))
		}
		for _, input := range effective.Credential.Keys {
			if environment[input] != "" || api.GroupedCredentialInput(effective.Definition, input) {
				continue
			}
			// Optional inputs the user left blank in the form are legitimately
			// absent; required coverage was already enforced by the satisfied
			// check above. Undeclared or required names still fail closed.
			if slices.ContainsFunc(effective.Definition.Credentials, func(c api.CredentialInput) bool { return c.Name == input && !c.Required }) {
				continue
			}
			_ = release(false, false)
			return api.CredentialInjection{}, fmt.Errorf("%w: %w: broker delivery %s", api.ErrIsolation, api.ErrCredentialDelivery, input)
		}
		proxy := api.ProxyStubPlan(effective, environment)
		mounts := make([]api.Mount, 0, len(materialized.Mounts))
		var checkpoint func() error
		for _, mount := range materialized.Mounts {
			mounts = append(mounts, api.Mount{Source: mount.Source, Target: mount.Target, ReadOnly: mount.ReadOnly})
			if !mount.ReadOnly {
				checkpoint = func() error { return release(true, true) }
			}
		}
		injection := api.CredentialInjection{Environment: environment, Mounts: mounts, Proxy: proxy, Checkpoint: checkpoint, Cleanup: func() error {
			clear(environment)
			clear(materialized.Env)
			return release(false, false)
		}}
		if err := materializeOAuthStateView(effective.Definition.OAuth, &injection); err != nil {
			_ = injection.Cleanup()
			return api.CredentialInjection{}, err
		}
		return injection, nil
	}
}

// teamBrokerInjector wraps the runtime injector with the lazy per-member
// grant mint a shared (team-owned) credential needs: the reference cannot
// pin one grant, so the member's binding mints its own on first use, signed
// as the manager of the credential's owning context and attributed to the
// member. The minted id lands in the binding grant ledger — durable, so
// disable, remove, rotation, member purge and team drop retire exactly the
// grants they created. The shared reference itself is never mutated.
func teamBrokerInjector(store *Store, controlConfig, runtimeConfig *credentialbroker.Config) api.CredentialInjector {
	inner := brokerRuntimeInjector(controlConfig, runtimeConfig)
	return func(ctx context.Context, effective api.EffectiveBinding) (api.CredentialInjection, error) {
		reference := effective.Credential
		if reference == nil || reference.Backend != "credential-broker" || reference.BrokerContextID == "" {
			return inner(ctx, effective)
		}
		if store == nil || controlConfig == nil {
			return api.CredentialInjection{}, fmt.Errorf("%w: team broker grant store unavailable", api.ErrIsolation)
		}
		if reference.BrokerGrantID != "" {
			return inner(ctx, effective)
		}
		grantID, err := ensureTeamGrant(ctx, store, controlConfig, effective)
		if err != nil {
			return api.CredentialInjection{}, err
		}
		clone := *reference
		clone.BrokerGrantID = grantID
		effective.Credential = &clone
		return inner(ctx, effective)
	}
}

// ensureTeamGrant returns the binding's live grant against the shared
// credential, minting one when the ledger has none. The grant request names
// the credential's owning context — the broker rejects any other — while
// principal, runtime, binding and workload stay the member's own.
func ensureTeamGrant(ctx context.Context, store *Store, controlConfig *credentialbroker.Config, effective api.EffectiveBinding) (string, error) {
	reference := effective.Credential
	if grant, ok := store.BrokerGrantFor(effective.Binding.ToolBindingID, reference.CredentialRefID); ok {
		return grant.GrantID, nil
	}
	if effective.WorkloadID == "" {
		return "", fmt.Errorf("%w: workload identity", api.ErrIsolation)
	}
	control, err := controlConfig.NewContextManaged(effectiveBrokerEnvelope(effective), "broker:control")
	if err != nil {
		return "", err
	}
	execution := "dedicated"
	if effective.Connection != nil && effective.Connection.Metadata["shareable"] == "connection" {
		execution = "shared"
	}
	grant, err := control.Grant(ctx, reference.Locator, brokerv1.GrantRequest{
		ContractID: reference.BrokerContractID, ContractRevision: reference.BrokerContractRevision,
		PrincipalID: effective.Binding.PrincipalID, ContextID: reference.BrokerContextID, RuntimeID: effective.Binding.RuntimeID,
		BindingID: effective.Binding.ToolBindingID, WorkloadID: effective.WorkloadID, Execution: execution,
		IdempotencyKey: api.DeterministicID("teamgrant", effective.Binding.ToolBindingID, reference.CredentialRefID),
	})
	if err != nil {
		return "", err
	}
	return store.RecordBrokerGrant(api.BrokerBindingGrant{
		BindingID: effective.Binding.ToolBindingID, ConnectionID: reference.ConnectionID,
		CredentialRefID: reference.CredentialRefID, ContextID: reference.BrokerContextID,
		GrantID: grant.ID, PolicyVersion: effective.Binding.PolicyVersion,
	})
}

func effectiveBrokerEnvelope(effective api.EffectiveBinding) identity.Envelope {
	contextID := effective.Binding.ContextID
	if effective.Credential != nil && effective.Credential.BrokerContextID != "" {
		// The broker keys context-owned credentials and their grants to the
		// owning context: acquire and materialize must present it or the
		// grant's identity check denies the member's own context.
		contextID = effective.Credential.BrokerContextID
	}
	return identity.Envelope{
		Schema: identity.Schema, PrincipalID: effective.Binding.PrincipalID,
		ExternalIdentityID: effective.Binding.PrincipalID, ContextID: contextID,
		RuntimeID: effective.Binding.RuntimeID, ConversationID: contextID,
		DeliveryTargetID: contextID, PolicyVersion: effective.Binding.PolicyVersion,
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
