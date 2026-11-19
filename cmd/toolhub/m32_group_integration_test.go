//go:build integration

package main

import (
	"context"
	"encoding/json"
	"fmt"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/letya999/hermes-hub/internal/devcheck"
	"github.com/letya999/hermes-hub/internal/runtime"
	"github.com/letya999/hermes-hub/internal/toolhub/api"
)

func m32CleanSource(dir string) (string, error) {
	status, err := exec.Command("git", "-C", dir, "status", "--porcelain", "--untracked-files=normal").Output()
	if err != nil {
		return "", err
	}
	if len(status) != 0 {
		return "", fmt.Errorf("M32 acceptance requires clean committed source")
	}
	source, err := exec.Command("git", "-C", dir, "rev-parse", "HEAD").Output()
	return strings.TrimSpace(string(source)), err
}

func m32ContainerInputs(t *testing.T) (string, string) {
	t.Helper()
	image := os.Getenv("HUB_M32_IMAGE")
	if image == "" {
		t.Skip("set HUB_M32_IMAGE to run the isolated container acceptance leg")
	}
	source, err := m32CleanSource(".")
	m32Require(t, err)
	for _, address := range []string{"0.0.0.0:8090", "0.0.0.0:8081"} {
		listener, err := net.Listen("tcp", address)
		if err != nil {
			t.Fatalf("M32 acceptance requires isolated host ports: %v", err)
		}
		m32Require(t, listener.Close())
	}
	for _, selected := range []string{image, os.Getenv("HUB_M32_SCRIPT_IMAGE")} {
		if selected == "" {
			t.Fatal("M32 acceptance requires the runtime and core images")
		}
		revision, err := exec.Command("docker", "image", "inspect", "--format", `{{index .Config.Labels "org.opencontainers.image.revision"}}`, selected).Output()
		m32Require(t, err)
		if source != strings.TrimSpace(string(revision)) {
			t.Fatal("M32 selected image does not match source HEAD")
		}
	}
	environment := os.Getenv("HUB_M32_ENVIRONMENT")
	if environment == "" {
		environment = "prod"
	}
	t.Logf("source=%s runtime_image=%s components=host-ToolHub,host-supervisor,host-communication,container-Hermes,container-relays", source, image)
	return image, environment
}

func TestM32SourceRequiresCleanCommit(t *testing.T) {
	dir := t.TempDir()
	for _, args := range [][]string{
		{"init", "--quiet"},
		{"-c", "user.name=M32 fixture", "-c", "user.email=m32@localhost", "commit", "--quiet", "--allow-empty", "-m", "M32 fixture"},
	} {
		m32Require(t, exec.Command("git", append([]string{"-C", dir}, args...)...).Run())
	}
	if source, err := m32CleanSource(dir); err != nil || source == "" {
		t.Fatalf("clean source rejected: %v", err)
	}
	path := filepath.Join(dir, "source.txt")
	m32Require(t, os.WriteFile(path, []byte("untracked\n"), 0600))
	if _, err := m32CleanSource(dir); err == nil {
		t.Fatal("untracked source accepted as exact HEAD")
	}
	m32Require(t, exec.Command("git", "-C", dir, "add", "source.txt").Run())
	if _, err := m32CleanSource(dir); err == nil {
		t.Fatal("staged source accepted as exact HEAD")
	}
	m32Require(t, exec.Command("git", "-C", dir, "-c", "user.name=M32 fixture", "-c", "user.email=m32@localhost", "commit", "--quiet", "-m", "M32 source").Run())
	m32Require(t, os.WriteFile(path, []byte("modified\n"), 0600))
	if _, err := m32CleanSource(dir); err == nil {
		t.Fatal("modified source accepted as exact HEAD")
	}
}

func TestM32RuntimeStatusUsesEnrolledBinding(t *testing.T) {
	f, err := devcheck.NewM32GroupFixture(t.TempDir(), "prod", "http://127.0.0.1:18888/v1", "status")
	m32Require(t, err)
	h := newM32Lifecycle(t, t.Context(), "", "prod", f)
	for _, user := range f.Users {
		_, running, err := h.runtimeStatus(user)
		m32Require(t, err)
		if running {
			t.Fatal("status lookup started a runtime")
		}
	}
}

