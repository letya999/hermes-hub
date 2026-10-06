package toolhub

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"
)

// TestDaemonDockerHelper fakes `docker exec -i <container> hubctl
// tools-daemon`: it echoes each framed request back as a reply carrying the
// tool name and resolved container, so the test can prove demultiplexing,
// session reuse, death and respawn without a Docker engine.
func TestDaemonDockerHelper(t *testing.T) {
	if os.Getenv("FAKE_DAEMON") != "1" {
		return
	}
	state := os.Getenv("FAKE_DAEMON_STATE")
	idx := slices.Index(os.Args, "exec")
	container := os.Args[idx+2]
	log, _ := os.OpenFile(filepath.Join(state, "spawned"), os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o600)
	fmt.Fprintln(log, container)
	_ = log.Close()
	if _, err := os.Stat(filepath.Join(state, "die")); err == nil {
		os.Exit(3)
	}
	reader := bufio.NewReader(os.Stdin)
	enc := json.NewEncoder(os.Stdout)
	for {
		line, err := reader.ReadBytes('\n')
		if err != nil {
			os.Exit(0)
		}
		var frame AgentExecFrame
		if json.Unmarshal(line, &frame) != nil {
			os.Exit(4)
		}
		if frame.Cancel {
			// Record the cancel without replying: the caller already left.
			mark, _ := os.OpenFile(filepath.Join(state, "cancels"), os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o600)
			fmt.Fprintln(mark, frame.ID)
			_ = mark.Close()
			continue
		}
		if _, err := os.Stat(filepath.Join(state, "die-on-call")); err == nil {
			os.Exit(3)
		}
		go func(f AgentExecFrame) {
			if f.Tool == "slow" {
				time.Sleep(60 * time.Millisecond)
			}
			_ = enc.Encode(AgentExecReply{ID: f.ID, AgentExecResult: AgentExecResult{
				Result: map[string]any{"echo": f.Tool, "container": container},
			}})
		}(frame)
	}
}

func fakeDaemon(t *testing.T) ([]string, string) {
	t.Helper()
	state := t.TempDir()
	t.Setenv("FAKE_DAEMON", "1")
	t.Setenv("FAKE_DAEMON_STATE", state)
	return []string{os.Args[0], "-test.run=TestDaemonDockerHelper", "--"}, state
}

func spawnLog(t *testing.T, state string) []string {
	t.Helper()
	body, err := os.ReadFile(filepath.Join(state, "spawned"))
	if err != nil {
		return nil
	}
	return strings.Split(strings.TrimSpace(string(body)), "\n")
}

func fixedContainer(name string, err error) func(EffectiveBinding) (string, error) {
	return func(EffectiveBinding) (string, error) { return name, err }
}

func TestAgentExecPoolCallAndReuse(t *testing.T) {
	docker, state := fakeDaemon(t)
	pool := NewAgentExecPool(docker, fixedContainer("runtime-1", nil))
	defer pool.Close()
	effective := EffectiveBinding{Binding: ToolBinding{ToolBindingID: "bind-1"}}
	out, err := pool.Exec(context.Background(), effective, AgentExecRequest{Tool: "file_read"})
	if err != nil {
		t.Fatal(err)
	}
	echo, _ := out.Result.(map[string]any)
	if echo["echo"] != "file_read" || echo["container"] != "runtime-1" {
		t.Fatalf("bad reply: %+v", out.Result)
	}
	if _, err := pool.Exec(context.Background(), effective, AgentExecRequest{Tool: "file_list"}); err != nil {
		t.Fatal(err)
	}
	// Two calls share one daemon: the resolved container spawned once.
	if got := spawnLog(t, state); len(got) != 1 || got[0] != "runtime-1" {
		t.Fatalf("daemon respawned per call: %v", got)
	}
}

func TestAgentExecPoolDemultiplexesConcurrentCalls(t *testing.T) {
	docker, _ := fakeDaemon(t)
	pool := NewAgentExecPool(docker, fixedContainer("runtime-1", nil))
	defer pool.Close()
	effective := EffectiveBinding{Binding: ToolBinding{ToolBindingID: "bind-1"}}
	var wg sync.WaitGroup
	results := make([]AgentExecResult, 2)
	errs := make([]error, 2)
	for i, tool := range []string{"slow", "fast"} {
		wg.Add(1)
		go func(i int, tool string) {
			defer wg.Done()
			results[i], errs[i] = pool.Exec(context.Background(), effective, AgentExecRequest{Tool: tool})
		}(i, tool)
	}
	wg.Wait()
	for i := range errs {
		if errs[i] != nil {
			t.Fatal(errs[i])
		}
	}
	slow, _ := results[0].Result.(map[string]any)
	fast, _ := results[1].Result.(map[string]any)
	if slow["echo"] != "slow" || fast["echo"] != "fast" {
		t.Fatalf("replies crossed calls: %+v %+v", results[0].Result, results[1].Result)
	}
}

