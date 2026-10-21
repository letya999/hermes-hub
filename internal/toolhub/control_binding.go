package toolhub

import (
	"context"
	"errors"
	"fmt"
	brokerclient "github.com/letya999/credential-broker/client"
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
		Required: credentialHints(definition), Optional: optionalCredentialHints(definition), Permissions: api.ToolNames(definition), Effects: api.EffectNames(definition),
		IdempotencyKey: requestKey, Revision: 1, CreatedAt: c.now(),
	}
	if len(onboarding.Required) > 0 {
		onboarding.Phase = api.PhaseAwaitingCreds
		onboarding.FormNonce = api.RandomNonce()
		onboarding.FormExpires = c.now().Add(c.ttl())
		c.armWizard(&onboarding)
	} else {
		onboarding.Phase = api.PhaseAwaitingConfirm
		onboarding.ConfirmationNonce = api.RandomNonce()
		onboarding.ConfirmationExpires = c.now().Add(c.ttl())
		c.armWizard(&onboarding)
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
		if input.PerRequest || !contractCoverable(input) {
			continue
		}
		declared[input.Name] = true
		if input.Required {
			required[input.Name] = true
		}
	}
	if len(declared) == 0 {
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
	// All-optional credentials must not bind an unrelated catalog contract;
	// they fall through to the synthesis fallback.
	for i, candidate := range contracts {
		if len(required) == 0 || definition.OAuth != nil && bound == "" {
			break
		}
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
	if best >= 0 {
		definition.CredentialContractID = contracts[best].ID
		definition.CredentialContractRevision = contracts[best].Revision
		definition.CredentialContractEnv = bestEnv
		return nil
	}
	// A prepared definition pins a reviewed contract that may be missing from
	// the broker catalog (fresh volume, ledger reset). Register the embedded
	// reviewed contract and re-run selection — synthesizing from discovered
	// credentials instead would overflow the broker's contract bounds on
	// noisy upstream metadata and lose the reviewed delivery mapping.
	if bound != "" && definition.CredentialContractRevision > 0 {
		if reviewed, ok, rerr := api.PreparedContractFor(bound, definition.CredentialContractRevision); rerr == nil && ok {
			if stored, rerr := control.RegisterContract(ctx, reviewed); rerr == nil {
				contracts = append(contracts, stored)
				for i, candidate := range contracts {
					if candidate.ID != bound || candidate.Revision != definition.CredentialContractRevision {
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
						if declared[target] && env[target] == "" {
							env[target] = target
						}
					}
					if definition.CredentialsSatisfied(func(name string) bool { return delivered[env[name]] }) {
						definition.CredentialContractID = contracts[i].ID
						definition.CredentialContractRevision = contracts[i].Revision
						definition.CredentialContractEnv = env
						return nil
					}
					break
				}
			}
		}
	}
	// Nothing in the catalog covers the declared credentials. A pinned
	// contract that exists but does not cover keeps the stored binding; every
	// other gap is healed by registering a contract synthesized from the
	// reviewed definition. The broker form stays the human consent point.
	for _, candidate := range contracts {
		if bound != "" && candidate.ID == bound && candidate.Revision == definition.CredentialContractRevision {
			// The pinned contract stays authoritative for what it delivers; a
			// discovered input it cannot cover (over-broad upstream examples,
			// placeholder heuristics) must not kill prepare before the form.
			// It demotes to optional and reaches the same form as an extra.
			delivered := map[string]bool{}
			for _, delivery := range candidate.Deliveries {
				target := delivery.Target
				if delivery.Type != "env" {
					target = delivery.EnvName
				}
				if target != "" {
					delivered[target] = true
				}
			}
			for i, input := range definition.Credentials {
				if !input.Required || input.PerRequest || !contractCoverable(input) {
					continue
				}
				if !delivered[definition.CredentialContractEnv[input.Name]] {
					definition.Credentials[i].Required = false
				}
			}
			return nil
		}
	}
	// Bounded-cli credentials use the local protected form, not the broker.
	if definition.Transport == api.BoundedCLI && bound == "" {
		return nil
	}
	// A shared definition without the Sharable flag cannot hold static
	// credentials at all — it is definition-invalid, so synthesis must not
	// rescue it. Sharable shared definitions get an allow_shared contract
	// for the same single form-collected credential.
	if definition.Workload.Class == api.Shared && bound == "" && !definition.Workload.Sharable {
		return nil
	}
	synthesized, err := synthesizeContract(*definition)
	if err != nil {
		return err
	}
	stored, err := control.RegisterContract(ctx, synthesized)
	if err != nil {
		return fmt.Errorf("register credential broker contract %s@%d: %w", synthesized.ID, synthesized.Revision, err)
	}
	definition.CredentialContractID = stored.ID
	definition.CredentialContractRevision = stored.Revision
	env := map[string]string{}
	for _, input := range definition.Credentials {
		if input.PerRequest {
			continue
		}
		if env[input.Name] == "" {
			env[input.Name] = input.Name
		}
	}
	definition.CredentialContractEnv = env
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

func (c *ControlPlane) cutAuthorization(onboarding api.Onboarding, purge bool) error {
	if err := c.revokeAccess(onboarding, api.RevokedStatus); err != nil {
		return err
	}
	// A member of a shared connection cuts only its own binding: revoking the
	// connection, its ciphertext or the shared workload is owner-only.
	if c.Store.sharedForeignConnection(onboarding.PrincipalID, onboarding.ConnectionID) {
		return nil
	}
	if err := c.revokeBrokerCredential(onboarding); err != nil {
		return err
	}
	if err := c.revokeLocalCredentialStatus(onboarding); err != nil {
		return err
	}
	return c.releaseBindingWorkloads(onboarding, purge)
}

// revokeAccess is the immediate cut: the binding leaves the projection and
// the connection drops to connStatus (deleting during remove, revoked for
// disable). A member of a shared connection touches only its own binding.
func (c *ControlPlane) revokeAccess(onboarding api.Onboarding, connStatus api.Status) error {
	if onboarding.BindingID != "" {
		if err := c.Store.SetBindingStatus(onboarding.BindingID, api.RevokedStatus); err != nil && !errors.Is(err, api.ErrRevoked) && !errors.Is(err, api.ErrNotFound) {
			return err
		}
	}
	if c.Store.sharedForeignConnection(onboarding.PrincipalID, onboarding.ConnectionID) {
		return nil
	}
	if onboarding.ConnectionID != "" {
		if err := c.Store.SetConnectionStatus(onboarding.ConnectionID, connStatus); err != nil && !errors.Is(err, api.ErrRevoked) && !errors.Is(err, api.ErrNotFound) {
			return err
		}
	}
	return nil
}

func (c *ControlPlane) revokeBrokerCredential(onboarding api.Onboarding) error {
	if c.Broker != nil && c.Broker.Enabled() && onboarding.BrokerCredentialID != "" {
		auth := identity.Envelope{Schema: identity.Schema, PrincipalID: onboarding.PrincipalID, ExternalIdentityID: onboarding.PrincipalID, ContextID: onboarding.ContextID, RuntimeID: onboarding.RuntimeID, ConversationID: onboarding.PrincipalID, DeliveryTargetID: onboarding.PrincipalID, PolicyVersion: onboarding.PolicyVersion}
		if control, err := c.brokerControl(auth, c.onboardingShared(onboarding)); err == nil {
			if err := control.Revoke(context.Background(), onboarding.BrokerCredentialID); err != nil && !brokerclient.IsNotFound(err) {
				return err
			}
		}
	}
	return nil
}

func (c *ControlPlane) revokeLocalCredentialStatus(onboarding api.Onboarding) error {
	if c.Secrets != nil && onboarding.Locator != "" {
		owner := onboarding.PrincipalID
		if onboarding.CredentialOwner != "" {
			owner = onboarding.CredentialOwner
		}
		// A broker-held locator owns no local ciphertext: not-found means the
		// revoke below (and the delete in the caller) is already a no-op.
		if err := c.Secrets.SetStatus(onboarding.Locator, owner, credstore.StatusRevoked); err != nil && !errors.Is(err, credstore.ErrNotFound) {
			return err
		}
	}
	return nil
}

// releaseBindingWorkloads stops the workload owned by the onboarding binding.
// Recorded workload instances cover every class; the deterministic recompute
// covers admissions whose record is gone (e.g. a store snapshot predating the
// record or a controller that kept the containers after losing its own map).
func (c *ControlPlane) releaseBindingWorkloads(onboarding api.Onboarding, purge bool) error {
	var firstErr error
	note := func(err error) {
		if err != nil && firstErr == nil {
			firstErr = err
		}
	}
	if c.CLIRelease != nil && onboarding.BindingID != "" && identity.ValidID(onboarding.PrincipalID) {
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		note(c.CLIRelease(ctx, api.CliReleaseRequest{PrincipalID: onboarding.PrincipalID, BindingID: onboarding.BindingID, Reason: "binding-release"}))
	}
	if c.Release == nil || onboarding.BindingID == "" {
		return firstErr
	}
	released := map[string]bool{}
	release := func(workloadID string) {
		if workloadID == "" || released[workloadID] {
			return
		}
		released[workloadID] = true
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		note(c.Release(ctx, api.ControllerRelease{WorkloadID: workloadID, Purge: purge}))
	}
	for _, workloadID := range c.Store.WorkloadIDsForBinding(onboarding.BindingID) {
		release(workloadID)
	}
	binding, err := c.Store.binding(onboarding.BindingID)
	if err != nil {
		return firstErr
	}
	definition, err := c.definitionOf(onboarding)
	if err != nil {
		return firstErr
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
	return firstErr
}

func (c *ControlPlane) materializeBinding(ctx context.Context, auth identity.Envelope, onboarding api.Onboarding, definition api.ToolDefinition) (api.ToolBinding, error) {
	rotation := onboarding.BrokerRotateCredentialID != ""
	if existing := c.Store.findBinding(auth, definition); existing != nil && existing.Status != api.RevokedStatus && !rotation {
		connection, ref, err := c.Store.OwnedConnection(auth, existing.ConnectionID)
		if onboarding.Locator == "" || err == nil && ref.Locator == onboarding.Locator && (!c.brokerCredentialRequired(definition, onboarding) || c.Store.brokerGrantReusable(auth, definition, ownerConnectionMatch{connection: connection, credential: ref})) {
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
	matches := c.Store.matchingOwnerConnectionsHealableLocked(auth, definition)
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
		// An OAuth rotation onto this same credential that already re-minted
		// a reusable grant is done: Broker keys token state by binding, so a
		// second rotation would strand the token the callback just wrote.
		if rotation && definition.OAuth != nil && onboarding.BrokerRotateCredentialID == onboarding.BrokerCredentialID && matches[0].credential.Locator == onboarding.Locator && c.Store.brokerGrantReusable(auth, definition, matches[0]) {
			rotation = false
		}
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
			if err := c.Release(ctx, api.ControllerRelease{WorkloadID: workloadID}); err != nil {
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
					// The broker may have dropped this grant already (ledger
					// reset or volume drift); absent is the post-revoke state.
					if !brokerclient.IsNotFound(err) {
						return api.ToolBinding{}, err
					}
				}
			}
			next := connection.Revision + 1
			reference := api.CredentialReference{CredentialRefID: api.CredentialReferenceID(connection.ConnectionID, next), ConnectionID: connection.ConnectionID, Revision: next, Backend: "credential-broker", Locator: onboarding.Locator, Keys: brokerCredentialKeys(definition, onboarding), BrokerContractID: definition.CredentialContractID, BrokerContractRevision: definition.CredentialContractRevision, BrokerEnv: api.CloneMap(definition.CredentialContractEnv)}
			candidate := api.ToolBinding{Schema: api.SchemaVersion, PrincipalID: auth.PrincipalID, ContextID: auth.ContextID, RuntimeID: auth.RuntimeID, DefinitionID: definition.DefinitionID, DefinitionVersion: definition.Version, ConnectionID: connection.ConnectionID, ConnectionRevision: next, CredentialRefID: reference.CredentialRefID, CredentialRevision: next, PolicyVersion: auth.PolicyVersion, WorkloadClass: definition.Workload.Class, Status: api.ActiveStatus, Revision: 1, ProjectionRevision: 1}
			candidate.ToolBindingID = api.DeterministicBindingID(candidate.PrincipalID, candidate.ContextID, candidate.RuntimeID, candidate.DefinitionID, candidate.DefinitionVersion, candidate.ConnectionID, candidate.CredentialRefID)
			grantID, err := c.ensureBrokerGrant(ctx, auth, definition, onboarding, candidate)
			if err != nil {
				return api.ToolBinding{}, err
			}
			reference.BrokerGrantID = grantID
			reference.BrokerGrantPolicy = auth.PolicyVersion
			reference.BrokerGrantBinding = candidate.ToolBindingID
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
		reference.Keys = brokerCredentialKeys(definition, onboarding)
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
		reference.BrokerGrantPolicy = auth.PolicyVersion
		reference.BrokerGrantBinding = candidate.ToolBindingID
	}
	if err := c.Store.PutCredentialReference(reference); err != nil {
		return api.ToolBinding{}, err
	}
	connection := api.Connection{Schema: api.SchemaVersion, ConnectionID: connectionID, Owner: api.OwnerRef{Type: api.PrincipalOwner, ID: auth.PrincipalID}, DefinitionID: definition.DefinitionID, CredentialRefID: reference.CredentialRefID, Revision: 1, Status: api.ActiveStatus}
	if onboarding.CredentialOwner != "" && onboarding.CredentialOwner != auth.PrincipalID {
		connection.Metadata = map[string]string{"credential_owner": onboarding.CredentialOwner}
	}
	if onboarding.OAuthProvider != "" {
		if connection.Metadata == nil {
			connection.Metadata = map[string]string{}
		}
		connection.Metadata["oauth_provider"] = onboarding.OAuthProvider
		connection.Metadata["oauth_connection"] = onboarding.OnboardingID
	}
	if onboarding.RecipeDigest != "" {
		if connection.Metadata == nil {
			connection.Metadata = map[string]string{}
		}
		connection.Metadata[api.LabelRecipe] = onboarding.RecipeDigest
		if resolution, err := c.Store.Recipe(onboarding.RecipeDigest); err == nil {
			if credDigest, err := api.CredentialRecipeDigest(resolution); err == nil && credDigest != "" {
				connection.Metadata[api.LabelCredentialRecipe] = credDigest
			}
		}
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
	if definition.Transport == api.RemoteMCP {
		// A shared remote connection may hold an OAuth-managed token set: the
		// marker lets the call path offer one bounded refresh-and-retry; the
		// locator itself carries provider and connection ids.
		connection.Metadata["oauth_provider"] = remoteOAuthProvider(definition)
	}
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
	// A drift-parked connection owes the human's re-pin before enable: the
	// just-consumed confirm nonce is the acceptance, so apply the observed
	// digests the reconciler recorded and return the connection to service.
	if onboarding.ConnectionID != "" {
		if connection, ok := c.Store.connectionFor(onboarding.ConnectionID); ok && connection.Status == api.DegradedStatus &&
			(connection.Metadata[api.LabelAcceptSurface] != "" || connection.Metadata[api.LabelAcceptRecipe] != "") {
			if err := c.Store.AcceptConnectionDrift(onboarding.ConnectionID); err != nil {
				return err
			}
		}
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

// completeDeviceAuth finishes an onboarding whose user authorized through
// RFC 8628 device flow: PollDevice already persisted the token, so the tail
// matches the authorization-code callback — locator on the onboarding, then
// the shared finishAuthorization path.
func (c *ControlPlane) completeDeviceAuth(ctx context.Context, onboarding api.Onboarding, meta oauth.TokenMeta) error {
	onboarding.Locator = meta.Locator
	onboarding.CredentialOwner = onboarding.PrincipalID
	onboarding.DeviceCode, onboarding.DeviceUserCode, onboarding.DeviceVerification = "", "", ""
	onboarding.Phase = api.PhaseAwaitingConfirm
	onboarding.ConfirmationNonce = api.RandomNonce()
	onboarding.ConfirmationExpires = c.now().Add(c.ttl())
	onboarding.Revision++
	if err := c.Store.PutOnboarding(onboarding); err != nil {
		return err
	}
	return c.finishAuthorization(ctx, onboarding.OnboardingID)
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
