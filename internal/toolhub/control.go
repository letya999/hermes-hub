package toolhub

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"log"
	"maps"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"

	brokerv1 "github.com/letya999/credential-broker/api/v1"
	"github.com/letya999/hermes-hub/internal/credentialbroker"
	"github.com/letya999/hermes-hub/internal/credstore"
	"github.com/letya999/hermes-hub/internal/identity"
	"github.com/letya999/hermes-hub/internal/oauth"
)

type SourceReview struct {
	Definition   ToolDefinition
	Permissions  []string
	Effects      []string
	ReviewDigest string
	Recipe       *RecipeResolution
}

type SourceReviewer func(context.Context, ArtifactSource, *RecipeCandidate) (SourceReview, error)
type SourceResolver func(context.Context, string) (ArtifactSource, error)

type ControlPlane struct {
	Store            *Store
	Secrets          credstore.Backend
	Reviewer         SourceReviewer
	SourceResolver   SourceResolver
	RecipeCatalogs   []RecipeCatalog
	OAuth            *oauth.Broker
	Injector         CredentialInjector
	oauthMu          sync.Mutex
	oauthFlows       map[string]*preparedOAuthFlow
	Now              func() time.Time
	Listen           string
	FormOrigin       string
	WorkloadRoot     string
	ConfirmationTTL  time.Duration
	Ready            func(context.Context, EffectiveBinding) error
	Broker           *credentialbroker.Config
	discoveryMu      sync.Mutex
	discoveryChoices map[string]discoverySelection
	// Release asks the workload controller to stop a running workload. Revoke
	// and remove use it so a cut connector does not keep a materialized
	// credential alive in a running container until the idle TTL fires.
	Release func(context.Context, string) error
	// PrepareSyncWindow bounds how long prepare_source waits for review+build
	// before returning the durable "preparing" record; the work then continues
	// on a detached context and its result is read via status. Zero uses the
	// default; negative runs the whole prepare synchronously (tests).
	PrepareSyncWindow time.Duration
	// PrepareDone, when set, runs after a backgrounded prepare finishes —
	// whatever the outcome — so transports can wake open sessions.
	PrepareDone func(context.Context, identity.Envelope, Onboarding)
}

func (c *ControlPlane) now() time.Time {
	if c != nil && c.Now != nil {
		return c.Now().UTC()
	}
	return time.Now().UTC()
}

func (c *ControlPlane) ttl() time.Duration {
	if c != nil && c.ConfirmationTTL > 0 {
		return c.ConfirmationTTL
	}
	return 10 * time.Minute
}

func (c *ControlPlane) origin() string {
	if c != nil && c.FormOrigin != "" {
		return strings.TrimRight(c.FormOrigin, "/")
	}
	if c != nil && c.Listen != "" {
		if strings.Contains(c.Listen, "://") {
			return strings.TrimRight(c.Listen, "/")
		}
		return "http://" + c.Listen
	}
	return "http://127.0.0.1"
}

func (c *ControlPlane) Invoke(ctx context.Context, auth identity.Envelope, op string, args map[string]any) (map[string]any, error) {
	if c == nil || c.Store == nil {
		return nil, fmt.Errorf("%w: control plane", ErrInvalid)
	}
	if err := RejectAuthorityArguments(args); err != nil {
		return nil, err
	}
	if err := auth.Validate(auth.PrincipalID, auth.ContextID, auth.RuntimeID, auth.PolicyVersion); err != nil {
		return nil, fmt.Errorf("%w: %v", ErrUnauthorized, err)
	}
	switch op {
	case "discover":
		return c.discover(ctx, auth, argString(args, "query"))
	case "prepare_source":
		return c.prepareSource(ctx, auth, args)
	case "status":
		return c.status(auth, args)
	case "required_credentials":
		return c.requiredCredentials(auth, args)
	case "confirm":
		return c.confirm(ctx, auth, args)
	case "enable":
		return c.enable(ctx, auth, args)
	case "rotate":
		return c.Rotate(auth, args)
	case "disable":
		return c.setPhase(auth, args, DisabledStatus, PhaseDisabled)
	case "revoke":
		return c.revoke(auth, args)
	case "remove":
		return c.remove(auth, args)
	default:
		return nil, fmt.Errorf("%w: unknown control operation", ErrInvalid)
	}
}

func (c *ControlPlane) prepareSource(ctx context.Context, auth identity.Envelope, args map[string]any) (map[string]any, error) {
	requestKey := argString(args, "request_key")
	if requestKey != "" {
		if existing, ok := c.Store.FindOnboardingByKey(auth, requestKey); ok && existing.Phase != PhaseFailed && existing.Phase != PhaseRemoved {
			if err := c.regenerateBrokerRequest(ctx, auth, &existing); err != nil {
				return nil, err
			}
			if err := c.refreshBrokerRequest(ctx, auth, &existing); err != nil {
				return nil, err
			}
			if err := c.refreshCredentialForm(&existing); err != nil {
				return nil, err
			}
			if err := c.refreshConfirmation(&existing); err != nil {
				return nil, err
			}
			return c.statusBody(existing, false), nil
		}
	}
	source := argString(args, "source")
	definitionID := argString(args, "definition_id")
	version := argString(args, "version")
	var selected *RecipeCandidate
	if candidate := argString(args, "candidate_id"); candidate != "" {
		if source != "" || definitionID != "" || version != "" {
			return nil, fmt.Errorf("%w: select one source", ErrInvalid)
		}
		choice, err := c.selectedCandidate(auth, candidate)
		if err != nil {
			return nil, err
		}
		source = choice.sourceURL()
		if choice.Source.CommitSHA == "" && choice.Source.Subfolder != "" {
			if c.SourceResolver == nil {
				return nil, fmt.Errorf("%w: source resolver unavailable", ErrInvalid)
			}
			pinned, err := c.SourceResolver(ctx, choice.Source.Repository)
			if err != nil {
				return nil, err
			}
			if pinned.Repository != choice.Source.Repository || pinned.Subfolder != "" {
				return nil, fmt.Errorf("%w: source resolution drift", ErrStale)
			}
			pinned.Subfolder = choice.Source.Subfolder
			if _, err := pinned.ArchiveURL(); err != nil {
				return nil, err
			}
			source = pinned.Repository + "/tree/" + pinned.CommitSHA + "/" + pinned.Subfolder
		}
		selected = choice.recipe
	}
	if source != "" {
		return c.prepareSelfInstallAsync(ctx, auth, source, requestKey, selected)
	}
	if definitionID != "" && version != "" {
		return c.prepareCatalog(ctx, auth, definitionID, version, requestKey)
	}
	return nil, fmt.Errorf("%w: source or definition required", ErrInvalid)
}

func (c *ControlPlane) prepareCatalog(ctx context.Context, auth identity.Envelope, definitionID, version, requestKey string) (map[string]any, error) {
	definition, err := c.Store.Definition(definitionID, version)
	if err != nil {
		return nil, err
	}
	if err := c.Store.RequireCatalogAccess(auth, definition); err != nil {
		return nil, err
	}
	onboarding, err := c.newOnboarding(auth, OnboardingCatalog, requestKey, definition, "", "")
	if err != nil {
		return nil, err
	}
	if err := c.ensureBrokerRequest(ctx, auth, &onboarding, definition); err != nil {
		return nil, err
	}
	return c.statusBody(onboarding, false), nil
}

// prepareWindow is how long a prepare_source call waits for review+build
// before handing the caller the durable "preparing" record.
func (c *ControlPlane) prepareWindow() time.Duration {
	if c == nil || c.PrepareSyncWindow == 0 {
		return 90 * time.Second
	}
	return c.PrepareSyncWindow
}

func (c *ControlPlane) prepareSelfInstall(ctx context.Context, auth identity.Envelope, sourceURL, requestKey string, selected *RecipeCandidate) (map[string]any, error) {
	source, config, err := c.resolveSelfInstallSource(ctx, auth, sourceURL)
	if err != nil {
		return nil, err
	}
	preparing, inFlight, err := c.ensurePreparing(auth, source, config, requestKey)
	if err != nil {
		return nil, err
	}
	if inFlight {
		return c.statusBody(preparing, false), nil
	}
	return c.finishSelfInstall(ctx, auth, preparing, source, requestKey, selected)
}

