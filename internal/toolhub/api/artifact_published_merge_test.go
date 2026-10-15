package api

import (
	"errors"
	"strings"
	"testing"
)

func TestImportPublishedArtifactContract(t *testing.T) {
	sha := "0123456789abcdef0123456789abcdef01234567"
	digest := "sha256:" + strings.Repeat("cd", 32)
	source := ArtifactSource{Repository: "https://github.com/acme/demo", CommitSHA: sha}
	config := DefaultSelfInstallConfig(source)

	resolution := RecipeResolution{
		Source: source, State: "ready",
		Launch: LaunchRecipe{
			Transport: ContainerMCP, Artifact: "ghcr.io/acme/demo",
			Digest: digest, Entrypoint: []string{"/app/server"},
			Network: []string{"api.example.io"},
		},
		Connection: ConnectionRecipe{Fields: []ConnectionField{{Name: "API_TOKEN", Required: true}}},
		Evidence: []RecipeEvidence{
			{Source: "oci", Digest: "sha256:" + strings.Repeat("11", 32), Detail: "provenance attestation"},
			{Source: "oci", Digest: "sha256:" + strings.Repeat("22", 32), Detail: "sbom attestation"},
		},
	}
	imported, err := ImportPublishedArtifact(source, config, resolution, nil)
	if err != nil {
		t.Fatal(err)
	}
	s := imported.Definition.Source
	if s.Image != "ghcr.io/acme/demo" || s.Digest != digest || s.ArchiveDigest != "" ||
		s.ProvenanceDigest == "" || s.SBOMDigest == "" || s.ReviewDigest == "" {
		t.Fatalf("source: %+v", s)
	}
	if imported.Definition.Transport != ContainerMCP || len(imported.Definition.Credentials) != 1 || imported.Definition.Credentials[0].Name != "API_TOKEN" {
		t.Fatalf("definition: %+v", imported.Definition)
	}
	if imported.Recipe.Format != "published-oci-v1" {
		t.Fatalf("recipe: %+v", imported.Recipe)
	}
	if err := imported.Definition.Validate(); err != nil {
		t.Fatalf("imported definition invalid: %v", err)
	}

	// not-ready resolution, wrong source, missing evidence are refused
	notReady := resolution
	notReady.State = "draft"
	if _, err := ImportPublishedArtifact(source, config, notReady, nil); !errors.Is(err, ErrInvalid) {
		t.Fatalf("draft: %v", err)
	}
	wrong := resolution
	wrong.Source = ArtifactSource{Repository: "https://github.com/other/x", CommitSHA: sha}
	if _, err := ImportPublishedArtifact(source, config, wrong, nil); !errors.Is(err, ErrInvalid) {
		t.Fatalf("wrong source: %v", err)
	}
	noDigest := resolution
	noDigest.Launch.Digest = "sha256:short"
	if _, err := ImportPublishedArtifact(source, config, noDigest, nil); !errors.Is(err, ErrInvalid) {
		t.Fatalf("bad digest: %v", err)
	}
	// missing OCI attestations is an authorization failure, not a parse error
	noAttest := resolution
	noAttest.Evidence = []RecipeEvidence{{Source: "github", Detail: "readme"}}
	if _, err := ImportPublishedArtifact(source, config, noAttest, nil); !errors.Is(err, ErrUnauthorized) {
		t.Fatalf("no attestations: %v", err)
	}
}
