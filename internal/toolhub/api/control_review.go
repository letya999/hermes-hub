package api

import (
	"context"
	"errors"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"time"

	"github.com/letya999/hermes-hub/internal/identity"
)

// DefaultSourceReviewer is the production self-install reviewer. A verified
// catalog image is preferred and admitted by digest; the restricted GitHub
// build remains the explicit safe fallback when no published image can pass
// the local tools/list probe.
func DefaultSourceReviewer(artifactsDir, seccompPath string) SourceReviewer {
	return DefaultSourceReviewerWithCatalogs(artifactsDir, seccompPath, nil)
}

// DefaultSourceReviewerWithCatalogs keeps catalog integrations explicit. A
// catalog must be supplied by a verified adapter; Resolver never guesses a
// provider API or treats a name-only catalog hit as an artifact match.
func DefaultSourceReviewerWithCatalogs(artifactsDir, seccompPath string, catalogs []RecipeCatalog) SourceReviewer {
	return func(ctx context.Context, source ArtifactSource, selected *RecipeCandidate) (SourceReview, error) {
		if !filepath.IsAbs(artifactsDir) || !filepath.IsAbs(seccompPath) {
			return SourceReview{}, fmt.Errorf("%w: absolute artifact and seccomp paths required", ErrInvalid)
		}
		if _, err := os.Stat(seccompPath); err != nil {
			return SourceReview{}, fmt.Errorf("%w: seccomp: %v", ErrInvalid, err)
		}
		if selected != nil && (!CandidateMatches(*selected, source) || selected.Launch.Transport != ContainerMCP || !DigestPattern.MatchString(selected.Launch.Digest)) {
			return SourceReview{}, fmt.Errorf("%w: selected registry artifact does not match the exact source", ErrInvalid)
		}
		if pinned, matched, pinErr := preparedLookup(source); pinErr != nil {
			return SourceReview{}, pinErr
		} else if matched && pinned.Image != nil {
			// T0: the CI-built image is the reviewed artifact — admission
			// resolves the pin and never fetches sources or runs a build.
			return admitPinnedPrepared(ctx, source, pinned)
		}
		contextBytes, err := fetchRecipeContext(ctx, source, 64<<20)
		if err != nil {
			return SourceReview{}, err
		}
		resolverCatalogs := catalogs
		if selected != nil {
			resolverCatalogs = nil
		}
		resolution, err := (RecipeResolver{Catalogs: resolverCatalogs}).Resolve(ctx, source, contextBytes)
		if err != nil {
			return SourceReview{}, err
		}
		if selected != nil {
			lookup, lookupErr := LookupRecipe(source)
			if lookupErr != nil {
				return SourceReview{}, fmt.Errorf("%w: selected registry artifact does not match the exact source", ErrInvalid)
			}
			verified, accepted, verifyErr := VerifyRecipeCandidateAt(ctx, nil, "", *selected, lookup)
			if verifyErr != nil {
				return SourceReview{}, verifyErr
			}
			if !accepted || verified.Launch.Digest != selected.Launch.Digest {
				return SourceReview{}, fmt.Errorf("%w: selected registry artifact changed or lost OCI proof", ErrStale)
			}
			resolution.Launch = verified.Launch
			resolution.Connection = mergeConnection(resolution.Connection, verified.Connection)
			resolution.Evidence = append(resolution.Evidence, verified.Evidence...)
			resolution.Evidence = append(resolution.Evidence, verified.Launch.Evidence...)
			resolution.State = "ready"
		}
		config := DefaultSelfInstallConfig(source)
		prepared, matched, err := preparedLookup(source)
		if err != nil {
			return SourceReview{}, err
		}
		if matched {
			config = prepared.Apply(config)
			resolution.Connection = prepared.Overlay(resolution.Connection)
			// The reviewed entry is the credential authority here just as in
			// the pinned path: its claims belong in the ledger or the
			// resolution cannot explain which source made the fields required.
			preparedCandidate := RecipeCandidate{
				Repository: source.Repository, Subfolder: source.Subfolder, CommitSHA: source.CommitSHA,
				Connection: prepared.Connection, Source: "prepared", CredentialRank: RankR0,
			}
			resolution.Ledger = append(resolution.Ledger, candidateLedgerEntries(preparedCandidate, false, time.Now().UTC().Format(time.RFC3339))...)
			enrichResolution(&resolution)
		}
		if resolution.State == "ready" {
			ApplyLaunchState(&config, resolution.Launch)
		}
		config.Credentials = resolution.Connection.CredentialInputs()
		if resolution.Connection.OAuth != nil {
			config.OAuth = resolution.Connection.OAuth
			config.Workload.Stateful = true
		}
		// Only reviewed deliveries enter the Broker mapping; unrelated optional
		// upstream settings remain metadata, not required form fields.
		if matched {
			for _, field := range prepared.Connection.Fields {
				config.CredentialContractEnv[field.Name] = field.Name
			}
		}
		if resolution.State == "ready" && resolution.Launch.Transport == ContainerMCP {
			egress := config.Execution.Egress
			if len(egress) == 0 {
				egress = DiscoverOpenAPIEgress(contextBytes)
			}
			config.ProxyEnvironment = MergeProxyEnvironment(config.ProxyEnvironment, DiscoverProxyEnvironment(contextBytes), ClaimedEnvironmentNames(config))
			published, publishedErr := ImportPublishedArtifact(source, config, resolution, egress)
			if publishedErr == nil {
				admitted, _, probeErr := PreflightPublishedArtifact(ctx, published)
				if probeErr == nil {
					return SourceReview{Definition: admitted.Definition, Permissions: ToolNames(admitted.Definition), Effects: EffectNames(admitted.Definition), ReviewDigest: admitted.Definition.Source.ReviewDigest, Recipe: &resolution}, nil
				}
				if review, ok := ReviewFromCredentialGate(admitted, probeErr, &resolution); ok {
					return review, nil
				}
				publishedErr = probeErr
			}
			if selected != nil {
				return SourceReview{}, fmt.Errorf("%w: selected registry artifact failed preflight: %v", ErrIsolation, publishedErr)
			}
		}
		imported, err := ImportGitHubArtifact(ctx, source, config, RestrictedBuildConfig{
			SeccompPath: seccompPath, ArtifactDirectory: artifactsDir, MaxArtifactBytes: 8 << 30,
		})
		if err != nil {
			return SourceReview{}, err
		}
		if matched && !slices.Equal(imported.Recipe.Entrypoint, prepared.Entrypoint) {
			return SourceReview{}, fmt.Errorf("%w: generated entrypoint differs from reviewed prepared entry", ErrStale)
		}
		if len(imported.Definition.Credentials) == 0 {
			MergeConnectionForDefinition(resolution.Connection, &imported.Definition)
		}
		imported, _, err = PreflightImportedArtifact(ctx, imported, artifactsDir)
		if review, ok := ReviewFromCredentialGate(imported, err, &resolution); ok {
			return review, nil
		}
		if err != nil {
			return SourceReview{}, err
		}
		return SourceReview{
			Definition: imported.Definition, Permissions: ToolNames(imported.Definition),
			Effects: EffectNames(imported.Definition), ReviewDigest: imported.Definition.Source.ReviewDigest, Recipe: &resolution,
		}, nil
	}
}

