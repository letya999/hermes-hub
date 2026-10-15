package toolhub

import (
	"context"
	"errors"
	"fmt"
	"github.com/letya999/hermes-hub/internal/credstore"
	"github.com/letya999/hermes-hub/internal/identity"
	"github.com/letya999/hermes-hub/internal/oauth"
	"github.com/letya999/hermes-hub/internal/toolhub/api"
	"net/http"
	"net/url"
	"strings"
	"time"
)

func (c *ControlPlane) newOnboarding(auth identity.Envelope, mode, requestKey string, definition api.ToolDefinition, sourceURL, commit string) (api.Onboarding, error) {
	idSeed := requestKey
	if idSeed == "" {
		idSeed = mode + ":" + definition.DefinitionID + "@" + definition.Version + ":" + sourceURL + ":" + commit
	}
	onboarding := api.Onboarding{
		Schema: api.SchemaVersion, OnboardingID: api.DeterministicID("onboard", auth.PrincipalID, auth.ContextID, auth.RuntimeID, idSeed),
		PrincipalID: auth.PrincipalID, ContextID: auth.ContextID, RuntimeID: auth.RuntimeID, PolicyVersion: auth.PolicyVersion,
		Mode: mode, SourceURL: sourceURL, CommitSHA: commit, DefinitionID: definition.DefinitionID, DefinitionVersion: definition.Version,
		Required: credentialHints(definition), Permissions: api.ToolNames(definition), Effects: api.EffectNames(definition),
		IdempotencyKey: requestKey, Revision: 1, CreatedAt: c.now(),
	}
	if len(onboarding.Required) > 0 {
		onboarding.Phase = api.PhaseAwaitingCreds
		onboarding.FormNonce = api.RandomNonce()
		onboarding.FormExpires = c.now().Add(c.ttl())
	} else {
		onboarding.Phase = api.PhaseAwaitingConfirm
		onboarding.ConfirmationNonce = api.RandomNonce()
		onboarding.ConfirmationExpires = c.now().Add(c.ttl())
	}
	if err := c.Store.PutOnboarding(onboarding); err != nil {
		return api.Onboarding{}, err
	}
	return onboarding, nil
}

// bindReviewedContract attaches a reviewed credential-broker contract to a
// freshly reviewed definition whose required credentials have no contract
// reference yet. A contract is eligible only when its deliveries produce an env
// variable for every required credential name; the most specific candidate wins
// so a narrow single-purpose contract beats a broad multi-delivery one.
// Deliveries for optional declared inputs are wired too, so values entered at
// onboarding actually reach the workload env. An already bound definition keeps
// its contract id and existing mappings; missing optional deliveries from the
// same contract are backfilled. If the bound contract vanished or no longer
// covers the required set, the stored binding is left untouched rather than
// silently rebound to a different contract.
func (c *ControlPlane) bindReviewedContract(ctx context.Context, auth identity.Envelope, definition *api.ToolDefinition) error {
	if c == nil || c.Broker == nil || !c.Broker.Enabled() || definition == nil {
		return nil
	}
	required := map[string]bool{}
	declared := map[string]bool{}
	for _, input := range definition.Credentials {
		declared[input.Name] = true
		if input.Required {
			required[input.Name] = true
		}
	}
	if len(required) == 0 {
		return nil
	}
	control, err := c.Broker.New(auth, "broker:control")
	if err != nil {
		return err
	}
	contracts, err := control.Contracts(ctx)
	if err != nil {
		return err
	}
	bound := definition.CredentialContractID
	best := -1
	bestExtra := 0
	var bestEnv map[string]string
	for i, candidate := range contracts {
		if bound != "" && candidate.ID != bound {
			continue
		}
		// A definition that already pins a reviewed revision must not slide to
		// an older revision of the same contract still present in the broker.
		if definition.CredentialContractRevision > 0 && candidate.Revision != definition.CredentialContractRevision {
			continue
		}
		if definition.Workload.Class == api.Shared && !candidate.AllowShared {
			continue
		}
		env := api.CloneMap(definition.CredentialContractEnv)
		if env == nil {
			env = map[string]string{}
		}
		delivered := map[string]bool{}
		for _, delivery := range candidate.Deliveries {
			target := delivery.Target
			if delivery.Type != "env" {
				target = delivery.EnvName
			}
			if target == "" {
				continue
			}
			delivered[target] = true
			if !declared[target] {
				continue
			}
			if env[target] == "" {
				env[target] = target
			}
		}
		if !definition.CredentialsSatisfied(func(name string) bool { return delivered[env[name]] }) {
			continue
		}
		extra := len(candidate.Deliveries) - len(env)
		if best == -1 || extra < bestExtra || (extra == bestExtra && candidate.ID < contracts[best].ID) {
			best, bestExtra, bestEnv = i, extra, env
		}
	}
	if best == -1 {
		return nil
	}
	definition.CredentialContractID = contracts[best].ID
	definition.CredentialContractRevision = contracts[best].Revision
	definition.CredentialContractEnv = bestEnv
	return nil
}

