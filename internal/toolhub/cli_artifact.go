package toolhub

// Owner-scoped bounded-cli artifact onboarding: an immutable Git commit goes
// through the same restricted OCI build as MCP artifacts, and the resulting
// definition carries the verified image + manifest digest plus the declared
// binary's guest path. Cells run the image itself — the binary never leaves
// its verified layer, so no extracted host file can drift under the pin.

import (
	"context"
	"fmt"
	"log"
	"net/http"
	"path"
	"strings"
	"time"

	"github.com/letya999/hermes-hub/internal/identity"
)

// CLIArtifactPipeline is the owner CLI artifact seam: production wiring runs
// the restricted build; tests substitute fakes. A nil pipeline fails the
// cli_artifact source path closed.
type CLIArtifactPipeline struct {
	Build func(ctx context.Context, source ArtifactSource, config ArtifactImportConfig) (ImportedArtifact, error)
}

// CLIArtifactPipelineFromEnv wires the production pipeline: the restricted
// build reuses the configured artifact directory and seccomp profile.
func CLIArtifactPipelineFromEnv() (*CLIArtifactPipeline, error) {
	artifacts, seccomp := controlArtifactPaths(envOr("HUB_STATE", "/state"))
	if artifacts == "" || seccomp == "" {
		return nil, nil
	}
	return &CLIArtifactPipeline{
		Build: func(ctx context.Context, source ArtifactSource, config ArtifactImportConfig) (ImportedArtifact, error) {
			build := RestrictedBuildConfig{SeccompPath: seccomp, ArtifactDirectory: artifacts, MaxArtifactBytes: 8 << 30}
			if source.PackageRegistry != "" {
				return ImportCLIPackageArtifact(ctx, source, config, build, packageHTTPClient())
			}
			if source.Tag != "" {
				return ImportCLIReleaseArtifact(ctx, source, config, build, releaseHTTPClient(), githubAPIBase())
			}
			return ImportCLIArtifact(ctx, source, config, build)
		},
	}, nil
}

// cliArtifactSource resolves the immutable onboarding source: a
// github-release:owner/repo@tag pin, an explicitly pinned commit URL, or the
// configured resolver's pin of a bare repository. Mutable refs and local
// paths never reach the pipeline.
func (c *ControlPlane) cliArtifactSource(ctx context.Context, spec map[string]any) (ArtifactSource, error) {
	raw := argString(spec, "source")
	if strings.HasPrefix(raw, "github-release:") {
		return parseGitHubReleaseSource(raw, argString(spec, "asset"), argString(spec, "digest"), argString(spec, "member"))
	}
	for _, prefix := range []string{"pypi:", "uvx:", "npm:", "npx:"} {
		if strings.HasPrefix(raw, prefix) {
			return parsePackageSource(raw)
		}
	}
	if argString(spec, "asset") != "" || argString(spec, "digest") != "" || argString(spec, "member") != "" {
		return ArtifactSource{}, fmt.Errorf("%w: asset, digest and member apply to github-release sources only", ErrInvalid)
	}
	source, err := ParseGitHubSource(raw)
	if err != nil {
		if c.SourceResolver == nil {
			return ArtifactSource{}, fmt.Errorf("%w: immutable commit-pinned source required", ErrInvalid)
		}
		source, err = c.SourceResolver(ctx, raw)
		if err != nil {
			return ArtifactSource{}, err
		}
	}
	if _, err := source.ArchiveURL(); err != nil {
		return ArtifactSource{}, err
	}
	return source, nil
}