// admitPinnedPrepared admits a catalog-pinned T0 entry: the CI-built image,
// its attestations and the reviewed connection recipe form a ready
// resolution, so admission only needs the published-artifact path.
func admitPinnedPrepared(ctx context.Context, source ArtifactSource, prepared PreparedEntry) (SourceReview, error) {
	pin := prepared.Image
	repository, _, _ := strings.Cut(pin.Ref, "@")
	resolution := RecipeResolution{
		Source: source,
		State:  "ready",
		Launch: LaunchRecipe{
			Transport:  ContainerMCP,
			Artifact:   repository,
			Digest:     pin.Digest,
			Entrypoint: append([]string(nil), prepared.Entrypoint...),
			Network:    append([]string(nil), prepared.Egress...),
		},
		Connection: prepared.Connection,
		Evidence: []RecipeEvidence{
			{Source: "oci", Digest: pin.Provenance, Detail: "provenance attestation"},
			{Source: "oci", Digest: pin.SBOM, Detail: "sbom attestation"},
		},
	}
	// The reviewed catalog never reaches the network resolver, so record its
	// claims here: a ready resolution without a ledger cannot be replayed or
	// explain its trust tier.
	candidate := RecipeCandidate{
		Repository: source.Repository, Subfolder: source.Subfolder, CommitSHA: source.CommitSHA,
		Launch: resolution.Launch, Connection: prepared.Connection,
		Source: "prepared", LaunchRank: RankL0, CredentialRank: RankR0,
	}
	resolution.Ledger = candidateLedgerEntries(candidate, false, time.Now().UTC().Format(time.RFC3339))
	enrichResolution(&resolution)
	config := NormalizeArtifactImportConfig(prepared.Apply(DefaultSelfInstallConfig(source)))
	published, err := ImportPublishedArtifact(source, config, resolution, config.Execution.Egress)
	if err != nil {
		return SourceReview{}, err
	}
	admitted, _, err := PreflightPublishedArtifact(ctx, published)
	if err != nil {
		if review, ok := ReviewFromCredentialGate(admitted, err, &resolution); ok {
			return review, nil
		}
		return SourceReview{}, err
	}
	return SourceReview{
		Definition: admitted.Definition, Permissions: ToolNames(admitted.Definition),
		Effects: EffectNames(admitted.Definition), ReviewDigest: admitted.Definition.Source.ReviewDigest, Recipe: &resolution,
	}, nil
}

