//go:build integration

package devcheck

import (
	"context"
	"crypto/rand"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/letya999/hermes-hub/internal/supervisor"
)

// SupervisorSmoke exercises the real host supervisor against the pinned image:
// one context is started, kept ready, reaped, and cold-started from the same home.
func supervisorSmoke(ctx context.Context, image string) error {
	suffix := make([]byte, 4)
	if _, err := rand.Read(suffix); err != nil {
		return err
	}
	network := fmt.Sprintf("hermes-supervisor-%x", suffix)
	root, err := os.MkdirTemp("", "hermes-supervisor-smoke-")
	if err != nil {
		return err
	}
	defer os.RemoveAll(root)
	cleanup := func() { _ = exec.Command("docker", "network", "rm", network).Run() }
	defer cleanup()
	if err := exec.CommandContext(ctx, "docker", "network", "create", network).Run(); err != nil {
		return err
	}
	contextRoot := filepath.Join(root, "alice")
	for _, name := range []string{"runtime", "hermes", "workspace", "connections", "archive"} {
		if err := os.MkdirAll(filepath.Join(contextRoot, name), 0777); err != nil {
			return err
		}
	}
	auth := "context-auth-0123456789abcdef0123456789abcdef"
	if err := os.WriteFile(filepath.Join(contextRoot, "runtime.auth"), []byte("HUB_RUNTIME_AUTH="+auth+"\n"), 0600); err != nil {
		return err
	}
	marker := filepath.Join(contextRoot, "workspace", "cold-restore-marker")
	if err := os.WriteFile(marker, []byte("keep"), 0600); err != nil {
		return err
	}
	control := "supervisor-control-0123456789abcdef"
	m, err := supervisor.New(supervisor.Config{SpacesRoot: root, Image: image, Network: network, RuntimeAuth: control, WarmTTL: time.Second, PortBase: 28000})
	if err != nil {
		return err
	}
	binding := supervisor.Binding{PrincipalID: "alice", ContextID: "alice", RuntimeID: "alice", RuntimeMode: "gateway", UserID: "alice", OrganizationID: "personal", PolicyVersion: "policy-1", ContextRoot: contextRoot}
	first, err := m.Ensure(ctx, binding)
	if err != nil {
		return err
	}
	if first.State != supervisor.Busy {
		return fmt.Errorf("supervisor runtime not busy: %s", first.State)
	}
	if err := m.ReleaseBinding(binding); err != nil {
		return err
	}
	if err := m.Reap(ctx, time.Now().Add(2*time.Second)); err != nil {
		return err
	}
	stopped, _, err := m.Status(binding)
	if err != nil || stopped.State != supervisor.Stopped {
		return fmt.Errorf("runtime did not stop: state=%s err=%v", stopped.State, err)
	}
	cold, err := m.Ensure(ctx, binding)
	if err != nil {
		return err
	}
	if cold.RuntimeID != first.RuntimeID || cold.Generation == first.Generation {
		return fmt.Errorf("cold start changed logical identity: first=%+v cold=%+v", first, cold)
	}
	if body, err := os.ReadFile(marker); err != nil || strings.TrimSpace(string(body)) != "keep" {
		return fmt.Errorf("context state was not preserved: %v", err)
	}
	_ = m.ReleaseBinding(binding)
	_ = m.Reap(ctx, time.Now().Add(2*time.Second))
	fmt.Println("Host supervisor Docker smoke passed: warm reuse, idle reap, cold restore, logical runtime ID")
	return nil
}