// ImportCLIArtifact runs the bounded-cli variant of the source→image path: a
// repository Dockerfile keeps the standard recipe; without one the cli-mode
// recipe builds a multi-stage minimal-runtime image whose entrypoint is the
// declared guest binary.
func ImportCLIArtifact(ctx context.Context, source ArtifactSource, config ArtifactImportConfig, build RestrictedBuildConfig) (ImportedArtifact, error) {
	if _, err := source.ArchiveURL(); err != nil {
		return ImportedArtifact{}, err
	}
	config = normalizeArtifactImportConfig(config)
	if !validCommand(config.Image) || strings.Contains(config.Image, "@") {
		return ImportedArtifact{}, fmt.Errorf("%w: local immutable image name required", ErrInvalid)
	}
	if len(config.Entrypoint) != 1 || !validGuestBinaryPath(config.Entrypoint[0]) {
		return ImportedArtifact{}, fmt.Errorf("%w: cli artifact entrypoint must be one guest binary path", ErrInvalid)
	}
	contextBytes, err := FetchRepositoryArtifactContext(ctx, source, 64<<20)
	if err != nil {
		return ImportedArtifact{}, err
	}
	for _, overlay := range config.ContextSources {
		if contextBytes, err = OverlayArtifactContext(ctx, overlay.Source, overlay.Into, contextBytes, 64<<20); err != nil {
			return ImportedArtifact{}, err
		}
	}
	var recipe ArtifactRecipe
	var generated []byte
	if contextHasDockerfile(contextBytes) {
		recipe, generated, err = GenerateArtifactRecipe(contextBytes, config.Language, config.BaseImage, config.Entrypoint)
	} else {
		recipe, generated, err = GenerateCLIRecipe(contextBytes, config.Language, config.BaseImage, config.Entrypoint[0])
	}
	if err != nil {
		if source.Tag == "" {
			return ImportedArtifact{}, fmt.Errorf("%w (hint: if this project ships prebuilt binaries, retry with source github-release:owner/repo@tag-or-latest plus an asset glob instead of a source build)", err)
		}
		return ImportedArtifact{}, err
	}
	artifact, err := BuildRestrictedOCI(ctx, recipe, generated, build)
	if err != nil {
		return ImportedArtifact{}, err
	}
	if _, err := LoadStoredOCIArtifact(ctx, build.ArtifactDirectory, artifact.ArchiveDigest, config.Image, build.MaxArtifactBytes); err != nil {
		return ImportedArtifact{}, fmt.Errorf("%w: local artifact materialization failed", err)
	}
	recipeDigest, err := recipeDigest(recipe)
	if err != nil {
		return ImportedArtifact{}, err
	}
	reviewDigest, err := reviewDigest(source, recipe, artifact, config)
	if err != nil {
		return ImportedArtifact{}, err
	}
	definition := ToolDefinition{
		Schema: SchemaVersion, DefinitionID: config.DefinitionID, Version: config.Version,
		Transport: BoundedCLI,
		Source: DefinitionSource{
			Image: config.Image, Digest: artifact.Evidence.ImageManifestDigest,
			Command: firstArg(recipe.Entrypoint), Args: remainingArgs(recipe.Entrypoint),
			Repository: source.Repository, Subfolder: source.Subfolder, CommitSHA: source.CommitSHA,
			ArchiveDigest: artifact.ArchiveDigest, ProvenanceDigest: artifact.Evidence.ProvenanceDigest,
			SBOMDigest: artifact.Evidence.SBOMDigest, RecipeDigest: recipeDigest, ReviewDigest: reviewDigest,
			ContextSources: append([]ArtifactOverlay(nil), config.ContextSources...),
		},
		Tools: append([]ToolSpec(nil), config.Tools...), Credentials: append([]CredentialInput(nil), config.Credentials...),
		RuntimeEnvironment: config.RuntimeEnvironment, Workload: config.Workload, Execution: config.Execution, Health: config.Health,
	}
	if err := definition.Validate(); err != nil {
		return ImportedArtifact{}, err
	}
	return ImportedArtifact{Definition: definition, Recipe: recipe, Artifact: artifact}, nil
}