func DefaultSelfInstallConfig(source ArtifactSource) ArtifactImportConfig {
	id := "user-mcp"
	if parsed, err := url.Parse(source.Repository); err == nil {
		parts := strings.Split(strings.Trim(parsed.Path, "/"), "/")
		if len(parts) == 2 {
			id = SanitizeDefinitionID(parts[1])
		}
	}
	return ArtifactImportConfig{
		DefinitionID: id, Version: "0.0.2", Image: "hermes-artifact/" + id,
		Tools:    []ToolSpec{{Name: "mcp", Effect: ReadEffect}},
		Workload: WorkloadPolicy{Class: PerUser, Stateful: false, Rationale: "user self-install MCP credentials are principal-owned", ToolHiveVersion: "v0.48.0"},
		Execution: ExecutionPolicy{
			TimeoutSeconds: 60, OutputBytes: 2 << 20, CPUMillis: 1000, MemoryMiB: 512, MaxPIDs: 64,
		},
		Health: HealthProbe{Kind: "exec", Value: "/app/health", TimeoutSeconds: 5},
	}
}

// ApplyLaunchState projects reviewed durable-state declarations from a recipe
// onto the import config. Only hub-managed named volumes are honored; compose
// host mounts and unsafe targets are dropped because ExecutionPolicy.Validate
// would reject the definition anyway and silently rewriting them is worse.
func ApplyLaunchState(config *ArtifactImportConfig, launch LaunchRecipe) {
	if launch.Transport != ContainerMCP {
		return
	}
	for _, mount := range launch.Mounts {
		if mount.Source != "connection-state" {
			continue
		}
		if !strings.HasPrefix(mount.Target, "/") || strings.Contains(mount.Target, "..") || slices.ContainsFunc(config.Execution.Mounts, func(existing Mount) bool { return existing.Target == mount.Target }) {
			continue
		}
		config.Execution.Mounts = append(config.Execution.Mounts, mount)
	}
	if len(config.Execution.Mounts) > 0 {
		config.Workload.Stateful = true
	}
	for name, value := range launch.Environment {
		if EnvKey(name) == "" {
			continue
		}
		if config.RuntimeEnvironment == nil {
			config.RuntimeEnvironment = map[string]string{}
		}
		if _, fixed := config.RuntimeEnvironment[name]; !fixed {
			config.RuntimeEnvironment[name] = value
		}
	}
}

func ReviewFromCredentialGate(imported ImportedArtifact, err error, resolution *RecipeResolution) (SourceReview, bool) {
	var gate *CredentialGate
	if !errors.As(err, &gate) || gate == nil || imported.Definition.DefinitionID == "" {
		return SourceReview{}, false
	}
	detail := gate.Detail
	if detail == "" {
		detail = gate.Error()
	}
	return SourceReview{
		Definition: imported.Definition, AdmissionPending: true, AdmissionDetail: detail, AdmissionGroups: gate.Groups, Recipe: resolution,
	}, true
}

func SanitizeDefinitionID(name string) string {
	name = strings.ToLower(strings.TrimSpace(name))
	var b strings.Builder
	for _, r := range name {
		if r >= 'a' && r <= 'z' || r >= '0' && r <= '9' || r == '-' || r == '_' {
			b.WriteRune(r)
		}
	}
	id := strings.Trim(b.String(), "-_")
	if id != "" && id[0] >= '0' && id[0] <= '9' {
		id = "m-" + id
	}
	if len(id) > 40 {
		id = id[:40]
	}
	if !identity.ValidID(id) {
		return "user-mcp"
	}
	return id
}

func ControlArtifactPaths(stateRoot string) (artifacts, seccomp string) {
	artifacts = strings.TrimSpace(os.Getenv("HUB_ARTIFACT_DIR"))
	if artifacts == "" && strings.TrimSpace(stateRoot) != "" {
		artifacts = filepath.Join(stateRoot, "artifacts")
	}
	seccomp = strings.TrimSpace(os.Getenv("HUB_BUILD_SECCOMP"))
	if seccomp == "" {
		if root, err := moduleRoot(); err == nil {
			seccomp = filepath.Join(root, "docker", "seccomp-buildkit-rootless.json")
		}
	}
	if artifacts != "" {
		if abs, err := filepath.Abs(artifacts); err == nil {
			artifacts = abs
		}
	}
	if seccomp != "" {
		if abs, err := filepath.Abs(seccomp); err == nil {
			seccomp = abs
		}
	}
	return artifacts, seccomp
}
