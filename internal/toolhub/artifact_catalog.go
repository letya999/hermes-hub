package toolhub

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/letya999/hermes-hub/internal/identity"
)

// ArtifactImportConfig is the small operator-facing part of automatic import.
// Source, recipe and OCI evidence are produced by the pipeline; the operator
// supplies only the MCP's declared contract (tools, effects and env names).
type ArtifactImportConfig struct {
	DefinitionID string
	Version      string
	Language     string
	BaseImage    string
	Entrypoint   []string
	Image        string
	Tools        []ToolSpec
	Credentials  []CredentialInput
	Environment  []string
	Workload     WorkloadPolicy
	Execution    ExecutionPolicy
	Health       HealthProbe
}

type ImportedArtifact struct {
	Definition ToolDefinition    `json:"definition"`
	Recipe     ArtifactRecipe    `json:"recipe"`
	Artifact   StoredOCIArtifact `json:"artifact"`
}

type artifactReviewContract struct {
	DefinitionID string
	Version      string
	Image        string
	Tools        []ToolSpec
	Credentials  []CredentialInput
	Environment  []string
	Workload     WorkloadPolicy
	Execution    ExecutionPolicy
	Health       HealthProbe
}

// ImportGitHubArtifact performs the complete source→recipe→isolated OCI path.
// It intentionally stops at quarantine: callers must review the returned
// secret-free definition before RegisterTrustedArtifact.
func ImportGitHubArtifact(ctx context.Context, source ArtifactSource, config ArtifactImportConfig, build RestrictedBuildConfig) (ImportedArtifact, error) {
	if _, err := source.ArchiveURL(); err != nil {
		return ImportedArtifact{}, err
	}
	config = normalizeArtifactImportConfig(config)
	if !validCommand(config.Image) || strings.Contains(config.Image, "@") {
		return ImportedArtifact{}, fmt.Errorf("%w: local immutable image name required", ErrInvalid)
	}
	contextBytes, err := FetchRepositoryArtifactContext(ctx, source, 64<<20)
	if err != nil {
		return ImportedArtifact{}, err
	}
	recipe, generated, err := GenerateArtifactRecipe(contextBytes, config.Language, config.BaseImage, config.Entrypoint)
	if err != nil {
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
		Transport: ContainerMCP,
		Source: DefinitionSource{
			Image: config.Image, Digest: artifact.Evidence.ImageManifestDigest,
			Command: firstArg(recipe.Entrypoint), Args: remainingArgs(recipe.Entrypoint),
			Repository: source.Repository, CommitSHA: source.CommitSHA,
			ArchiveDigest: artifact.ArchiveDigest, ProvenanceDigest: artifact.Evidence.ProvenanceDigest,
			SBOMDigest: artifact.Evidence.SBOMDigest, RecipeDigest: recipeDigest, ReviewDigest: reviewDigest,
		},
		Tools: append([]ToolSpec(nil), config.Tools...), Credentials: append([]CredentialInput(nil), config.Credentials...), Environment: append([]string(nil), config.Environment...),
		Workload: config.Workload, Execution: config.Execution, Health: config.Health,
	}
	if err := definition.Validate(); err != nil {
		return ImportedArtifact{}, err
	}
	return ImportedArtifact{Definition: definition, Recipe: recipe, Artifact: artifact}, nil
}

// RegisterTrustedArtifact is the only publication step. The review digest is
// recomputed from all immutable inputs, so changing source/artifact/contract
// cannot silently reuse a previous approval.
func (s *Store) RegisterTrustedArtifact(imported ImportedArtifact, approvedReviewDigest string) error {
	if s == nil || imported.Definition.Transport != ContainerMCP || approvedReviewDigest == "" || imported.Definition.Source.ReviewDigest != approvedReviewDigest {
		return fmt.Errorf("%w: artifact review approval required", ErrUnauthorized)
	}
	if expected, err := reviewDigestForImported(imported); err != nil || expected != approvedReviewDigest {
		return fmt.Errorf("%w: artifact review packet drift", ErrStale)
	}
	if err := ValidateTrustedArtifactDefinition(imported.Definition); err != nil {
		return err
	}
	if imported.Definition.Source.Digest != imported.Artifact.Evidence.ImageManifestDigest || imported.Definition.Source.ArchiveDigest != imported.Artifact.ArchiveDigest || imported.Definition.Source.ProvenanceDigest != imported.Artifact.Evidence.ProvenanceDigest || imported.Definition.Source.SBOMDigest != imported.Artifact.Evidence.SBOMDigest {
		return fmt.Errorf("%w: artifact evidence does not match definition", ErrStale)
	}
	entrypoint := append([]string{imported.Definition.Source.Command}, imported.Definition.Source.Args...)
	if len(imported.Definition.Source.Command) == 0 {
		entrypoint = nil
	}
	if len(entrypoint) != len(imported.Recipe.Entrypoint) {
		return fmt.Errorf("%w: artifact entrypoint drift", ErrStale)
	}
	for i := range entrypoint {
		if entrypoint[i] != imported.Recipe.Entrypoint[i] {
			return fmt.Errorf("%w: artifact entrypoint drift", ErrStale)
		}
	}
	return s.RegisterDefinition(imported.Definition)
}