func TestAgentExecPoolRespawnsAfterDaemonDeath(t *testing.T) {
	docker, state := fakeDaemon(t)
	pool := NewAgentExecPool(docker, fixedContainer("runtime-1", nil))
	defer pool.Close()
	effective := EffectiveBinding{Binding: ToolBinding{ToolBindingID: "bind-1"}}
	if _, err := pool.Exec(context.Background(), effective, AgentExecRequest{Tool: "file_read"}); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(state, "die-on-call"), []byte("1"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(context.Background(), effective, AgentExecRequest{Tool: "file_read"}); err == nil {
		t.Fatal("call on a dead daemon reported success")
	}
	// The dead session is dropped; the next call spawns a fresh daemon.
	if err := os.Remove(filepath.Join(state, "die-on-call")); err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(5 * time.Second)
	var err error
	for {
		_, err = pool.Exec(context.Background(), effective, AgentExecRequest{Tool: "file_read"})
		if err == nil {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("daemon never respawned: %v", err)
		}
		time.Sleep(20 * time.Millisecond)
	}
	if got := spawnLog(t, state); len(got) != 2 {
		t.Fatalf("expected one respawn, got %v", got)
	}
}

func TestAgentExecPoolHonorsCallerDeadline(t *testing.T) {
	docker, _ := fakeDaemon(t)
	pool := NewAgentExecPool(docker, fixedContainer("runtime-1", nil))
	defer pool.Close()
	effective := EffectiveBinding{Binding: ToolBinding{ToolBindingID: "bind-1"}}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Millisecond)
	defer cancel()
	if _, err := pool.Exec(ctx, effective, AgentExecRequest{Tool: "slow"}); err == nil {
		t.Fatal("deadline-exceeded call returned success")
	}
}

// A cancelled call must propagate a cancel frame into the daemon: the
// in-flight execution stops instead of finishing unobserved.
func TestAgentExecPoolSendsCancelFrame(t *testing.T) {
	docker, state := fakeDaemon(t)
	pool := NewAgentExecPool(docker, fixedContainer("runtime-1", nil))
	defer pool.Close()
	effective := EffectiveBinding{Binding: ToolBinding{ToolBindingID: "bind-1"}}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() {
		_, err := pool.Exec(ctx, effective, AgentExecRequest{Tool: "slow"})
		done <- err
	}()
	time.Sleep(30 * time.Millisecond)
	cancel()
	if err := <-done; err == nil {
		t.Fatal("cancelled call returned success")
	}
	deadline := time.Now().Add(2 * time.Second)
	for {
		if body, err := os.ReadFile(filepath.Join(state, "cancels")); err == nil && len(body) > 0 {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("no cancel frame reached the daemon")
		}
		time.Sleep(20 * time.Millisecond)
	}
}

func TestAgentExecPoolCloseDropsSessions(t *testing.T) {
	docker, state := fakeDaemon(t)
	pool := NewAgentExecPool(docker, fixedContainer("runtime-1", nil))
	effective := EffectiveBinding{Binding: ToolBinding{ToolBindingID: "bind-1"}}
	if _, err := pool.Exec(context.Background(), effective, AgentExecRequest{Tool: "file_read"}); err != nil {
		t.Fatal(err)
	}
	pool.Close()
	if _, err := pool.Exec(context.Background(), effective, AgentExecRequest{Tool: "file_read"}); err != nil {
		t.Fatalf("pool did not respawn after Close: %v", err)
	}
	if got := spawnLog(t, state); len(got) != 2 {
		t.Fatalf("expected respawn after Close, got %v", got)
	}
}

func TestAgentExecPoolResolveAndSpawnFailures(t *testing.T) {
	docker, _ := fakeDaemon(t)
	pool := NewAgentExecPool(docker, fixedContainer("", ErrIsolation))
	defer pool.Close()
	effective := EffectiveBinding{Binding: ToolBinding{ToolBindingID: "bind-1"}}
	if _, err := pool.Exec(context.Background(), effective, AgentExecRequest{Tool: "file_read"}); !errors.Is(err, ErrIsolation) {
		t.Fatalf("unresolved container dispatched: %v", err)
	}
	bad := NewAgentExecPool([]string{"/nonexistent/docker-binary"}, fixedContainer("runtime-1", nil))
	defer bad.Close()
	if _, err := bad.Exec(context.Background(), effective, AgentExecRequest{Tool: "file_read"}); err == nil {
		t.Fatal("spawn failure reported success")
	}
}

func TestBoundedWriterCapsCapture(t *testing.T) {
	var buf bytes.Buffer
	w := &boundedWriter{w: &buf, max: 8}
	if _, err := w.Write([]byte("0123456789abcdef")); err != nil {
		t.Fatal(err)
	}
	if buf.String() != "01234567" {
		t.Fatalf("bounded writer kept %q", buf.String())
	}
	if _, err := w.Write([]byte("zz")); err != nil || buf.String() != "01234567" {
		t.Fatalf("full writer behaved wrong: %q %v", buf.String(), err)
	}
}
