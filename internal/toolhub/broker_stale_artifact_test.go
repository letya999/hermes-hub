package toolhub

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/letya999/hermes-hub/internal/toolhub/api"
)

// The same stored definition is reused without re-review when its quarantine
// tar is still present: a pruned daemon image is recoverable through
// StoredArtifactLoader, so skipping review stays sound.
func TestPrepareSourceReusesWhenArtifactPresent(t *testing.T) {
	store := NewStore()
	repo := "https://github.com/example/mcp-kept-artifact"
	definition := remoteDefinition()
	definition.DefinitionID = "mcp-kept-artifact"
	definition.Version = "0.0.2"
	definition.Transport = api.ContainerMCP
	definition.Source = api.DefinitionSource{
		Image:         "hermes-artifact/mcp-kept-artifact",
		Digest:        "sha256:3333333333333333333333333333333333333333333333333333333333333333",
		Repository:    repo,
		CommitSHA:     "0123456789abcdef0123456789abcdef01234567",
		ArchiveDigest: "sha256:00000000000000000000000000000000000000000000000000000000deadbeef",
		RecipeDigest:  "sha256:2222222222222222222222222222222222222222222222222222222222222222",
		ReviewDigest:  "sha256:1111111111111111111111111111111111111111111111111111111111111111",
		Command:       "/app/mcp-server",
		Args:          []string{"stdio"},
	}
	definition.Workload.ToolHiveVersion = "v0.48.0"
	definition.Workload.SidecarImages = []string{"ghcr.io/stacklok/toolhive/egress-proxy@sha256:0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef"}
	definition.Health = api.HealthProbe{Kind: "exec", Value: "/app/mcp-server --help", TimeoutSeconds: 5}
	if digest, err := api.ConfirmedToolContractDigest(api.ConfirmedToolContract{Source: api.ToolContractReviewManifest, Tools: definition.Tools}); err != nil {
		t.Fatal(err)
	} else {
		definition.Source.ToolContractSource = api.ToolContractReviewManifest
		definition.Source.ToolContractDigest = digest
	}
	if err := store.RegisterDefinition(definition); err != nil {
		t.Fatal(err)
	}
	if err := store.PutPublication(api.DefinitionPublication{DefinitionID: definition.DefinitionID, Version: definition.Version, Visibility: api.PublicationUser, OwnerPrincipalID: "alice"}); err != nil {
		t.Fatal(err)
	}
	if err := putGrant(t, store, api.OperatorGrant(api.GrantSelfInstall, "alice", "", "")); err != nil {
		t.Fatal(err)
	}

	artifacts := t.TempDir()
	if err := os.WriteFile(filepath.Join(artifacts, api.ArtifactObjectName(definition.Source.ArchiveDigest)), []byte("quarantine tar placeholder"), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("HUB_ARTIFACT_DIR", artifacts)
	control := &ControlPlane{Store: store, WorkloadRoot: t.TempDir(), Now: time.Now}
	control.SourceResolver = func(_ context.Context, raw string) (api.ArtifactSource, error) {
		return api.ArtifactSource{Repository: raw, CommitSHA: definition.Source.CommitSHA}, nil
	}
	control.Reviewer = func(_ context.Context, _ api.ArtifactSource, _ *api.RecipeCandidate) (api.SourceReview, error) {
		t.Fatal("reviewer ran although a usable reviewed definition exists")
		return api.SourceReview{}, errors.New("unreachable")
	}
	body, err := control.prepareSource(t.Context(), aliceAuth(), map[string]any{"source": repo, "request_key": "reuse-present-artifact"})
	if err != nil {
		t.Fatalf("usable definition was not reused: %v", err)
	}
	if body["definition_id"] != definition.DefinitionID || body["version"] != definition.Version {
		t.Fatalf("unexpected definition selected: %+v", body)
	}
}
