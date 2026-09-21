//go:build integration

package toolhub

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestRestrictedBuildKitRealPipeline(t *testing.T) {
	probe, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if output, err := exec.CommandContext(probe, "docker", "info", "--format", "{{.OSType}}").Output(); err != nil || strings.TrimSpace(string(output)) != "linux" {
		t.Skip("local Linux Docker runtime unavailable")
	}
	source := languageContext(t, map[string]string{"requirements.txt": "\n"})
	recipe, buildContext, err := GenerateArtifactRecipe(source, "python", "python@sha256:09f7da3bc104798d0afb40bc08d23ab2da20a76130cec1f2ef170848f5d85217", []string{"/bin/true"})
	if err != nil {
		t.Fatal(err)
	}
	seccomp, err := filepath.Abs(filepath.Join("..", "..", "docker", "seccomp-buildkit-rootless.json"))
	if err != nil {
		t.Fatal(err)
	}
	store := t.TempDir()
	ctx, stop := context.WithTimeout(context.Background(), 2*time.Minute)
	defer stop()
	artifact, err := BuildRestrictedOCI(ctx, recipe, buildContext, RestrictedBuildConfig{SeccompPath: seccomp, ArtifactDirectory: store, MaxArtifactBytes: 1 << 30})
	if err != nil {
		t.Fatal(err)
	}
	if artifact.ArchiveDigest == "" || artifact.Evidence.ProvenanceDigest == "" || artifact.Evidence.SBOMDigest == "" {
		t.Fatalf("missing immutable build evidence: %+v", artifact)
	}
	files, err := os.ReadDir(store)
	if err != nil || len(files) != 1 || files[0].Name() != artifactObjectName(artifact.ArchiveDigest) {
		t.Fatalf("unexpected quarantine objects: %v: %v", files, err)
	}
	file, reopened, err := OpenStoredOCIArtifact(ctx, store, artifact.ArchiveDigest, 1<<30)
	if err != nil {
		t.Fatal(err)
	}
	defer file.Close()
	if reopened != artifact {
		t.Fatalf("stored artifact evidence drifted: %+v != %+v", reopened, artifact)
	}
}
