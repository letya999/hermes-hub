package toolhub

import (
	"context"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"strings"

	"github.com/letya999/hermes-hub/internal/identity"
)

// DefaultSourceReviewer is the production self-install reviewer: M5.1 GitHub
// import plus MCP tools/list preflight. Missing absolute paths fail closed
// before any network fetch.
func DefaultSourceReviewer(artifactsDir, seccompPath string) SourceReviewer {
	return func(ctx context.Context, source ArtifactSource) (SourceReview, error) {
		if !filepath.IsAbs(artifactsDir) || !filepath.IsAbs(seccompPath) {
			return SourceReview{}, fmt.Errorf("%w: absolute artifact and seccomp paths required", ErrInvalid)
		}
		if _, err := os.Stat(seccompPath); err != nil {
			return SourceReview{}, fmt.Errorf("%w: seccomp: %v", ErrInvalid, err)
		}
		imported, err := ImportGitHubArtifact(ctx, source, defaultSelfInstallConfig(source), RestrictedBuildConfig{
			SeccompPath: seccompPath, ArtifactDirectory: artifactsDir, MaxArtifactBytes: 8 << 30,
		})
		if err != nil {
			return SourceReview{}, err
		}
		imported, _, err = PreflightImportedArtifact(ctx, imported, artifactsDir)
		if err != nil {
			return SourceReview{}, err
		}
		return SourceReview{
			Definition: imported.Definition, Permissions: toolNames(imported.Definition),
			Effects: effectNames(imported.Definition), ReviewDigest: imported.Definition.Source.ReviewDigest,
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
		DefinitionID: id, Version: "0.0.1", Image: "hermes-artifact/" + id,
		Tools:    []ToolSpec{{Name: "mcp", Effect: ReadEffect}},
		Workload: WorkloadPolicy{Class: PerUser, Stateful: true, Rationale: "user self-install MCP is principal-owned", ToolHiveVersion: "v0.48.0"},
		Execution: ExecutionPolicy{
			TimeoutSeconds: 60, OutputBytes: 2 << 20, CPUMillis: 1000, MemoryMiB: 512, MaxPIDs: 64,
			Egress: []string{"github.com"},
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
