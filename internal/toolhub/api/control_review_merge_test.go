package api

import (
	"fmt"
	"testing"
)

func TestSourceReviewHelpers(t *testing.T) {
	// sanitize produces a valid id or falls back
	for name, want := range map[string]string{
		"My Tool":      "mytool",
		"../evil/path": "evilpath",
		"9lives":       "m-9lives",
		"":             "user-mcp",
		"!!!":          "user-mcp",
		"repo.üñícode": "repocode",
	} {
		if got := SanitizeDefinitionID(name); got != want {
			t.Fatalf("%q -> %q (want %q)", name, got, want)
		}
	}
	long := SanitizeDefinitionID("a" + fmt.Sprintf("%050d", 0) + "verylongrepositorynamethatexceedslimits")
	if len(long) > 40 {
		t.Fatalf("long id: %q", long)
	}

	// self-install defaults: per-user, credentialed, bounded exec, exec health probe
	cfg := DefaultSelfInstallConfig(ArtifactSource{Repository: "https://github.com/acme/My-Server"})
	if cfg.DefinitionID != "my-server" || cfg.Workload.Class != PerUser || cfg.Image != "hermes-artifact/my-server" {
		t.Fatalf("config: %+v", cfg)
	}
	if cfg.Execution.TimeoutSeconds <= 0 || cfg.Health.Value != "/app/health" {
		t.Fatalf("defaults: %+v", cfg)
	}
	fallback := DefaultSelfInstallConfig(ArtifactSource{Repository: "not a url"})
	if fallback.DefinitionID != "user-mcp" {
		t.Fatalf("fallback id: %q", fallback.DefinitionID)
	}
}
