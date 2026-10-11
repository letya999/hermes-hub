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
	"regexp"
	"slices"
	"strings"
	"sync"
	"time"

	brokerv1 "github.com/letya999/credential-broker/api/v1"
	brokerclient "github.com/letya999/credential-broker/client"
	"github.com/letya999/hermes-hub/internal/credentialbroker"
	"github.com/letya999/hermes-hub/internal/credstore"
	"github.com/letya999/hermes-hub/internal/identity"
	"github.com/letya999/hermes-hub/internal/oauth"
)

type SourceReview struct {
	Definition       ToolDefinition
	Permissions      []string
	Effects          []string
	ReviewDigest     string
	Recipe           *RecipeResolution
	AdmissionPending bool
	AdmissionDetail  string
	AdmissionGroups  [][]string
}

type SourceReviewer func(context.Context, ArtifactSource, *RecipeCandidate) (SourceReview, error)
type SourceResolver func(context.Context, string) (ArtifactSource, error)

type ControlPlane struct {
	Store          *Store
	Secrets        credstore.Backend
	Reviewer       SourceReviewer
	SourceResolver SourceResolver
	RecipeCatalogs []RecipeCatalog
	OAuth          *oauth.Broker
	Injector       CredentialInjector
	admissionMu    sync.Mutex
	oauthMu        sync.Mutex
	oauthFlows     map[string]*preparedOAuthFlow
	Now            func() time.Time
	Listen         string
	FormOrigin     string
	WorkloadRoot   string
	// DiagnosticsDir points at the bounded operator diagnostics directory; the
	// diagnostics control op reads only the collected log file inside it.
	DiagnosticsDir   string
	ConfirmationTTL  time.Duration
	Ready            func(context.Context, EffectiveBinding) error
	Broker           *credentialbroker.Config
	BrokerRuntime    *credentialbroker.Config
	discoveryMu      sync.Mutex
	discoveryChoices map[string]discoverySelection
	// Release asks the workload controller to stop a running workload. Revoke
	// and remove use it so a cut connector does not keep a materialized
	// credential alive in a running container until the idle TTL fires.
	Release func(context.Context, string) error
	// CLIRelease drops bounded-cli sandbox cells by principal/binding/job
	// selector: disable and revoke kill warm cells, the /v1 job-end route
	// kills task cells. Nil leaves cells to the controller's own ladder.
	CLIRelease func(context.Context, cliReleaseRequest) error
	// PrepareSyncWindow bounds how long prepare_source waits for review+build
	// before returning the durable "preparing" record; the work then continues
	// on a detached context and its result is read via status. Zero uses the
	// default; negative runs the whole prepare synchronously (tests).
	PrepareSyncWindow time.Duration
	// PrepareDone, when set, runs after a backgrounded prepare finishes —
	// whatever the outcome — so transports can wake open sessions.
	PrepareDone func(context.Context, identity.Envelope, Onboarding)
	// AdmitWithCredentials re-runs tools/list with owner-submitted secrets
	// after a server refused MCP until those secrets existed. Nil means a
	// deferred admission cannot be completed.
	AdmitWithCredentials func(context.Context, ToolDefinition, map[string]string) (ToolDefinition, error)
	// CLIArtifacts is the owner bounded-cli artifact pipeline (immutable
	// source → restricted build → digest-pinned extracted binary). Nil fails
	// the cli artifact source path closed.
	CLIArtifacts *CLIArtifactPipeline
	// Governance is the host-owned tool policy document (issue 139). The
	// grant_request control op stamps the in-conversation request onto a
	// host-created pending grant; nil disables the op entirely.
	Governance *GovernanceStore
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
	if err := c.Store.RequireControlOperation(auth, op); err != nil {
		return nil, err
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
	case "diagnostics":
		return c.diagnostics(ctx, auth, args)
	case "grant_request":
		return c.grantRequest(auth, args)
	default:
		return nil, fmt.Errorf("%w: unknown control operation", ErrInvalid)
	}
}

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

