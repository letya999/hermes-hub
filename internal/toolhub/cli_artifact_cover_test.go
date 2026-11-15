package toolhub

import (
	"context"
	"net/http"
	"strings"
	"testing"

	"github.com/letya999/hermes-hub/internal/toolhub/api"
)

func TestImportCLIArtifactGuards(t *testing.T) {
	ctx := context.Background()
	build := api.RestrictedBuildConfig{}
	source := api.ArtifactSource{
		Repository: "https://github.com/owner/repo",
		CommitSHA:  strings.Repeat("ab", 20),
	}
	config := api.ArtifactImportConfig{
		DefinitionID: "def-1",
		Image:        "hermes-artifact/def-1",
		Entrypoint:   []string{"/tool"},
	}
	// Bad source is refused before any fetch.
	badSource := source
	badSource.Repository = "http://insecure.example/repo"
	if _, err := ImportCLIArtifact(ctx, badSource, config, build); err == nil {
		t.Fatal("insecure repository admitted")
	}
	badSource.Repository = "https://github.com/owner/repo"
	badSource.CommitSHA = "not-a-sha"
	if _, err := ImportCLIArtifact(ctx, badSource, config, build); err == nil {
		t.Fatal("unpinned sha admitted")
	}
	// Image name must be local and immutable.
	for _, image := range []string{"", "bad;image", "img@sha256:abc", "sh"} {
		bad := config
		bad.Image = image
		if _, err := ImportCLIArtifact(ctx, source, bad, build); err == nil {
			t.Fatalf("image %q admitted", image)
		}
	}
	// Entrypoint must be one guest path.
	for _, ep := range [][]string{nil, {"a", "b"}, {"relative"}, {"/a/../b"}, {"x;rm"}} {
		bad := config
		bad.Entrypoint = ep
		if _, err := ImportCLIArtifact(ctx, source, bad, build); err == nil {
			t.Fatalf("entrypoint %v admitted", ep)
		}
	}
	// A valid front end proceeds to the fetch and fails on the network —
	// reaching the fetch proves the guards passed.
	if _, err := ImportCLIArtifact(ctx, source, config, build); err == nil {
		t.Fatal("unreachable source produced artifact")
	}
}

func TestImportCLIReleaseArtifactGuards(t *testing.T) {
	ctx := context.Background()
	build := api.RestrictedBuildConfig{}
	source := api.ArtifactSource{Repository: "https://github.com/owner/repo", Tag: "v1", Asset: "tool-linux-amd64"}
	config := api.ArtifactImportConfig{DefinitionID: "def-1", Image: "hermes-artifact/def-1", Entrypoint: []string{"/tool"}}
	// .mcpb dispatches to the bundle importer.
	mcpb := source
	mcpb.Asset = "bundle.mcpb"
	mcpb.Tag = ""
	if _, err := ImportCLIReleaseArtifact(ctx, mcpb, config, build, http.DefaultClient, "https://api.example"); err == nil {
		t.Fatal("mcpb without tag admitted")
	}
	for _, bad := range []api.ArtifactSource{
		{Repository: source.Repository, Asset: "a"},
		{Repository: source.Repository, Tag: "v1"},
	} {
		if _, err := ImportCLIReleaseArtifact(ctx, bad, config, build, http.DefaultClient, "https://api.example"); err == nil {
			t.Fatal("incomplete release source admitted")
		}
	}
	for _, image := range []string{"bad;image", "img@sha256:abc"} {
		bad := config
		bad.Image = image
		if _, err := ImportCLIReleaseArtifact(ctx, source, bad, build, http.DefaultClient, "https://api.example"); err == nil {
			t.Fatalf("image %q admitted", image)
		}
	}
	bad := config
	bad.Entrypoint = []string{"relative"}
	if _, err := ImportCLIReleaseArtifact(ctx, source, bad, build, http.DefaultClient, "https://api.example"); err == nil {
		t.Fatal("bad entrypoint admitted")
	}
	// mcpb direct guard.
	if _, err := ImportMCPBReleaseArtifact(ctx, api.ArtifactSource{}, config, build, http.DefaultClient, "https://api.example"); err == nil {
		t.Fatal("empty mcpb source admitted")
	}
}