func TestM32RealGroupMemoryIsolation(t *testing.T) {
	image, environment := m32ContainerInputs(t)
	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Minute)
	defer cancel()
	suffix := strconv.FormatInt(time.Now().UnixNano(), 36)
	users := []string{"m32a" + suffix, "m32b" + suffix}
	var prompts [2]atomic.Bool
	provider, release, entered := m32HeldProvider(t, func(messages json.RawMessage) {
		body := string(messages)
		if strings.Contains(body, "M32 private group-boundary "+users[0]) && strings.Contains(body, "M32-request-instruction-marker-"+users[0]) {
			prompts[0].Store(true)
			if !strings.Contains(body, "M32-private-marker-"+users[0]) || strings.Contains(body, "M32-private-marker-"+users[1]) {
				t.Errorf("private model prompt lost owner isolation: own=%t foreign=%t unavailable=%t curated=%t", strings.Contains(body, "M32-private-marker-"+users[0]), strings.Contains(body, "M32-private-marker-"+users[1]), strings.Contains(body, "Memory context unavailable"), strings.Contains(body, "Owner-curated memory"))
			}
		}
		if strings.Contains(body, "M32 group boundary "+users[1]) && strings.Contains(body, "M32-request-instruction-marker-"+users[1]) {
			prompts[1].Store(true)
			if strings.Contains(body, "M32-private-marker-") {
				t.Error("group model prompt received private memory")
			}
		}
	})
	defer release()
	port := provider.Listener.Addr().(*net.TCPAddr).Port
	f, err := devcheck.NewM32GroupFixture(t.TempDir(), environment, fmt.Sprintf("http://host.docker.internal:%d/v1", port), suffix)
	m32Require(t, err)
	h := newM32Lifecycle(t, ctx, image, environment, f)
	stop := h.startToolHub(t, ctx)
	t.Cleanup(stop)
	results, failures := make(chan runtime.ExecuteResponse, 2), make(chan error, 2)
	for i, user := range f.Users {
		scope, text := "user:"+user, "M32 private group-boundary "+user
		if i == 1 {
			scope, text = "group:"+user, "M32 group boundary "+user
		}
		id := "m32-group-boundary-" + strconv.Itoa(i)
		request := runtime.ExecuteRequest{Envelope: f.Auth[user], JobID: id, IdempotencyKey: id, OrganizationID: "personal", UserID: user, ActorID: user, ScopeID: scope, Channel: "telegram_bot", Trigger: "message", Text: text}
		request.Instructions = "M32-request-instruction-marker-" + user
		go func() {
			response, err := h.execute(ctx, request)
			results <- response
			failures <- err
		}()
	}
	for !prompts[0].Load() || !prompts[1].Load() {
		select {
		case <-entered:
		case err := <-failures:
			t.Fatalf("private/group overlap failed: %v", err)
		case <-ctx.Done():
			t.Fatal("private and group model requests did not overlap")
		}
	}
	var runs [2]api.RunContext
	for i := range f.Users {
		if !prompts[i].Load() {
			t.Fatal("actual private/group model prompt was not observed")
		}
		runs[i] = h.waitRun(t, ctx, "m32-group-boundary-"+strconv.Itoa(i))
		want := api.RunKindPrivate
		if i == 1 {
			want = api.RunKindGroup
		}
		if runs[i].Status != "running" || api.Kind(runs[i]) != want || runs[i].RuntimeGeneration != runs[i].CurrentGeneration {
			t.Fatal("supervisor did not persist the exact private/group run kind")
		}
	}
	owner, group := f.Users[0], f.Users[1]
	if own := h.call(t, ctx, owner, runs[0], "memory_search", map[string]any{"query": "M32-private-marker-" + owner}, true); !strings.Contains(own, "M32-private-marker-"+owner) {
		t.Fatal("concurrent private owner could not recall its memory")
	}
	for _, claim := range []api.RunContext{runs[1], runs[0], {}} {
		h.call(t, ctx, group, claim, "memory_search", map[string]any{"query": "M32"}, false)
		h.call(t, ctx, group, claim, "memory_card", map[string]any{"op": "remember", "name": "M32 group denied", "text": "must not land"}, false)
	}
	h.call(t, ctx, owner, runs[1], "memory_search", map[string]any{"query": "M32"}, false)
	for _, user := range f.Users {
		h.assertRuntimeIsolation(t, user)
		if _, err := os.Stat(filepath.Join(f.Root, "vaults", user, "memory", "cards", "notes", "inbox", "m32-group-denied.md")); !os.IsNotExist(err) {
			t.Fatal("group run mutated a private vault")
		}
	}
	release()
	for range f.Users {
		m32Require(t, <-failures)
		if response := <-results; response.Status != "completed" || response.Text != "M32 acceptance complete" {
			t.Fatal("actual private/group Hermes run did not complete")
		}
	}
	for i, user := range f.Users {
		h.call(t, ctx, user, runs[i], "memory_search", map[string]any{"query": "M32"}, false)
	}
}