func (c *ControlPlane) acceptCredentialGate(ctx context.Context, auth identity.Envelope, preparing Onboarding, review SourceReview) (map[string]any, error) {
	definition := review.Definition
	brokerGate := definition.CredentialContractID != ""
	if brokerGate && (c.Broker == nil || !c.Broker.Enabled()) {
		return nil, fmt.Errorf("%w: reviewed credential Broker is required for authenticated preflight", ErrIsolation)
	}
	if len(requiredSecretNames(definition)) == 0 && !brokerGate {
		return nil, fmt.Errorf("%w: credential gate named no secrets", ErrInvalid)
	}
	names := requiredCredentialNames(definition)
	hints := credentialGateHints(names, review.AdmissionGroups)
	if len(hints) == 0 {
		return nil, fmt.Errorf("%w: credential gate named no secrets", ErrInvalid)
	}
	if len(definition.CredentialGroups) == 0 {
		known := map[string]bool{}
		for _, name := range names {
			known[name] = true
		}
		for _, group := range review.AdmissionGroups {
			var members []string
			for _, name := range group {
				if known[name] {
					members = append(members, name)
				}
			}
			if len(members) > 0 {
				definition.CredentialGroups = append(definition.CredentialGroups, members)
			}
		}
		if len(definition.CredentialGroups) < 2 {
			definition.CredentialGroups = nil
		}
	}
	onboarding := preparing
	if latest, err := c.Store.onboarding(preparing.OnboardingID); err == nil {
		onboarding = latest
	}
	// The draft is not a tool contract. Placeholder import tools must not be
	// listed or registered until tools/list succeeds with the submitted secrets.
	definition.Tools = nil
	definition.Source.ToolContractDigest = ""
	definition.Source.ToolContractSource = ""
	onboarding.Phase = PhaseAwaitingCreds
	onboarding.Required = hints
	onboarding.AdmissionPending = true
	onboarding.Error = publicPrepareError(errors.New(review.AdmissionDetail))
	onboarding.Permissions = nil
	onboarding.Effects = nil
	onboarding.Definition = &definition
	if definition.DefinitionID != "" {
		onboarding.DefinitionID = definition.DefinitionID
	}
	if definition.Version != "" {
		onboarding.DefinitionVersion = definition.Version
	}
	onboarding.ReviewDigest = ""
	if brokerGate {
		onboarding.FormNonce = ""
		onboarding.FormExpires = time.Time{}
	} else {
		onboarding.FormNonce = randomNonce()
		onboarding.FormExpires = c.now().Add(c.ttl())
	}
	onboarding.ConfirmationNonce = ""
	onboarding.ConfirmationUsed = false
	onboarding.Revision++
	if err := c.Store.PutOnboarding(onboarding); err != nil {
		return nil, err
	}
	if brokerGate {
		if err := c.ensureBrokerRequest(ctx, auth, &onboarding, definition); err != nil {
			return nil, err
		}
	}
	return c.statusBody(onboarding, false), nil
}

func requiredSecretNames(definition ToolDefinition) []string {
	names := make([]string, 0, len(definition.Credentials))
	for _, input := range definition.Credentials {
		if input.Required && secretName(input.Name) {
			names = append(names, input.Name)
		}
	}
	return names
}

func credentialGateHints(names []string, groups [][]string) []CredentialHint {
	groupOf := map[string]int{}
	for index, group := range groups {
		for _, name := range group {
			groupOf[name] = index + 1
		}
	}
	hints := make([]CredentialHint, 0, len(names))
	for _, name := range names {
		hints = append(hints, CredentialHint{
			Name: name, Type: connectionType(name), Secret: secretName(name),
			Delivery: "env", Target: name, Hint: "protected loopback form",
			AlternativeGroup: groupOf[name],
		})
	}
	return hints
}

func filterSubmittedCredentials(hints []CredentialHint, values map[string]string) (map[string]string, error) {
	alternatives := map[int][]CredentialHint{}
	filtered := map[string]string{}
	for _, hint := range hints {
		if hint.AlternativeGroup > 0 {
			alternatives[hint.AlternativeGroup] = append(alternatives[hint.AlternativeGroup], hint)
			continue
		}
		value := strings.TrimSpace(values[hint.Name])
		if value == "" {
			return nil, fmt.Errorf("%w: missing %s", ErrInvalid, hint.Name)
		}
		filtered[hint.Name] = value
	}
	if len(alternatives) == 0 {
		return filtered, nil
	}
	matched := 0
	partial := false
	var missingNames []string
	var chosen map[string]string
	completeGroups := []map[string]string{}
	for _, group := range alternatives {
		filled := map[string]string{}
		missing := false
		for _, hint := range group {
			value := strings.TrimSpace(values[hint.Name])
			if value == "" {
				missing = true
				continue
			}
			filled[hint.Name] = value
		}
		if missing {
			if len(filled) > 0 {
				partial = true
				for _, hint := range group {
					if strings.TrimSpace(values[hint.Name]) == "" {
						missingNames = append(missingNames, hint.Name)
					}
				}
			}
			continue
		}
		if len(filled) > 0 {
			matched++
			chosen = filled
			completeGroups = append(completeGroups, filled)
		}
	}
	if matched != 1 {
		if picked, ok := pickDuplicatedAlternative(completeGroups); ok {
			chosen = picked
			matched = 1
		}
	}
	if matched == 0 && partial {
		return nil, fmt.Errorf("%w: incomplete credential alternative: %s", ErrInvalid, strings.Join(missingNames, ","))
	}
	if matched != 1 {
		return nil, fmt.Errorf("%w: choose one credential alternative", ErrInvalid)
	}
	for name, value := range chosen {
		filtered[name] = value
	}
	return filtered, nil
}