func (c *ControlPlane) Rotate(auth identity.Envelope, args map[string]any) (map[string]any, error) {
	if err := api.RejectAuthorityArguments(args); err != nil {
		return nil, err
	}
	if c.Broker != nil && c.Broker.Enabled() {
		if c.Store == nil {
			return nil, api.ErrInvalid
		}
		onboarding, err := c.resolveOnboarding(auth, args)
		if err != nil {
			return nil, err
		}
		if onboarding.BrokerCredentialID == "" {
			// A remote-mcp onboarding stores its credential in the local
			// encrypted backend, never the Broker; fall through to the local
			// rotation path instead of deadlocking on a missing record.
			if onboarding.Locator == "" {
				return nil, api.ErrNotFound
			}
			return c.rotateLocal(auth, onboarding)
		}
		if onboarding.BrokerRotateCredentialID != "" && onboarding.Phase == api.PhaseAwaitingCreds {
			return c.statusBody(onboarding, true), nil
		}
		definition, err := c.definitionOf(onboarding)
		if err != nil {
			return nil, err
		}
		onboarding.BrokerRotateCredentialID = onboarding.BrokerCredentialID
		onboarding.BrokerRequestID, onboarding.BrokerAuthorizationURL = "", ""
		onboarding.BrokerAttempts++
		onboarding.Phase = api.PhaseAwaitingCreds
		onboarding.Error = ""
		onboarding.ErrorCode = ""
		onboarding.ConfirmationNonce = ""
		onboarding.ConfirmationUsed = false
		onboarding.Revision++
		if err := c.ensureBrokerRequest(context.Background(), auth, &onboarding, definition); err != nil {
			return nil, err
		}
		return c.statusBody(onboarding, true), nil
	}
	onboarding, err := c.resolveOnboarding(auth, args)
	if err != nil {
		return nil, err
	}
	return c.rotateLocal(auth, onboarding)
}

func (c *ControlPlane) rotateLocal(auth identity.Envelope, onboarding api.Onboarding) (map[string]any, error) {
	if onboarding.ConnectionID == "" || onboarding.Locator == "" {
		return nil, fmt.Errorf("%w: connection", api.ErrNotFound)
	}
	// Only the policy's store owner rotates a shared credential — a member's
	// rotate would rewrite the connection every member resolves through.
	if c.Store.sharedForeignConnection(auth.PrincipalID, onboarding.ConnectionID) {
		return nil, fmt.Errorf("%w: shared credential rotation is owner-only", api.ErrUnauthorized)
	}
	keys := credentialNames(onboarding)
	if binding, err := c.Store.binding(onboarding.BindingID); err == nil && binding.CredentialRefID != "" {
		c.Store.mu.RLock()
		if ref, ok := c.Store.credentials[binding.CredentialRefID]; ok {
			keys = append([]string(nil), ref.Keys...)
		}
		c.Store.mu.RUnlock()
	}
	next, err := c.Store.RotateCredential(onboarding.ConnectionID, credstore.BackendLocal, onboarding.Locator+"-rotated", keys)
	if err != nil {
		return nil, err
	}
	if err := c.Store.SetBindingStatus(onboarding.BindingID, api.RevokedStatus); err != nil && !errors.Is(err, api.ErrRevoked) && !errors.Is(err, api.ErrNotFound) {
		return nil, err
	}
	onboarding.CredentialRefID = next.CredentialRefID
	onboarding.Phase = api.PhaseRevoked
	onboarding.Revision++
	if err := c.Store.PutOnboarding(onboarding); err != nil {
		return nil, err
	}
	return c.statusBody(onboarding, false), nil
}

