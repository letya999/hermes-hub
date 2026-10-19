package api

import (
	"errors"
	"strings"
	"testing"
)

// ValidateImportedReview refuses review packets that lack the immutable
// review digest, any of the four evidence digests, or a valid definition.
func TestValidateImportedReview(t *testing.T) {
	t.Parallel()
	sha := "0123456789abcdef0123456789abcdef01234567"
	source := ArtifactSource{Repository: "https://github.com/acme/demo", CommitSHA: sha}
	resolution := RecipeResolution{
		Source: source, State: "ready",
		Launch: LaunchRecipe{
			Transport: ContainerMCP, Artifact: "ghcr.io/acme/demo",
			Digest: "sha256:" + strings.Repeat("cd", 32), Entrypoint: []string{"/app/server"},
			Network: []string{"api.example.io"},
		},
		Connection: ConnectionRecipe{Fields: []ConnectionField{{Name: "API_TOKEN", Required: true}}},
		Evidence: []RecipeEvidence{
			{Source: "oci", Digest: "sha256:" + strings.Repeat("11", 32), Detail: "provenance"},
			{Source: "oci", Digest: "sha256:" + strings.Repeat("22", 32), Detail: "sbom"},
		},
	}
	base, err := ImportPublishedArtifact(source, DefaultSelfInstallConfig(source), resolution, nil)
	if err != nil {
		t.Fatal(err)
	}
	base.Artifact.ArchiveDigest = "sha256:arc"
	imported := func(mutate func(*ImportedArtifact)) ImportedArtifact {
		a := base
		mutate(&a)
		return a
	}
	if err := ValidateImportedReview(imported(func(*ImportedArtifact) {})); err != nil {
		t.Fatalf("complete review rejected: %v", err)
	}
	if err := ValidateImportedReview(imported(func(a *ImportedArtifact) { a.Definition.Source.ReviewDigest = "" })); !errors.Is(err, ErrInvalid) || !strings.Contains(err.Error(), "review digest") {
		t.Fatalf("missing review digest: %v", err)
	}
	for _, clear := range []func(*ImportedArtifact){
		func(a *ImportedArtifact) { a.Artifact.ArchiveDigest = "" },
		func(a *ImportedArtifact) { a.Artifact.Evidence.ImageManifestDigest = "" },
		func(a *ImportedArtifact) { a.Artifact.Evidence.ProvenanceDigest = "" },
		func(a *ImportedArtifact) { a.Artifact.Evidence.SBOMDigest = "" },
	} {
		if err := ValidateImportedReview(imported(clear)); !errors.Is(err, ErrInvalid) || !strings.Contains(err.Error(), "evidence") {
			t.Fatalf("incomplete evidence: %v", err)
		}
	}
	if err := ValidateImportedReview(imported(func(a *ImportedArtifact) { a.Definition.DefinitionID = "!!" })); err == nil {
		t.Fatal("invalid definition must fail")
	}
}