// prepareSelfInstallAsync runs review+build under a detached context: a slow or
// lost HTTP response no longer discards the work. The caller waits
// PrepareSyncWindow for a completed result, then receives the durable
// "preparing" record — the outcome is read via status afterwards.
func (c *ControlPlane) prepareSelfInstallAsync(ctx context.Context, auth identity.Envelope, sourceURL, requestKey string, selected *RecipeCandidate) (map[string]any, error) {
	if window := c.prepareWindow(); window < 0 {
		return c.prepareSelfInstall(ctx, auth, sourceURL, requestKey, selected)
	}
	source, config, err := c.resolveSelfInstallSource(ctx, auth, sourceURL)
	if err != nil {
		return nil, err
	}
	preparing, inFlight, err := c.ensurePreparing(auth, source, config, requestKey)
	if err != nil {
		return nil, err
	}
	if inFlight {
		return c.statusBody(preparing, false), nil
	}
	type outcome struct {
		body map[string]any
		err  error
	}
	done := make(chan outcome, 1)
	background, stop := context.WithTimeout(context.WithoutCancel(ctx), 35*time.Minute)
	go func() {
		defer stop()
		body, err := c.finishSelfInstall(background, auth, preparing, source, requestKey, selected)
		latest := c.latestPrepareRecord(auth, preparing)
		log.Printf("toolhub prepare done: onboarding=%s phase=%s err=%v", latest.OnboardingID, latest.Phase, err)
		if c.PrepareDone != nil {
			c.PrepareDone(background, auth, latest)
		}
		done <- outcome{body, err}
	}()
	timer := time.NewTimer(c.prepareWindow())
	defer timer.Stop()
	select {
	case result := <-done:
		return result.body, result.err
	case <-ctx.Done():
	case <-timer.C:
	}
	log.Printf("toolhub prepare_source: onboarding=%s still preparing; outcome continues in background", preparing.OnboardingID)
	return c.statusBody(preparing, false), nil
}

func (c *ControlPlane) resolveSelfInstallSource(ctx context.Context, auth identity.Envelope, sourceURL string) (ArtifactSource, ArtifactImportConfig, error) {
	if err := c.Store.RequireSelfInstall(auth); err != nil {
		return ArtifactSource{}, ArtifactImportConfig{}, err
	}
	source, err := ParseGitHubSource(sourceURL)
	unpinned := err != nil
	if unpinned && c.SourceResolver != nil {
		source, err = c.SourceResolver(ctx, sourceURL)
	}
	if err != nil {
		return ArtifactSource{}, ArtifactImportConfig{}, err
	}
	// A bare URL means "this repository", not mutable HEAD: with a reviewed
	// prepared entry its pinned commit is the source, never drifting code.
	if unpinned {
		if prepared, ok, perr := preparedForRepository(source.Repository, source.Subfolder); perr == nil && ok {
			source = prepared.Source
		}
	}
	return source, defaultSelfInstallConfig(source), nil
}

// ensurePreparing registers a preparing record before review+build so status
// works mid-flight and a failed prepare leaves a durable failure instead of
// "record not found". A fresh record already in preparing means an identical
// prepare is running — callers must not spawn a second review+build. A stub
// older than the background deadline belongs to a crashed prepare and is
// overwritten to retry.
func (c *ControlPlane) ensurePreparing(auth identity.Envelope, source ArtifactSource, config ArtifactImportConfig, requestKey string) (Onboarding, bool, error) {
	idSeed := requestKey
	if idSeed == "" {
		idSeed = string(OnboardingSelfInstall) + ":" + config.DefinitionID + "@" + config.Version + ":" + source.Repository + ":" + source.CommitSHA
	}
	preparing := Onboarding{
		Schema: SchemaVersion, OnboardingID: deterministicID("onboard", auth.PrincipalID, auth.ContextID, auth.RuntimeID, idSeed),
		PrincipalID: auth.PrincipalID, ContextID: auth.ContextID, RuntimeID: auth.RuntimeID, PolicyVersion: auth.PolicyVersion,
		Mode: OnboardingSelfInstall, Phase: PhasePreparing, SourceURL: source.Repository, CommitSHA: source.CommitSHA,
		DefinitionID: config.DefinitionID, DefinitionVersion: config.Version,
		IdempotencyKey: requestKey, Revision: 1, CreatedAt: c.now(),
	}
	return c.Store.ClaimPreparingOnboarding(preparing, 35*time.Minute, c.now())
}

// latestPrepareRecord resolves the onboarding a backgrounded prepare actually
// produced: normally the stub's own record, but a patch bump can move the
// result to a sibling id. Best effort for the wake notification.
func (c *ControlPlane) latestPrepareRecord(auth identity.Envelope, stub Onboarding) Onboarding {
	if current, err := c.Store.onboarding(stub.OnboardingID); err == nil && current.Phase != PhaseRemoved {
		return current
	}
	c.Store.mu.RLock()
	defer c.Store.mu.RUnlock()
	latest := stub
	for _, candidate := range c.Store.onboardings {
		if candidate.PrincipalID != auth.PrincipalID || candidate.ContextID != auth.ContextID || candidate.RuntimeID != auth.RuntimeID || candidate.PolicyVersion != auth.PolicyVersion || candidate.Phase == PhaseRemoved {
			continue
		}
		same := stub.IdempotencyKey != "" && candidate.IdempotencyKey == stub.IdempotencyKey
		if !same && stub.IdempotencyKey == "" {
			same = candidate.Mode == stub.Mode && candidate.SourceURL == stub.SourceURL && candidate.CommitSHA == stub.CommitSHA
		}
		if same && candidate.CreatedAt.After(latest.CreatedAt) {
			latest = candidate
		}
	}
	return latest
}

func (c *ControlPlane) finishSelfInstall(ctx context.Context, auth identity.Envelope, preparing Onboarding, source ArtifactSource, requestKey string, selected *RecipeCandidate) (map[string]any, error) {
	failPrepare := func(err error) (map[string]any, error) {
		if latest, latestErr := c.Store.onboarding(preparing.OnboardingID); latestErr == nil && latest.Phase == PhasePreparing {
			latest.Phase = PhaseFailed
			_ = c.Store.PutOnboarding(latest)
		}
		return nil, err
	}
	review := SourceReview{}
	var err error
	if existing, ok := c.Store.reusableSelfInstallDefinition(auth, source, preparing.DefinitionID, preparing.DefinitionVersion); selected == nil && ok && c.selfInstallUsable(existing) && completeToolSchemas(existing) {
		review = SourceReview{Definition: existing, Permissions: toolNames(existing), Effects: effectNames(existing), ReviewDigest: existing.Source.ReviewDigest}
	} else {
		if c.Reviewer == nil {
			return failPrepare(fmt.Errorf("%w: source reviewer unavailable", ErrInvalid))
		}
		review, err = c.Reviewer(ctx, source, selected)
		if err != nil {
			return failPrepare(err)
		}
	}
	if err := c.bindReviewedContract(ctx, auth, &review.Definition); err != nil {
		return failPrepare(err)
	}
	if !c.selfInstallUsable(review.Definition) {
		return failPrepare(fmt.Errorf("%w: no reviewed credential broker contract covers %s credentials", ErrUnauthorized, review.Definition.DefinitionID))
	}
	// The produced definition may differ from an immutable record stored under
	// the same id+version by an older pipeline (for example, without a broker
	// contract binding). Bump the patch component until the key is free or the
	// stored record is identical.
	for i := 0; i < 100; i++ {
		stored, lookupErr := c.Store.Definition(review.Definition.DefinitionID, review.Definition.Version)
		if lookupErr != nil || definitionsEqual(stored, review.Definition) {
			break
		}
		review.Definition.Version = nextPatchVersion(review.Definition.Version)
	}
	if err := review.Definition.Validate(); err != nil {
		return failPrepare(err)
	}
	if err := c.Store.RegisterDefinition(review.Definition); err != nil {
		return failPrepare(err)
	}
	if err := c.Store.PutPublication(DefinitionPublication{DefinitionID: review.Definition.DefinitionID, Version: review.Definition.Version, Visibility: PublicationUser, OwnerPrincipalID: auth.PrincipalID}); err != nil {
		return failPrepare(err)
	}
	onboarding, err := c.newOnboarding(auth, OnboardingSelfInstall, requestKey, review.Definition, source.Repository, source.CommitSHA)
	if err != nil {
		return failPrepare(err)
	}
	if onboarding.OnboardingID != preparing.OnboardingID {
		// A patch bump or a reviewed definition id shifted the deterministic
		// key; callers holding the stub id follow SupersededBy to the result.
		preparing.Phase = PhaseRemoved
		preparing.SupersededBy = onboarding.OnboardingID
		_ = c.Store.PutOnboarding(preparing)
	}
	onboarding.Permissions = append([]string(nil), review.Permissions...)
	onboarding.Effects = append([]string(nil), review.Effects...)
	onboarding.ReviewDigest = review.ReviewDigest
	onboarding.Subfolder = source.Subfolder
	if review.Recipe != nil {
		copyRecipe := *review.Recipe
		onboarding.Recipe = &copyRecipe
		onboarding.Required = credentialHintsForRecipe(review.Definition, review.Recipe.Connection)
	}
	copyDef := review.Definition
	onboarding.Definition = &copyDef
	if err := c.Store.PutOnboarding(onboarding); err != nil {
		return failPrepare(err)
	}
	if err := c.ensureBrokerRequest(ctx, auth, &onboarding, review.Definition); err != nil {
		return failPrepare(err)
	}
	return c.statusBody(onboarding, false), nil
}