func (c *ControlPlane) cutAuthorization(onboarding api.Onboarding) error {
	if onboarding.BindingID != "" {
		if err := c.Store.SetBindingStatus(onboarding.BindingID, api.RevokedStatus); err != nil && !errors.Is(err, api.ErrRevoked) && !errors.Is(err, api.ErrNotFound) {
			return err
		}
	}
	// A member of a shared connection cuts only its own binding: revoking the
	// connection, its ciphertext or the shared workload is owner-only.
	if c.Store.sharedForeignConnection(onboarding.PrincipalID, onboarding.ConnectionID) {
		return nil
	}
	if onboarding.ConnectionID != "" {
		if err := c.Store.SetConnectionStatus(onboarding.ConnectionID, api.RevokedStatus); err != nil && !errors.Is(err, api.ErrRevoked) && !errors.Is(err, api.ErrNotFound) {
			return err
		}
	}
	if c.Broker != nil && c.Broker.Enabled() && onboarding.BrokerCredentialID != "" {
		auth := identity.Envelope{Schema: identity.Schema, PrincipalID: onboarding.PrincipalID, ExternalIdentityID: onboarding.PrincipalID, ContextID: onboarding.ContextID, RuntimeID: onboarding.RuntimeID, ConversationID: onboarding.PrincipalID, DeliveryTargetID: onboarding.PrincipalID, PolicyVersion: onboarding.PolicyVersion}
		if control, err := c.Broker.New(auth, "broker:control"); err == nil {
			if err := control.Revoke(context.Background(), onboarding.BrokerCredentialID); err != nil {
				return err
			}
		}
	}
	if c.Secrets != nil && onboarding.Locator != "" {
		owner := onboarding.PrincipalID
		if onboarding.CredentialOwner != "" {
			owner = onboarding.CredentialOwner
		}
		_ = c.Secrets.SetStatus(onboarding.Locator, owner, credstore.StatusRevoked)
	}
	c.releaseBindingWorkloads(onboarding)
	return nil
}

// releaseBindingWorkloads stops the workload owned by the onboarding binding.
// Recorded workload instances cover every class; the deterministic recompute
// covers admissions whose record is gone (e.g. a store snapshot predating the
// record or a controller that kept the containers after losing its own map).
func (c *ControlPlane) releaseBindingWorkloads(onboarding api.Onboarding) {
	if c.CLIRelease != nil && onboarding.BindingID != "" && identity.ValidID(onboarding.PrincipalID) {
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		_ = c.CLIRelease(ctx, api.CliReleaseRequest{PrincipalID: onboarding.PrincipalID, BindingID: onboarding.BindingID, Reason: "binding-release"})
	}
	if c.Release == nil || onboarding.BindingID == "" {
		return
	}
	released := map[string]bool{}
	release := func(workloadID string) {
		if workloadID == "" || released[workloadID] {
			return
		}
		released[workloadID] = true
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		_ = c.Release(ctx, workloadID)
	}
	for _, workloadID := range c.Store.WorkloadIDsForBinding(onboarding.BindingID) {
		release(workloadID)
	}
	binding, err := c.Store.binding(onboarding.BindingID)
	if err != nil {
		return
	}
	definition, err := c.definitionOf(onboarding)
	if err != nil {
		return
	}
	switch definition.Workload.Class {
	case api.Shared:
		release(api.WorkloadInstanceID(definition.DefinitionID, api.Shared, "", ""))
	case api.PerUser:
		ownerID := binding.ToolBindingID
		if binding.ConnectionID != "" {
			ownerID = onboarding.ContextID + ":" + onboarding.PrincipalID + ":" + binding.ConnectionID
		}
		release(api.WorkloadInstanceID(definition.DefinitionID, api.PerUser, ownerID, ""))
	}
}

