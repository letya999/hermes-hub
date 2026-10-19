package toolhub

import (
	"context"
	"fmt"
	"github.com/letya999/hermes-hub/internal/identity"
	"github.com/letya999/hermes-hub/internal/toolhub/api"
	"log"
	"maps"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"time"
)

func (c *ControlPlane) prepareSource(ctx context.Context, auth identity.Envelope, args map[string]any) (map[string]any, error) {
	if _, requested := args["telegram_send"]; requested {
		return c.prepareTelegramSend(ctx, auth, args)
	}
	requestKey := api.ArgString(args, "request_key")
	if requestKey != "" {
		if existing, ok := c.Store.FindOnboardingByKey(auth, requestKey); ok && existing.Phase != api.PhaseFailed && existing.Phase != api.PhaseRemoved && !c.stalePreparing(existing) {
			if credentialSource := api.ArgString(args, "credential_source"); credentialSource != "" {
				if err := c.rebuildCredentialSource(&existing, credentialSource); err != nil {
					return nil, err
				}
			}
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
	source := api.ArgString(args, "source")
	if strings.HasPrefix(source, "github-release:") {
		return nil, fmt.Errorf("%w: CLI release source belongs in cli.source; supply cli.name, cli.binary and cli.asset, not top-level source", api.ErrInvalid)
	}
	definitionID := api.ArgString(args, "definition_id")
	version := api.ArgString(args, "version")
	if spec, requested := args["cli"]; requested {
		if source != "" || definitionID != "" || version != "" || api.ArgString(args, "remote_url") != "" || api.ArgString(args, "candidate_id") != "" {
			return nil, fmt.Errorf("%w: select one source", api.ErrInvalid)
		}
		return c.prepareCLI(ctx, auth, spec, requestKey)
	}
	if remote := api.ArgString(args, "remote_url"); remote != "" {
		if source != "" || definitionID != "" || version != "" || api.ArgString(args, "candidate_id") != "" {
			return nil, fmt.Errorf("%w: select one source", api.ErrInvalid)
		}
		return c.prepareRemote(ctx, auth, remote, args)
	}
	var selected *api.RecipeCandidate
	if candidate := api.ArgString(args, "candidate_id"); candidate != "" {
		if source != "" || definitionID != "" || version != "" {
			return nil, fmt.Errorf("%w: select one source", api.ErrInvalid)
		}
		choice, err := c.selectedCandidate(auth, candidate)
		if err != nil {
			return nil, err
		}
		source = choice.SourceURL()
		if choice.Source.CommitSHA == "" && choice.Source.Subfolder != "" {
			if c.SourceResolver == nil {
				return nil, fmt.Errorf("%w: source resolver unavailable", api.ErrInvalid)
			}
			pinned, err := c.SourceResolver(ctx, choice.Source.Repository)
			if err != nil {
				return nil, err
			}
			if pinned.Repository != choice.Source.Repository || pinned.Subfolder != "" {
				return nil, fmt.Errorf("%w: source resolution drift", api.ErrStale)
			}
			pinned.Subfolder = choice.Source.Subfolder
			if _, err := pinned.ArchiveURL(); err != nil {
				return nil, err
			}
			source = pinned.Repository + "/tree/" + pinned.CommitSHA + "/" + pinned.Subfolder
		}
		selected = choice.Recipe
	}
	if source != "" {
		shared, err := sharedScope(args)
		if err != nil {
			return nil, err
		}
		if shared {
			if err := c.Store.RequireControlOperation(auth, "install_shared"); err != nil {
				return nil, err
			}
		}
		return c.prepareSelfInstallAsync(ctx, auth, source, requestKey, selected, shared, api.ArgString(args, "credential_source"))
	}
	if definitionID != "" && version != "" {
		return c.prepareCatalog(ctx, auth, definitionID, version, requestKey)
	}
	return nil, fmt.Errorf("%w: source or definition required", api.ErrInvalid)
}

func (c *ControlPlane) prepareCatalog(ctx context.Context, auth identity.Envelope, definitionID, version, requestKey string) (map[string]any, error) {
	definition, err := c.Store.Definition(definitionID, version)
	if err != nil {
		return nil, err
	}
	if err := c.Store.RequireCatalogAccess(auth, definition); err != nil {
		return nil, err
	}
	onboarding, err := c.newOnboarding(auth, api.OnboardingCatalog, requestKey, definition, "", "")
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

func (c *ControlPlane) prepareSelfInstall(ctx context.Context, auth identity.Envelope, sourceURL, requestKey string, selected *api.RecipeCandidate, shared bool, credentialSource string) (map[string]any, error) {
	source, config, err := c.resolveSelfInstallSource(ctx, auth, sourceURL)
	if err != nil {
		return nil, err
	}
	preparing, inFlight, err := c.ensurePreparing(auth, source, config, requestKey, shared)
	if err != nil {
		return nil, err
	}
	if inFlight {
		return c.statusBody(preparing, false), nil
	}
	return c.finishSelfInstall(ctx, auth, preparing, source, requestKey, selected, shared, credentialSource)
}

// prepareSelfInstallAsync runs review+build under a detached context: a slow or
// lost HTTP response no longer discards the work. The caller waits
// PrepareSyncWindow for a completed result, then receives the durable
// "preparing" record — the outcome is read via status afterwards.
func (c *ControlPlane) prepareSelfInstallAsync(ctx context.Context, auth identity.Envelope, sourceURL, requestKey string, selected *api.RecipeCandidate, shared bool, credentialSource string) (map[string]any, error) {
	if window := c.prepareWindow(); window < 0 {
		return c.prepareSelfInstall(ctx, auth, sourceURL, requestKey, selected, shared, credentialSource)
	}
	source, config, err := c.resolveSelfInstallSource(ctx, auth, sourceURL)
	if err != nil {
		return nil, err
	}
	preparing, inFlight, err := c.ensurePreparing(auth, source, config, requestKey, shared)
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
		api.DeliverPrepareEvent(background, auth, preparing, "prepare-started")
		body, err := c.finishSelfInstall(background, auth, preparing, source, requestKey, selected, shared, credentialSource)
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

func (c *ControlPlane) resolveSelfInstallSource(ctx context.Context, auth identity.Envelope, sourceURL string) (api.ArtifactSource, api.ArtifactImportConfig, error) {
	if err := c.Store.RequireSelfInstall(auth); err != nil {
		return api.ArtifactSource{}, api.ArtifactImportConfig{}, err
	}
	source, err := api.ParseGitHubSource(sourceURL)
	unpinned := err != nil
	if unpinned && c.SourceResolver != nil {
		source, err = c.SourceResolver(ctx, sourceURL)
	}
	if err != nil {
		return api.ArtifactSource{}, api.ArtifactImportConfig{}, err
	}
	// A bare URL means "this repository", not mutable HEAD: with a reviewed
	// prepared entry its pinned commit is the source, never drifting code.
	if unpinned {
		if prepared, ok, perr := api.PreparedForRepository(source.Repository, source.Subfolder); perr == nil && ok {
			source = prepared.Source
		}
	}
	return source, api.DefaultSelfInstallConfig(source), nil
}

// prepareClaimTTL bounds one in-flight prepare: past it the record belongs to
// a crashed or restarted build and the next call must reclaim it.
const prepareClaimTTL = 35 * time.Minute

// stalePreparing reports a preparing record past the claim window. Without
// this check a crashed prepare deadlocks its request_key: every repeat call
// early-returns the dead stub and no review ever restarts.
func (c *ControlPlane) stalePreparing(onboarding api.Onboarding) bool {
	return onboarding.Phase == api.PhasePreparing && !c.now().Before(onboarding.CreatedAt.Add(prepareClaimTTL))
}

// ensurePreparing registers a preparing record before review+build so status
// works mid-flight and a failed prepare leaves a durable failure instead of
// "record not found". A fresh record already in preparing means an identical
// prepare is running — callers must not spawn a second review+build. A stub
// older than the background deadline belongs to a crashed prepare and is
// overwritten to retry.
func (c *ControlPlane) ensurePreparing(auth identity.Envelope, source api.ArtifactSource, config api.ArtifactImportConfig, requestKey string, shared bool) (api.Onboarding, bool, error) {
	idSeed := requestKey
	if idSeed == "" {
		idSeed = string(api.OnboardingSelfInstall) + ":" + config.DefinitionID + "@" + config.Version + ":" + source.Repository + ":" + source.CommitSHA + ":" + source.Tag + ":" + source.Asset + ":" + source.PackageRegistry + ":" + source.PackageName + ":" + source.PackageVersion
	}
	if shared {
		idSeed += "|shared"
	}
	preparing := api.Onboarding{
		Schema: api.SchemaVersion, OnboardingID: api.DeterministicID("onboard", auth.PrincipalID, auth.ContextID, auth.RuntimeID, idSeed),
		PrincipalID: auth.PrincipalID, ContextID: auth.ContextID, RuntimeID: auth.RuntimeID, PolicyVersion: auth.PolicyVersion,
		Mode: api.OnboardingSelfInstall, Phase: api.PhasePreparing, SourceURL: source.Repository, CommitSHA: source.CommitSHA,
		DefinitionID: config.DefinitionID, DefinitionVersion: config.Version,
		IdempotencyKey: requestKey, Revision: 1, CreatedAt: c.now(),
	}
	return c.Store.ClaimPreparingOnboarding(preparing, prepareClaimTTL, c.now())
}

// latestPrepareRecord resolves the onboarding a backgrounded prepare actually
// produced: normally the stub's own record, but a patch bump can move the
// result to a sibling id. Best effort for the wake notification.
func (c *ControlPlane) latestPrepareRecord(auth identity.Envelope, stub api.Onboarding) api.Onboarding {
	if current, err := c.Store.onboarding(stub.OnboardingID); err == nil && current.Phase != api.PhaseRemoved {
		return current
	}
	c.Store.mu.RLock()
	defer c.Store.mu.RUnlock()
	latest := stub
	for _, candidate := range c.Store.onboardings {
		if candidate.PrincipalID != auth.PrincipalID || candidate.ContextID != auth.ContextID || candidate.RuntimeID != auth.RuntimeID || candidate.PolicyVersion != auth.PolicyVersion || candidate.Phase == api.PhaseRemoved {
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

func (c *ControlPlane) finishSelfInstall(ctx context.Context, auth identity.Envelope, preparing api.Onboarding, source api.ArtifactSource, requestKey string, selected *api.RecipeCandidate, shared bool, credentialSource string) (map[string]any, error) {
	failPrepare := func(err error) (map[string]any, error) {
		if latest, latestErr := c.Store.onboarding(preparing.OnboardingID); latestErr == nil && latest.Phase == api.PhasePreparing {
			latest.Phase = api.PhaseFailed
			if persistErr := c.persistFailure(latest, api.PublicPrepareError(err), err); persistErr != err {
				return nil, persistErr
			}
		}
		return nil, err
	}
	review := api.SourceReview{}
	var err error
	if existing, ok := c.Store.reusableSelfInstallDefinition(auth, source, preparing.DefinitionID, preparing.DefinitionVersion); !shared && selected == nil && ok && c.selfInstallUsable(existing) && completeToolSchemas(existing) && selfInstallContractIntact(existing) && !api.DeploySecretRequired(existing) {
		review = api.SourceReview{Definition: existing, Permissions: api.ToolNames(existing), Effects: api.EffectNames(existing), ReviewDigest: existing.Source.ReviewDigest}
	} else {
		if c.Reviewer == nil {
			return failPrepare(fmt.Errorf("%w: source reviewer unavailable", api.ErrInvalid))
		}
		review, err = c.Reviewer(ctx, source, selected)
		if err != nil {
			return failPrepare(err)
		}
	}
	credentialSources := recipeCredentialSources(review.Recipe)
	if credentialSource != "" {
		if review.Recipe == nil {
			return failPrepare(fmt.Errorf("%w: credential_source %q has no discovered recipe to filter", api.ErrInvalid, credentialSource))
		}
		filtered, _ := api.FilterConnectionSource(review.Recipe.Connection, credentialSource)
		if len(filtered.Fields) == 0 && len(review.Recipe.Connection.Fields) > 0 {
			return failPrepare(fmt.Errorf("%w: credential_source %q not among %s", api.ErrInvalid, credentialSource, strings.Join(credentialSources, ", ")))
		}
		review.Recipe.Connection = filtered
		api.MergeConnectionForDefinition(filtered, &review.Definition)
	}
	if review.AdmissionPending {
		return c.acceptCredentialGate(ctx, auth, preparing, review)
	}
	// The shared class must be on the definition before contract binding so a
	// synthesized contract is registered with allow_shared — a shared
	// workload's binding may only reference shared-capable contracts.
	if shared {
		review.Definition.Workload.Class = api.Shared
		review.Definition.Workload.Sharable = true
		review.Definition.Workload.Rationale = "operator-shared repository connector; catalog-visible, members bind one policy-owned connection and credential"
	}
	if err := c.bindReviewedContract(ctx, auth, &review.Definition); err != nil {
		return failPrepare(err)
	}
	if !c.selfInstallUsable(review.Definition) {
		if c.Broker != nil && c.Broker.Enabled() && coverableCredentials(review.Definition) && review.Definition.CredentialContractID == "" {
			names := requiredCredentialNames(review.Definition)
			if len(names) == 0 {
				for _, input := range review.Definition.Credentials {
					if input.PerRequest || !contractCoverable(input) {
						continue
					}
					names = append(names, input.Name)
				}
				slices.Sort(names)
			}
			return failPrepare(fmt.Errorf("%w: no reviewed credential broker contract delivers %s for %s — an operator must add a contract covering these names before onboarding can collect them", api.ErrUnauthorized, strings.Join(names, ", "), review.Definition.DefinitionID))
		}
		return failPrepare(fmt.Errorf("%w: no reviewed credential broker contract covers %s credentials", api.ErrUnauthorized, review.Definition.DefinitionID))
	}
	// The produced definition may differ from an immutable record stored under
	// the same id+version by an older pipeline (for example, without a broker
	// contract binding). Bump the patch component until the key is free or the
	// stored record is identical.
	for i := 0; i < 100; i++ {
		stored, lookupErr := c.Store.Definition(review.Definition.DefinitionID, review.Definition.Version)
		if lookupErr != nil || api.DefinitionsEqual(stored, review.Definition) {
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
	if err := c.Store.PutPublication(api.DefinitionPublication{DefinitionID: review.Definition.DefinitionID, Version: review.Definition.Version, Visibility: api.PublicationUser, OwnerPrincipalID: auth.PrincipalID}); err != nil {
		return failPrepare(err)
	}
	if shared {
		if err := c.Store.PromoteToCatalog(review.Definition.DefinitionID, review.Definition.Version, auth.PrincipalID); err != nil {
			return failPrepare(err)
		}
	}
	onboarding, err := c.newOnboarding(auth, api.OnboardingSelfInstall, requestKey, review.Definition, source.Repository, source.CommitSHA)
	if err != nil {
		return failPrepare(err)
	}
	if onboarding.OnboardingID != preparing.OnboardingID {
		// A patch bump or a reviewed definition id shifted the deterministic
		// key; callers holding the stub id follow SupersededBy to the result.
		preparing.Phase = api.PhaseRemoved
		preparing.SupersededBy = onboarding.OnboardingID
		if err := c.Store.PutOnboarding(preparing); err != nil {
			log.Printf("toolhub: superseded onboarding stub %s not recorded: %v", preparing.OnboardingID, err)
		}
	}
	onboarding.Permissions = append([]string(nil), review.Permissions...)
	onboarding.Effects = append([]string(nil), review.Effects...)
	onboarding.ReviewDigest = review.ReviewDigest
	onboarding.Subfolder = source.Subfolder
	onboarding.CredentialSources = credentialSources
	if review.Recipe != nil {
		copyRecipe := *review.Recipe
		onboarding.Recipe = &copyRecipe
		onboarding.Required = credentialHintsForRecipe(review.Definition, review.Recipe.Connection)
	}
	// Recipe hints can name required inputs the secret-name heuristic missed —
	// a URL-only contract needs no token. The phase must reflect the final
	// Required set or the onboarding deadlocks: confirm rejects a missing
	// locator while the broker URL only surfaces in awaiting-credentials.
	if len(onboarding.Required) > 0 && onboarding.Locator == "" && onboarding.Phase == api.PhaseAwaitingConfirm {
		onboarding.Phase = api.PhaseAwaitingCreds
		onboarding.FormNonce = api.RandomNonce()
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

func recipeCredentialSources(recipe *api.RecipeResolution) []string {
	if recipe == nil {
		return nil
	}
	return api.ConnectionSources(recipe.Connection)
}

// rebuildCredentialSource applies the user's authoritative metadata source to
// an existing onboarding's recipe. The hint set and the embedded definition
// copy are rebuilt from that source alone; the registered definition and the
// reviewed broker contract stay unchanged.
func (c *ControlPlane) rebuildCredentialSource(onboarding *api.Onboarding, source string) error {
	if onboarding == nil || onboarding.Recipe == nil {
		return fmt.Errorf("%w: credential_source %q has no discovered recipe to filter", api.ErrInvalid, source)
	}
	filtered, sources := api.FilterConnectionSource(onboarding.Recipe.Connection, source)
	if len(filtered.Fields) == 0 && len(onboarding.Recipe.Connection.Fields) > 0 {
		return fmt.Errorf("%w: credential_source %q not among %s", api.ErrInvalid, source, strings.Join(sources, ", "))
	}
	onboarding.Recipe.Connection = filtered
	defForHints := api.ToolDefinition{Credentials: filtered.CredentialInputs()}
	if onboarding.Definition != nil {
		onboarding.Definition.Credentials = defForHints.Credentials
		defForHints = *onboarding.Definition
	}
	onboarding.Required = credentialHintsForRecipe(defForHints, filtered)
	onboarding.Optional = optionalCredentialHints(defForHints)
	onboarding.CredentialSources = sources
	onboarding.Revision++
	return c.Store.PutOnboarding(*onboarding)
}

// selfInstallUsable reports whether a stored definition can proceed past the
// broker-contract gate, so stale records predating contract binding are not
// reused and re-registered into an eternal failure.
func completeToolSchemas(definition api.ToolDefinition) bool {
	if definition.Source.ToolContractSource != api.ToolContractPreflight {
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
func selfInstallContractIntact(definition api.ToolDefinition) bool {
	return api.VerifyDefinitionToolContract(definition) == nil
}

func (c *ControlPlane) selfInstallUsable(definition api.ToolDefinition) bool {
	entry, matched, err := api.PreparedForSource(api.ArtifactSource{Repository: definition.Source.Repository, CommitSHA: definition.Source.CommitSHA, Subfolder: definition.Source.Subfolder})
	if err != nil {
		return false
	}
	if matched && (definition.Workload.Stateful != entry.Stateful || !maps.Equal(definition.RuntimeEnvironment, entry.RuntimeEnvironment) || definition.CredentialContractID != entry.ContractID || definition.CredentialContractRevision != entry.ContractRevision || definition.Source.Command != entry.Entrypoint[0] || !slices.Equal(definition.Source.Args, entry.Entrypoint[1:])) {
		return false
	}
	if !selfInstallArtifactPresent(definition) {
		return false
	}
	return c == nil || c.Broker == nil || !c.Broker.Enabled() || !coverableCredentials(definition) || definition.CredentialContractID != ""
}

// coverableCredentials reports whether the definition declares any input a
// broker contract can collect and deliver. Local-plane credentials
// (http_header, brokered, oauth) are collected elsewhere, so a definition
// carrying only those needs no contract at all.
func coverableCredentials(definition api.ToolDefinition) bool {
	for _, input := range definition.Credentials {
		if !input.PerRequest && contractCoverable(input) {
			return true
		}
	}
	return false
}

// selfInstallArtifactPresent requires the reviewed OCI tar to still sit in the
// quarantine store. A reused definition whose archive is gone wedges admission
// on "reviewed artifact image unavailable" the moment the daemon image is
// pruned, so reuse falls back to a full review that rebuilds the durable copy.
// Integrity is not re-checked here: OpenStoredOCIArtifact verifies checksum and
// OCI evidence on every read before the image enters the daemon.
func selfInstallArtifactPresent(definition api.ToolDefinition) bool {
	digest := strings.TrimSpace(definition.Source.ArchiveDigest)
	if digest == "" {
		return true
	}
	artifacts, _ := api.ControlArtifactPaths(api.EnvOr("HUB_STATE", "/state"))
	if artifacts == "" || !api.DigestPattern.MatchString(digest) {
		return false
	}
	if err := api.SafeStorePath(filepath.Join(artifacts, "artifact-check")); err != nil {
		return false
	}
	root, err := os.OpenRoot(artifacts)
	if err != nil {
		return false
	}
	defer root.Close()
	info, err := root.Lstat(api.ArtifactObjectName(digest))
	return err == nil && info.Mode().IsRegular()
}