// pickDuplicatedAlternative keeps one group when the same secret was copied
// into every alternative. The kept group is the one whose env name contains
// that secret's prefix, the text before the first "-" or "_".
func pickDuplicatedAlternative(groups []map[string]string) (map[string]string, bool) {
	if len(groups) < 2 {
		return nil, false
	}
	uniq := map[string]struct{}{}
	for _, group := range groups {
		for _, value := range group {
			uniq[value] = struct{}{}
		}
	}
	if len(uniq) != 1 {
		return nil, false
	}
	var only string
	for value := range uniq {
		only = value
	}
	prefix := only
	if i := strings.IndexAny(prefix, "-_"); i > 0 {
		prefix = prefix[:i]
	}
	if len(prefix) < 3 || len(prefix) > 12 {
		return nil, false
	}
	var hits []map[string]string
	needle := strings.ToUpper(prefix)
	for _, group := range groups {
		for name := range group {
			if strings.Contains(strings.ToUpper(name), needle) {
				hits = append(hits, group)
				break
			}
		}
	}
	if len(hits) != 1 {
		return nil, false
	}
	return hits[0], true
}

func (c *ControlPlane) noteFormRejection(onboarding Onboarding, err error) {
	if c == nil || c.Store == nil || err == nil || !errors.Is(err, ErrInvalid) {
		return
	}
	onboarding.Error = credentialFormRejection(err)
	onboarding.Revision++
	if persistErr := c.Store.PutOnboarding(onboarding); persistErr != nil {
		log.Printf("toolhub: form rejection on %s not recorded: %v", onboarding.OnboardingID, persistErr)
	}
}

func credentialFormRejection(err error) string {
	if err == nil {
		return ""
	}
	text := err.Error()
	switch {
	case strings.Contains(text, "incomplete credential alternative"):
		if missing := missingAlternativeNames(text); missing != "" {
			return "В выбранном наборе заполнены не все поля. Не заполнено: " + missing + "."
		}
		return "В выбранном наборе заполнены не все поля."
	case strings.Contains(text, "choose one credential alternative"):
		return "Нужен один полный набор полей."
	case strings.Contains(text, "unrecognized credential alternative"):
		return "Это поле не входит в рецепт сервера."
	case strings.Contains(text, "missing "):
		return "Не заполнено обязательное поле."
	default:
		return publicPrepareError(err)
	}
}

func missingAlternativeNames(text string) string {
	const marker = "incomplete credential alternative:"
	index := strings.Index(text, marker)
	if index < 0 {
		return ""
	}
	var labels []string
	for _, part := range strings.Split(text[index+len(marker):], ",") {
		name := strings.TrimSpace(part)
		if name == "" || strings.ContainsAny(name, " \t") {
			break
		}
		labels = append(labels, credentialFieldLabel(CredentialHint{Name: name}))
	}
	return strings.Join(labels, ", ")
}