func (c *ControlPlane) materializeBinding(ctx context.Context, auth identity.Envelope, onboarding api.Onboarding, definition api.ToolDefinition) (api.ToolBinding, error) {
	rotation := onboarding.BrokerRotateCredentialID != ""
	if existing := c.Store.findBinding(auth, definition); existing != nil && existing.Status != api.RevokedStatus && !rotation {
		_, ref, err := c.Store.OwnedConnection(auth, existing.ConnectionID)
		if onboarding.Locator == "" || err == nil && ref.Locator == onboarding.Locator {
			if c.Ready != nil {
				return c.Store.EnableReady(ctx, auth, definition.DefinitionID, definition.Version, c.Ready)
			}
			return *existing, nil
		}
		rotation = true
	}
	if c.Ready == nil && !rotation {
		binding, err := c.Store.Enable(auth, definition.DefinitionID, definition.Version)
		if err == nil {
			return binding, nil
		}
	}
	if len(definition.Credentials) == 0 || onboarding.Locator == "" {
		if c.Ready != nil && len(definition.Credentials) == 0 {
			return c.Store.EnableReady(ctx, auth, definition.DefinitionID, definition.Version, c.Ready)
		}
		return api.ToolBinding{}, fmt.Errorf("%w: active owner connection and credential are required", api.ErrUnauthorized)
	}
	// A new onboarding for another version of the same connector replaces the
	// owner's credential revision. Creating a second active connection makes
	// Store.Enable correctly reject the ambiguous owner, so rotate the
	// existing connection instead.
	c.Store.mu.RLock()
	matches := c.Store.matchingOwnerConnectionsLocked(auth, definition)
	c.Store.mu.RUnlock()
	if len(matches) > 1 {
		return api.ToolBinding{}, fmt.Errorf("%w: ambiguous owner connection", api.ErrUnauthorized)
	}
	if len(matches) == 1 {
		// A reused credential keeps the broker grant minted for the binding it
		// was issued under. The grant pins the deterministic binding ID — which
		// includes the definition version — plus the admitting policy epoch, so
		// a version upgrade must re-mint it through the rotation path instead of
		// presenting a grant the new binding cannot acquire.
		if !rotation && matches[0].credential.Locator == onboarding.Locator && (!c.brokerCredentialRequired(definition, onboarding) || c.Store.brokerGrantReusable(auth, definition, matches[0])) {
			if c.Ready != nil {
				return c.Store.EnableReady(ctx, auth, definition.DefinitionID, definition.Version, c.Ready)
			}
			return c.Store.Enable(auth, definition.DefinitionID, definition.Version)
		}
		connection := matches[0].connection
		// A per-owner workload ID is stable across credential revisions. Stop
		// its old process before a new grant/credential can be admitted under it.
		if definition.Transport == api.ContainerMCP {
			if c.Release == nil {
				return api.ToolBinding{}, fmt.Errorf("%w: rotation requires workload release", api.ErrIsolation)
			}
			workloadID := api.WorkloadInstanceID(definition.DefinitionID, api.PerUser, auth.ContextID+":"+auth.PrincipalID+":"+connection.ConnectionID, "")
			if err := c.Release(ctx, workloadID); err != nil {
				return api.ToolBinding{}, err
			}
		}
		if c.brokerCredentialRequired(definition, onboarding) {
			if previous := matches[0].credential; previous.Backend == "credential-broker" && previous.BrokerGrantID != "" {
				control, err := c.Broker.New(auth, "broker:control")
				if err != nil {
					return api.ToolBinding{}, err
				}
				if err := control.Do(ctx, http.MethodPost, "/v1/grants/"+url.PathEscape(previous.BrokerGrantID)+"/revoke", nil, nil); err != nil {
					return api.ToolBinding{}, err
				}
			}
			next := connection.Revision + 1
			reference := api.CredentialReference{CredentialRefID: api.CredentialReferenceID(connection.ConnectionID, next), ConnectionID: connection.ConnectionID, Revision: next, Backend: "credential-broker", Locator: onboarding.Locator, Keys: credentialNames(onboarding), BrokerContractID: definition.CredentialContractID, BrokerContractRevision: definition.CredentialContractRevision, BrokerEnv: api.CloneMap(definition.CredentialContractEnv)}
			candidate := api.ToolBinding{Schema: api.SchemaVersion, PrincipalID: auth.PrincipalID, ContextID: auth.ContextID, RuntimeID: auth.RuntimeID, DefinitionID: definition.DefinitionID, DefinitionVersion: definition.Version, ConnectionID: connection.ConnectionID, ConnectionRevision: next, CredentialRefID: reference.CredentialRefID, CredentialRevision: next, PolicyVersion: auth.PolicyVersion, WorkloadClass: definition.Workload.Class, Status: api.ActiveStatus, Revision: 1, ProjectionRevision: 1}
			candidate.ToolBindingID = api.DeterministicBindingID(candidate.PrincipalID, candidate.ContextID, candidate.RuntimeID, candidate.DefinitionID, candidate.DefinitionVersion, candidate.ConnectionID, candidate.CredentialRefID)
			grantID, err := c.ensureBrokerGrant(ctx, auth, definition, onboarding, candidate)
			if err != nil {
				return api.ToolBinding{}, err
			}
			reference.BrokerGrantID = grantID
			reference, err = c.Store.RotateCredentialRecord(reference)
			if err != nil {
				return api.ToolBinding{}, err
			}
			if reference.Locator != onboarding.Locator {
				return api.ToolBinding{}, fmt.Errorf("%w: credential rotation", api.ErrConflict)
			}
		} else {
			reference, err := c.Store.RotateCredential(connection.ConnectionID, credstore.BackendLocal, onboarding.Locator, credentialNames(onboarding))
			if err != nil {
				return api.ToolBinding{}, err
			}
			if reference.Locator != onboarding.Locator {
				return api.ToolBinding{}, fmt.Errorf("%w: credential rotation", api.ErrConflict)
			}
		}
		if c.Ready != nil {
			return c.Store.EnableReady(ctx, auth, definition.DefinitionID, definition.Version, c.Ready)
		}
		return c.Store.Enable(auth, definition.DefinitionID, definition.Version)
	}
	// A sharable shared-class definition under an active shared-credential
	// policy binds the member to one connection owned by the policy's store
	// owner — every member then resolves the same workload.
	if definition.Workload.Class == api.Shared && definition.Workload.Sharable {
		c.Store.mu.RLock()
		policy := c.Store.sharedPolicyLocked(auth, definition.DefinitionID)
		c.Store.mu.RUnlock()
		if policy != nil {
			return c.materializeSharedBinding(ctx, auth, onboarding, definition, *policy)
		}
	}
	connectionID := api.DeterministicID("conn", auth.PrincipalID, definition.DefinitionID, onboarding.OnboardingID)
	reference := api.CredentialReference{Schema: api.SchemaVersion, CredentialRefID: api.CredentialReferenceID(connectionID, 1), ConnectionID: connectionID, Revision: 1, Backend: credstore.BackendLocal, Locator: onboarding.Locator, Keys: credentialNames(onboarding), Status: api.ActiveStatus}
	if c.brokerCredentialRequired(definition, onboarding) {
		reference.Backend = "credential-broker"
		reference.BrokerContractID = definition.CredentialContractID
		reference.BrokerContractRevision = definition.CredentialContractRevision
		reference.BrokerEnv = api.CloneMap(definition.CredentialContractEnv)
	}
	if reference.Backend == "credential-broker" {
		candidate := api.ToolBinding{Schema: api.SchemaVersion, PrincipalID: auth.PrincipalID, ContextID: auth.ContextID, RuntimeID: auth.RuntimeID, DefinitionID: definition.DefinitionID, DefinitionVersion: definition.Version, ConnectionID: connectionID, ConnectionRevision: 1, CredentialRefID: reference.CredentialRefID, CredentialRevision: 1, PolicyVersion: auth.PolicyVersion, WorkloadClass: definition.Workload.Class, Status: api.ActiveStatus, Revision: 1, ProjectionRevision: 1}
		candidate.ToolBindingID = api.DeterministicBindingID(candidate.PrincipalID, candidate.ContextID, candidate.RuntimeID, candidate.DefinitionID, candidate.DefinitionVersion, candidate.ConnectionID, candidate.CredentialRefID)
		grantID, err := c.ensureBrokerGrant(ctx, auth, definition, onboarding, candidate)
		if err != nil {
			return api.ToolBinding{}, err
		}
		reference.BrokerGrantID = grantID
	}
	if err := c.Store.PutCredentialReference(reference); err != nil {
		return api.ToolBinding{}, err
	}
	connection := api.Connection{Schema: api.SchemaVersion, ConnectionID: connectionID, Owner: api.OwnerRef{Type: api.PrincipalOwner, ID: auth.PrincipalID}, DefinitionID: definition.DefinitionID, CredentialRefID: reference.CredentialRefID, Revision: 1, Status: api.ActiveStatus}
	if onboarding.CredentialOwner != "" && onboarding.CredentialOwner != auth.PrincipalID {
		connection.Metadata = map[string]string{"credential_owner": onboarding.CredentialOwner}
	}
	if err := c.Store.PutConnection(connection); err != nil {
		return api.ToolBinding{}, err
	}
	if c.Ready != nil {
		return c.Store.EnableReady(ctx, auth, definition.DefinitionID, definition.Version, c.Ready)
	}
	return c.Store.Enable(auth, definition.DefinitionID, definition.Version)
}