func ValidateTrustedArtifactDefinition(definition ToolDefinition) error {
	if err := definition.Validate(); err != nil {
		return err
	}
	if definition.Transport != ContainerMCP || definition.Source.Repository == "" || definition.Source.CommitSHA == "" || definition.Source.ArchiveDigest == "" || definition.Source.ProvenanceDigest == "" || definition.Source.SBOMDigest == "" || definition.Source.RecipeDigest == "" || definition.Source.ReviewDigest == "" {
		return fmt.Errorf("%w: immutable build evidence required", ErrUnauthorized)
	}
	return verifyDefinitionToolContract(definition)
}

func normalizeArtifactImportConfig(config ArtifactImportConfig) ArtifactImportConfig {
	if config.Image == "" && identity.ValidID(config.DefinitionID) {
		config.Image = "hermes-artifact/" + config.DefinitionID
	}
	if config.Workload.Class == "" {
		config.Workload.Class = PerUser
	}
	if config.Workload.ToolHiveVersion == "" {
		config.Workload.ToolHiveVersion = "v0.48.0"
	}
	if len(config.Workload.SidecarImages) == 0 {
		config.Workload.SidecarImages = []string{restrictedBuildProxyImage}
	}
	return config
}

func recipeDigest(recipe ArtifactRecipe) (string, error) {
	encoded, err := json.Marshal(recipe)
	if err != nil {
		return "", err
	}
	hash := sha256.Sum256(encoded)
	return "sha256:" + hex.EncodeToString(hash[:]), nil
}

func reviewDigest(source ArtifactSource, recipe ArtifactRecipe, artifact StoredOCIArtifact, config ArtifactImportConfig) (string, error) {
	// Canonical JSON of immutable metadata is the review handle. It contains no
	// context bytes, credentials or secret values.
	value := struct {
		Source   ArtifactSource
		Recipe   ArtifactRecipe
		Artifact StoredOCIArtifact
		Contract artifactReviewContract
	}{Source: source, Recipe: recipe, Artifact: artifact, Contract: artifactReviewContract{config.DefinitionID, config.Version, config.Image, config.Tools, config.Credentials, config.Environment, config.Workload, config.Execution, config.Health}}
	encoded, err := json.Marshal(value)
	if err != nil {
		return "", err
	}
	hash := sha256.Sum256(encoded)
	return "sha256:" + hex.EncodeToString(hash[:]), nil
}

func reviewDigestForImported(imported ImportedArtifact) (string, error) {
	source := ArtifactSource{Repository: imported.Definition.Source.Repository, CommitSHA: imported.Definition.Source.CommitSHA}
	config := ArtifactImportConfig{
		DefinitionID: imported.Definition.DefinitionID, Version: imported.Definition.Version,
		Image: imported.Definition.Source.Image, Tools: imported.Definition.Tools,
		Entrypoint:  append([]string{imported.Definition.Source.Command}, imported.Definition.Source.Args...),
		Credentials: imported.Definition.Credentials, Environment: imported.Definition.Environment,
		Workload: imported.Definition.Workload, Execution: imported.Definition.Execution, Health: imported.Definition.Health,
	}
	return reviewDigest(source, imported.Recipe, imported.Artifact, config)
}

func alignImportedEntrypoint(imported *ImportedArtifact) {
	if imported == nil || len(imported.Recipe.Entrypoint) == 0 {
		return
	}
	imported.Definition.Source.Command = firstArg(imported.Recipe.Entrypoint)
	imported.Definition.Source.Args = remainingArgs(imported.Recipe.Entrypoint)
}

func firstArg(argv []string) string {
	if len(argv) == 0 {
		return ""
	}
	return argv[0]
}

func remainingArgs(argv []string) []string {
	if len(argv) < 2 {
		return nil
	}
	return append([]string(nil), argv[1:]...)
}

// ImportedReviewDigest is the immutable review handle operators pass to
// hubctl artifact approve --review-digest. It is recomputed from source,
// recipe, OCI evidence and the review contract; claimed tool lists alone
// cannot produce a trusted registration.
func ImportedReviewDigest(imported ImportedArtifact) (string, error) {
	return reviewDigestForImported(imported)
}

// ValidateImportedReview is useful to callers that persist the review packet
// outside the ToolHub store. It does not execute code or contact a registry.
func ValidateImportedReview(imported ImportedArtifact) error {
	if imported.Definition.Source.ReviewDigest == "" {
		return fmt.Errorf("%w: missing artifact review digest", ErrInvalid)
	}
	if imported.Artifact.ArchiveDigest == "" || imported.Artifact.Evidence.ImageManifestDigest == "" || imported.Artifact.Evidence.ProvenanceDigest == "" || imported.Artifact.Evidence.SBOMDigest == "" {
		return fmt.Errorf("%w: incomplete artifact evidence", ErrInvalid)
	}
	return imported.Definition.Validate()
}
