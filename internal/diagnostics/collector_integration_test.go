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
//	HUB_DIAG_IMAGE=hermes-hub:0.5.0-dev go test -tags integration -run TestCollectRealDocker ./internal/diagnostics
func TestCollectRealDockerScopesToProject(t *testing.T) {
	if _, err := exec.LookPath("docker"); err != nil {
		t.Skip("docker not installed")
	}
	image := os.Getenv("HUB_DIAG_IMAGE")
	if image == "" {
		image = "hermes-hub:0.5.0-dev"
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
		args = append(args, image, `{"event":"fixture","job_id":"capdiag-marker-`+name+`"}`)
		if out, err := Docker(ctx, args...); err != nil {
			t.Fatalf("docker %v: %v (%s)", args, err, out)
		}
	}
	runFixture(own, "--label", "com.docker.compose.project="+project)
	runFixture(foreign)
	t.Cleanup(func() { _, _ = Docker(context.Background(), "rm", "-f", own, foreign) })

	dir := t.TempDir()
	path := filepath.Join(dir, "hermes-diagnostics.txt")
	if err := Collect(ctx, path, nil, Scope{Project: project, Owner: "capdiag-owner"}); err != nil {
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

// Actual Docker backlog proof; never selected by the default non-integration suite.
func TestCollectRealDockerBacklog5001(t *testing.T) {
	if _, err := exec.LookPath("docker"); err != nil {
		t.Skip("docker unavailable")
	}
	image := os.Getenv("HUB_DIAG_IMAGE")
	if image == "" {
		t.Skip("set HUB_DIAG_IMAGE to an admitted test image")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	name := "capdiag-backlog-" + fmt.Sprint(time.Now().UnixNano())
	project := name
	script := `i=0; while [ "$i" -le 5000 ]; do printf '{"event":"fixture","job_id":"backlog-owned","count":%s}\n' "$i"; i=$((i+1)); done`
	if _, err := Docker(ctx, "run", "-d", "--name", name, "--label", "com.docker.compose.project="+project, "--entrypoint", "sh", image, "-c", script); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _, _ = Docker(context.Background(), "rm", "-f", name) })
	if _, err := Docker(ctx, "wait", name); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(t.TempDir(), "diagnostic.log")
	for range 3 {
		if err := Collect(ctx, path, nil, Scope{Project: project}); err != nil {
			t.Fatal(err)
		}
	}
	body, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	lines := strings.Split(strings.TrimSpace(string(body)), "\n")
	if len(lines) != 5001 {
		t.Fatalf("backlog lines=%d, want 5001", len(lines))
	}
	for _, value := range []string{`"count":0,`, `"count":5000,`} {
		if strings.Count(string(body), value) != 1 {
			t.Fatalf("missing/duplicated boundary %s", value)
		}
	}
}