// materializeSharedBinding records the policy's owner-scoped connection and
// credential reference — deterministic ids make every member's enable converge
// on the same records — then enables the member's own audited binding on it.
func (c *ControlPlane) materializeSharedBinding(ctx context.Context, auth identity.Envelope, onboarding api.Onboarding, definition api.ToolDefinition, policy api.SharedCredentialPolicy) (api.ToolBinding, error) {
	connectionID := api.DeterministicID("conn", "shared", policy.PolicyID, definition.DefinitionID, policy.Locator)
	reference := api.CredentialReference{Schema: api.SchemaVersion, CredentialRefID: api.CredentialReferenceID(connectionID, 1), ConnectionID: connectionID, Revision: 1, Backend: credstore.BackendLocal, Locator: policy.Locator, Keys: credentialNames(onboarding), Status: api.ActiveStatus}
	if err := c.Store.PutCredentialReference(reference); err != nil {
		return api.ToolBinding{}, err
	}
	connection := api.Connection{Schema: api.SchemaVersion, ConnectionID: connectionID, Owner: api.OwnerRef{Type: api.PrincipalOwner, ID: policy.StoreOwner}, DefinitionID: definition.DefinitionID, CredentialRefID: reference.CredentialRefID, Revision: 1, Status: api.ActiveStatus, Metadata: map[string]string{"credential_owner": policy.StoreOwner, "shared_policy": policy.PolicyID}}
	if err := c.Store.PutConnection(connection); err != nil {
		return api.ToolBinding{}, err
	}
	if c.Ready != nil {
		return c.Store.EnableReady(ctx, auth, definition.DefinitionID, definition.Version, c.Ready)
	}
	return c.Store.Enable(auth, definition.DefinitionID, definition.Version)
}