// credentialProbeFailure is the text shown on the form and stored for the
// channel. It keeps the server's message and drops stack traces.
func credentialProbeFailure(err error) string {
	text := publicPrepareError(err)
	if text == "" {
		return ""
	}
	var messages []string
	rest := text
	const marker = `"message":"`
	for {
		index := strings.Index(rest, marker)
		if index < 0 {
			break
		}
		rest = rest[index+len(marker):]
		end := strings.Index(rest, `"`)
		if end <= 0 || end > 180 {
			break
		}
		messages = append(messages, rest[:end])
		rest = rest[end+1:]
	}
	for i := len(messages) - 1; i >= 0; i-- {
		lower := strings.ToLower(messages[i])
		if strings.Contains(lower, "fail") || strings.Contains(lower, "invalid") || strings.Contains(lower, "error") || strings.Contains(lower, "denied") {
			return messages[i]
		}
	}
	if strings.Contains(text, "tools/list empty") {
		return "Сервер не ответил списком инструментов."
	}
	if len(messages) > 0 {
		return messages[len(messages)-1]
	}
	if len(text) > 240 {
		text = text[:240]
	}
	return text
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

var secretValuePattern = regexp.MustCompile(`(?i)\b(xox[baprs]-[A-Za-z0-9-]{8,}|sk-[A-Za-z0-9]{16,}|[A-Fa-f0-9]{32,})\b`)

// persistFailure records the public failure reason on the onboarding before
// returning the original error. A failed state write is folded into the
// returned error instead of silently dropping both cause and state update.
func (c *ControlPlane) persistFailure(onboarding Onboarding, errorText string, cause error) error {
	onboarding.Error = errorText
	onboarding.Revision++
	if err := c.Store.PutOnboarding(onboarding); err != nil {
		return fmt.Errorf("%w (recording failure state failed: %v)", cause, err)
	}
	return cause
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
		// A definition that already pins a reviewed revision must not slide to
		// an older revision of the same contract still present in the broker.
		if definition.CredentialContractRevision > 0 && candidate.Revision != definition.CredentialContractRevision {
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
		if !definition.credentialsSatisfied(func(name string) bool { return delivered[env[name]] }) {
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
	// Bounded-cli credentials are owner-declared environment values, not
	// provider sessions: without a reviewed broker contract the protected
	// loopback form carries them. A bound contract keeps the broker path.
	if definition.Transport == BoundedCLI && definition.CredentialContractID == "" {
		return nil
	}
	if definition.CredentialContractID == "" || definition.CredentialContractRevision < 1 || len(definition.CredentialContractEnv) == 0 {
		return fmt.Errorf("%w: reviewed credential broker contract is required for %s", ErrUnauthorized, definition.DefinitionID)
	}
	control, err := c.Broker.New(auth, "broker:control")
	if err != nil {
		return err
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
			if ref.Backend == "credential-broker" && ref.BrokerContractID == definition.CredentialContractID && ref.BrokerContractRevision == definition.CredentialContractRevision && brokerCredentialActive(ctx, control, ref.Locator) {
				onboarding.Locator, onboarding.BrokerCredentialID = ref.Locator, ref.Locator
				onboarding.BrokerContractID, onboarding.BrokerContractRevision = ref.BrokerContractID, ref.BrokerContractRevision
				onboarding.Phase = PhaseAwaitingConfirm
				onboarding.FormNonce = ""
				onboarding.FormExpires = time.Time{}
				onboarding.ConfirmationNonce = randomNonce()
				onboarding.ConfirmationExpires = c.now().Add(c.ttl())
				onboarding.Revision++
				if err := c.Store.PutOnboarding(*onboarding); err != nil {
					return err
				}
				return c.refreshBrokerRequest(ctx, auth, onboarding)
			}
		}
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
		consumerID := definition.DefinitionID
		if onboarding.BrokerRotateCredentialID != "" {
			var credential brokerv1.Credential
			if err := control.Do(ctx, http.MethodGet, "/v1/credentials/"+url.PathEscape(onboarding.BrokerRotateCredentialID), nil, &credential); err != nil {
				return err
			}
			connectionID = credential.ConnectionID
			consumerID = credential.ConsumerID
		}
		request, err := control.CreateRequest(ctx, brokerv1.CreateRequest{
			ContractID: definition.CredentialContractID, ContractRevision: definition.CredentialContractRevision,
			ConnectionID: connectionID, ConsumerID: consumerID, RotateCredentialID: onboarding.BrokerRotateCredentialID,
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

// brokerCredentialActive verifies a stored broker credential is still live
// before a new onboarding reuses it. Revocation happens at the broker, so the
// toolhub connection record alone cannot prove the credential works.
func brokerCredentialActive(ctx context.Context, control *brokerclient.Client, id string) bool {
	if id == "" {
		return false
	}
	var credential brokerv1.Credential
	if err := control.Do(ctx, http.MethodGet, "/v1/credentials/"+url.PathEscape(id), nil, &credential); err != nil {
		return false
	}
	return credential.Status == "active"
}

func (c *ControlPlane) refreshBrokerRequest(ctx context.Context, auth identity.Envelope, onboarding *Onboarding) error {
	if c == nil || c.Broker == nil || !c.Broker.Enabled() || onboarding == nil {
		return nil
	}
	if onboarding.BrokerRequestID == "" {
		if onboarding.AdmissionPending && onboarding.BrokerCredentialID != "" {
			return c.completeBrokerAdmission(ctx, auth, onboarding, onboarding.BrokerCredentialID)
		}
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
		if onboarding.AdmissionPending {
			return c.completeBrokerAdmission(ctx, auth, onboarding, request.CredentialID)
		}
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

func (c *ControlPlane) completeBrokerAdmission(ctx context.Context, auth identity.Envelope, onboarding *Onboarding, credentialID string) error {
	c.admissionMu.Lock()
	defer c.admissionMu.Unlock()
	current, err := c.Store.OnboardingFor(auth, onboarding.OnboardingID)
	if err != nil {
		return err
	}
	if !current.AdmissionPending {
		*onboarding = current
		return nil
	}
	if current.BrokerRequestID != onboarding.BrokerRequestID || current.BrokerCredentialID != "" && current.BrokerCredentialID != credentialID && current.BrokerRotateCredentialID != current.BrokerCredentialID {
		return ErrStale
	}
	if strings.HasPrefix(current.Error, "Проверка не прошла") {
		*onboarding = current
		return nil
	}
	definition, err := c.definitionOf(current)
	if err != nil {
		return err
	}
	admitted, err := c.admitWithBrokerCredential(ctx, auth, current, definition, credentialID)
	if err == nil && !c.selfInstallUsable(admitted) {
		err = fmt.Errorf("%w: admitted Broker definition differs from reviewed entry", ErrUnauthorized)
	}
	if err == nil {
		err = c.registerAdmittedDefinition(&admitted, current.PrincipalID)
	}
	latest, readErr := c.Store.OnboardingFor(auth, current.OnboardingID)
	if readErr != nil {
		return readErr
	}
	if !latest.AdmissionPending || latest.Phase != current.Phase || latest.BrokerRequestID != current.BrokerRequestID || latest.BrokerRotateCredentialID != current.BrokerRotateCredentialID {
		return ErrStale
	}
	latest.BrokerCredentialID, latest.Locator = credentialID, credentialID
	latest.FormNonce, latest.FormExpires = "", time.Time{}
	if err != nil {
		latest.Phase = PhaseAwaitingCreds
		latest.Error = "Проверка не прошла. " + credentialProbeFailure(err)
	} else {
		latest.Definition = &admitted
		latest.DefinitionID, latest.DefinitionVersion = admitted.DefinitionID, admitted.Version
		latest.Permissions, latest.Effects = toolNames(admitted), effectNames(admitted)
		latest.ReviewDigest = admitted.Source.ReviewDigest
		latest.AdmissionPending = false
		latest.Error = ""
		latest.Phase = PhaseAwaitingConfirm
		latest.ConfirmationNonce = randomNonce()
		latest.ConfirmationExpires = c.now().Add(c.ttl())
		latest.ConfirmationUsed = false
	}
	latest.Revision++
	if err := c.Store.PutOnboarding(latest); err != nil {
		return err
	}
	*onboarding = latest
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
	// Unprepared servers with no reviewed contract still use the protected
	// loopback form. A prepared authenticated source resumes through Broker.
	if onboarding.AdmissionPending && definition.CredentialContractID == "" {
		return nil
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
	if onboarding.BrokerRequestID != "" || onboarding.AdmissionPending && onboarding.BrokerCredentialID != "" {
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
	if err := c.refreshBrokerRequest(ctx, auth, &onboarding); err != nil {
		return nil, err
	}
	definition, err := c.definitionOf(onboarding)
	if err != nil {
		return nil, err
	}
	if err := rejectEscalation(definition, args); err != nil {
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
		return nil, c.persistFailure(onboarding, publicPrepareError(err), err)
	}
	onboarding.ConfirmationUsed = true
	onboarding.BindingID = binding.ToolBindingID
	onboarding.ConnectionID = binding.ConnectionID
	onboarding.CredentialRefID = binding.CredentialRefID
	onboarding.BrokerRotateCredentialID = ""
	onboarding.Error = ""
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
			return nil, c.persistFailure(onboarding, publicPrepareError(err), err)
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
			return nil, c.persistFailure(onboarding, publicPrepareError(err), err)
		}
		onboarding.BindingID = binding.ToolBindingID
		onboarding.ConnectionID = binding.ConnectionID
		onboarding.CredentialRefID = binding.CredentialRefID
	} else if err := c.Store.SupersedeSiblings(auth, existing.DefinitionID, existing.ToolBindingID); err != nil {
		// An enable on an already-active binding short-circuits Store.Enable,
		// so it is the only place sibling versions can still be retired.
		return nil, err
	}
	onboarding.Error = ""
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
			// A remote-mcp onboarding stores its credential in the local
			// encrypted backend, never the Broker; fall through to the local
			// rotation path instead of deadlocking on a missing record.
			if onboarding.Locator == "" {
				return nil, ErrNotFound
			}
			return c.rotateLocal(auth, onboarding)
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
		onboarding.Error = ""
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

func (c *ControlPlane) rotateLocal(auth identity.Envelope, onboarding Onboarding) (map[string]any, error) {
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
	if err := c.Store.SetBindingStatus(onboarding.BindingID, RevokedStatus); err != nil && !errors.Is(err, ErrRevoked) && !errors.Is(err, ErrNotFound) {
		return nil, err
	}
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
		if err := c.Store.SetConnectionStatus(onboarding.ConnectionID, RevokedStatus); err != nil && !errors.Is(err, ErrRevoked) && !errors.Is(err, ErrNotFound) {
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
func (c *ControlPlane) releaseBindingWorkloads(onboarding Onboarding) {
	if c.CLIRelease != nil && onboarding.BindingID != "" && identity.ValidID(onboarding.PrincipalID) {
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		_ = c.CLIRelease(ctx, cliReleaseRequest{PrincipalID: onboarding.PrincipalID, BindingID: onboarding.BindingID, Reason: "binding-release"})
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
		if definition.Transport == ContainerMCP {
			if c.Release == nil {
				return ToolBinding{}, fmt.Errorf("%w: rotation requires workload release", ErrIsolation)
			}
			workloadID := WorkloadInstanceID(definition.DefinitionID, PerUser, auth.ContextID+":"+auth.PrincipalID+":"+connection.ConnectionID, "")
			if err := c.Release(ctx, workloadID); err != nil {
				return ToolBinding{}, err
			}
		}
		if c.brokerCredentialRequired(definition, onboarding) {
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
	if c.brokerCredentialRequired(definition, onboarding) {
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

// brokerCredentialRequired is true when the definition is bound to a reviewed
// broker contract or the onboarding already holds a broker credential. A
// credential-gate admission has neither, so its loopback secret stays local.
func (c *ControlPlane) brokerCredentialRequired(definition ToolDefinition, onboarding Onboarding) bool {
	return c != nil && c.Broker != nil && c.Broker.Enabled() && (definition.CredentialContractID != "" || onboarding.BrokerCredentialID != "")
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

func cloneStringGroups(groups [][]string) [][]string {
	if groups == nil {
		return nil
	}
	out := make([][]string, len(groups))
	for i, group := range groups {
		out[i] = append([]string(nil), group...)
	}
	return out
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
	if onboarding.AdmissionPending && onboarding.Definition != nil && onboarding.Definition.CredentialContractID != "" {
		return fmt.Errorf("%w: contracted credentials must be submitted to Broker", ErrUnauthorized)
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
	filtered, err := filterSubmittedCredentials(onboarding.Required, values)
	if err != nil {
		c.noteFormRejection(onboarding, err)
		return err
	}
	authEnv := onboardingEnvelope(onboarding)
	if onboarding.AdmissionPending {
		definition, defErr := c.definitionOf(onboarding)
		if defErr != nil {
			return defErr
		}
		if c.AdmitWithCredentials == nil {
			return fmt.Errorf("%w: credential admission probe is not configured", ErrInvalid)
		}
		onboarding.Error = "Проверяю поля у сервера."
		onboarding.Revision++
		if err := c.Store.PutOnboarding(onboarding); err != nil {
			return err
		}
		// The admission probe below can hold the form POST for tens of
		// seconds; tell the channel a real check is running, not a hang.
		deliverPrepareEvent(context.Background(), authEnv, onboarding, "credentials-check")
		admitted, admitErr := c.AdmitWithCredentials(context.Background(), definition, filtered)
		if admitErr != nil {
			failure := "Проверка не прошла. " + credentialProbeFailure(admitErr)
			if persistErr := c.persistFailure(onboarding, failure, admitErr); persistErr != admitErr {
				return persistErr
			}
			rejected := onboarding
			rejected.Error = failure
			deliverPrepareEvent(context.Background(), authEnv, rejected, "credentials-rejected")
			return fmt.Errorf("%w: %s", ErrIsolation, failure)
		}
		if regErr := c.registerAdmittedDefinition(&admitted, onboarding.PrincipalID); regErr != nil {
			return c.persistFailure(onboarding, credentialProbeFailure(regErr), regErr)
		}
		onboarding.Definition = &admitted
		onboarding.DefinitionID = admitted.DefinitionID
		onboarding.DefinitionVersion = admitted.Version
		onboarding.Permissions = toolNames(admitted)
		onboarding.Effects = effectNames(admitted)
		onboarding.ReviewDigest = admitted.Source.ReviewDigest
		onboarding.AdmissionPending = false
		onboarding.Error = ""
	}
	locator := onboarding.Locator
	owner := onboarding.PrincipalID
	shared := false
	c.Store.mu.RLock()
	if policy := c.Store.sharedPolicyLocked(authEnv, onboarding.DefinitionID); policy != nil {
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

// errCredentialSaved marks a failure after the loopback secret is already stored.
var errCredentialSaved = errors.New("credential saved")

// AuthorizeCredentials treats the protected form submission as the user's
// confirmation and completes the binding without another model round-trip.
func (c *ControlPlane) AuthorizeCredentials(ctx context.Context, onboardingID, nonce string, values map[string]string) error {
	if err := c.SubmitCredentials(onboardingID, nonce, values); err != nil {
		return err
	}
	if err := c.finishAuthorization(ctx, onboardingID); err != nil {
		if latest, lerr := c.Store.onboarding(onboardingID); lerr == nil {
			notify := latest
			notify.Error = err.Error()
			deliverPrepareEvent(ctx, onboardingEnvelope(latest), notify, "binding-failed")
		}
		return fmt.Errorf("%w: %w", errCredentialSaved, err)
	}
	return nil
}

// registerAdmittedDefinition stores a credential-gate manifest. An immutable
// record that differs, or a user publication owned by someone else, takes the
// next patch version instead of replacing the existing publication.
func (c *ControlPlane) registerAdmittedDefinition(definition *ToolDefinition, principalID string) error {
	if c == nil || c.Store == nil || definition == nil {
		return fmt.Errorf("%w: admitted definition", ErrInvalid)
	}
	for i := 0; i < 100; i++ {
		stored, err := c.Store.Definition(definition.DefinitionID, definition.Version)
		if err != nil {
			break
		}
		owner, userOwned := c.Store.userPublicationOwner(definition.DefinitionID, definition.Version)
		if definitionsEqual(stored, *definition) && (!userOwned || owner == principalID) {
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
			return fmt.Errorf("%w: user definition owner", ErrUnauthorized)
		}
		return nil
	}
	if _, published := c.Store.storedPublication(definition.DefinitionID, definition.Version); published {
		return nil
	}
	return c.Store.PutPublication(DefinitionPublication{DefinitionID: definition.DefinitionID, Version: definition.Version, Visibility: PublicationUser, OwnerPrincipalID: principalID})
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
	if body["phase"] == PhaseAwaitingOAuth {
		return nil
	}
	if _, err = c.enable(ctx, auth, map[string]any{"onboarding_id": onboardingID}); err != nil {
		return err
	}
	// The form submit drove this enable out-of-band; without a channel
	// notice the owner sees the form's success page but never the chat
	// confirmation, tools included.
	if latest, lerr := c.Store.onboarding(onboardingID); lerr == nil {
		postPrepareNotice(ctx, auth, latest, "", len(latest.Permissions))
	}
	return nil
}

func onboardingEnvelope(onboarding Onboarding) identity.Envelope {
	return identity.Envelope{Schema: identity.Schema, PrincipalID: onboarding.PrincipalID, ExternalIdentityID: onboarding.PrincipalID, ContextID: onboarding.ContextID, RuntimeID: onboarding.RuntimeID, ConversationID: onboarding.PrincipalID, DeliveryTargetID: onboarding.PrincipalID, PolicyVersion: onboarding.PolicyVersion}
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
		if onboarding.AdmissionPending {
			body["tools_confirmed"] = false
			if onboarding.BrokerContractID != "" {
				body["instructions"] = "The server needs provider login before tools/list. Open the Credential Broker authorization_url, enter credentials there, then call status. Never ask for a session in chat."
				if strings.HasPrefix(onboarding.Error, "Проверка не прошла") {
					body["next_action"] = "rotate"
					body["instructions"] = "The Broker credential failed admission. Read the recorded error; rotate this onboarding to enter a fresh credential."
				}
			} else {
				body["instructions"] = "The server did not answer tools/list until credentials exist. The channel notice already contains the loopback form URL on this host at 127.0.0.1:8090. Do not send a second link. Do not invent a CORS, browser-host, or blocked-port failure. Do not ask for a token in chat. The form lists the field names from the server or its connection recipe. The shortest set is open; submit only that set unless the user opens another. tools/list runs after a successful submit."
				switch {
				case strings.HasPrefix(onboarding.Error, "Проверяю поля"):
					body["instructions"] = "A credential submit is being checked with the server. Tell the user to wait. Do not repeat an older form error and do not ask for a token in chat."
				case strings.Contains(onboarding.Error, "набор") || strings.Contains(onboarding.Error, "Не заполнено") || strings.Contains(onboarding.Error, "рецепт"):
					body["instructions"] = "The previous form submit was rejected before the server was contacted. Read error and tell the user that text. Send the current form URL from required_credentials. Do not invent a network, CORS, or host failure, and do not ask for a token in chat."
				case strings.HasPrefix(onboarding.Error, "Проверка не прошла"):
					body["instructions"] = "The credential check already ran. Read error and tell the user that text. Do not repeat an older rejection and do not ask for a token in chat. The form remains available from required_credentials."
				}
			}
		}
	case PhaseAwaitingConfirm:
		body["next_action"] = "confirm"
		body["instructions"] = "Call confirm with onboarding_id and the nonce from this response, then call enable. No browser action is required."
		if onboarding.Error != "" {
			body["instructions"] = "The last confirm or enable failed; read error and tell the user that text verbatim. Do not invent a runtime restart, controller outage, or network cause. Retry confirm with the nonce only after the recorded cause is addressed or looks transient; do not poll in a loop."
		}
	case PhaseAwaitingOAuth:
		body["next_action"] = "authorize"
		body["instructions"] = "Send authorization_url to the user. After the browser callback, call status to verify the connection."
	case PhaseConfirmed:
		body["next_action"] = "enable"
		body["instructions"] = "Call enable with onboarding_id."
	case PhaseEnabled:
		body["next_action"] = "ready"
	}
	if onboarding.Error != "" {
		body["error"] = onboarding.Error
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

func (s *Store) storedPublication(id, version string) (DefinitionPublication, bool) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	pub, ok := s.publications[definitionKey(id, version)]
	return pub, ok
}

func (s *Store) userPublicationOwner(id, version string) (string, bool) {
	pub, ok := s.storedPublication(id, version)
	if !ok || pub.Visibility != PublicationUser {
		return "", false
	}
	return pub.OwnerPrincipalID, true
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

// brokerGrantReusable reports whether the broker grant stored on a reused
// credential still authorizes the binding this enable would run under. A grant
// is minted for the deterministic binding ID — which folds in the definition
// version and credential reference — and pins the admitting policy epoch. A
// stored binding that still resolves to that identity proves the grant covers
// it; any version or epoch change requires a fresh grant.
func (s *Store) brokerGrantReusable(auth identity.Envelope, definition ToolDefinition, match ownerConnectionMatch) bool {
	if match.credential.BrokerGrantID == "" {
		return false
	}
	id := DeterministicBindingID(auth.PrincipalID, auth.ContextID, auth.RuntimeID, definition.DefinitionID, definition.Version, match.connection.ConnectionID, match.credential.CredentialRefID)
	s.mu.RLock()
	defer s.mu.RUnlock()
	binding, ok := s.bindings[id]
	return ok && binding.Status != RevokedStatus && binding.PolicyVersion == auth.PolicyVersion
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
	groupOf := map[string]int{}
	for index, group := range definition.CredentialGroups {
		for _, name := range group {
			groupOf[name] = index + 1
		}
	}
	hints := make([]CredentialHint, 0, len(definition.Credentials))
	for _, input := range definition.Credentials {
		upper := strings.ToUpper(input.Name)
		// Every required stored input gates onboarding, not only secret-named
		// ones — a URL-only contract otherwise skips the credentials phase and
		// deadlocks at confirm on a locator that was never collected. Per-request
		// inputs are supplied at call time, not stored during onboarding.
		if !input.Required || input.PerRequest {
			continue
		}
		kind := connectionType(input.Name)
		delivery := "env"
		if strings.Contains(upper, "CREDENTIALS") {
			kind, delivery = "json", "json"
		}
		hints = append(hints, CredentialHint{Name: input.Name, Type: kind, Secret: secretName(input.Name), Delivery: delivery, Target: input.Name, AlternativeGroup: groupOf[input.Name], Hint: "protected loopback form"})
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
