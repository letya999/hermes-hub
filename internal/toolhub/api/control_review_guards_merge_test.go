package api

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"
)

// DefaultSourceReviewerWithCatalogs refuses relative artifact/seccomp paths,
// a missing seccomp profile, and a registry selection that does not match the
// exact source.
func TestDefaultSourceReviewerGuards(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	reviewer := DefaultSourceReviewerWithCatalogs("rel/artifacts", "rel/seccomp", nil)
	if _, err := reviewer(ctx, ArtifactSource{}, nil); !errors.Is(err, ErrInvalid) {
		t.Fatalf("relative paths: %v", err)
	}
	dir := t.TempDir()
	reviewer = DefaultSourceReviewerWithCatalogs(dir, filepath.Join(dir, "missing.json"), nil)
	if _, err := reviewer(ctx, ArtifactSource{}, nil); !errors.Is(err, ErrInvalid) {
		t.Fatalf("missing seccomp: %v", err)
	}
	seccomp := filepath.Join(dir, "seccomp.json")
	if err := os.WriteFile(seccomp, []byte("{}"), 0600); err != nil {
		t.Fatal(err)
	}
	source := ArtifactSource{Repository: "https://github.com/o/r", CommitSHA: "abc"}
	bad := RecipeCandidate{Repository: "https://github.com/other/r", CommitSHA: "def"}
	reviewer = DefaultSourceReviewerWithCatalogs(dir, seccomp, nil)
	if _, err := reviewer(ctx, source, &bad); !errors.Is(err, ErrInvalid) {
		t.Fatalf("mismatched selection: %v", err)
	}
	// A matching selection without a container launch/digest is refused.
	matching := RecipeCandidate{Repository: source.Repository, CommitSHA: source.CommitSHA}
	if _, err := reviewer(ctx, source, &matching); !errors.Is(err, ErrInvalid) {
		t.Fatalf("selection without digest: %v", err)
	}
}

// SanitizeDefinitionID, ControlArtifactPaths and ReviewFromCredentialGate
// shape the review surface: ids normalize, paths default under the state root
// and credential gates become admission-pending reviews.
func TestReviewSurfaceHelpers(t *testing.T) {
	if SanitizeDefinitionID("  My Cool.Server!! ") != "mycoolserver" {
		t.Fatalf("sanitize: %q", SanitizeDefinitionID("  My Cool.Server!! "))
	}
	if SanitizeDefinitionID("9lives") != "m-9lives" {
		t.Fatal("leading digit must gain a prefix")
	}
	if SanitizeDefinitionID("!!!") != "user-mcp" {
		t.Fatal("empty id must fall back")
	}
	t.Setenv("HUB_ARTIFACT_DIR", "")
	t.Setenv("HUB_BUILD_SECCOMP", "")
	artifacts, seccomp := ControlArtifactPaths(t.TempDir())
	if artifacts == "" || seccomp == "" {
		t.Fatal("artifact dir and seccomp must resolve")
	}
	if _, ok := ReviewFromCredentialGate(ImportedArtifact{Definition: ToolDefinition{DefinitionID: "x"}}, &CredentialGate{Names: []string{"TOKEN"}, Groups: [][]string{{"TOKEN"}}}, nil); !ok {
		t.Fatal("credential gate must produce an admission-pending review")
	}
	if _, ok := ReviewFromCredentialGate(ImportedArtifact{}, errors.New("boom"), nil); ok {
		t.Fatal("non-gate error must not produce a review")
	}
	def := DefaultSelfInstallConfig(ArtifactSource{Repository: "https://github.com/o/My-Repo"})
	if def.DefinitionID != "my-repo" || def.Workload.Class != PerUser || def.Execution.TimeoutSeconds != 60 {
		t.Fatalf("self-install config: %+v", def)
	}
}
