package toolhub

import (
	"context"
	"fmt"
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

func TestImportPublishedArtifactUsesDigestAndAttestationEvidence(t *testing.T) {
	const commit = "0123456789abcdef0123456789abcdef01234567"
	source := ArtifactSource{Repository: "https://github.com/acme/weather", CommitSHA: commit}
	resolution := RecipeResolution{
		Source: source, State: "ready",
		Launch: LaunchRecipe{Transport: ContainerMCP, Artifact: "ghcr.io/acme/weather", Digest: "sha256:" + repeatHex('a'), Entrypoint: []string{"/app/server"}},
		Evidence: []RecipeEvidence{
			{Source: "oci", Digest: "sha256:" + repeatHex('b'), Detail: "immutable provenance referrer"},
			{Source: "oci", Digest: "sha256:" + repeatHex('c'), Detail: "immutable SBOM referrer"},
		},
	}
	imported, err := ImportPublishedArtifact(source, defaultSelfInstallConfig(source), resolution, nil)
	if err != nil {
		t.Fatal(err)
	}
	if imported.Artifact.ArchiveDigest != "" || imported.Definition.Source.Image != resolution.Launch.Artifact || imported.Definition.Source.Digest != resolution.Launch.Digest || imported.Definition.Source.ProvenanceDigest == "" || imported.Definition.Source.SBOMDigest == "" {
		t.Fatalf("published evidence: %+v", imported)
	}
	if err := ValidateTrustedArtifactDefinition(imported.Definition); err == nil {
		t.Fatal("published packet accepted without a real tools/list contract")
	}
	if err := AttachConfirmedToolContract(&imported, ConfirmedToolContract{Source: ToolContractPreflight, Tools: imported.Definition.Tools}); err != nil {
		t.Fatal(err)
	}
	if err := ValidateTrustedArtifactDefinition(imported.Definition); err != nil {
		t.Fatalf("published trusted packet rejected: %v", err)
	}
}

func TestPreflightPublishedArtifactKeepsTheRealToolsListGate(t *testing.T) {
	d := statefulContainerDefinition()
	d.Source.Repository = "https://github.com/acme/weather"
	d.Source.CommitSHA = "0123456789abcdef0123456789abcdef01234567"
	d.Source.ArchiveDigest = ""
	packet := ImportedArtifact{Definition: d}
	originalPull, originalList := publishedImagePull, publishedMCPToolList
	defer func() { publishedImagePull, publishedMCPToolList = originalPull, originalList }()
	pulled := false
	publishedImagePull = func(context.Context, string, string) error { pulled = true; return nil }
	publishedMCPToolList = func(context.Context, ImportedArtifact) ([]ToolSpec, error) {
		return []ToolSpec{{Name: "read", Effect: ReadEffect}}, nil
	}
	preflighted, contract, err := PreflightPublishedArtifact(context.Background(), packet)
	if err != nil || !pulled || contract.Source != ToolContractPreflight || len(preflighted.Definition.Tools) != 1 || preflighted.Definition.Source.ToolContractDigest == "" {
		t.Fatalf("published preflight: pulled=%v contract=%+v packet=%+v err=%v", pulled, contract, preflighted, err)
	}
	if err := pullPublishedImage(context.Background(), "bad image", "sha256:bad"); err == nil {
		t.Fatal("invalid published image reference accepted")
	}
}

func TestPreflightImportedArtifactUsesTheSameToolsListContract(t *testing.T) {
	d := statefulContainerDefinition()
	d.Credentials = []CredentialInput{{Name: "API_TOKEN", Required: true}}
	packet := ImportedArtifact{Definition: d, Artifact: StoredOCIArtifact{ArchiveDigest: "sha256:" + repeatHex('a')}}
	originalLoader, originalList := storedArtifactLoader, localMCPToolList
	defer func() { storedArtifactLoader, localMCPToolList = originalLoader, originalList }()
	storedArtifactLoader = func(context.Context, string, string, string, int64) (string, error) {
		return packet.Definition.Source.Image, nil
	}
	localMCPToolList = func(context.Context, ImportedArtifact) ([]ToolSpec, error) {
		return []ToolSpec{{Name: "read", Effect: ReadEffect}}, nil
	}
	preflighted, contract, err := PreflightImportedArtifact(context.Background(), packet, t.TempDir())
	if err != nil || contract.Source != ToolContractPreflight || preflighted.Definition.Source.ToolContractDigest == "" {
		t.Fatalf("local preflight: packet=%+v contract=%+v err=%v", preflighted, contract, err)
	}
}

func TestPreflightPromotesCredentialsProvenByToolsList(t *testing.T) {
	for _, test := range []struct {
		name         string
		credentials  []CredentialInput
		failText     string
		wantAttempts int
		wantErr      bool
		promoted     bool
	}{
		{"optional-secret-promoted", []CredentialInput{{Name: "API_TOKEN"}, {Name: "MCP_MODE"}}, "", 2, false, true},
		{"already-required", []CredentialInput{{Name: "API_TOKEN", Required: true}}, "", 1, false, false},
		{"no-secret-fields", []CredentialInput{{Name: "MCP_MODE"}}, "", 1, true, false},
		{"no-credentials", nil, "", 1, true, false},
		{"undeclared-from-auth-error", nil, "Error: authentication required: set GITHUB_PERSONAL_ACCESS_TOKEN, configure GitHub App auth, or pass --oauth-client-id", 2, false, true},
		{"undeclared-nonsecret-ignored", nil, "Error: set MCP_MODE to configure transport", 1, true, false},
	} {
		t.Run(test.name, func(t *testing.T) {
			d := statefulContainerDefinition()
			d.Credentials = test.credentials
			packet := ImportedArtifact{Definition: d, Artifact: StoredOCIArtifact{ArchiveDigest: "sha256:" + repeatHex('a')}}
			originalLoader, originalList := storedArtifactLoader, localMCPToolList
			defer func() { storedArtifactLoader, localMCPToolList = originalLoader, originalList }()
			storedArtifactLoader = func(context.Context, string, string, string, int64) (string, error) {
				return packet.Definition.Source.Image, nil
			}
			failText := test.failText
			if failText == "" {
				failText = "authentication required"
			}
			attempts := 0
			localMCPToolList = func(_ context.Context, candidate ImportedArtifact) ([]ToolSpec, error) {
				attempts++
				for _, input := range candidate.Definition.Credentials {
					if input.Required && secretName(input.Name) {
						return []ToolSpec{{Name: "read", Effect: ReadEffect}}, nil
					}
				}
				return nil, fmt.Errorf("%w: %s", ErrIsolation, failText)
			}
			preflighted, _, err := PreflightImportedArtifact(context.Background(), packet, t.TempDir())
			if (err != nil) != test.wantErr || attempts != test.wantAttempts {
				t.Fatalf("preflight: attempts=%d err=%v", attempts, err)
			}
			if test.wantErr || !test.promoted {
				return
			}
			for _, input := range preflighted.Definition.Credentials {
				if input.Required != secretName(input.Name) {
					t.Fatalf("promotion boundary wrong: %+v", preflighted.Definition.Credentials)
				}
			}
		})
	}
}

func TestPreflightPromotionNarrowsToNamedCredentials(t *testing.T) {
	d := statefulContainerDefinition()
	d.Credentials = []CredentialInput{{Name: "API_TOKEN"}, {Name: "GITHUB_PERSONAL_ACCESS_TOKEN"}, {Name: "MCP_MODE"}}
	packet := ImportedArtifact{Definition: d, Artifact: StoredOCIArtifact{ArchiveDigest: "sha256:" + repeatHex('a')}}
	originalLoader, originalList := storedArtifactLoader, localMCPToolList
	defer func() { storedArtifactLoader, localMCPToolList = originalLoader, originalList }()
	storedArtifactLoader = func(context.Context, string, string, string, int64) (string, error) {
		return packet.Definition.Source.Image, nil
	}
	attempts := 0
	localMCPToolList = func(_ context.Context, candidate ImportedArtifact) ([]ToolSpec, error) {
		attempts++
		for _, input := range candidate.Definition.Credentials {
			if input.Name == "GITHUB_PERSONAL_ACCESS_TOKEN" && input.Required {
				return []ToolSpec{{Name: "read", Effect: ReadEffect}}, nil
			}
		}
		return nil, fmt.Errorf("%w: authentication required: set GITHUB_PERSONAL_ACCESS_TOKEN", ErrIsolation)
	}
	preflighted, _, err := PreflightImportedArtifact(context.Background(), packet, t.TempDir())
	if err != nil || attempts != 2 {
		t.Fatalf("preflight: attempts=%d err=%v", attempts, err)
	}
	for _, input := range preflighted.Definition.Credentials {
		want := input.Name == "GITHUB_PERSONAL_ACCESS_TOKEN"
		if input.Required != want {
			t.Fatalf("promotion boundary wrong for %s: %+v", input.Name, preflighted.Definition.Credentials)
		}
	}
}

func TestPreflightPublishedArtifactPromotesProvenCredentials(t *testing.T) {
	d := statefulContainerDefinition()
	d.Source.Repository = "https://github.com/acme/weather"
	d.Source.CommitSHA = "0123456789abcdef0123456789abcdef01234567"
	d.Source.ArchiveDigest = ""
	d.Source.Digest = "sha256:" + repeatHex('b')
	d.Credentials = []CredentialInput{{Name: "API_TOKEN"}}
	packet := ImportedArtifact{Definition: d}
	originalPull, originalList := publishedImagePull, publishedMCPToolList
	defer func() { publishedImagePull, publishedMCPToolList = originalPull, originalList }()
	publishedImagePull = func(context.Context, string, string) error { return nil }
	publishedMCPToolList = func(_ context.Context, candidate ImportedArtifact) ([]ToolSpec, error) {
		for _, input := range candidate.Definition.Credentials {
			if input.Required && secretName(input.Name) {
				return []ToolSpec{{Name: "read", Effect: ReadEffect}}, nil
			}
		}
		return nil, fmt.Errorf("%w: authentication required", ErrIsolation)
	}
	preflighted, _, err := PreflightPublishedArtifact(context.Background(), packet)
	if err != nil || !preflighted.Definition.Credentials[0].Required {
		t.Fatalf("published promotion failed: %+v err=%v", preflighted.Definition.Credentials, err)
	}
}

func TestImportPublishedArtifactRejectsIncompleteOrMismatchedResolution(t *testing.T) {
	const commit = "0123456789abcdef0123456789abcdef01234567"
	source := ArtifactSource{Repository: "https://github.com/acme/weather", CommitSHA: commit}
	base := RecipeResolution{Source: source, State: "ready", Launch: LaunchRecipe{Transport: ContainerMCP, Artifact: "ghcr.io/acme/weather", Digest: "sha256:" + repeatHex('a'), Entrypoint: []string{"/app/server"}}, Evidence: []RecipeEvidence{{Source: "oci", Digest: "sha256:" + repeatHex('b'), Detail: "immutable provenance referrer"}, {Source: "oci", Digest: "sha256:" + repeatHex('c'), Detail: "immutable SBOM referrer"}}}
	for name, candidate := range map[string]RecipeResolution{
		"draft":           {Source: source, State: "draft", Launch: base.Launch, Evidence: base.Evidence},
		"wrong-source":    {Source: ArtifactSource{Repository: "https://github.com/other/weather", CommitSHA: commit}, State: "ready", Launch: base.Launch, Evidence: base.Evidence},
		"wrong-transport": {Source: source, State: "ready", Launch: LaunchRecipe{Transport: RemoteMCP, Endpoint: "https://mcp.example"}, Evidence: base.Evidence},
		"bad-digest":      {Source: source, State: "ready", Launch: LaunchRecipe{Transport: ContainerMCP, Artifact: "ghcr.io/acme/weather", Digest: "bad", Entrypoint: []string{"/app/server"}}, Evidence: base.Evidence},
		"missing-proof":   {Source: source, State: "ready", Launch: base.Launch, Evidence: nil},
	} {
		t.Run(name, func(t *testing.T) {
			if _, err := ImportPublishedArtifact(source, defaultSelfInstallConfig(source), candidate, nil); err == nil {
				t.Fatal("incomplete published recipe accepted")
			}
		})
	}
}

func repeatHex(value byte) string {
	result := make([]byte, 64)
	for i := range result {
		result[i] = value
	}
	return string(result)
}
