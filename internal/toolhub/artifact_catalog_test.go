package toolhub

import (
	"context"
	"testing"
)

func TestTrustedArtifactRegistrationRequiresUnchangedReviewPacket(t *testing.T) {
	definition := statefulContainerDefinition()
	definition.Source.Repository = "https://github.com/example/mcp"
	definition.Source.CommitSHA = "0123456789012345678901234567890123456789"
	definition.Source.ArchiveDigest = "sha256:" + repeatHex('a')
	definition.Source.ProvenanceDigest = "sha256:" + repeatHex('b')
	definition.Source.SBOMDigest = "sha256:" + repeatHex('c')
	definition.Source.RecipeDigest = "sha256:" + repeatHex('d')
	artifact := ImportedArtifact{Definition: definition, Recipe: ArtifactRecipe{Format: "dockerfile-v1", Dockerfile: ".hub/Dockerfile", Entrypoint: []string{"/app/server"}}, Artifact: StoredOCIArtifact{ArchiveDigest: definition.Source.ArchiveDigest, Evidence: OCIArtifactEvidence{ImageManifestDigest: definition.Source.Digest, ProvenanceDigest: definition.Source.ProvenanceDigest, SBOMDigest: definition.Source.SBOMDigest}}}
	config := ArtifactImportConfig{DefinitionID: definition.DefinitionID, Version: definition.Version, Image: definition.Source.Image, Tools: definition.Tools, Credentials: definition.Credentials, Environment: definition.Environment, Workload: definition.Workload, Execution: definition.Execution, Health: definition.Health}
	digest, err := reviewDigest(ArtifactSource{Repository: definition.Source.Repository, CommitSHA: definition.Source.CommitSHA}, artifact.Recipe, artifact.Artifact, config)
	if err != nil {
		t.Fatal(err)
	}
	definition.Source.ReviewDigest = digest
	artifact.Definition = definition
	if err := AttachConfirmedToolContract(&artifact, ConfirmedToolContract{Source: ToolContractPreflight, Tools: artifact.Definition.Tools}); err != nil {
		t.Fatal(err)
	}
	definition = artifact.Definition
	if err := ValidateTrustedArtifactDefinition(definition); err != nil {
		t.Fatalf("trusted definition rejected: %v", err)
	}
	store := NewStore()
	if err := store.RegisterTrustedArtifact(artifact, digest); err != nil {
		t.Fatalf("valid review rejected: %v", err)
	}
	base := artifact
	for name, mutate := range map[string]func(*ImportedArtifact){
		"empty-approval": func(_ *ImportedArtifact) {},
		"review-drift":   func(value *ImportedArtifact) { value.Recipe.Entrypoint = append(value.Recipe.Entrypoint, "--changed") },
		"evidence-drift": func(value *ImportedArtifact) { value.Artifact.Evidence.SBOMDigest = "sha256:" + repeatHex('9') },
		"entrypoint-length": func(value *ImportedArtifact) {
			value.Recipe.Entrypoint = append(value.Recipe.Entrypoint, "--changed")
		},
		"entrypoint-value": func(value *ImportedArtifact) { value.Recipe.Entrypoint = []string{"/app/other"} },
	} {
		t.Run(name, func(t *testing.T) {
			candidate := base
			mutate(&candidate)
			approved := digest
			if name == "evidence-drift" || name == "entrypoint-length" || name == "entrypoint-value" {
				var err error
				candidate.Definition.Source.ReviewDigest, err = reviewDigestForImported(candidate)
				if err != nil {
					t.Fatal(err)
				}
				approved = candidate.Definition.Source.ReviewDigest
			}
			if name == "empty-approval" {
				approved = ""
			}
			if err := store.RegisterTrustedArtifact(candidate, approved); err == nil {
				t.Fatal("unsafe review packet accepted")
			}
		})
	}
	artifact.Definition.Source.Digest = "sha256:" + repeatHex('e')
	if err := store.RegisterTrustedArtifact(artifact, digest); err == nil {
		t.Fatal("artifact drift accepted")
	}
}

func TestAlignImportedEntrypointCopiesRecipeWhenDefinitionOmitsCommand(t *testing.T) {
	artifact := importedReviewPacket(t)
	artifact.Recipe.Entrypoint = []string{"node", "/app/packages/mcp/dist/index.js"}
	artifact.Definition.Source.Command = ""
	artifact.Definition.Source.Args = nil
	alignImportedEntrypoint(&artifact)
	if artifact.Definition.Source.Command != "node" || len(artifact.Definition.Source.Args) != 1 || artifact.Definition.Source.Args[0] != "/app/packages/mcp/dist/index.js" {
		t.Fatalf("recipe entrypoint not copied: %+v", artifact.Definition.Source)
	}
}

