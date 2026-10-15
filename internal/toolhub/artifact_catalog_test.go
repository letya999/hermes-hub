package toolhub

import (
	"context"
	"fmt"
	"github.com/letya999/hermes-hub/internal/toolhub/api"
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
	artifact := api.ImportedArtifact{Definition: definition, Recipe: api.ArtifactRecipe{Format: "dockerfile-v1", Dockerfile: ".hub/Dockerfile", Entrypoint: []string{"/app/server"}}, Artifact: api.StoredOCIArtifact{ArchiveDigest: definition.Source.ArchiveDigest, Evidence: api.OCIArtifactEvidence{ImageManifestDigest: definition.Source.Digest, ProvenanceDigest: definition.Source.ProvenanceDigest, SBOMDigest: definition.Source.SBOMDigest}}}
	config := api.ArtifactImportConfig{DefinitionID: definition.DefinitionID, Version: definition.Version, Image: definition.Source.Image, Tools: definition.Tools, Credentials: definition.Credentials, Environment: definition.Environment, Workload: definition.Workload, Execution: definition.Execution, Health: definition.Health}
	digest, err := api.ReviewDigest(api.ArtifactSource{Repository: definition.Source.Repository, CommitSHA: definition.Source.CommitSHA}, artifact.Recipe, artifact.Artifact, config)
	if err != nil {
		t.Fatal(err)
	}
	definition.Source.ReviewDigest = digest
	artifact.Definition = definition
	if err := api.AttachConfirmedToolContract(&artifact, api.ConfirmedToolContract{Source: api.ToolContractPreflight, Tools: artifact.Definition.Tools}); err != nil {
		t.Fatal(err)
	}
	definition = artifact.Definition
	if err := api.ValidateTrustedArtifactDefinition(definition); err != nil {
		t.Fatalf("trusted definition rejected: %v", err)
	}
	store := NewStore()
	if err := store.RegisterTrustedArtifact(artifact, digest); err != nil {
		t.Fatalf("valid review rejected: %v", err)
	}
	base := artifact
	for name, mutate := range map[string]func(*api.ImportedArtifact){
		"empty-approval": func(_ *api.ImportedArtifact) {},
		"review-drift": func(value *api.ImportedArtifact) {
			value.Recipe.Entrypoint = append(value.Recipe.Entrypoint, "--changed")
		},
		"evidence-drift": func(value *api.ImportedArtifact) { value.Artifact.Evidence.SBOMDigest = "sha256:" + repeatHex('9') },
		"entrypoint-length": func(value *api.ImportedArtifact) {
			value.Recipe.Entrypoint = append(value.Recipe.Entrypoint, "--changed")
		},
		"entrypoint-value": func(value *api.ImportedArtifact) { value.Recipe.Entrypoint = []string{"/app/other"} },
	} {
		t.Run(name, func(t *testing.T) {
			candidate := base
			mutate(&candidate)
			approved := digest
			if name == "evidence-drift" || name == "entrypoint-length" || name == "entrypoint-value" {
				var err error
				candidate.Definition.Source.ReviewDigest, err = api.ReviewDigestForImported(candidate)
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
	api.AlignImportedEntrypoint(&artifact)
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
	config := api.NormalizeArtifactImportConfig(api.ArtifactImportConfig{DefinitionID: "mcp", Version: "1.0.0"})
	if config.Image == "" || config.Workload.Class != api.PerUser || config.Workload.ToolHiveVersion != "v0.48.0" || len(config.Workload.SidecarImages) != 1 {
		t.Fatalf("defaults not applied: %+v", config)
	}
	recipe := api.ArtifactRecipe{Format: "dockerfile-v1", Dockerfile: ".hub/Dockerfile", Files: []api.ArtifactFile{{Path: ".hub/Dockerfile", Digest: "sha256:" + repeatHex('a')}}, DependencyLocks: []string{".hub/Dockerfile"}, BaseImages: []string{"python@sha256:" + repeatHex('b')}, Entrypoint: []string{"/app/server"}}
	if digest, err := api.RecipeDigest(recipe); err != nil || len(digest) != len("sha256:")+64 {
		t.Fatalf("recipe digest: %q %v", digest, err)
	}
	valid := api.ImportedArtifact{Definition: statefulContainerDefinition(), Artifact: api.StoredOCIArtifact{ArchiveDigest: "sha256:" + repeatHex('c'), Evidence: api.OCIArtifactEvidence{ImageManifestDigest: "sha256:" + repeatHex('d'), ProvenanceDigest: "sha256:" + repeatHex('e'), SBOMDigest: "sha256:" + repeatHex('f')}}}
	valid.Definition.Source.ArchiveDigest = valid.Artifact.ArchiveDigest
	valid.Definition.Source.ProvenanceDigest = valid.Artifact.Evidence.ProvenanceDigest
	valid.Definition.Source.SBOMDigest = valid.Artifact.Evidence.SBOMDigest
	valid.Definition.Source.ReviewDigest = "sha256:" + repeatHex('0')
	if err := api.ValidateImportedReview(valid); err != nil {
		t.Fatalf("valid review rejected: %v", err)
	}
	valid.Definition.Source.ReviewDigest = ""
	if err := api.ValidateImportedReview(valid); err == nil {
		t.Fatal("missing review digest accepted")
	}
	valid.Artifact.Evidence.SBOMDigest = ""
	if err := api.ValidateImportedReview(valid); err == nil {
		t.Fatal("incomplete review accepted")
	}
	if err := api.ValidateTrustedArtifactDefinition(statefulContainerDefinition()); err == nil {
		t.Fatal("unattested container accepted as trusted")
	}
}

func TestArtifactEntrypointArgumentHelpers(t *testing.T) {
	if got := api.FirstArg(nil); got != "" {
		t.Fatalf("empty entrypoint command: %q", got)
	}
	if got := api.FirstArg([]string{"/app/server", "--stdio"}); got != "/app/server" {
		t.Fatalf("first entrypoint argument: %q", got)
	}
	if got := api.RemainingArgs(nil); got != nil {
		t.Fatalf("empty entrypoint arguments: %#v", got)
	}
	if got := api.RemainingArgs([]string{"/app/server"}); got != nil {
		t.Fatalf("single entrypoint argument: %#v", got)
	}
	got := api.RemainingArgs([]string{"/app/server", "--stdio", "--safe"})
	if len(got) != 2 || got[0] != "--stdio" || got[1] != "--safe" {
		t.Fatalf("remaining entrypoint arguments: %#v", got)
	}
}

func TestImportedReviewDigestMatchesPacketHandle(t *testing.T) {
	artifact := importedReviewPacket(t)
	got, err := api.ImportedReviewDigest(artifact)
	if err != nil || got != artifact.Definition.Source.ReviewDigest {
		t.Fatalf("ImportedReviewDigest: %q %v (packet %q)", got, err, artifact.Definition.Source.ReviewDigest)
	}
}

func TestImportGitHubArtifactRejectsBeforeNetwork(t *testing.T) {
	if _, err := api.ImportGitHubArtifact(context.Background(), api.ArtifactSource{Repository: "http://not-github", CommitSHA: "bad"}, api.ArtifactImportConfig{}, api.RestrictedBuildConfig{}); err == nil {
		t.Fatal("invalid source accepted")
	}
	if _, err := api.ImportGitHubArtifact(context.Background(), api.ArtifactSource{Repository: "https://github.com/example/mcp", CommitSHA: "0123456789012345678901234567890123456789"}, api.ArtifactImportConfig{DefinitionID: "mcp", Version: "1.0.0", Image: "repo@sha256:bad"}, api.RestrictedBuildConfig{}); err == nil {
		t.Fatal("unsafe image accepted")
	}
}

func TestImportPublishedArtifactUsesDigestAndAttestationEvidence(t *testing.T) {
	const commit = "0123456789abcdef0123456789abcdef01234567"
	source := api.ArtifactSource{Repository: "https://github.com/acme/weather", CommitSHA: commit}
	resolution := api.RecipeResolution{
		Source: source, State: "ready",
		Launch: api.LaunchRecipe{Transport: api.ContainerMCP, Artifact: "ghcr.io/acme/weather", Digest: "sha256:" + repeatHex('a'), Entrypoint: []string{"/app/server"}},
		Evidence: []api.RecipeEvidence{
			{Source: "oci", Digest: "sha256:" + repeatHex('b'), Detail: "immutable provenance referrer"},
			{Source: "oci", Digest: "sha256:" + repeatHex('c'), Detail: "immutable SBOM referrer"},
		},
	}
	imported, err := api.ImportPublishedArtifact(source, api.DefaultSelfInstallConfig(source), resolution, nil)
	if err != nil {
		t.Fatal(err)
	}
	if imported.Artifact.ArchiveDigest != "" || imported.Definition.Source.Image != resolution.Launch.Artifact || imported.Definition.Source.Digest != resolution.Launch.Digest || imported.Definition.Source.ProvenanceDigest == "" || imported.Definition.Source.SBOMDigest == "" {
		t.Fatalf("published evidence: %+v", imported)
	}
	if err := api.ValidateTrustedArtifactDefinition(imported.Definition); err == nil {
		t.Fatal("published packet accepted without a real tools/list contract")
	}
	if err := api.AttachConfirmedToolContract(&imported, api.ConfirmedToolContract{Source: api.ToolContractPreflight, Tools: imported.Definition.Tools}); err != nil {
		t.Fatal(err)
	}
	if err := api.ValidateTrustedArtifactDefinition(imported.Definition); err != nil {
		t.Fatalf("published trusted packet rejected: %v", err)
	}
}

func TestPreflightPublishedArtifactKeepsTheRealToolsListGate(t *testing.T) {
	d := statefulContainerDefinition()
	d.Source.Repository = "https://github.com/acme/weather"
	d.Source.CommitSHA = "0123456789abcdef0123456789abcdef01234567"
	d.Source.ArchiveDigest = ""
	packet := api.ImportedArtifact{Definition: d}
	originalPull, originalList := api.PublishedImagePull, api.PublishedMCPToolList
	defer func() { api.PublishedImagePull, api.PublishedMCPToolList = originalPull, originalList }()
	pulled := false
	api.PublishedImagePull = func(context.Context, string, string) error { pulled = true; return nil }
	api.PublishedMCPToolList = func(context.Context, api.ImportedArtifact) ([]api.ToolSpec, error) {
		return []api.ToolSpec{{Name: "read", Effect: api.ReadEffect}}, nil
	}
	preflighted, contract, err := api.PreflightPublishedArtifact(context.Background(), packet)
	if err != nil || !pulled || contract.Source != api.ToolContractPreflight || len(preflighted.Definition.Tools) != 1 || preflighted.Definition.Source.ToolContractDigest == "" {
		t.Fatalf("published preflight: pulled=%v contract=%+v packet=%+v err=%v", pulled, contract, preflighted, err)
	}
	if err := api.PullPublishedImage(context.Background(), "bad image", "sha256:bad"); err == nil {
		t.Fatal("invalid published image reference accepted")
	}
}

func TestPreflightImportedArtifactUsesTheSameToolsListContract(t *testing.T) {
	d := statefulContainerDefinition()
	d.Credentials = []api.CredentialInput{{Name: "API_TOKEN", Required: true}}
	packet := api.ImportedArtifact{Definition: d, Artifact: api.StoredOCIArtifact{ArchiveDigest: "sha256:" + repeatHex('a')}}
	originalLoader, originalList := api.StoredArtifactLoader, api.LocalMCPToolList
	defer func() { api.StoredArtifactLoader, api.LocalMCPToolList = originalLoader, originalList }()
	api.StoredArtifactLoader = func(context.Context, string, string, string, int64) (string, error) {
		return packet.Definition.Source.Image, nil
	}
	api.LocalMCPToolList = func(context.Context, api.ImportedArtifact) ([]api.ToolSpec, error) {
		return []api.ToolSpec{{Name: "read", Effect: api.ReadEffect}}, nil
	}
	preflighted, contract, err := api.PreflightImportedArtifact(context.Background(), packet, t.TempDir())
	if err != nil || contract.Source != api.ToolContractPreflight || preflighted.Definition.Source.ToolContractDigest == "" {
		t.Fatalf("local preflight: packet=%+v contract=%+v err=%v", preflighted, contract, err)
	}
}

func TestPreflightPromotesCredentialsProvenByToolsList(t *testing.T) {
	for _, test := range []struct {
		name         string
		credentials  []api.CredentialInput
		failText     string
		wantAttempts int
		wantErr      bool
		promoted     bool
	}{
		{"optional-secret-promoted", []api.CredentialInput{{Name: "API_TOKEN"}, {Name: "MCP_MODE"}}, "", 2, false, true},
		{"already-required", []api.CredentialInput{{Name: "API_TOKEN", Required: true}}, "", 1, false, false},
		{"no-secret-fields", []api.CredentialInput{{Name: "MCP_MODE"}}, "", 1, true, false},
		{"no-credentials", nil, "", 1, true, false},
		{"undeclared-from-auth-error", nil, "Error: authentication required: set GITHUB_PERSONAL_ACCESS_TOKEN, configure GitHub App auth, or pass --oauth-client-id", 2, false, true},
		{"undeclared-nonsecret-ignored", nil, "Error: set MCP_MODE to configure transport", 1, true, false},
	} {
		t.Run(test.name, func(t *testing.T) {
			d := statefulContainerDefinition()
			d.Credentials = test.credentials
			packet := api.ImportedArtifact{Definition: d, Artifact: api.StoredOCIArtifact{ArchiveDigest: "sha256:" + repeatHex('a')}}
			originalLoader, originalList := api.StoredArtifactLoader, api.LocalMCPToolList
			defer func() { api.StoredArtifactLoader, api.LocalMCPToolList = originalLoader, originalList }()
			api.StoredArtifactLoader = func(context.Context, string, string, string, int64) (string, error) {
				return packet.Definition.Source.Image, nil
			}
			failText := test.failText
			if failText == "" {
				failText = "authentication required"
			}
			attempts := 0
			api.LocalMCPToolList = func(_ context.Context, candidate api.ImportedArtifact) ([]api.ToolSpec, error) {
				attempts++
				for _, input := range candidate.Definition.Credentials {
					if input.Required && api.SecretName(input.Name) {
						return []api.ToolSpec{{Name: "read", Effect: api.ReadEffect}}, nil
					}
				}
				return nil, fmt.Errorf("%w: %s", api.ErrIsolation, failText)
			}
			preflighted, _, err := api.PreflightImportedArtifact(context.Background(), packet, t.TempDir())
			if (err != nil) != test.wantErr || attempts != test.wantAttempts {
				t.Fatalf("preflight: attempts=%d err=%v", attempts, err)
			}
			if test.wantErr || !test.promoted {
				return
			}
			for _, input := range preflighted.Definition.Credentials {
				if input.Required != api.SecretName(input.Name) {
					t.Fatalf("promotion boundary wrong: %+v", preflighted.Definition.Credentials)
				}
			}
		})
	}
}

func TestPreflightPromotionNarrowsToNamedCredentials(t *testing.T) {
	d := statefulContainerDefinition()
	d.Credentials = []api.CredentialInput{{Name: "API_TOKEN"}, {Name: "GITHUB_PERSONAL_ACCESS_TOKEN"}, {Name: "MCP_MODE"}}
	packet := api.ImportedArtifact{Definition: d, Artifact: api.StoredOCIArtifact{ArchiveDigest: "sha256:" + repeatHex('a')}}
	originalLoader, originalList := api.StoredArtifactLoader, api.LocalMCPToolList
	defer func() { api.StoredArtifactLoader, api.LocalMCPToolList = originalLoader, originalList }()
	api.StoredArtifactLoader = func(context.Context, string, string, string, int64) (string, error) {
		return packet.Definition.Source.Image, nil
	}
	attempts := 0
	api.LocalMCPToolList = func(_ context.Context, candidate api.ImportedArtifact) ([]api.ToolSpec, error) {
		attempts++
		for _, input := range candidate.Definition.Credentials {
			if input.Name == "GITHUB_PERSONAL_ACCESS_TOKEN" && input.Required {
				return []api.ToolSpec{{Name: "read", Effect: api.ReadEffect}}, nil
			}
		}
		return nil, fmt.Errorf("%w: authentication required: set GITHUB_PERSONAL_ACCESS_TOKEN", api.ErrIsolation)
	}
	preflighted, _, err := api.PreflightImportedArtifact(context.Background(), packet, t.TempDir())
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
	d.Credentials = []api.CredentialInput{{Name: "API_TOKEN"}}
	packet := api.ImportedArtifact{Definition: d}
	originalPull, originalList := api.PublishedImagePull, api.PublishedMCPToolList
	defer func() { api.PublishedImagePull, api.PublishedMCPToolList = originalPull, originalList }()
	api.PublishedImagePull = func(context.Context, string, string) error { return nil }
	api.PublishedMCPToolList = func(_ context.Context, candidate api.ImportedArtifact) ([]api.ToolSpec, error) {
		for _, input := range candidate.Definition.Credentials {
			if input.Required && api.SecretName(input.Name) {
				return []api.ToolSpec{{Name: "read", Effect: api.ReadEffect}}, nil
			}
		}
		return nil, fmt.Errorf("%w: authentication required", api.ErrIsolation)
	}
	preflighted, _, err := api.PreflightPublishedArtifact(context.Background(), packet)
	if err != nil || !preflighted.Definition.Credentials[0].Required {
		t.Fatalf("published promotion failed: %+v err=%v", preflighted.Definition.Credentials, err)
	}
}

func TestImportPublishedArtifactRejectsIncompleteOrMismatchedResolution(t *testing.T) {
	const commit = "0123456789abcdef0123456789abcdef01234567"
	source := api.ArtifactSource{Repository: "https://github.com/acme/weather", CommitSHA: commit}
	base := api.RecipeResolution{Source: source, State: "ready", Launch: api.LaunchRecipe{Transport: api.ContainerMCP, Artifact: "ghcr.io/acme/weather", Digest: "sha256:" + repeatHex('a'), Entrypoint: []string{"/app/server"}}, Evidence: []api.RecipeEvidence{{Source: "oci", Digest: "sha256:" + repeatHex('b'), Detail: "immutable provenance referrer"}, {Source: "oci", Digest: "sha256:" + repeatHex('c'), Detail: "immutable SBOM referrer"}}}
	for name, candidate := range map[string]api.RecipeResolution{
		"draft":           {Source: source, State: "draft", Launch: base.Launch, Evidence: base.Evidence},
		"wrong-source":    {Source: api.ArtifactSource{Repository: "https://github.com/other/weather", CommitSHA: commit}, State: "ready", Launch: base.Launch, Evidence: base.Evidence},
		"wrong-transport": {Source: source, State: "ready", Launch: api.LaunchRecipe{Transport: api.RemoteMCP, Endpoint: "https://mcp.example"}, Evidence: base.Evidence},
		"bad-digest":      {Source: source, State: "ready", Launch: api.LaunchRecipe{Transport: api.ContainerMCP, Artifact: "ghcr.io/acme/weather", Digest: "bad", Entrypoint: []string{"/app/server"}}, Evidence: base.Evidence},
		"missing-proof":   {Source: source, State: "ready", Launch: base.Launch, Evidence: nil},
	} {
		t.Run(name, func(t *testing.T) {
			if _, err := api.ImportPublishedArtifact(source, api.DefaultSelfInstallConfig(source), candidate, nil); err == nil {
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
