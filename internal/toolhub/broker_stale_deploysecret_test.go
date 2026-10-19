package toolhub

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/letya999/hermes-hub/internal/toolhub/api"
)

// A stored self-install definition whose required set still carries a
// deploy-secret name (MCP server's own listener guard, e.g.
// MCP_HTTP_AUTH_TOKEN) must not be reused: its requiredness predates the
// deploy-secret demotion heuristic, so reuse would resurrect a credential the
// owner can never satisfy — the demote form produces no env for it and
// admission fails closed on every retry. Failing closed here forces a fresh
// review, which re-parses the source with the demotion applied.
func TestPrepareSourceReReviewsDeploySecretRequirement(t *testing.T) {
	store := NewStore()
	repo := "https://github.com/example/mcp-deploy-secret"
	stale := remoteDefinition()
	stale.DefinitionID = "mcp-deploy-secret"
	stale.Version = "0.0.2"
	stale.Transport = api.ContainerMCP
	stale.Source = api.DefinitionSource{
		Image:         "hermes-artifact/mcp-deploy-secret",
		Digest:        "sha256:3333333333333333333333333333333333333333333333333333333333333333",
		Repository:    repo,
		CommitSHA:     "0123456789abcdef0123456789abcdef01234567",
		ArchiveDigest: "sha256:00000000000000000000000000000000000000000000000000000000deadbeef",
		RecipeDigest:  "sha256:2222222222222222222222222222222222222222222222222222222222222222",
		ReviewDigest:  "sha256:1111111111111111111111111111111111111111111111111111111111111111",
		Command:       "/app/mcp-server",
		Args:          []string{"stdio"},
	}
	stale.Workload.ToolHiveVersion = "v0.48.0"
	stale.Workload.SidecarImages = []string{"ghcr.io/stacklok/toolhive/egress-proxy@sha256:0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef"}
	stale.Health = api.HealthProbe{Kind: "exec", Value: "/app/mcp-server --help", TimeoutSeconds: 5}
	stale.Credentials = []api.CredentialInput{
		{Name: "MCP_HTTP_AUTH_TOKEN", Required: true, Delivery: "env", Source: "docker-compose.yml"},
	}
	if digest, err := api.ConfirmedToolContractDigest(api.ConfirmedToolContract{Source: api.ToolContractReviewManifest, Tools: stale.Tools}); err != nil {
		t.Fatal(err)
	} else {
		stale.Source.ToolContractSource = api.ToolContractReviewManifest
		stale.Source.ToolContractDigest = digest
	}
	if err := store.RegisterDefinition(stale); err != nil {
		t.Fatal(err)
	}
	if err := store.PutPublication(api.DefinitionPublication{DefinitionID: stale.DefinitionID, Version: stale.Version, Visibility: api.PublicationUser, OwnerPrincipalID: "alice"}); err != nil {
		t.Fatal(err)
	}
	if err := putGrant(t, store, api.OperatorGrant(api.GrantSelfInstall, "alice", "", "")); err != nil {
		t.Fatal(err)
	}

	artifacts := t.TempDir()
	if err := os.WriteFile(filepath.Join(artifacts, api.ArtifactObjectName(stale.Source.ArchiveDigest)), []byte("quarantine tar placeholder"), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("HUB_ARTIFACT_DIR", artifacts)

	fresh := stale
	fresh.Credentials = []api.CredentialInput{
		{Name: "MCP_HTTP_AUTH_TOKEN", Required: false, Delivery: "env", Source: "docker-compose.yml"},
	}
	reviewerRan := false
	control := &ControlPlane{Store: store, WorkloadRoot: t.TempDir(), Now: time.Now}
	control.SourceResolver = func(_ context.Context, raw string) (api.ArtifactSource, error) {
		return api.ArtifactSource{Repository: raw, CommitSHA: stale.Source.CommitSHA}, nil
	}
	control.Reviewer = func(_ context.Context, _ api.ArtifactSource, _ *api.RecipeCandidate) (api.SourceReview, error) {
		reviewerRan = true
		return api.SourceReview{Definition: fresh, Permissions: api.ToolNames(fresh), Effects: api.EffectNames(fresh), ReviewDigest: "sha256:4444444444444444444444444444444444444444444444444444444444444444"}, nil
	}
	body, err := control.prepareSource(t.Context(), aliceAuth(), map[string]any{"source": repo, "request_key": "deploy-secret-reuse"})
	if err != nil {
		t.Fatalf("re-reviewed prepare failed: %v", err)
	}
	if !reviewerRan {
		t.Fatal("stale deploy-secret definition was reused instead of re-reviewed")
	}
	if body["version"] == stale.Version {
		t.Fatalf("demoted definition must not reuse the stale version key: %+v", body)
	}
}
