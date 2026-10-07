//go:build integration

package diagnostics

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// Real-Docker end-to-end check for the collector's project scoping: a
// container carrying the owning compose-project label is drained, a foreign
// container's logs never reach the diagnostics file. Run with:
//
//	HUB_DIAG_IMAGE=hermes-hub:0.3.0-dev go test -tags integration -run TestCollectRealDocker ./internal/diagnostics
func TestCollectRealDockerScopesToProject(t *testing.T) {
	if _, err := exec.LookPath("docker"); err != nil {
		t.Skip("docker not installed")
	}
	image := os.Getenv("HUB_DIAG_IMAGE")
	if image == "" {
		image = "hermes-hub:0.3.0-dev"
	}
	if _, err := Docker(context.Background(), "image", "inspect", image); err != nil {
		t.Skipf("image %s unavailable: %v", image, err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()

	project := "capdiag-proj"
	suffix := fmt.Sprint(time.Now().UnixNano())
	own := "capdiag-own-" + suffix
	foreign := "capdiag-foreign-" + suffix

	runFixture := func(name string, labels ...string) {
		t.Helper()
		args := append([]string{"run", "-d", "--name", name, "--entrypoint", "echo"}, labels...)
		args = append(args, image, "capdiag-marker-"+name)
		if out, err := Docker(ctx, args...); err != nil {
			t.Fatalf("docker %v: %v (%s)", args, err, out)
		}
	}
	runFixture(own, "--label", "com.docker.compose.project="+project)
	runFixture(foreign)
	t.Cleanup(func() { _, _ = Docker(context.Background(), "rm", "-f", own, foreign) })

	dir := t.TempDir()
	path := filepath.Join(dir, "hermes-diagnostics.txt")
	if err := Collect(ctx, path, Docker, Scope{Project: project, Owner: "capdiag-owner"}); err != nil {
		t.Fatalf("collect: %v", err)
	}
	body, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read diagnostics: %v", err)
	}
	if !strings.Contains(string(body), "capdiag-marker-"+own) {
		t.Errorf("own container logs missing from diagnostics:\n%s", body)
	}
	if strings.Contains(string(body), own) && !strings.Contains(string(body), "["+own+"]") {
		t.Errorf("own container name must appear as a bracketed attribution:\n%s", body)
	}
	if strings.Contains(string(body), "capdiag-marker-"+foreign) || strings.Contains(string(body), "["+foreign+"]") {
		t.Errorf("foreign container leaked into scoped diagnostics:\n%s", body)
	}

	// The drained-once cursor marks the stopped own container and nothing else.
	cursor, err := os.ReadFile(path + ".cursor.json")
	if err != nil {
		t.Fatalf("read cursor: %v", err)
	}
	if strings.Contains(string(cursor), foreign) {
		t.Error("foreign container must not enter the cursor")
	}
}
