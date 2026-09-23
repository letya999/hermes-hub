package toolhub

import (
	"context"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"slices"
	"strings"

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
		if selected != nil && (!candidateMatches(*selected, source) || selected.Launch.Transport != ContainerMCP || !digestPattern.MatchString(selected.Launch.Digest)) {
			return SourceReview{}, fmt.Errorf("%w: selected registry artifact does not match the exact source", ErrInvalid)
		}
		contextBytes, err := FetchRepositoryRecipeContext(ctx, source, 64<<20)
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
			lookup, lookupErr := recipeLookup(source)
			if lookupErr != nil {
				return SourceReview{}, fmt.Errorf("%w: selected registry artifact does not match the exact source", ErrInvalid)
			}
			verified, accepted, verifyErr := verifyRecipeCandidateAt(ctx, nil, "", *selected, lookup)
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
		config := defaultSelfInstallConfig(source)
		prepared, matched, err := preparedForSource(source)
		if err != nil {
			return SourceReview{}, err
		}
		if matched {
			config = prepared.apply(config)
			resolution.Connection = prepared.overlay(resolution.Connection)
		}
		config.Credentials = resolution.Connection.CredentialInputs()
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
				egress = discoverOpenAPIEgress(contextBytes)
			}
			published, publishedErr := ImportPublishedArtifact(source, config, resolution, egress)
			if publishedErr == nil {
				if published, _, publishedErr = PreflightPublishedArtifact(ctx, published); publishedErr == nil {
					return SourceReview{Definition: published.Definition, Permissions: toolNames(published.Definition), Effects: effectNames(published.Definition), ReviewDigest: published.Definition.Source.ReviewDigest, Recipe: &resolution}, nil
				}
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
			mergeConnectionForDefinition(resolution.Connection, &imported.Definition)
		}
		imported, _, err = PreflightImportedArtifact(ctx, imported, artifactsDir)
		if err != nil {
			return SourceReview{}, err
		}
		return SourceReview{
			Definition: imported.Definition, Permissions: toolNames(imported.Definition),
			Effects: effectNames(imported.Definition), ReviewDigest: imported.Definition.Source.ReviewDigest, Recipe: &resolution,
		}, nil
	}
}

func defaultSelfInstallConfig(source ArtifactSource) ArtifactImportConfig {
	id := "user-mcp"
	if parsed, err := url.Parse(source.Repository); err == nil {
		parts := strings.Split(strings.Trim(parsed.Path, "/"), "/")
		if len(parts) == 2 {
			id = sanitizeDefinitionID(parts[1])
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

func sanitizeDefinitionID(name string) string {
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

func controlArtifactPaths(stateRoot string) (artifacts, seccomp string) {
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