func (c *ControlPlane) newOnboarding(auth identity.Envelope, mode, requestKey string, definition ToolDefinition, sourceURL, commit string) (Onboarding, error) {
	idSeed := requestKey
	if idSeed == "" {
		idSeed = mode + ":" + definition.DefinitionID + "@" + definition.Version + ":" + sourceURL + ":" + commit
	}
	onboarding := Onboarding{
		Schema: SchemaVersion, OnboardingID: deterministicID("onboard", auth.PrincipalID, auth.ContextID, auth.RuntimeID, idSeed),
		PrincipalID: auth.PrincipalID, ContextID: auth.ContextID, RuntimeID: auth.RuntimeID, PolicyVersion: auth.PolicyVersion,
		Mode: mode, SourceURL: sourceURL, CommitSHA: commit, DefinitionID: definition.DefinitionID, DefinitionVersion: definition.Version,
		Required: credentialHints(definition), Permissions: toolNames(definition), Effects: effectNames(definition),
		IdempotencyKey: requestKey, Revision: 1, CreatedAt: c.now(),
	}
	if len(onboarding.Required) > 0 {
		onboarding.Phase = PhaseAwaitingCreds
		onboarding.FormNonce = randomNonce()
		onboarding.FormExpires = c.now().Add(c.ttl())
	} else {
		onboarding.Phase = PhaseAwaitingConfirm
		onboarding.ConfirmationNonce = randomNonce()
		onboarding.ConfirmationExpires = c.now().Add(c.ttl())
	}
	if err := c.Store.PutOnboarding(onboarding); err != nil {
		return Onboarding{}, err
	}
	return onboarding, nil
}

// selfInstallUsable reports whether a stored definition can proceed past the
// broker-contract gate, so stale records predating contract binding are not
// reused and re-registered into an eternal failure.
func completeToolSchemas(definition ToolDefinition) bool {
	if definition.Source.ToolContractSource != ToolContractPreflight {
		return true
	}
	for _, tool := range definition.Tools {
		if len(tool.InputSchema) == 0 {
			return false
		}
	}
	return true
}