// registerAdmittedDefinition stores a credential-gate manifest. An immutable
// record that differs, or a user publication owned by someone else, takes the
// next patch version instead of replacing the existing publication.
func (c *ControlPlane) registerAdmittedDefinition(definition *api.ToolDefinition, principalID string) error {
	if c == nil || c.Store == nil || definition == nil {
		return fmt.Errorf("%w: admitted definition", api.ErrInvalid)
	}
	for i := 0; i < 100; i++ {
		stored, err := c.Store.Definition(definition.DefinitionID, definition.Version)
		if err != nil {
			break
		}
		owner, userOwned := c.Store.userPublicationOwner(definition.DefinitionID, definition.Version)
		if api.DefinitionsEqual(stored, *definition) && (!userOwned || owner == principalID) {
			break
		}
		definition.Version = nextPatchVersion(definition.Version)
	}
	if err := definition.Validate(); err != nil {
		return err
	}
	if err := c.Store.RegisterDefinition(*definition); err != nil {
		return err
	}
	owner, userOwned := c.Store.userPublicationOwner(definition.DefinitionID, definition.Version)
	if userOwned {
		if owner != principalID {
			return fmt.Errorf("%w: user definition owner", api.ErrUnauthorized)
		}
		return nil
	}
	if _, published := c.Store.storedPublication(definition.DefinitionID, definition.Version); published {
		return nil
	}
	return c.Store.PutPublication(api.DefinitionPublication{DefinitionID: definition.DefinitionID, Version: definition.Version, Visibility: api.PublicationUser, OwnerPrincipalID: principalID})
}

