package api

import (
	"errors"
	"path/filepath"
	"testing"
)

// Definition IDs are sanitized to a bounded safe slug; the default
// self-install config derives its id from the repository path, and control
// artifact paths honor env overrides over the state root.
func TestControlReviewHelpers(t *testing.T) {
	for name, want := range map[string]string{
		"FS MCP":        "fsmcp",
		"fs-mcp_server": "fs-mcp_server",
		"42tools":       "m-42tools",
		"!!!":           "user-mcp",
		"":              "user-mcp",
		"  padded--x  ": "padded--x",
		"MiXeD_Case-9":  "mixed_case-9",
	} {
		if got := SanitizeDefinitionID(name); got != want {
			t.Fatalf("SanitizeDefinitionID(%q) = %q, want %q", name, got, want)
		}
	}
	long := SanitizeDefinitionID("a" + string(make([]byte, 64)))
	if len(long) > 40 {
		t.Fatalf("id too long: %d", len(long))
	}

	cfg := DefaultSelfInstallConfig(ArtifactSource{Repository: "https://github.com/acme/fs-mcp"})
	if cfg.DefinitionID != "fs-mcp" || cfg.Image != "hermes-artifact/fs-mcp" {
		t.Fatalf("self install id: %+v", cfg)
	}
	cfg = DefaultSelfInstallConfig(ArtifactSource{Repository: "not-a-url"})
	if cfg.DefinitionID != "user-mcp" {
		t.Fatalf("bad repository id: %+v", cfg)
	}

	review, ok := ReviewFromCredentialGate(ImportedArtifact{}, errors.New("plain"), nil)
	if ok {
		t.Fatalf("non-gate error produced review: %+v", review)
	}
	gated := ImportedArtifact{}
	gated.Definition.DefinitionID = "fs-mcp"
	review, ok = ReviewFromCredentialGate(gated, &CredentialGate{Detail: "need token"}, &RecipeResolution{State: "x"})
	if !ok || !review.AdmissionPending || review.AdmissionDetail != "need token" || review.Recipe == nil {
		t.Fatalf("gate review: %+v %v", review, ok)
	}

	root := t.TempDir()
	t.Setenv("HUB_ARTIFACT_DIR", "")
	t.Setenv("HUB_BUILD_SECCOMP", filepath.Join(root, "seccomp.json"))
	artifacts, seccomp := ControlArtifactPaths(root)
	if artifacts != filepath.Join(root, "artifacts") || !filepath.IsAbs(seccomp) || seccomp != filepath.Join(root, "seccomp.json") {
		t.Fatalf("paths: %q %q", artifacts, seccomp)
	}
	custom := filepath.Join(root, "custom-artifacts")
	t.Setenv("HUB_ARTIFACT_DIR", custom)
	artifacts, _ = ControlArtifactPaths(root)
	if artifacts != custom {
		t.Fatalf("env artifact dir: %q", artifacts)
	}
}