func (c *ControlPlane) selfInstallUsable(definition ToolDefinition) bool {
	entry, matched, err := preparedForSource(ArtifactSource{Repository: definition.Source.Repository, CommitSHA: definition.Source.CommitSHA, Subfolder: definition.Source.Subfolder})
	if err != nil {
		return false
	}
	if matched && (definition.Workload.Stateful != entry.Stateful || !maps.Equal(definition.RuntimeEnvironment, entry.RuntimeEnvironment) || definition.CredentialContractID != entry.ContractID || definition.CredentialContractRevision != entry.ContractRevision) {
		return false
	}
	return c == nil || c.Broker == nil || !c.Broker.Enabled() || len(definition.Credentials) == 0 || definition.CredentialContractID != ""
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
func (c *ControlPlane) bindReviewedContract(ctx context.Context, auth identity.Envelope, definition *ToolDefinition) error {
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
		if definition.Workload.Class == Shared && !candidate.AllowShared {
			continue
		}
		env := cloneMap(definition.CredentialContractEnv)
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
		covered := 0
		for name := range required {
			if delivered[env[name]] {
				covered++
			}
		}
		if covered != len(required) {
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

func (c *ControlPlane) ensureBrokerRequest(ctx context.Context, auth identity.Envelope, onboarding *Onboarding, definition ToolDefinition) error {
	if c == nil || c.Broker == nil || !c.Broker.Enabled() || len(definition.Credentials) == 0 {
		return nil
	}
	if definition.CredentialContractID == "" || definition.CredentialContractRevision < 1 || len(definition.CredentialContractEnv) == 0 {
		return fmt.Errorf("%w: reviewed credential broker contract is required for %s", ErrUnauthorized, definition.DefinitionID)
	}
	if onboarding.BrokerRequestID == "" && onboarding.BrokerRotateCredentialID == "" {
		if onboarding.BrokerCredentialID != "" {
			return nil
		}
		c.Store.mu.RLock()
		matches := c.Store.matchingOwnerConnectionsLocked(auth, definition)
		c.Store.mu.RUnlock()
		if len(matches) > 1 {
			return fmt.Errorf("%w: ambiguous owner connection", ErrUnauthorized)
		}
		if len(matches) == 1 {
			ref := matches[0].credential
			if ref.Backend == "credential-broker" && ref.BrokerContractID == definition.CredentialContractID && ref.BrokerContractRevision == definition.CredentialContractRevision {
				onboarding.Locator, onboarding.BrokerCredentialID = ref.Locator, ref.Locator
				onboarding.BrokerContractID, onboarding.BrokerContractRevision = ref.BrokerContractID, ref.BrokerContractRevision
				onboarding.Phase = PhaseAwaitingConfirm
				onboarding.FormNonce = ""
				onboarding.FormExpires = time.Time{}
				onboarding.ConfirmationNonce = randomNonce()
				onboarding.ConfirmationExpires = c.now().Add(c.ttl())
				onboarding.Revision++
				return c.Store.PutOnboarding(*onboarding)
			}
		}
	}
	control, err := c.Broker.New(auth, "broker:control")
	if err != nil {
		return err
	}
	if onboarding.BrokerRequestID != "" {
		// A dead link must not pin the onboarding forever: expired or canceled
		// requests are replaced by a fresh one under a new idempotency key.
		request, err := control.Request(ctx, onboarding.BrokerRequestID)
		if err != nil {
			return err
		}
		if request.Status == "expired" || request.Status == "canceled" {
			onboarding.BrokerRequestID = ""
			onboarding.BrokerAuthorizationURL = ""
			onboarding.BrokerAttempts++
		}
	}
	if onboarding.BrokerRequestID == "" {
		ownerKind := "user"
		if definition.Workload.Class == Shared {
			ownerKind = "context"
		}
		connectionID := deterministicID("conn", auth.PrincipalID, definition.DefinitionID, onboarding.OnboardingID)
		if onboarding.BrokerRotateCredentialID != "" {
			var credential brokerv1.Credential
			if err := control.Do(ctx, http.MethodGet, "/v1/credentials/"+url.PathEscape(onboarding.BrokerRotateCredentialID), nil, &credential); err != nil {
				return err
			}
			connectionID = credential.ConnectionID
		}
		request, err := control.CreateRequest(ctx, brokerv1.CreateRequest{
			ContractID: definition.CredentialContractID, ContractRevision: definition.CredentialContractRevision,
			ConnectionID: connectionID, RotateCredentialID: onboarding.BrokerRotateCredentialID,
			OnboardingID: onboarding.OnboardingID, IdempotencyKey: fmt.Sprintf("%s/broker-%d", onboarding.OnboardingID, onboarding.BrokerAttempts),
			OwnerKind: ownerKind,
		})
		if err != nil {
			return err
		}
		onboarding.BrokerRequestID = request.ID
		onboarding.BrokerAuthorizationURL = request.AuthorizationURL
		onboarding.BrokerContractID = request.ContractID
		onboarding.BrokerContractRevision = request.ContractRevision
		onboarding.FormNonce = ""
		onboarding.FormExpires = time.Time{}
		onboarding.Revision++
		if err := c.Store.PutOnboarding(*onboarding); err != nil {
			return err
		}
	}
	return c.refreshBrokerRequest(ctx, auth, onboarding)
}

func (c *ControlPlane) refreshBrokerRequest(ctx context.Context, auth identity.Envelope, onboarding *Onboarding) error {
	if c == nil || c.Broker == nil || !c.Broker.Enabled() || onboarding == nil || onboarding.BrokerRequestID == "" {
		return nil
	}
	control, err := c.Broker.New(auth, "broker:control")
	if err != nil {
		return err
	}
	request, err := control.Request(ctx, onboarding.BrokerRequestID)
	if err != nil {
		return err
	}
	onboarding.BrokerAuthorizationURL = request.AuthorizationURL
	onboarding.BrokerContractID = request.ContractID
	onboarding.BrokerContractRevision = request.ContractRevision
	changed := false
	if request.Status == "ready" && request.CredentialID != "" {
		if onboarding.BrokerCredentialID != request.CredentialID || onboarding.Locator != request.CredentialID || onboarding.Phase == PhaseAwaitingCreds {
			onboarding.BrokerCredentialID = request.CredentialID
			onboarding.Locator = request.CredentialID
			onboarding.Phase = PhaseAwaitingConfirm
			onboarding.FormNonce = ""
			onboarding.FormExpires = time.Time{}
			onboarding.ConfirmationNonce = randomNonce()
			onboarding.ConfirmationExpires = c.now().Add(c.ttl())
			onboarding.ConfirmationUsed = false
			changed = true
		}
	}
	if changed {
		onboarding.Revision++
		return c.Store.PutOnboarding(*onboarding)
	}
	return nil
}

func (c *ControlPlane) status(auth identity.Envelope, args map[string]any) (map[string]any, error) {
	if argString(args, "onboarding_id") == "" && argString(args, "definition_id") == "" {
		c.Store.mu.RLock()
		var latest Onboarding
		for _, candidate := range c.Store.onboardings {
			if candidate.PrincipalID == auth.PrincipalID && candidate.ContextID == auth.ContextID && candidate.RuntimeID == auth.RuntimeID && candidate.PolicyVersion == auth.PolicyVersion && candidate.Phase != PhaseRemoved && (latest.OnboardingID == "" || candidate.CreatedAt.After(latest.CreatedAt)) {
				latest = candidate
			}
		}
		c.Store.mu.RUnlock()
		if latest.OnboardingID == "" {
			return nil, fmt.Errorf("%w: onboarding", ErrNotFound)
		}
		args = map[string]any{"onboarding_id": latest.OnboardingID}
	}
	onboarding, err := c.resolveOnboarding(auth, args)
	if err != nil {
		return nil, err
	}
	if onboarding.Phase == PhaseAwaitingOAuth {
		definition, err := c.definitionOf(onboarding)
		if err != nil {
			return nil, err
		}
		return c.ensurePreparedOAuth(context.Background(), auth, onboarding, definition)
	}
	if err := c.regenerateBrokerRequest(context.Background(), auth, &onboarding); err != nil {
		return nil, err
	}
	if err := c.refreshBrokerRequest(context.Background(), auth, &onboarding); err != nil {
		return nil, err
	}
	if err := c.refreshConfirmation(&onboarding); err != nil {
		return nil, err
	}
	return c.statusBody(onboarding, false), nil
}

// regenerateBrokerRequest replaces an expired or canceled broker request while
// the onboarding still waits for credentials, so a dead connect link heals on
// the next status poll instead of pinning the onboarding forever.
func (c *ControlPlane) regenerateBrokerRequest(ctx context.Context, auth identity.Envelope, onboarding *Onboarding) error {
	if onboarding == nil || onboarding.Phase != PhaseAwaitingCreds {
		return nil
	}
	definition, err := c.definitionOf(*onboarding)
	if err != nil {
		return err
	}
	return c.ensureBrokerRequest(ctx, auth, onboarding, definition)
}

func (c *ControlPlane) requiredCredentials(auth identity.Envelope, args map[string]any) (map[string]any, error) {
	onboarding, err := c.resolveOnboarding(auth, args)
	if err != nil {
		return nil, err
	}
	if err := c.regenerateBrokerRequest(context.Background(), auth, &onboarding); err != nil {
		return nil, err
	}
	if err := c.refreshBrokerRequest(context.Background(), auth, &onboarding); err != nil {
		return nil, err
	}
	if err := c.refreshCredentialForm(&onboarding); err != nil {
		return nil, err
	}
	body := c.statusBody(onboarding, true)
	if onboarding.Phase == PhaseAwaitingCreds && onboarding.FormNonce != "" && c.now().Before(onboarding.FormExpires) {
		body["input"] = "localhost-form"
		body["form_url"] = c.origin() + "/credentials/" + onboarding.OnboardingID + "?nonce=" + url.QueryEscape(onboarding.FormNonce)
	}
	if onboarding.BrokerRequestID != "" && onboarding.BrokerAuthorizationURL != "" && onboarding.Phase == PhaseAwaitingCreds {
		body["input"] = "credential-broker"
		body["broker_request_id"] = onboarding.BrokerRequestID
		body["authorization_url"] = onboarding.BrokerAuthorizationURL
		delete(body, "form_url")
		delete(body, "form_path")
	}
	if c.OAuth != nil {
		body["oauth"] = "pkce"
	}
	return body, nil
}

// refreshCredentialForm makes an idempotent resume useful after the original
// loopback link expires. The old nonce is replaced, so an old URL cannot be
// replayed while the same request key still identifies the onboarding.
func (c *ControlPlane) refreshCredentialForm(onboarding *Onboarding) error {
	if onboarding == nil || onboarding.Phase != PhaseAwaitingCreds {
		return nil
	}
	now := c.now()
	if onboarding.FormNonce != "" && now.Before(onboarding.FormExpires) {
		return nil
	}
	onboarding.FormNonce = randomNonce()
	onboarding.FormExpires = now.Add(c.ttl())
	onboarding.Revision++
	return c.Store.PutOnboarding(*onboarding)
}

func (c *ControlPlane) refreshConfirmation(onboarding *Onboarding) error {
	if onboarding == nil || onboarding.Phase != PhaseAwaitingConfirm || (onboarding.ConfirmationNonce != "" && c.now().Before(onboarding.ConfirmationExpires)) {
		return nil
	}
	onboarding.ConfirmationNonce = randomNonce()
	onboarding.ConfirmationExpires = c.now().Add(c.ttl())
	onboarding.ConfirmationUsed = false
	onboarding.Revision++
	return c.Store.PutOnboarding(*onboarding)
}

func (c *ControlPlane) confirm(ctx context.Context, auth identity.Envelope, args map[string]any) (map[string]any, error) {
	onboarding, err := c.resolveOnboarding(auth, args)
	if err != nil {
		return nil, err
	}
	definition, err := c.definitionOf(onboarding)
	if err != nil {
		return nil, err
	}
	if err := rejectEscalation(definition, args); err != nil {
		return nil, err
	}
	if err := c.refreshBrokerRequest(ctx, auth, &onboarding); err != nil {
		return nil, err
	}
	if onboarding.Phase == PhaseAwaitingOAuth {
		return c.ensurePreparedOAuth(ctx, auth, onboarding, definition)
	}
	nonce := argString(args, "nonce")
	if onboarding.ConfirmationUsed {
		return nil, fmt.Errorf("%w: replayed nonce", ErrUnauthorized)
	}
	if onboarding.ConfirmationNonce == "" || nonce != onboarding.ConfirmationNonce {
		return nil, fmt.Errorf("%w: confirmation nonce", ErrUnauthorized)
	}
	if onboarding.ConfirmationExpires.IsZero() || c.now().After(onboarding.ConfirmationExpires) {
		return nil, fmt.Errorf("%w: expired confirmation", ErrUnauthorized)
	}
	if len(onboarding.Required) > 0 && onboarding.Locator == "" {
		return nil, fmt.Errorf("%w: credentials required", ErrUnauthorized)
	}
	binding, err := c.materializeBinding(ctx, auth, onboarding, definition)
	if err != nil {
		if errors.Is(err, ErrUnauthorized) {
			if entry, matched, lookupErr := preparedForSource(ArtifactSource{Repository: definition.Source.Repository, Subfolder: definition.Source.Subfolder, CommitSHA: definition.Source.CommitSHA}); lookupErr == nil && matched && entry.OAuth != nil {
				return c.ensurePreparedOAuth(ctx, auth, onboarding, definition)
			}
		}
		return nil, err
	}
	onboarding.ConfirmationUsed = true
	onboarding.BindingID = binding.ToolBindingID
	onboarding.ConnectionID = binding.ConnectionID
	onboarding.CredentialRefID = binding.CredentialRefID
	onboarding.BrokerRotateCredentialID = ""
	onboarding.Phase = PhaseConfirmed
	onboarding.Revision++
	if err := c.Store.PutOnboarding(onboarding); err != nil {
		return nil, err
	}
	return c.statusBody(onboarding, false), nil
}

func (c *ControlPlane) enable(ctx context.Context, auth identity.Envelope, args map[string]any) (map[string]any, error) {
	onboarding, err := c.resolveOnboarding(auth, args)
	if err != nil {
		definitionID := argString(args, "definition_id")
		version := argString(args, "version")
		if definitionID == "" || version == "" {
			return nil, err
		}
		definition, defErr := c.Store.Definition(definitionID, version)
		if defErr != nil {
			return nil, defErr
		}
		if err := rejectEscalation(definition, args); err != nil {
			return nil, err
		}
		if pub := c.Store.publicationFor(definition); pub.Visibility != PublicationUser {
			if err := c.Store.RequireCatalogAccess(auth, definition); err != nil {
				return nil, err
			}
		}
		var binding ToolBinding
		var enableErr error
		if c.Ready != nil {
			binding, enableErr = c.Store.EnableReady(ctx, auth, definitionID, version, c.Ready)
		} else {
			binding, enableErr = c.Store.Enable(auth, definitionID, version)
		}
		if enableErr != nil {
			return nil, enableErr
		}
		return map[string]any{"phase": PhaseEnabled, "status": string(binding.Status), "binding_id": binding.ToolBindingID, "definition_id": binding.DefinitionID, "version": binding.DefinitionVersion}, nil
	}
	definition, err := c.definitionOf(onboarding)
	if err != nil {
		return nil, err
	}
	if err := rejectEscalation(definition, args); err != nil {
		return nil, err
	}
	if onboarding.BindingID == "" {
		if onboarding.Phase != PhaseConfirmed && onboarding.Phase != PhaseEnabled && onboarding.Phase != PhaseDisabled {
			return nil, fmt.Errorf("%w: confirm required", ErrUnauthorized)
		}
		binding, err := c.materializeBinding(ctx, auth, onboarding, definition)
		if err != nil {
			return nil, err
		}
		onboarding.BindingID = binding.ToolBindingID
		onboarding.ConnectionID = binding.ConnectionID
		onboarding.CredentialRefID = binding.CredentialRefID
	}
	existing, err := c.Store.binding(onboarding.BindingID)
	if err != nil {
		return nil, err
	}
	if existing.Status == RevokedStatus {
		return nil, ErrRevoked
	}
	if existing.Status != ActiveStatus {
		var binding ToolBinding
		if c.Ready != nil {
			binding, err = c.Store.EnableReady(ctx, auth, definition.DefinitionID, definition.Version, c.Ready)
		} else {
			binding, err = c.Store.Enable(auth, definition.DefinitionID, definition.Version)
		}
		if err != nil {
			return nil, err
		}
		onboarding.BindingID = binding.ToolBindingID
		onboarding.ConnectionID = binding.ConnectionID
		onboarding.CredentialRefID = binding.CredentialRefID
	}
	onboarding.Phase = PhaseEnabled
	onboarding.Revision++
	if err := c.Store.PutOnboarding(onboarding); err != nil {
		return nil, err
	}
	return c.statusBody(onboarding, false), nil
}

func (c *ControlPlane) setPhase(auth identity.Envelope, args map[string]any, status Status, phase string) (map[string]any, error) {
	onboarding, err := c.resolveOnboarding(auth, args)
	if err != nil {
		return nil, err
	}
	if onboarding.BindingID == "" {
		return nil, fmt.Errorf("%w: binding", ErrNotFound)
	}
	if err := c.Store.SetBindingStatus(onboarding.BindingID, status); err != nil {
		return nil, err
	}
	onboarding.Phase = phase
	onboarding.Revision++
	if err := c.Store.PutOnboarding(onboarding); err != nil {
		return nil, err
	}
	return c.statusBody(onboarding, false), nil
}

func (c *ControlPlane) revoke(auth identity.Envelope, args map[string]any) (map[string]any, error) {
	onboarding, err := c.resolveOnboarding(auth, args)
	if err != nil {
		return nil, err
	}
	if err := c.cutAuthorization(onboarding); err != nil {
		return nil, err
	}
	onboarding.Phase = PhaseRevoked
	onboarding.Revision++
	if err := c.Store.PutOnboarding(onboarding); err != nil {
		return nil, err
	}
	return c.statusBody(onboarding, false), nil
}

func (c *ControlPlane) remove(auth identity.Envelope, args map[string]any) (map[string]any, error) {
	onboarding, err := c.resolveOnboarding(auth, args)
	if err != nil {
		return nil, err
	}
	_ = c.cutAuthorization(onboarding)
	if onboarding.BindingID != "" {
		if binding, err := c.Store.binding(onboarding.BindingID); err == nil {
			definition, _ := c.definitionOf(onboarding)
			effective := EffectiveBinding{Binding: binding, Definition: definition}
			if binding.ConnectionID != "" {
				if conn, _, err := c.Store.OwnedConnection(auth, binding.ConnectionID); err == nil {
					effective.Connection = &conn
				}
			}
			if err := RemoveBindingWorkspace(c.WorkloadRoot, effective); err != nil {
				return nil, err
			}
		}
	}
	onboarding.Phase = PhaseRemoved
	onboarding.Revision++
	if err := c.Store.PutOnboarding(onboarding); err != nil {
		return nil, err
	}
	return c.statusBody(onboarding, false), nil
}

func (c *ControlPlane) Rotate(auth identity.Envelope, args map[string]any) (map[string]any, error) {
	if err := RejectAuthorityArguments(args); err != nil {
		return nil, err
	}
	if c.Broker != nil && c.Broker.Enabled() {
		if c.Store == nil {
			return nil, ErrInvalid
		}
		onboarding, err := c.resolveOnboarding(auth, args)
		if err != nil {
			return nil, err
		}
		if onboarding.BrokerCredentialID == "" {
			return nil, ErrNotFound
		}
		if onboarding.BrokerRotateCredentialID != "" && onboarding.Phase == PhaseAwaitingCreds {
			return c.statusBody(onboarding, true), nil
		}
		definition, err := c.definitionOf(onboarding)
		if err != nil {
			return nil, err
		}
		onboarding.BrokerRotateCredentialID = onboarding.BrokerCredentialID
		onboarding.BrokerRequestID, onboarding.BrokerAuthorizationURL = "", ""
		onboarding.BrokerAttempts++
		onboarding.Phase = PhaseAwaitingCreds
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
	if onboarding.ConnectionID == "" || onboarding.Locator == "" {
		return nil, fmt.Errorf("%w: connection", ErrNotFound)
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
	_ = c.Store.SetBindingStatus(onboarding.BindingID, RevokedStatus)
	onboarding.CredentialRefID = next.CredentialRefID
	onboarding.Phase = PhaseRevoked
	onboarding.Revision++
	if err := c.Store.PutOnboarding(onboarding); err != nil {
		return nil, err
	}
	return c.statusBody(onboarding, false), nil
}

func (c *ControlPlane) cutAuthorization(onboarding Onboarding) error {
	if onboarding.BindingID != "" {
		if err := c.Store.SetBindingStatus(onboarding.BindingID, RevokedStatus); err != nil && !errors.Is(err, ErrRevoked) && !errors.Is(err, ErrNotFound) {
			return err
		}
	}
	if onboarding.ConnectionID != "" {
		_ = c.Store.SetConnectionStatus(onboarding.ConnectionID, RevokedStatus)
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
func (c *ControlPlane) releaseBindingWorkloads(onboarding Onboarding) {
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
	case Shared:
		release(WorkloadInstanceID(definition.DefinitionID, Shared, "", ""))
	case PerUser:
		ownerID := binding.ToolBindingID
		if binding.ConnectionID != "" {
			ownerID = onboarding.ContextID + ":" + onboarding.PrincipalID + ":" + binding.ConnectionID
		}
		release(WorkloadInstanceID(definition.DefinitionID, PerUser, ownerID, ""))
	}
}

func (c *ControlPlane) materializeBinding(ctx context.Context, auth identity.Envelope, onboarding Onboarding, definition ToolDefinition) (ToolBinding, error) {
	rotation := onboarding.BrokerRotateCredentialID != ""
	if existing := c.Store.findBinding(auth, definition); existing != nil && existing.Status != RevokedStatus && !rotation {
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
		return ToolBinding{}, fmt.Errorf("%w: active owner connection and credential are required", ErrUnauthorized)
	}
	// A new onboarding for another version of the same connector replaces the
	// owner's credential revision. Creating a second active connection makes
	// Store.Enable correctly reject the ambiguous owner, so rotate the
	// existing connection instead.
	c.Store.mu.RLock()
	matches := c.Store.matchingOwnerConnectionsLocked(auth, definition)
	c.Store.mu.RUnlock()
	if len(matches) > 1 {
		return ToolBinding{}, fmt.Errorf("%w: ambiguous owner connection", ErrUnauthorized)
	}
	if len(matches) == 1 {
		if !rotation && matches[0].credential.Locator == onboarding.Locator {
			if c.Ready != nil {
				return c.Store.EnableReady(ctx, auth, definition.DefinitionID, definition.Version, c.Ready)
			}
			return c.Store.Enable(auth, definition.DefinitionID, definition.Version)
		}
		connection := matches[0].connection
		// A per-owner workload ID is stable across credential revisions. Stop
		// its old process before a new grant/credential can be admitted under it.
		if definition.Transport == ContainerMCP {
			if c.Release == nil {
				return ToolBinding{}, fmt.Errorf("%w: rotation requires workload release", ErrIsolation)
			}
			workloadID := WorkloadInstanceID(definition.DefinitionID, PerUser, auth.ContextID+":"+auth.PrincipalID+":"+connection.ConnectionID, "")
			if err := c.Release(ctx, workloadID); err != nil {
				return ToolBinding{}, err
			}
		}
		if c.Broker != nil && c.Broker.Enabled() {
			if previous := matches[0].credential; previous.Backend == "credential-broker" && previous.BrokerGrantID != "" {
				control, err := c.Broker.New(auth, "broker:control")
				if err != nil {
					return ToolBinding{}, err
				}
				if err := control.Do(ctx, http.MethodPost, "/v1/grants/"+url.PathEscape(previous.BrokerGrantID)+"/revoke", nil, nil); err != nil {
					return ToolBinding{}, err
				}
			}
			next := connection.Revision + 1
			reference := CredentialReference{CredentialRefID: CredentialReferenceID(connection.ConnectionID, next), ConnectionID: connection.ConnectionID, Revision: next, Backend: "credential-broker", Locator: onboarding.Locator, Keys: credentialNames(onboarding), BrokerContractID: definition.CredentialContractID, BrokerContractRevision: definition.CredentialContractRevision, BrokerEnv: cloneMap(definition.CredentialContractEnv)}
			candidate := ToolBinding{Schema: SchemaVersion, PrincipalID: auth.PrincipalID, ContextID: auth.ContextID, RuntimeID: auth.RuntimeID, DefinitionID: definition.DefinitionID, DefinitionVersion: definition.Version, ConnectionID: connection.ConnectionID, ConnectionRevision: next, CredentialRefID: reference.CredentialRefID, CredentialRevision: next, PolicyVersion: auth.PolicyVersion, WorkloadClass: definition.Workload.Class, Status: ActiveStatus, Revision: 1, ProjectionRevision: 1}
			candidate.ToolBindingID = DeterministicBindingID(candidate.PrincipalID, candidate.ContextID, candidate.RuntimeID, candidate.DefinitionID, candidate.DefinitionVersion, candidate.ConnectionID, candidate.CredentialRefID)
			grantID, err := c.ensureBrokerGrant(ctx, auth, definition, onboarding, candidate)
			if err != nil {
				return ToolBinding{}, err
			}
			reference.BrokerGrantID = grantID
			reference, err = c.Store.RotateCredentialRecord(reference)
			if err != nil {
				return ToolBinding{}, err
			}
			if reference.Locator != onboarding.Locator {
				return ToolBinding{}, fmt.Errorf("%w: credential rotation", ErrConflict)
			}
		} else {
			reference, err := c.Store.RotateCredential(connection.ConnectionID, credstore.BackendLocal, onboarding.Locator, credentialNames(onboarding))
			if err != nil {
				return ToolBinding{}, err
			}
			if reference.Locator != onboarding.Locator {
				return ToolBinding{}, fmt.Errorf("%w: credential rotation", ErrConflict)
			}
		}
		if c.Ready != nil {
			return c.Store.EnableReady(ctx, auth, definition.DefinitionID, definition.Version, c.Ready)
		}
		return c.Store.Enable(auth, definition.DefinitionID, definition.Version)
	}
	connectionID := deterministicID("conn", auth.PrincipalID, definition.DefinitionID, onboarding.OnboardingID)
	reference := CredentialReference{Schema: SchemaVersion, CredentialRefID: CredentialReferenceID(connectionID, 1), ConnectionID: connectionID, Revision: 1, Backend: credstore.BackendLocal, Locator: onboarding.Locator, Keys: credentialNames(onboarding), Status: ActiveStatus}
	if c.Broker != nil && c.Broker.Enabled() {
		reference.Backend = "credential-broker"
		reference.BrokerContractID = definition.CredentialContractID
		reference.BrokerContractRevision = definition.CredentialContractRevision
		reference.BrokerEnv = cloneMap(definition.CredentialContractEnv)
	}
	if reference.Backend == "credential-broker" {
		candidate := ToolBinding{Schema: SchemaVersion, PrincipalID: auth.PrincipalID, ContextID: auth.ContextID, RuntimeID: auth.RuntimeID, DefinitionID: definition.DefinitionID, DefinitionVersion: definition.Version, ConnectionID: connectionID, ConnectionRevision: 1, CredentialRefID: reference.CredentialRefID, CredentialRevision: 1, PolicyVersion: auth.PolicyVersion, WorkloadClass: definition.Workload.Class, Status: ActiveStatus, Revision: 1, ProjectionRevision: 1}
		candidate.ToolBindingID = DeterministicBindingID(candidate.PrincipalID, candidate.ContextID, candidate.RuntimeID, candidate.DefinitionID, candidate.DefinitionVersion, candidate.ConnectionID, candidate.CredentialRefID)
		grantID, err := c.ensureBrokerGrant(ctx, auth, definition, onboarding, candidate)
		if err != nil {
			return ToolBinding{}, err
		}
		reference.BrokerGrantID = grantID
	}
	if err := c.Store.PutCredentialReference(reference); err != nil {
		return ToolBinding{}, err
	}
	connection := Connection{Schema: SchemaVersion, ConnectionID: connectionID, Owner: OwnerRef{Type: PrincipalOwner, ID: auth.PrincipalID}, DefinitionID: definition.DefinitionID, CredentialRefID: reference.CredentialRefID, Revision: 1, Status: ActiveStatus}
	if onboarding.CredentialOwner != "" && onboarding.CredentialOwner != auth.PrincipalID {
		connection.Metadata = map[string]string{"credential_owner": onboarding.CredentialOwner}
	}
	if err := c.Store.PutConnection(connection); err != nil {
		return ToolBinding{}, err
	}
	if c.Ready != nil {
		return c.Store.EnableReady(ctx, auth, definition.DefinitionID, definition.Version, c.Ready)
	}
	return c.Store.Enable(auth, definition.DefinitionID, definition.Version)
}

func (c *ControlPlane) ensureBrokerGrant(ctx context.Context, auth identity.Envelope, definition ToolDefinition, onboarding Onboarding, binding ToolBinding) (string, error) {
	if c.Broker == nil || !c.Broker.Enabled() || onboarding.BrokerCredentialID == "" {
		return "", fmt.Errorf("%w: credential broker credential", ErrUnauthorized)
	}
	if binding.WorkloadClass == PerJob {
		return "", fmt.Errorf("%w: credential broker per-job grants are not supported yet", ErrUnauthorized)
	}
	owner := (*OwnerRef)(nil)
	if binding.WorkloadClass != Shared {
		owner = &OwnerRef{Type: ContextOwner, ID: auth.ContextID}
	}
	workload, err := NewWorkloadInstance(binding, owner, "", 1, c.now(), time.Time{})
	if err != nil {
		return "", err
	}
	control, err := c.Broker.New(auth, "broker:control")
	if err != nil {
		return "", err
	}
	execution := "dedicated"
	if binding.WorkloadClass == Shared {
		execution = "shared"
	}
	grant, err := control.Grant(ctx, onboarding.BrokerCredentialID, brokerv1.GrantRequest{
		ContractID: definition.CredentialContractID, ContractRevision: definition.CredentialContractRevision,
		PrincipalID: auth.PrincipalID, ContextID: auth.ContextID, RuntimeID: auth.RuntimeID,
		BindingID: binding.ToolBindingID, WorkloadID: workload.WorkloadID, Execution: execution,
		IdempotencyKey: deterministicID("grant", onboarding.OnboardingID, binding.ToolBindingID),
	})
	if err != nil {
		return "", err
	}
	return grant.ID, nil
}

func cloneMap(values map[string]string) map[string]string {
	if len(values) == 0 {
		return nil
	}
	copy := make(map[string]string, len(values))
	for key, value := range values {
		copy[key] = value
	}
	return copy
}

func (c *ControlPlane) SubmitCredentials(onboardingID, nonce string, values map[string]string) error {
	if c == nil || c.Store == nil || c.Secrets == nil {
		return fmt.Errorf("%w: credential store", ErrInvalid)
	}
	onboarding, err := c.Store.onboarding(onboardingID)
	if err != nil {
		return err
	}
	if onboarding.FormNonce == "" || nonce != onboarding.FormNonce {
		return fmt.Errorf("%w: form nonce", ErrUnauthorized)
	}
	if onboarding.FormExpires.IsZero() || c.now().After(onboarding.FormExpires) {
		return fmt.Errorf("%w: expired form", ErrUnauthorized)
	}
	if onboarding.Phase != PhaseAwaitingCreds && onboarding.Phase != PhaseAwaitingConfirm {
		return fmt.Errorf("%w: credentials not expected", ErrUnauthorized)
	}
	filtered := map[string]string{}
	for _, hint := range onboarding.Required {
		value := strings.TrimSpace(values[hint.Name])
		if value == "" {
			return fmt.Errorf("%w: missing %s", ErrInvalid, hint.Name)
		}
		filtered[hint.Name] = value
	}
	locator := onboarding.Locator
	owner := onboarding.PrincipalID
	shared := false
	c.Store.mu.RLock()
	auth := identity.Envelope{Schema: identity.Schema, PrincipalID: onboarding.PrincipalID, ExternalIdentityID: onboarding.PrincipalID, ContextID: onboarding.ContextID, RuntimeID: onboarding.RuntimeID, ConversationID: onboarding.PrincipalID, DeliveryTargetID: onboarding.PrincipalID, PolicyVersion: onboarding.PolicyVersion}
	if policy := c.Store.sharedPolicyLocked(auth, onboarding.DefinitionID); policy != nil {
		locator = policy.Locator
		owner = policy.StoreOwner
		onboarding.CredentialOwner = policy.StoreOwner
		shared = true
	}
	c.Store.mu.RUnlock()
	if !shared {
		if locator == "" {
			locator, err = c.Secrets.NewLocator()
			if err != nil {
				return err
			}
		}
		if err := c.Secrets.Put(locator, owner, filtered); err != nil {
			return err
		}
	} else if locator == "" {
		return fmt.Errorf("%w: shared locator", ErrInvalid)
	}
	onboarding.Locator = locator
	onboarding.CredentialOwner = owner
	onboarding.FormNonce = ""
	onboarding.FormExpires = time.Time{}
	onboarding.Phase = PhaseAwaitingConfirm
	onboarding.ConfirmationNonce = randomNonce()
	onboarding.ConfirmationExpires = c.now().Add(c.ttl())
	onboarding.ConfirmationUsed = false
	onboarding.Revision++
	return c.Store.PutOnboarding(onboarding)
}

// AuthorizeCredentials treats the protected form submission as the user's
// confirmation and completes the binding without another model round-trip.
func (c *ControlPlane) AuthorizeCredentials(ctx context.Context, onboardingID, nonce string, values map[string]string) error {
	if err := c.SubmitCredentials(onboardingID, nonce, values); err != nil {
		return err
	}
	return c.finishAuthorization(ctx, onboardingID)
}

func (c *ControlPlane) finishAuthorization(ctx context.Context, onboardingID string) error {
	onboarding, err := c.Store.onboarding(onboardingID)
	if err != nil {
		return err
	}
	auth := identity.Envelope{Schema: identity.Schema, PrincipalID: onboarding.PrincipalID, ExternalIdentityID: onboarding.PrincipalID, ContextID: onboarding.ContextID, RuntimeID: onboarding.RuntimeID, ConversationID: onboarding.PrincipalID, DeliveryTargetID: onboarding.PrincipalID, PolicyVersion: onboarding.PolicyVersion}
	body, err := c.confirm(ctx, auth, map[string]any{"onboarding_id": onboardingID, "nonce": onboarding.ConfirmationNonce})
	if err != nil {
		return err
	}
	if body["phase"] == PhaseAwaitingOAuth {
		return nil
	}
	_, err = c.enable(ctx, auth, map[string]any{"onboarding_id": onboardingID})
	return err
}

func (c *ControlPlane) StartOAuth(auth identity.Envelope, onboardingID, redirect string) (oauth.StartResult, error) {
	if c == nil || c.OAuth == nil {
		return oauth.StartResult{}, fmt.Errorf("%w: oauth broker", ErrInvalid)
	}
	onboarding, err := c.Store.OnboardingFor(auth, onboardingID)
	if err != nil {
		return oauth.StartResult{}, err
	}
	return c.OAuth.StartAuth(oauth.AuthRequest{Principal: auth.PrincipalID, Context: auth.ContextID, Connection: onboarding.OnboardingID, Provider: "fixture", Redirect: redirect})
}

func (c *ControlPlane) HandleOAuthCallback(auth identity.Envelope, onboardingID, state, code, redirect string) error {
	if c == nil || c.OAuth == nil {
		return fmt.Errorf("%w: oauth broker", ErrInvalid)
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
	onboarding.Phase = PhaseAwaitingConfirm
	onboarding.ConfirmationNonce = randomNonce()
	onboarding.ConfirmationExpires = c.now().Add(c.ttl())
	onboarding.Revision++
	if err := c.Store.PutOnboarding(onboarding); err != nil {
		return err
	}
	return c.finishAuthorization(context.Background(), onboardingID)
}

func (c *ControlPlane) resolveOnboarding(auth identity.Envelope, args map[string]any) (Onboarding, error) {
	if id := argString(args, "onboarding_id"); id != "" {
		onboarding, err := c.Store.OnboardingFor(auth, id)
		if err != nil {
			return Onboarding{}, err
		}
		// Callers may hold a preparing stub that was superseded by the real
		// record (patch bump or a reviewed definition id): follow the pointer.
		if onboarding.Phase == PhaseRemoved && onboarding.SupersededBy != "" {
			if target, err := c.Store.OnboardingFor(auth, onboarding.SupersededBy); err == nil {
				return target, nil
			}
		}
		return onboarding, nil
	}
	definitionID := argString(args, "definition_id")
	version := argString(args, "version")
	if definitionID == "" {
		return Onboarding{}, fmt.Errorf("%w: onboarding", ErrNotFound)
	}
	c.Store.mu.RLock()
	defer c.Store.mu.RUnlock()
	var found Onboarding
	ok := false
	for _, onboarding := range c.Store.onboardings {
		if onboarding.PrincipalID == auth.PrincipalID && onboarding.ContextID == auth.ContextID && onboarding.RuntimeID == auth.RuntimeID && onboarding.PolicyVersion == auth.PolicyVersion && onboarding.DefinitionID == definitionID && (version == "" || onboarding.DefinitionVersion == version) && onboarding.Phase != PhaseRemoved {
			if ok && onboarding.CreatedAt.Before(found.CreatedAt) {
				continue
			}
			found, ok = onboarding, true
		}
	}
	if !ok {
		return Onboarding{}, fmt.Errorf("%w: onboarding", ErrNotFound)
	}
	return found, nil
}

func (c *ControlPlane) definitionOf(onboarding Onboarding) (ToolDefinition, error) {
	if onboarding.Definition != nil {
		return *onboarding.Definition, nil
	}
	return c.Store.Definition(onboarding.DefinitionID, onboarding.DefinitionVersion)
}

func (c *ControlPlane) statusBody(onboarding Onboarding, withHints bool) map[string]any {
	body := map[string]any{
		"onboarding_id":  onboarding.OnboardingID,
		"phase":          onboarding.Phase,
		"status":         onboarding.Phase,
		"mode":           onboarding.Mode,
		"definition_id":  onboarding.DefinitionID,
		"version":        onboarding.DefinitionVersion,
		"repository":     onboarding.SourceURL,
		"commit_sha":     onboarding.CommitSHA,
		"subfolder":      onboarding.Subfolder,
		"binding_id":     onboarding.BindingID,
		"permissions":    onboarding.Permissions,
		"effects":        onboarding.Effects,
		"principal_from": "request",
	}
	if onboarding.Recipe != nil {
		body["recipe"] = onboarding.Recipe
	}
	if onboarding.BrokerRequestID != "" {
		body["broker_request_id"] = onboarding.BrokerRequestID
		if onboarding.BrokerAuthorizationURL != "" {
			body["authorization_url"] = onboarding.BrokerAuthorizationURL
		}
	}
	if onboarding.Phase == PhaseAwaitingOAuth && onboarding.ProviderAuthorizationURL != "" {
		body["authorization_url"] = onboarding.ProviderAuthorizationURL
	}
	if withHints {
		hints := make([]map[string]string, 0, len(onboarding.Required))
		for _, hint := range onboarding.Required {
			hints = append(hints, map[string]string{"name": hint.Name, "type": hint.Type, "hint": hint.Hint})
		}
		body["credentials"] = hints
		if onboarding.FormNonce != "" {
			body["form_path"] = "/credentials/" + onboarding.OnboardingID + "?nonce=" + url.QueryEscape(onboarding.FormNonce)
			body["form_url"] = c.origin() + body["form_path"].(string)
		}
	}
	switch onboarding.Phase {
	case PhasePreparing:
		body["next_action"] = "poll_status"
		body["instructions"] = "Source review and build continue in the background; call status with onboarding_id until the phase changes."
	case PhaseFailed:
		body["next_action"] = "retry"
		body["instructions"] = "The prepare failed; call prepare_source again with the same request_key to retry."
	case PhaseAwaitingCreds:
		body["next_action"] = "submit_credentials"
		body["instructions"] = "Ask the user to open the credential URL, submit the form, then call this tool again."
	case PhaseAwaitingConfirm:
		body["next_action"] = "confirm"
		body["instructions"] = "Call confirm with onboarding_id and the nonce from this response, then call enable. No browser action is required."
	case PhaseAwaitingOAuth:
		body["next_action"] = "authorize"
		body["instructions"] = "Send authorization_url to the user. After the browser callback, call status to verify the connection."
	case PhaseConfirmed:
		body["next_action"] = "enable"
		body["instructions"] = "Call enable with onboarding_id."
	case PhaseEnabled:
		body["next_action"] = "ready"
	}
	if onboarding.Phase == PhaseAwaitingConfirm && onboarding.ConfirmationNonce != "" && !onboarding.ConfirmationUsed {
		body["nonce"] = onboarding.ConfirmationNonce
	}
	if onboarding.Phase == PhaseEnabled {
		if definition, err := c.definitionOf(onboarding); err == nil {
			tools := make([]string, 0, len(definition.Tools))
			for _, tool := range definition.Tools {
				tools = append(tools, ProjectedToolName(definition.DefinitionID, definition.Version, tool.Name))
			}
			body["projected_tools"] = tools
		}
	}
	return body
}

func (s *Store) Definition(id, version string) (ToolDefinition, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	definition, ok := s.definitions[definitionKey(id, version)]
	if !ok {
		return ToolDefinition{}, fmt.Errorf("%w: definition", ErrNotFound)
	}
	return definition, nil
}

func (s *Store) publicationFor(definition ToolDefinition) DefinitionPublication {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.publicationLocked(definition)
}

func (s *Store) onboarding(id string) (Onboarding, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	onboarding, ok := s.onboardings[id]
	if !ok {
		return Onboarding{}, fmt.Errorf("%w: onboarding", ErrNotFound)
	}
	return onboarding, nil
}

func (s *Store) binding(id string) (ToolBinding, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	binding, ok := s.bindings[id]
	if !ok {
		return ToolBinding{}, fmt.Errorf("%w: binding", ErrNotFound)
	}
	return binding, nil
}

func (s *Store) findBinding(auth identity.Envelope, definition ToolDefinition) *ToolBinding {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.findBindingLocked(auth, definition)
}

func (s *Store) WorkloadIDsForBinding(bindingID string) []string {
	s.mu.RLock()
	defer s.mu.RUnlock()
	ids := make([]string, 0)
	for _, workload := range s.workloads {
		if workload.BindingID == bindingID {
			ids = append(ids, workload.WorkloadID)
		}
	}
	return ids
}

func ParseGitHubSource(raw string) (ArtifactSource, error) {
	u, err := url.Parse(strings.TrimSpace(raw))
	if err != nil || u.Scheme != "https" || !strings.EqualFold(u.Host, "github.com") || u.User != nil || u.RawQuery != "" || u.ForceQuery || u.Fragment != "" {
		return ArtifactSource{}, fmt.Errorf("%w: public GitHub URL and exact commit SHA required", ErrInvalid)
	}
	parts := strings.Split(strings.Trim(u.Path, "/"), "/")
	if len(parts) >= 4 && (parts[2] == "commit" || parts[2] == "tree") {
		source := ArtifactSource{Repository: "https://github.com/" + parts[0] + "/" + parts[1], CommitSHA: strings.ToLower(parts[3])}
		if parts[2] == "tree" {
			source.Subfolder = strings.Join(parts[4:], "/")
		} else if len(parts) != 4 {
			return ArtifactSource{}, fmt.Errorf("%w: commit URL cannot include a subfolder", ErrInvalid)
		}
		if _, err := source.ArchiveURL(); err != nil {
			return ArtifactSource{}, err
		}
		return source, nil
	}
	return ArtifactSource{}, fmt.Errorf("%w: public GitHub URL and exact commit SHA required", ErrInvalid)
}

func rejectEscalation(definition ToolDefinition, args map[string]any) error {
	allowedEffects := map[string]bool{}
	allowedTools := map[string]bool{}
	for _, tool := range definition.Tools {
		allowedEffects[string(tool.Effect)] = true
		allowedTools[tool.Name] = true
	}
	for _, effect := range argStrings(args, "effects") {
		if !allowedEffects[effect] {
			return fmt.Errorf("%w: effect escalation", ErrUnauthorized)
		}
	}
	for _, name := range argStrings(args, "tools") {
		if !allowedTools[name] {
			return fmt.Errorf("%w: effect escalation", ErrUnauthorized)
		}
	}
	if n := argInt(args, "cpu_millis"); n > 0 && n > definition.Execution.CPUMillis {
		return fmt.Errorf("%w: budget escalation", ErrUnauthorized)
	}
	if n := argInt(args, "memory_mib"); n > 0 && n > definition.Execution.MemoryMiB {
		return fmt.Errorf("%w: budget escalation", ErrUnauthorized)
	}
	if n := argInt(args, "timeout_seconds"); n > 0 && n > definition.Execution.TimeoutSeconds {
		return fmt.Errorf("%w: budget escalation", ErrUnauthorized)
	}
	if n := argInt(args, "max_pids"); n > 0 && n > definition.Execution.MaxPIDs {
		return fmt.Errorf("%w: budget escalation", ErrUnauthorized)
	}
	if n := argInt(args, "output_bytes"); n > 0 && n > definition.Execution.OutputBytes {
		return fmt.Errorf("%w: budget escalation", ErrUnauthorized)
	}
	return nil
}

func credentialHints(definition ToolDefinition) []CredentialHint {
	hints := make([]CredentialHint, 0, len(definition.Credentials))
	for _, input := range definition.Credentials {
		upper := strings.ToUpper(input.Name)
		if !input.Required || !secretName(input.Name) && !strings.Contains(upper, "OAUTH") && !strings.Contains(upper, "CLIENT_ID") {
			continue
		}
		kind := "secret"
		delivery := "env"
		if strings.Contains(upper, "OAUTH") {
			kind = "oauth"
		}
		if strings.Contains(upper, "CREDENTIALS") {
			kind, delivery = "json", "json"
		}
		hints = append(hints, CredentialHint{Name: input.Name, Type: kind, Secret: true, Delivery: delivery, Target: input.Name, Hint: "protected loopback form"})
	}
	return hints
}

func credentialHintsForRecipe(definition ToolDefinition, recipe ConnectionRecipe) []CredentialHint {
	hints := make([]CredentialHint, 0, len(recipe.Fields))
	for _, field := range recipe.Fields {
		if !field.Required {
			continue
		}
		typ := field.Type
		if typ == "" {
			typ = "secret"
		}
		hint := "protected loopback form"
		if field.Delivery != "" {
			hint += "; delivery: " + field.Delivery
		}
		hints = append(hints, CredentialHint{
			Name: field.Name, Type: typ, Secret: field.Secret, Delivery: field.Delivery,
			Target: field.Target, Alternative: field.Alternative, Hint: hint,
		})
	}
	if len(hints) == 0 {
		return credentialHints(definition)
	}
	for index := range hints {
		for _, alternative := range recipe.Alternatives {
			if alternative.URL != "" {
				hints[index].AlternativeURL = alternative.URL
				break
			}
		}
	}
	return hints
}

func credentialNames(onboarding Onboarding) []string {
	names := make([]string, 0, len(onboarding.Required))
	for _, hint := range onboarding.Required {
		names = append(names, hint.Name)
	}
	return names
}

func toolNames(definition ToolDefinition) []string {
	names := make([]string, 0, len(definition.Tools))
	for _, tool := range definition.Tools {
		names = append(names, tool.Name)
	}
	return names
}

func effectNames(definition ToolDefinition) []string {
	seen := map[string]bool{}
	effects := make([]string, 0)
	for _, tool := range definition.Tools {
		name := string(tool.Effect)
		if !seen[name] {
			seen[name] = true
			effects = append(effects, name)
		}
	}
	return effects
}

func argString(args map[string]any, key string) string {
	if args == nil {
		return ""
	}
	value, ok := args[key]
	if !ok || value == nil {
		return ""
	}
	text, _ := value.(string)
	return strings.TrimSpace(text)
}

func argStrings(args map[string]any, key string) []string {
	if args == nil {
		return nil
	}
	value, ok := args[key]
	if !ok || value == nil {
		return nil
	}
	switch typed := value.(type) {
	case []string:
		return append([]string(nil), typed...)
	case []any:
		out := make([]string, 0, len(typed))
		for _, item := range typed {
			text, _ := item.(string)
			if text = strings.TrimSpace(text); text != "" {
				out = append(out, text)
			}
		}
		return out
	default:
		return nil
	}
}

func argInt(args map[string]any, key string) int {
	if args == nil {
		return 0
	}
	value, ok := args[key]
	if !ok || value == nil {
		return 0
	}
	switch typed := value.(type) {
	case int:
		return typed
	case int64:
		return int(typed)
	case float64:
		return int(typed)
	default:
		return 0
	}
}

func randomNonce() string {
	raw := make([]byte, 16)
	if _, err := rand.Read(raw); err != nil {
		sum := sha256.Sum256([]byte(time.Now().String()))
		return hex.EncodeToString(sum[:16])
	}
	return hex.EncodeToString(raw)
}