func (c *ControlPlane) finishAuthorization(ctx context.Context, onboardingID string) error {
	onboarding, err := c.Store.onboarding(onboardingID)
	if err != nil {
		return err
	}
	auth := onboardingEnvelope(onboarding)
	body, err := c.confirm(ctx, auth, map[string]any{"onboarding_id": onboardingID, "nonce": onboarding.ConfirmationNonce})
	if err != nil {
		return err
	}
	if body["phase"] == api.PhaseAwaitingOAuth {
		return nil
	}
	if _, err = c.enable(ctx, auth, map[string]any{"onboarding_id": onboardingID}); err != nil {
		return err
	}
	// The form submit drove this enable out-of-band; without a channel
	// notice the owner sees the form's success page but never the chat
	// confirmation, tools included.
	if latest, lerr := c.Store.onboarding(onboardingID); lerr == nil {
		api.PostPrepareNotice(ctx, auth, latest, "", len(latest.Permissions))
	}
	return nil
}

func onboardingEnvelope(onboarding api.Onboarding) identity.Envelope {
	return identity.Envelope{Schema: identity.Schema, PrincipalID: onboarding.PrincipalID, ExternalIdentityID: onboarding.PrincipalID, ContextID: onboarding.ContextID, RuntimeID: onboarding.RuntimeID, ConversationID: onboarding.PrincipalID, DeliveryTargetID: onboarding.PrincipalID, PolicyVersion: onboarding.PolicyVersion}
}

func (c *ControlPlane) StartOAuth(auth identity.Envelope, onboardingID, redirect string) (oauth.StartResult, error) {
	if c == nil || c.OAuth == nil {
		return oauth.StartResult{}, fmt.Errorf("%w: oauth broker", api.ErrInvalid)
	}
	onboarding, err := c.Store.OnboardingFor(auth, onboardingID)
	if err != nil {
		return oauth.StartResult{}, err
	}
	return c.OAuth.StartAuth(oauth.AuthRequest{Principal: auth.PrincipalID, Context: auth.ContextID, Connection: onboarding.OnboardingID, Provider: "fixture", Redirect: redirect})
}

func (c *ControlPlane) HandleOAuthCallback(auth identity.Envelope, onboardingID, state, code, redirect string) error {
	if c == nil || c.OAuth == nil {
		return fmt.Errorf("%w: oauth broker", api.ErrInvalid)
	}
	onboarding, err := c.Store.OnboardingFor(auth, onboardingID)
	if err != nil {
		return err
	}
	meta, err := c.OAuth.HandleCallback(auth.PrincipalID, auth.ContextID, onboarding.OnboardingID, state, code, redirect)
	if err != nil {
		return err
	}
	onboarding.Locator = meta.Locator
	onboarding.CredentialOwner = auth.PrincipalID
	onboarding.Phase = api.PhaseAwaitingConfirm
	onboarding.ConfirmationNonce = api.RandomNonce()
	onboarding.ConfirmationExpires = c.now().Add(c.ttl())
	onboarding.Revision++
	if err := c.Store.PutOnboarding(onboarding); err != nil {
		return err
	}
	return c.finishAuthorization(context.Background(), onboardingID)
}

