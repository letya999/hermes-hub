//go:build integration

package toolhub

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"testing"
	"time"
)

// Real-Docker end-to-end check for the socket boundary: foreign containers,
// networks and volumes are denied with ErrIsolation while labelled own
// objects pass. Run with:
//
//	HUB_SCOPE_IMAGE=hermes-hub:0.3.0-dev go test -tags integration -run TestDockerScopeRealDocker ./internal/toolhub
func TestDockerScopeRealDocker(t *testing.T) {
	if _, err := exec.LookPath("docker"); err != nil {
		t.Skip("docker not installed")
	}
	image := os.Getenv("HUB_SCOPE_IMAGE")
	if image == "" {
		image = "hermes-hub:0.3.0-dev"
	}
	if out, err := localCommand(context.Background(), "docker", "image", "inspect", image); err != nil {
		t.Skipf("image %s unavailable: %v (%s)", image, err, out)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()

	project := "hermes-hub-capscope-dev"
	agentNet := "hermes-hub-agent-capscope-dev"
	s := newDockerScope(project, agentNet, localCommand)

	suffix := fmt.Sprint(time.Now().UnixNano())
	foreign := "capscope-foreign-" + suffix
	own := "capscope-own-" + suffix
	foreignNet := "capscope-foreign-net-" + suffix
	ownNet := "capscope-own-net-" + suffix
	foreignVol := "capscope-foreign-vol-" + suffix
	ownVol := "capscope-own-vol-" + suffix

	must := func(args ...string) {
		t.Helper()
		if out, err := localCommand(ctx, "docker", args...); err != nil {
			t.Fatalf("docker %v: %v (%s)", args, err, out)
		}
	}
	must("create", "--name", foreign, image)
	must("create", "--name", own, "--label", "hermes-hub.scope="+project, image)
	must("network", "create", foreignNet)
	must("network", "create", "--label", "hermes-hub.scope="+project, ownNet)
	must("volume", "create", foreignVol)
	must("volume", "create", "--label", "hermes-hub.scope="+project, ownVol)
	t.Cleanup(func() {
		clean := context.Background()
		_, _ = localCommand(clean, "docker", "rm", "-f", foreign, own)
		_, _ = localCommand(clean, "docker", "network", "rm", foreignNet, ownNet)
		_, _ = localCommand(clean, "docker", "volume", "rm", foreignVol, ownVol)
	})

	denied := [][]string{
		{"inspect", foreign},
		{"container", "inspect", foreign},
		{"logs", foreign},
		{"exec", foreign, "true"},
		{"rm", foreign},
		{"stop", foreign},
		{"kill", foreign},
		{"cp", foreign + ":/etc/hostname", "."},
		{"network", "inspect", foreignNet},
		{"network", "rm", foreignNet},
		{"volume", "inspect", foreignVol},
		{"volume", "rm", foreignVol},
	}
	for _, args := range denied {
		if _, err := s.runScoped(ctx, localCommand, "docker", args...); !errors.Is(err, ErrIsolation) {
			t.Errorf("docker %v on foreign object: want ErrIsolation, got %v", args, err)
		}
	}
	// ToolHive removes by name through a non-docker binary.
	if _, err := s.runScoped(ctx, localCommand, "thv", "rm", foreign); !errors.Is(err, ErrIsolation) {
		t.Errorf("thv rm foreign: want ErrIsolation, got %v", err)
	}

	allowed := [][]string{
		{"inspect", own},
		{"network", "inspect", ownNet},
		{"volume", "inspect", ownVol},
	}
	for _, args := range allowed {
		if _, err := s.runScoped(ctx, localCommand, "docker", args...); err != nil {
			t.Errorf("docker %v on own object: %v", args, err)
		}
	}

	// A container attached to the managed agent network is in scope even
	// without labels (supervisor-spawned runtimes).
	managed := "capscope-managed-" + suffix
	must("create", "--name", managed, "--network", agentNetOK(t, agentNet), image)
	t.Cleanup(func() { _, _ = localCommand(context.Background(), "docker", "rm", "-f", managed) })
	if _, err := s.runScoped(ctx, localCommand, "docker", "inspect", managed); err != nil {
		t.Errorf("inspect managed-network container: %v", err)
	}

	// Nonexistent objects pass the guard (docker itself reports absence).
	if _, err := s.runScoped(ctx, localCommand, "docker", "inspect", "capscope-missing-"+suffix); errors.Is(err, ErrIsolation) {
		t.Error("missing object must not be denied by the scope guard")
	}
}

func agentNetOK(t *testing.T, name string) string {
	t.Helper()
	if _, err := localCommand(context.Background(), "docker", "network", "inspect", name); err != nil {
		if out, cerr := localCommand(context.Background(), "docker", "network", "create", name); cerr != nil {
			t.Fatalf("create agent net: %v (%s)", cerr, out)
		}
		t.Cleanup(func() { _, _ = localCommand(context.Background(), "docker", "network", "rm", name) })
	}
	return name
}