// ImportCLIReleaseArtifact fetches a github-release asset, verifies its
// sha256 against every available pin (spec digest, API digest, checksums
// file), and wraps the binary in a scratch image through the restricted
// build. The mutable tag is recorded, never trusted.
func ImportCLIReleaseArtifact(ctx context.Context, source ArtifactSource, config ArtifactImportConfig, build RestrictedBuildConfig, client *http.Client, apiBase string) (ImportedArtifact, error) {
	if source.Tag == "" || source.Asset == "" {
		return ImportedArtifact{}, fmt.Errorf("%w: github-release source requires tag and asset", ErrInvalid)
	}
	config = normalizeArtifactImportConfig(config)
	if !validCommand(config.Image) || strings.Contains(config.Image, "@") {
		return ImportedArtifact{}, fmt.Errorf("%w: local immutable image name required", ErrInvalid)
	}
	if len(config.Entrypoint) != 1 || !validGuestBinaryPath(config.Entrypoint[0]) {
		return ImportedArtifact{}, fmt.Errorf("%w: cli release entrypoint must be one guest binary path", ErrInvalid)
	}
	binary, assetName, assetDigest, resolvedTag, checksums, err := fetchReleaseBinary(ctx, source, client, apiBase, path.Base(config.Entrypoint[0]))
	if err != nil {
		return ImportedArtifact{}, err
	}
	recipe, contextBytes, err := generateCLIReleaseRecipe(config.Entrypoint[0], binary, checksums, config.BaseImage)
	if err != nil {
		return ImportedArtifact{}, err
	}
	artifact, err := BuildRestrictedOCI(ctx, recipe, contextBytes, build)
	if err != nil {
		return ImportedArtifact{}, err
	}
	if _, err := LoadStoredOCIArtifact(ctx, build.ArtifactDirectory, artifact.ArchiveDigest, config.Image, build.MaxArtifactBytes); err != nil {
		return ImportedArtifact{}, fmt.Errorf("%w: local artifact materialization failed", err)
	}
	recipeDigest, err := recipeDigest(recipe)
	if err != nil {
		return ImportedArtifact{}, err
	}
	reviewSource := source
	reviewSource.Tag = resolvedTag
	reviewSource.Asset = assetName
	reviewSource.AssetDigest = assetDigest
	reviewDigest, err := reviewDigest(reviewSource, recipe, artifact, config)
	if err != nil {
		return ImportedArtifact{}, err
	}
	definition := ToolDefinition{
		Schema: SchemaVersion, DefinitionID: config.DefinitionID, Version: config.Version,
		Transport: BoundedCLI,
		Source: DefinitionSource{
			Image: config.Image, Digest: artifact.Evidence.ImageManifestDigest,
			Command: firstArg(recipe.Entrypoint), Args: remainingArgs(recipe.Entrypoint),
			Repository: source.Repository,
			ReleaseTag: resolvedTag, ReleaseAsset: assetName, AssetDigest: assetDigest,
			ArchiveDigest: artifact.ArchiveDigest, ProvenanceDigest: artifact.Evidence.ProvenanceDigest,
			SBOMDigest: artifact.Evidence.SBOMDigest, RecipeDigest: recipeDigest, ReviewDigest: reviewDigest,
			ContextSources: append([]ArtifactOverlay(nil), config.ContextSources...),
		},
		Tools: append([]ToolSpec(nil), config.Tools...), Credentials: append([]CredentialInput(nil), config.Credentials...),
		RuntimeEnvironment: config.RuntimeEnvironment, Workload: config.Workload, Execution: config.Execution, Health: config.Health,
	}
	if err := definition.Validate(); err != nil {
		return ImportedArtifact{}, err
	}
	return ImportedArtifact{Definition: definition, Recipe: recipe, Artifact: artifact}, nil
}