func (c *ControlPlane) resolveOnboarding(auth identity.Envelope, args map[string]any) (api.Onboarding, error) {
	if id := api.ArgString(args, "onboarding_id"); id != "" {
		onboarding, err := c.Store.OnboardingFor(auth, id)
		if err != nil {
			return api.Onboarding{}, err
		}
		// Callers may hold a preparing stub that was superseded by the real
		// record (patch bump or a reviewed definition id): follow the pointer.
		if onboarding.Phase == api.PhaseRemoved && onboarding.SupersededBy != "" {
			if target, err := c.Store.OnboardingFor(auth, onboarding.SupersededBy); err == nil {
				return target, nil
			}
		}
		return onboarding, nil
	}
	operationID := api.ArgString(args, "operation_id")
	if operationID == "" {
		operationID = api.ArgString(args, "request_key")
	}
	if operationID != "" {
		if onboarding, ok := c.Store.FindOnboardingByKey(auth, operationID); ok {
			return onboarding, nil
		}
		return api.Onboarding{}, fmt.Errorf("%w: onboarding", api.ErrNotFound)
	}
	definitionID := api.ArgString(args, "definition_id")
	version := api.ArgString(args, "version")
	selector := api.ArgString(args, "selector")
	for _, alias := range []string{"name", "source"} {
		if selector == "" {
			selector = api.ArgString(args, alias)
		}
	}
	if definitionID == "" && selector == "" {
		return api.Onboarding{}, fmt.Errorf("%w: onboarding", api.ErrNotFound)
	}
	c.Store.mu.RLock()
	defer c.Store.mu.RUnlock()
	var matches []api.Onboarding
	for _, onboarding := range c.Store.onboardings {
		if onboarding.PrincipalID != auth.PrincipalID || onboarding.ContextID != auth.ContextID || onboarding.RuntimeID != auth.RuntimeID || onboarding.PolicyVersion != auth.PolicyVersion || onboarding.Phase == api.PhaseRemoved {
			continue
		}
		if selector != "" {
			// Conversational selector: the definition id, the exact source URL
			// or the repository basename all name this connector.
			if onboarding.DefinitionID != selector && onboarding.SourceURL != selector && sourceBasename(onboarding.SourceURL) != strings.ToLower(selector) {
				continue
			}
		} else if onboarding.DefinitionID != definitionID || version != "" && onboarding.DefinitionVersion != version {
			continue
		}
		matches = append(matches, onboarding)
	}
	switch len(matches) {
	case 0:
		return api.Onboarding{}, fmt.Errorf("%w: onboarding", api.ErrNotFound)
	case 1:
		return matches[0], nil
	}
	if selector != "" {
		// A conversational selector must never silently pick one record.
		candidates := make([]string, 0, len(matches))
		for _, onboarding := range matches {
			candidates = append(candidates, onboarding.OnboardingID+" ("+onboarding.DefinitionID+")")
		}
		return api.Onboarding{}, fmt.Errorf("%w: selector %q is ambiguous; candidates: %s", api.ErrInvalid, selector, strings.Join(candidates, ", "))
	}
	// The definition_id+version path historically resolves to the newest
	// matching record; keep that behavior for non-selector callers.
	found := matches[0]
	for _, onboarding := range matches[1:] {
		if onboarding.CreatedAt.After(found.CreatedAt) {
			found = onboarding
		}
	}
	return found, nil
}

// sourceBasename normalizes a repository URL to its lowercase final path
// segment, without a .git suffix — the friendly name a user would say.
func sourceBasename(sourceURL string) string {
	trimmed := strings.TrimSuffix(strings.TrimRight(sourceURL, "/"), ".git")
	if i := strings.LastIndex(trimmed, "/"); i >= 0 {
		trimmed = trimmed[i+1:]
	}
	return strings.ToLower(trimmed)
}

func (c *ControlPlane) definitionOf(onboarding api.Onboarding) (api.ToolDefinition, error) {
	if onboarding.Definition != nil {
		return *onboarding.Definition, nil
	}
	return c.Store.Definition(onboarding.DefinitionID, onboarding.DefinitionVersion)
}