func TestArtifactSourceMetadataRejectsMutableOrWrongTransport(t *testing.T) {
	definition := remoteDefinition()
	definition.Source.CommitSHA = "0123456789012345678901234567890123456789"
	if err := definition.Validate(); err == nil {
		t.Fatal("remote definition accepted artifact metadata")
	}
}

func TestArtifactReviewHelpersAndDefaults(t *testing.T) {
	config := normalizeArtifactImportConfig(ArtifactImportConfig{DefinitionID: "mcp", Version: "1.0.0"})
	if config.Image == "" || config.Workload.Class != PerUser || config.Workload.ToolHiveVersion != "v0.48.0" || len(config.Workload.SidecarImages) != 1 {
		t.Fatalf("defaults not applied: %+v", config)
	}
	recipe := ArtifactRecipe{Format: "dockerfile-v1", Dockerfile: ".hub/Dockerfile", Files: []ArtifactFile{{Path: ".hub/Dockerfile", Digest: "sha256:" + repeatHex('a')}}, DependencyLocks: []string{".hub/Dockerfile"}, BaseImages: []string{"python@sha256:" + repeatHex('b')}, Entrypoint: []string{"/app/server"}}
	if digest, err := recipeDigest(recipe); err != nil || len(digest) != len("sha256:")+64 {
		t.Fatalf("recipe digest: %q %v", digest, err)
	}
	valid := ImportedArtifact{Definition: statefulContainerDefinition(), Artifact: StoredOCIArtifact{ArchiveDigest: "sha256:" + repeatHex('c'), Evidence: OCIArtifactEvidence{ImageManifestDigest: "sha256:" + repeatHex('d'), ProvenanceDigest: "sha256:" + repeatHex('e'), SBOMDigest: "sha256:" + repeatHex('f')}}}
	valid.Definition.Source.ArchiveDigest = valid.Artifact.ArchiveDigest
	valid.Definition.Source.ProvenanceDigest = valid.Artifact.Evidence.ProvenanceDigest
	valid.Definition.Source.SBOMDigest = valid.Artifact.Evidence.SBOMDigest
	valid.Definition.Source.ReviewDigest = "sha256:" + repeatHex('0')
	if err := ValidateImportedReview(valid); err != nil {
		t.Fatalf("valid review rejected: %v", err)
	}
	valid.Definition.Source.ReviewDigest = ""
	if err := ValidateImportedReview(valid); err == nil {
		t.Fatal("missing review digest accepted")
	}
	valid.Artifact.Evidence.SBOMDigest = ""
	if err := ValidateImportedReview(valid); err == nil {
		t.Fatal("incomplete review accepted")
	}
	if err := ValidateTrustedArtifactDefinition(statefulContainerDefinition()); err == nil {
		t.Fatal("unattested container accepted as trusted")
	}
}

func TestArtifactEntrypointArgumentHelpers(t *testing.T) {
	if got := firstArg(nil); got != "" {
		t.Fatalf("empty entrypoint command: %q", got)
	}
	if got := firstArg([]string{"/app/server", "--stdio"}); got != "/app/server" {
		t.Fatalf("first entrypoint argument: %q", got)
	}
	if got := remainingArgs(nil); got != nil {
		t.Fatalf("empty entrypoint arguments: %#v", got)
	}
	if got := remainingArgs([]string{"/app/server"}); got != nil {
		t.Fatalf("single entrypoint argument: %#v", got)
	}
	got := remainingArgs([]string{"/app/server", "--stdio", "--safe"})
	if len(got) != 2 || got[0] != "--stdio" || got[1] != "--safe" {
		t.Fatalf("remaining entrypoint arguments: %#v", got)
	}
}

func TestImportedReviewDigestMatchesPacketHandle(t *testing.T) {
	artifact := importedReviewPacket(t)
	got, err := ImportedReviewDigest(artifact)
	if err != nil || got != artifact.Definition.Source.ReviewDigest {
		t.Fatalf("ImportedReviewDigest: %q %v (packet %q)", got, err, artifact.Definition.Source.ReviewDigest)
	}
}

func TestImportGitHubArtifactRejectsBeforeNetwork(t *testing.T) {
	if _, err := ImportGitHubArtifact(context.Background(), ArtifactSource{Repository: "http://not-github", CommitSHA: "bad"}, ArtifactImportConfig{}, RestrictedBuildConfig{}); err == nil {
		t.Fatal("invalid source accepted")
	}
	if _, err := ImportGitHubArtifact(context.Background(), ArtifactSource{Repository: "https://github.com/example/mcp", CommitSHA: "0123456789012345678901234567890123456789"}, ArtifactImportConfig{DefinitionID: "mcp", Version: "1.0.0", Image: "repo@sha256:bad"}, RestrictedBuildConfig{}); err == nil {
		t.Fatal("unsafe image accepted")
	}
}

func repeatHex(value byte) string {
	result := make([]byte, 64)
	for i := range result {
		result[i] = value
	}
	return string(result)
}
