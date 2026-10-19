package api

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"github.com/letya999/hermes-hub/internal/identity"
)

// ArtifactImportConfig is the small operator-facing part of automatic import.
// Source, recipe and OCI evidence are produced by the pipeline; the operator
// supplies only the MCP's declared contract (tools, effects and env names).
type ArtifactImportConfig struct {
	DefinitionID               string
	Version                    string
	Language                   string
	BaseImage                  string
	Entrypoint                 []string
	Image                      string
	Tools                      []ToolSpec
	Credentials                []CredentialInput
	CredentialGroups           [][]string
	CredentialContractID       string
	CredentialContractRevision int
	CredentialContractEnv      map[string]string
	Environment                []string
	RuntimeEnvironment         map[string]string
	ProxyEnvironment           []string
	Workload                   WorkloadPolicy
	Execution                  ExecutionPolicy
	Health                     HealthProbe
	// ContextSources merges additional pinned repositories into the build
	// context for upstream vendoring steps a tree fetch cannot reproduce.
	ContextSources []ArtifactOverlay
	// Toolsets/DefaultEnabled/DefaultExcluded carry the catalog's staged
	// projection surface into the admitted definition.
	Toolsets        map[string][]string
	DefaultEnabled  []string
	DefaultExcluded []string
}

type ImportedArtifact struct {
	Definition ToolDefinition    `json:"definition"`
	Recipe     ArtifactRecipe    `json:"recipe"`
	Artifact   StoredOCIArtifact `json:"artifact"`
}

type artifactReviewContract struct {
	DefinitionID               string
	Version                    string
	Image                      string
	Tools                      []ToolSpec
	Credentials                []CredentialInput
	CredentialGroups           [][]string
	CredentialContractID       string
	CredentialContractRevision int
	CredentialContractEnv      map[string]string
	Environment                []string
	RuntimeEnvironment         map[string]string
	ProxyEnvironment           []string
	Workload                   WorkloadPolicy
	Execution                  ExecutionPolicy
	Health                     HealthProbe
	ContextSources             []ArtifactOverlay
}

func NormalizeArtifactImportConfig(config ArtifactImportConfig) ArtifactImportConfig {
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
		config.Workload.SidecarImages = []string{RestrictedBuildProxyImage}
	}
	return config
}

func RecipeDigest(recipe ArtifactRecipe) (string, error) {
	encoded, err := json.Marshal(recipe)
	if err != nil {
		return "", err
	}
	hash := sha256.Sum256(encoded)
	return "sha256:" + hex.EncodeToString(hash[:]), nil
}

func ReviewDigest(source ArtifactSource, recipe ArtifactRecipe, artifact StoredOCIArtifact, config ArtifactImportConfig) (string, error) {
	// Canonical JSON of immutable metadata is the review handle. It Contains no
	// context bytes, credentials or secret values.
	value := struct {
		Source   ArtifactSource
		Recipe   ArtifactRecipe
		Artifact StoredOCIArtifact
		Contract artifactReviewContract
	}{Source: source, Recipe: recipe, Artifact: artifact, Contract: artifactReviewContract{config.DefinitionID, config.Version, config.Image, config.Tools, config.Credentials, config.CredentialGroups, config.CredentialContractID, config.CredentialContractRevision, config.CredentialContractEnv, config.Environment, config.RuntimeEnvironment, config.ProxyEnvironment, config.Workload, config.Execution, config.Health, config.ContextSources}}
	encoded, err := json.Marshal(value)
	if err != nil {
		return "", err
	}
	hash := sha256.Sum256(encoded)
	return "sha256:" + hex.EncodeToString(hash[:]), nil
}

func ReviewDigestForImported(imported ImportedArtifact) (string, error) {
	source := ArtifactSource{Repository: imported.Definition.Source.Repository, Subfolder: imported.Definition.Source.Subfolder, CommitSHA: imported.Definition.Source.CommitSHA,
		Tag: imported.Definition.Source.ReleaseTag, Asset: imported.Definition.Source.ReleaseAsset, AssetDigest: imported.Definition.Source.AssetDigest,
		PackageRegistry: imported.Definition.Source.PackageRegistry, PackageName: imported.Definition.Source.PackageName, PackageVersion: imported.Definition.Source.PackageVersion}
	config := ArtifactImportConfig{
		DefinitionID: imported.Definition.DefinitionID, Version: imported.Definition.Version,
		Image: imported.Definition.Source.Image, Tools: imported.Definition.Tools,
		Entrypoint:  append([]string{imported.Definition.Source.Command}, imported.Definition.Source.Args...),
		Credentials: imported.Definition.Credentials, CredentialGroups: imported.Definition.CredentialGroups, CredentialContractID: imported.Definition.CredentialContractID, CredentialContractRevision: imported.Definition.CredentialContractRevision, CredentialContractEnv: imported.Definition.CredentialContractEnv, Environment: imported.Definition.Environment,
		RuntimeEnvironment: imported.Definition.RuntimeEnvironment, ProxyEnvironment: imported.Definition.ProxyEnvironment, Workload: imported.Definition.Workload, Execution: imported.Definition.Execution, Health: imported.Definition.Health,
		ContextSources: append([]ArtifactOverlay(nil), imported.Definition.Source.ContextSources...),
	}
	return ReviewDigest(source, imported.Recipe, imported.Artifact, config)
}

func AlignImportedEntrypoint(imported *ImportedArtifact) {
	if imported == nil || len(imported.Recipe.Entrypoint) == 0 {
		return
	}
	imported.Definition.Source.Command = FirstArg(imported.Recipe.Entrypoint)
	imported.Definition.Source.Args = RemainingArgs(imported.Recipe.Entrypoint)
}

func FirstArg(argv []string) string {
	if len(argv) == 0 {
		return ""
	}
	return argv[0]
}

func RemainingArgs(argv []string) []string {
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
	return ReviewDigestForImported(imported)
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