// prepareCLIArtifact onboards an owner CLI from an immutable source. The
// restricted build produces verified OCI evidence; the digest-pinned image
// becomes the definition's artifact, and the standard confirm/enable
// lifecycle owns the rest.
func (c *ControlPlane) prepareCLIArtifact(ctx context.Context, auth identity.Envelope, spec map[string]any, requestKey string, shared bool) (map[string]any, error) {
	if c.CLIArtifacts == nil || c.CLIArtifacts.Build == nil {
		return nil, fmt.Errorf("%w: cli artifact onboarding is unavailable", ErrIsolation)
	}
	name := argString(spec, "name")
	binary := argString(spec, "binary")
	// binary is the guest path inside the built image; slash semantics.
	if !identity.ValidID(name) || !validGuestBinaryPath(binary) {
		return nil, fmt.Errorf("%w: cli artifact name and binary are required", ErrInvalid)
	}
	if argString(spec, "command") != "" {
		return nil, fmt.Errorf("%w: cli artifacts declare binary, not command", ErrInvalid)
	}
	source, err := c.cliArtifactSource(ctx, spec)
	if err != nil {
		return nil, err
	}
	version := argString(spec, "version")
	if version == "" {
		version = "1.0.0"
	}
	fixed, err := strictStringList(spec["args"], "args")
	if err != nil {
		return nil, err
	}
	egress, err := strictStringList(spec["egress"], "egress")
	if err != nil {
		return nil, err
	}
	if len(egress) == 0 {
		egress = []string{"127.0.0.1"}
	}
	runtimeEnv, err := cliSpecEnvironment(spec["runtime_env"])
	if err != nil {
		return nil, err
	}
	credentials, err := strictStringList(spec["credentials"], "credentials")
	if err != nil {
		return nil, err
	}
	inputs := make([]CredentialInput, 0, len(credentials))
	for _, credential := range credentials {
		inputs = append(inputs, CredentialInput{Name: credential, Required: true})
	}
	tools, err := cliSpecTools(spec["tools"])
	if err != nil {
		return nil, err
	}
	workload := cliSpecWorkload(spec, shared)
	// cli.base selects a pinned toolchain runtime for binaries that are not
	// static (dynamic loaders, libc). Only reviewed language pins resolve;
	// a base on a non-release source is meaningless and rejected downstream.
	var baseImage string
	if lang := argString(spec, "base"); lang != "" {
		var ok bool
		if baseImage, ok = languageBaseImages[lang]; !ok {
			return nil, fmt.Errorf("%w: cli base must be a reviewed toolchain language", ErrInvalid)
		}
	}
	config := ArtifactImportConfig{
		DefinitionID: name, Version: version, Image: "hermes-cli-artifact/" + name,
		Entrypoint:         []string{binary},
		BaseImage:          baseImage,
		Tools:              tools,
		Credentials:        inputs,
		RuntimeEnvironment: runtimeEnv,
		Workload:           workload,
		Execution:          ExecutionPolicy{TimeoutSeconds: 60, OutputBytes: 262144, CPUMillis: 2000, MemoryMiB: 2048, MaxPIDs: 128, Egress: egress},
		Health:             HealthProbe{Kind: "exec", Value: binary, TimeoutSeconds: 5},
	}
	preparing, inFlight, err := c.ensurePreparing(auth, source, config, requestKey)
	if err != nil {
		return nil, err
	}
	if inFlight {
		return c.statusBody(preparing, false), nil
	}
	if window := c.prepareWindow(); window < 0 {
		return c.finishCLIArtifact(ctx, auth, preparing, source, binary, fixed, config, requestKey, shared)
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
		body, err := c.finishCLIArtifact(background, auth, preparing, source, binary, fixed, config, requestKey, shared)
		latest := c.latestPrepareRecord(auth, preparing)
		log.Printf("toolhub cli artifact prepare done: onboarding=%s phase=%s err=%v", latest.OnboardingID, latest.Phase, err)
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
	log.Printf("toolhub cli artifact prepare: onboarding=%s still preparing; outcome continues in background", preparing.OnboardingID)
	return c.statusBody(preparing, false), nil
}

func (c *ControlPlane) finishCLIArtifact(ctx context.Context, auth identity.Envelope, preparing Onboarding, source ArtifactSource, binary string, fixedArgs []string, config ArtifactImportConfig, requestKey string, shared bool) (map[string]any, error) {
	failPrepare := func(err error) (map[string]any, error) {
		if latest, latestErr := c.Store.onboarding(preparing.OnboardingID); latestErr == nil && latest.Phase == PhasePreparing {
			latest.Phase = PhaseFailed
			if persistErr := c.persistFailure(latest, publicPrepareError(err), err); persistErr != err {
				return nil, persistErr
			}
		}
		return nil, err
	}
	imported, err := c.CLIArtifacts.Build(ctx, source, config)
	if err != nil {
		return failPrepare(err)
	}
	// The cell executes the verified image itself: the definition pins the
	// image manifest digest, and the entrypoint's first token is the guest
	// path inside that image — the same tuple /cli-exec re-verifies at
	// admission. Later entrypoint tokens become fixed argv prefixes.
	entrypoint := imported.Recipe.Entrypoint
	if len(entrypoint) == 0 {
		entrypoint = []string{binary}
	}
	health := config.Health
	health.Value = entrypoint[0]
	definition := ToolDefinition{
		Schema: SchemaVersion, DefinitionID: config.DefinitionID, Version: config.Version, Transport: BoundedCLI,
		Source: DefinitionSource{
			Image: imported.Definition.Source.Image, Digest: imported.Artifact.Evidence.ImageManifestDigest,
			Command: firstArg(entrypoint), Args: append(remainingArgs(entrypoint), fixedArgs...),
			Repository: source.Repository, Subfolder: source.Subfolder, CommitSHA: source.CommitSHA,
			ReleaseTag: imported.Definition.Source.ReleaseTag, ReleaseAsset: imported.Definition.Source.ReleaseAsset,
			AssetDigest:     imported.Definition.Source.AssetDigest,
			PackageRegistry: imported.Definition.Source.PackageRegistry, PackageName: imported.Definition.Source.PackageName,
			PackageVersion: imported.Definition.Source.PackageVersion,
			ArchiveDigest:  imported.Artifact.ArchiveDigest, ProvenanceDigest: imported.Artifact.Evidence.ProvenanceDigest,
			SBOMDigest: imported.Artifact.Evidence.SBOMDigest, RecipeDigest: imported.Definition.Source.RecipeDigest,
			ReviewDigest: imported.Definition.Source.ReviewDigest, ContextSources: append([]ArtifactOverlay(nil), imported.Definition.Source.ContextSources...),
		},
		Tools: append([]ToolSpec(nil), config.Tools...), Credentials: append([]CredentialInput(nil), config.Credentials...),
		RuntimeEnvironment: config.RuntimeEnvironment, Workload: config.Workload, Execution: config.Execution, Health: health,
	}
	if err := c.registerAdmittedDefinition(&definition, auth.PrincipalID); err != nil {
		return failPrepare(err)
	}
	if shared {
		if err := c.Store.PromoteToCatalog(definition.DefinitionID, definition.Version, auth.PrincipalID); err != nil {
			return failPrepare(err)
		}
	}
	onboarding, err := c.newOnboarding(auth, OnboardingSelfInstall, requestKey, definition, source.Repository, source.CommitSHA)
	if err != nil {
		return failPrepare(err)
	}
	if onboarding.OnboardingID != preparing.OnboardingID {
		preparing.Phase = PhaseRemoved
		preparing.SupersededBy = onboarding.OnboardingID
		if err := c.Store.PutOnboarding(preparing); err != nil {
			log.Printf("toolhub: superseded onboarding stub %s not recorded: %v", preparing.OnboardingID, err)
		}
	}
	onboarding.Permissions = toolNames(definition)
	onboarding.Effects = effectNames(definition)
	onboarding.ReviewDigest = definition.Source.ReviewDigest
	onboarding.Subfolder = source.Subfolder
	copyDef := definition
	onboarding.Definition = &copyDef
	if err := c.Store.PutOnboarding(onboarding); err != nil {
		return failPrepare(err)
	}
	if err := c.ensureBrokerRequest(ctx, auth, &onboarding, definition); err != nil {
		return failPrepare(err)
	}
	return c.statusBody(onboarding, false), nil
}
