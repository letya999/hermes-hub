package toolhub

import (
	"context"
	"fmt"
	"github.com/letya999/hermes-hub/internal/identity"
	"log"
	"maps"
	"slices"
	"strings"
	"time"
)

func (c *ControlPlane) prepareSource(ctx context.Context, auth identity.Envelope, args map[string]any) (map[string]any, error) {
	if _, requested := args["telegram_send"]; requested {
		return c.prepareTelegramSend(ctx, auth, args)
	}
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
	if strings.HasPrefix(source, "github-release:") {
		return nil, fmt.Errorf("%w: CLI release source belongs in cli.source; supply cli.name, cli.binary and cli.asset, not top-level source", ErrInvalid)
	}
	definitionID := argString(args, "definition_id")
	version := argString(args, "version")
	if spec, requested := args["cli"]; requested {
		if source != "" || definitionID != "" || version != "" || argString(args, "remote_url") != "" || argString(args, "candidate_id") != "" {
			return nil, fmt.Errorf("%w: select one source", ErrInvalid)
		}
		return c.prepareCLI(ctx, auth, spec, requestKey)
	}
	if remote := argString(args, "remote_url"); remote != "" {
		if source != "" || definitionID != "" || version != "" || argString(args, "candidate_id") != "" {
			return nil, fmt.Errorf("%w: select one source", ErrInvalid)
		}
		return c.prepareRemote(ctx, auth, remote, args)
	}
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
		deliverPrepareEvent(background, auth, preparing, "prepare-started")
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
		idSeed = string(OnboardingSelfInstall) + ":" + config.DefinitionID + "@" + config.Version + ":" + source.Repository + ":" + source.CommitSHA + ":" + source.Tag + ":" + source.Asset + ":" + source.PackageRegistry + ":" + source.PackageName + ":" + source.PackageVersion
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
			if persistErr := c.persistFailure(latest, publicPrepareError(err), err); persistErr != err {
				return nil, persistErr
			}
		}
		return nil, err
	}
	review := SourceReview{}
	var err error
	if existing, ok := c.Store.reusableSelfInstallDefinition(auth, source, preparing.DefinitionID, preparing.DefinitionVersion); selected == nil && ok && c.selfInstallUsable(existing) && completeToolSchemas(existing) && selfInstallContractIntact(existing) {
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
	if review.AdmissionPending {
		return c.acceptCredentialGate(ctx, auth, preparing, review)
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
		if err := c.Store.PutOnboarding(preparing); err != nil {
			log.Printf("toolhub: superseded onboarding stub %s not recorded: %v", preparing.OnboardingID, err)
		}
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
	// Recipe hints can name required inputs the secret-name heuristic missed —
	// a URL-only contract needs no token. The phase must reflect the final
	// Required set or the onboarding deadlocks: confirm rejects a missing
	// locator while the broker URL only surfaces in awaiting-credentials.
	if len(onboarding.Required) > 0 && onboarding.Locator == "" && onboarding.Phase == PhaseAwaitingConfirm {
		onboarding.Phase = PhaseAwaitingCreds
		onboarding.FormNonce = randomNonce()
		onboarding.FormExpires = c.now().Add(c.ttl())
		onboarding.ConfirmationNonce = ""
		onboarding.ConfirmationExpires = time.Time{}
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

func publicPrepareError(err error) string {
	if err == nil {
		return ""
	}
	text := secretValuePattern.ReplaceAllString(err.Error(), "[redacted]")
	text = strings.Join(strings.Fields(text), " ")
	if len(text) > 400 {
		text = text[:400]
	}
	return text
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

// selfInstallContractIntact rejects stored records whose confirmed tool
// contract no longer verifies. Reuse skips re-review, so a record stamped by
// an older pipeline would otherwise reach admission and fail there with a
// digest-drift error. Failing closed here forces a fresh review instead.
func selfInstallContractIntact(definition ToolDefinition) bool {
	return verifyDefinitionToolContract(definition) == nil
}

func (c *ControlPlane) selfInstallUsable(definition ToolDefinition) bool {
	entry, matched, err := preparedForSource(ArtifactSource{Repository: definition.Source.Repository, CommitSHA: definition.Source.CommitSHA, Subfolder: definition.Source.Subfolder})
	if err != nil {
		return false
	}
	if matched && (definition.Workload.Stateful != entry.Stateful || !maps.Equal(definition.RuntimeEnvironment, entry.RuntimeEnvironment) || definition.CredentialContractID != entry.ContractID || definition.CredentialContractRevision != entry.ContractRevision || definition.Source.Command != entry.Entrypoint[0] || !slices.Equal(definition.Source.Args, entry.Entrypoint[1:])) {
		return false
	}
	return c == nil || c.Broker == nil || !c.Broker.Enabled() || len(definition.Credentials) == 0 || definition.CredentialContractID != ""
}
