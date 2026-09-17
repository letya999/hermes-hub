package toolhub

import (
	"context"
	"errors"
	"path/filepath"
	"testing"
)

func TestDefaultSourceReviewerFailsClosedWithoutPaths(t *testing.T) {
	source, err := ParseGitHubSource(githubCommitURL())
	if err != nil {
		t.Fatal(err)
	}
	_, err = DefaultSourceReviewer("", "")(context.Background(), source)
	if !errors.Is(err, ErrInvalid) {
		t.Fatalf("empty paths: %v", err)
	}
	_, err = DefaultSourceReviewer(t.TempDir(), filepath.Join(t.TempDir(), "missing.json"))(context.Background(), source)
	if err == nil {
		t.Fatal("missing seccomp imported")
	}
	if errors.Is(err, ErrInvalid) && err.Error() == "invalid toolhub record: source reviewer unavailable" {
		t.Fatal("reviewer still reports unavailable")
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
	if got := defaultSelfInstallConfig(mustParseGitHub(t)).DefinitionID; got != "mcp" {
		t.Fatalf("repo id=%q", got)
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
