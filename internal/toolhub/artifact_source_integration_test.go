//go:build integration

package toolhub

import (
	"archive/tar"
	"bytes"
	"context"
	"io"
	"path/filepath"
	"slices"
	"testing"
	"time"
)

// Public source read only: no Docker, language runtime or account credential.
func TestArtifactSourceGitHubRead(t *testing.T) {
	source := ArtifactSource{Repository: "https://github.com/letya999/hermes-hub", CommitSHA: "d79fba521cf50b581701ee1c402b828e793262b6"}
	data, err := FetchArtifactContext(context.Background(), source, []string{"go.mod", "README.md"}, 64<<10)
	if err != nil {
		t.Fatal(err)
	}
	r := tar.NewReader(bytes.NewReader(data))
	for _, name := range []string{"go.mod", "README.md"} {
		h, err := r.Next()
		if err != nil || h.Name != name || h.Size == 0 {
			t.Fatalf("invalid selected context header %+v: %v", h, err)
		}
		if _, err := io.Copy(io.Discard, r); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := r.Next(); err != io.EOF {
		t.Fatalf("unexpected context file: %v", err)
	}
}

func TestArtifactRequestedPublicBuilds(t *testing.T) {
	seccomp, err := filepath.Abs(filepath.Join("..", "..", "docker", "seccomp-buildkit-rootless.json"))
	if err != nil {
		t.Fatal(err)
	}
	cases := []struct {
		name, repository, sha, language, base string
		entrypoint                            []string
	}{
		{"serena", "oraios/serena", "704e8c3d1929bdd74bf0c8eee1596aeddf03fdb2", "python", "python@sha256:09f7da3bc104798d0afb40bc08d23ab2da20a76130cec1f2ef170848f5d85217", []string{"/opt/venv/bin/serena-agent", "start-mcp-server", "--transport", "stdio", "--enable-web-dashboard", "false", "--open-web-dashboard", "false", "--enable-gui-log-window", "false", "--tool-timeout", "10"}},
		{"context7", "upstash/context7", "b653c3a07d7936bdc4c23fc1c88903120e0ece77", "node", "node@sha256:cd9f682fa2885cd1056e830424764158570061c59736a1da836bc3d73df095ae", nil},
		{"go-filesystem", "mark3labs/mcp-filesystem-server", "ba3f07f22c309d932fa9b1cebe1eb7c55fcbb83b", "go", "golang@sha256:648f440f42a0958804efb24df176f806f9d353b41f1c0627f666428e40310f6b", nil},
		{"rust-filesystem", "rust-mcp-stack/rust-mcp-filesystem", "ef4797360ea03eec5375e03a6f10d092dfc6a5e0", "rust", "rust@sha256:ebd900bae66fd508b466cef82d64a83a5fb34682e4c8b2797a42908bddc95a57", nil},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			ctx, cancel := context.WithTimeout(context.Background(), 10*time.Minute)
			defer cancel()
			source := ArtifactSource{Repository: "https://github.com/" + tc.repository, CommitSHA: tc.sha}
			data, err := FetchRepositoryArtifactContext(ctx, source, 64<<20)
			if err != nil {
				t.Fatalf("exact-SHA import: %v", err)
			}
			recipe, generated, err := GenerateArtifactRecipe(data, tc.language, tc.base, tc.entrypoint)
			if err != nil {
				t.Fatalf("recipe: %v", err)
			}
			artifact, err := BuildRestrictedOCI(ctx, recipe, generated, RestrictedBuildConfig{SeccompPath: seccomp, ArtifactDirectory: t.TempDir(), MaxArtifactBytes: 4 << 30})
			if err != nil {
				t.Fatalf("restricted build: %v", err)
			}
			if artifact.ArchiveDigest == "" || artifact.Evidence.ProvenanceDigest == "" || artifact.Evidence.SBOMDigest == "" {
				t.Fatalf("missing build evidence: %+v", artifact)
			}
			t.Logf("built %s at %s: archive %s, image %s", tc.name, tc.sha, artifact.ArchiveDigest, artifact.Evidence.ImageManifestDigest)
		})
	}
}

func TestArtifactAutomaticPythonGitHubContext(t *testing.T) {
	source := ArtifactSource{Repository: "https://github.com/chigwell/telegram-mcp", CommitSHA: "c9460f8ded6e2457bd70ebabfad840b58d23645d"}
	data, err := FetchRepositoryArtifactContext(context.Background(), source, 8<<20)
	if err != nil {
		t.Fatal(err)
	}
	r, generated, err := GenerateArtifactRecipe(data, "python", "python@sha256:09f7da3bc104798d0afb40bc08d23ab2da20a76130cec1f2ef170848f5d85217", nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(r.Files) < 2 || len(r.Entrypoint) == 0 {
		t.Fatal("empty generated recipe")
	}
	if err := r.VerifyContext(generated); err != nil {
		t.Fatal(err)
	}
}

// These are real public servers requested by the owner, not SDK mock fixtures.
// This gate covers source import and recipe generation, not runtime delivery.
func TestArtifactRequestedPublicServers(t *testing.T) {
	cases := []struct{ name, repository, sha, language, base string }{
		{"serena", "oraios/serena", "704e8c3d1929bdd74bf0c8eee1596aeddf03fdb2", "python", "python@sha256:09f7da3bc104798d0afb40bc08d23ab2da20a76130cec1f2ef170848f5d85217"},
		{"context7", "upstash/context7", "b653c3a07d7936bdc4c23fc1c88903120e0ece77", "node", "node@sha256:cd9f682fa2885cd1056e830424764158570061c59736a1da836bc3d73df095ae"},
		{"go-filesystem", "mark3labs/mcp-filesystem-server", "ba3f07f22c309d932fa9b1cebe1eb7c55fcbb83b", "go", "golang@sha256:648f440f42a0958804efb24df176f806f9d353b41f1c0627f666428e40310f6b"},
		{"rust-filesystem", "rust-mcp-stack/rust-mcp-filesystem", "ef4797360ea03eec5375e03a6f10d092dfc6a5e0", "rust", "rust@sha256:ebd900bae66fd508b466cef82d64a83a5fb34682e4c8b2797a42908bddc95a57"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			source := ArtifactSource{Repository: "https://github.com/" + tc.repository, CommitSHA: tc.sha}
			data, err := FetchRepositoryArtifactContext(context.Background(), source, 64<<20)
			if err != nil {
				t.Fatalf("exact-SHA import: %v", err)
			}
			t.Logf("imported %d context bytes at %s", len(data), tc.sha)
			var entrypoint []string
			if tc.name == "serena" {
				// Verified against this exact commit's CLI, not a guessed start script.
				// A package console command alone does not establish an MCP entrypoint.
				entrypoint = []string{"/opt/venv/bin/serena-agent", "start-mcp-server", "--transport", "stdio", "--enable-web-dashboard", "false", "--open-web-dashboard", "false", "--enable-gui-log-window", "false", "--tool-timeout", "10"}
			}
			recipe, generated, err := GenerateArtifactRecipe(data, tc.language, tc.base, entrypoint)
			if err != nil {
				t.Fatalf("recipe: %v", err)
			}
			if err := recipe.VerifyContext(generated); err != nil {
				t.Fatal(err)
			}
			if entrypoint != nil && !slices.Equal(recipe.Entrypoint, entrypoint) {
				t.Fatal("explicit MCP launch arguments not retained")
			}
			t.Logf("verified recipe: %d files, entrypoint %q; build/runtime NOT tested", len(recipe.Files), recipe.Entrypoint)
		})
	}
}
