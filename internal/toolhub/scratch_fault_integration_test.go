//go:build integration

package toolhub

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// Real-Docker fault injection for the unconfirmed-stop path: a paused
// executor cannot be removed, so `docker rm -f` fails and inspect still
// shows it. The binding must quarantine (durably) and deny replacement
// leases until the container is confirmed gone. Uses a created-but-never-
// started container purely as the runtime image donor; relPath stays empty
// so no exec-pack channel is needed. Run with:
//
//	HUB_SCRATCH_FAULT_IMAGE=hermes-hub:test go test -tags integration -run TestDockerScratchExecPausedQuarantine ./internal/toolhub
func TestDockerScratchExecPausedQuarantine(t *testing.T) {
	image := os.Getenv("HUB_SCRATCH_FAULT_IMAGE")
	if image == "" {
		image = "hermes-hub:test"
	}
	if _, err := exec.LookPath("docker"); err != nil {
		t.Skip("docker not available")
	}
	if out, err := exec.Command("docker", "info", "--format", "{{.OSType}}").CombinedOutput(); err != nil || strings.TrimSpace(string(out)) != "linux" {
		t.Skipf("local Linux Docker required: %s %v", out, err)
	}
	if out, err := exec.Command("docker", "image", "inspect", image).CombinedOutput(); err != nil {
		t.Skipf("image %s unavailable: %s", image, out)
	}

	docker := []string{"docker"}
	run := func(args ...string) (string, error) {
		out, err := exec.Command("docker", args...).CombinedOutput()
		return strings.TrimSpace(string(out)), err
	}
	suffix := fmt.Sprintf("%d", time.Now().UnixNano()%1_000_000)
	runtimeName := "hermes-scratch-fault-runtime-" + suffix
	if out, err := run("create", "--name", runtimeName, image); err != nil {
		t.Fatalf("runtime image donor: %s %v", out, err)
	}
	t.Cleanup(func() { _, _ = run("rm", "-f", runtimeName) })

	// Pause any hermes-scratch- container this test creates (recorded by
	// name, never one that existed before we started).
	before := map[string]bool{}
	if out, err := run("ps", "--filter", "name="+scratchMaxName, "--format", "{{.Names}}"); err == nil {
		for _, n := range strings.Fields(out) {
			before[n] = true
		}
	}
	paused := make(chan string, 1)
	go func() {
		for {
			out, err := run("ps", "--filter", "name="+scratchMaxName, "--format", "{{.Names}}")
			if err == nil {
				for _, n := range strings.Fields(out) {
					if !before[n] {
						if _, err := run("pause", n); err == nil {
							paused <- n
						}
						return
					}
				}
			}
			time.Sleep(200 * time.Millisecond)
		}
	}()

	leaseFile := filepath.Join(t.TempDir(), "executor-leases.json")
	if err := SetScratchLeasePath(leaseFile); err != nil {
		t.Fatal(err)
	}
	defer func() {
		scratchLeaseMu.Lock()
		scratchLeasePath = ""
		scratchLeaseMu.Unlock()
	}()

	applied := []string{}
	apply := func(_ context.Context, _ EffectiveBinding, request AgentExecRequest) (AgentExecResult, error) {
		applied = append(applied, fmt.Sprint(request.Arguments["path"]))
		return AgentExecResult{Result: map[string]any{"ok": true}}, nil
	}
	execFn := DockerScratchExec(docker, func(EffectiveBinding) (string, error) { return runtimeName, nil }, apply)
	effective := EffectiveBinding{Binding: ToolBinding{ToolBindingID: "bind-fault-" + suffix}}
	ok := func(context.Context, EffectiveBinding) error { return nil }
	req := func(command string, timeout int) AgentExecRequest {
		return AgentExecRequest{Arguments: map[string]any{"command": command}, TimeoutSeconds: timeout}
	}

	// The sandbox pauses mid-run; the lease expires while docker run is
	// still attached, rm -f refuses a paused container, inspect confirms it
	// alive -> unconfirmed stop -> ErrIsolation, never success.
	if _, err := execFn(context.Background(), effective, req("sleep 90", 15), ok); !errors.Is(err, ErrIsolation) {
		t.Fatalf("unconfirmed stop must report isolation, got %v", err)
	}
	var executor string
	select {
	case executor = <-paused:
	case <-time.After(5 * time.Second):
	}
	t.Cleanup(func() {
		if executor != "" {
			_, _ = run("unpause", executor)
			_, _ = run("rm", "-f", executor)
		}
	})
	if executor == "" {
		t.Fatal("no scratch executor was observed to pause")
	}

	// The lease is durable on disk and keeps denying while inspect still
	// finds the executor.
	body, err := os.ReadFile(leaseFile)
	if err != nil || !strings.Contains(string(body), effective.Binding.ToolBindingID) {
		t.Fatalf("quarantine was not persisted: %s %v", body, err)
	}
	if _, err := execFn(context.Background(), effective, req("echo denied", 5), ok); !errors.Is(err, ErrIsolation) {
		t.Fatalf("quarantined binding issued a replacement lease: %v", err)
	}

	// Confirm the executor gone; the next call clears the quarantine and a
	// normal run completes with a real export through the apply channel.
	if _, err := run("unpause", executor); err != nil {
		t.Fatalf("unpause: %v", err)
	}
	if _, err := run("rm", "-f", executor); err != nil {
		t.Fatalf("rm executor: %v", err)
	}
	executor = ""
	out, err := execFn(context.Background(), effective, req(`echo ok > "$OUTPUTS/proof.txt"; echo done`, 60), ok)
	if err != nil {
		t.Fatalf("cleared quarantine did not restore execution: %v", err)
	}
	summary, _ := out.Result.(map[string]any)
	if summary["stopped"] != true || len(applied) != 1 || !strings.HasSuffix(applied[0], "exports/proof.txt") {
		t.Fatalf("post-quarantine run lost stop state or export: %+v applied=%v", summary, applied)
	}
	if body, err := os.ReadFile(leaseFile); err != nil || strings.Contains(string(body), effective.Binding.ToolBindingID) {
		t.Fatalf("cleared quarantine still persisted: %s %v", body, err)
	}
}
