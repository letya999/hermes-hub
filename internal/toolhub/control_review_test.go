package toolhub

import (
	"context"
	"errors"
	"path/filepath"
	"strings"
	"testing"
)

func TestDefaultSourceReviewerFailsClosedWithoutPaths(t *testing.T) {
	source, err := ParseGitHubSource(githubCommitURL())
	if err != nil {
		t.Fatal(err)
	}
	_, err = DefaultSourceReviewer("", "")(context.Background(), source, nil)
	if !errors.Is(err, ErrInvalid) {
		t.Fatalf("empty paths: %v", err)
	}
	_, err = DefaultSourceReviewer(t.TempDir(), filepath.Join(t.TempDir(), "missing.json"))(context.Background(), source, nil)
	if err == nil {
		t.Fatal("missing seccomp imported")
	}
	if errors.Is(err, ErrInvalid) && err.Error() == "invalid toolhub record: source reviewer unavailable" {
		t.Fatal("reviewer still reports unavailable")
	}
}

func TestSelectedRegistryArtifactMustMatchExactSourceBeforeFetch(t *testing.T) {
	source := mustParseGitHub(t)
	seccomp, err := filepath.Abs(filepath.Join("..", "..", "docker", "seccomp-buildkit-rootless.json"))
	if err != nil {
		t.Fatal(err)
	}
	selected := RecipeCandidate{Repository: "https://github.com/other/mcp", CommitSHA: source.CommitSHA, Launch: LaunchRecipe{Transport: ContainerMCP, Artifact: "ghcr.io/other/mcp", Digest: "sha256:" + strings.Repeat("a", 64)}}
	if _, err := DefaultSourceReviewer(t.TempDir(), seccomp)(t.Context(), source, &selected); !errors.Is(err, ErrInvalid) {
		t.Fatalf("cross-source selected artifact reached fetch/build: %v", err)
	}
}

func TestSanitizeDefinitionID(t *testing.T) {
	if got := sanitizeDefinitionID("MCP.Server"); got != "mcpserver" {
		t.Fatalf("dots: %q", got)
	}
	if got := sanitizeDefinitionID("9lives"); got != "m-9lives" {
		t.Fatalf("digit: %q", got)
	}
	if got := sanitizeDefinitionID("***"); got != "user-mcp" {
		t.Fatalf("empty: %q", got)
	}
	config := defaultSelfInstallConfig(mustParseGitHub(t))
	if got := config.DefinitionID; got != "mcp" {
		t.Fatalf("repo id=%q", got)
	}
	if config.Workload.Stateful || len(config.Execution.Mounts) != 0 {
		t.Fatalf("generic self-install must default to stateless: %#v", config)
	}
}

func mustParseGitHub(t *testing.T) ArtifactSource {
	t.Helper()
	source, err := ParseGitHubSource(githubCommitURL())
	if err != nil {
		t.Fatal(err)
	}
	return source
}
