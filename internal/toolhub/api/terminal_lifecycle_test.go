package api

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"strings"
	"testing"
	"time"
)

// TestFakeDockerHelper is the re-executed child process that stands in for the
// docker CLI. It only activates under HUB_FAKE_DOCKER=1 and must os.Exit before
// the test framework prints anything, so the parent sees only the protocol
// bytes a real docker would emit.
func TestFakeDockerHelper(t *testing.T) {
	if os.Getenv("HUB_FAKE_DOCKER") != "1" {
		return
	}
	os.Exit(fakeDockerMain(os.Args[1:], os.Stdin, os.Stdout, os.Stderr))
}

func fakeDockerMain(args []string, stdin io.Reader, stdout, stderr io.Writer) int {
	var docker []string
	for _, a := range args {
		if strings.HasPrefix(a, "-test.") {
			continue
		}
		docker = append(docker, a)
	}
	if len(docker) == 0 {
		return 2
	}
	switch docker[0] {
	case "inspect":
		for i, a := range docker {
			if a == "-f" && i+1 < len(docker) && strings.Contains(docker[i+1], "Config.Image") {
				fmt.Fprintln(stdout, "runtime-image:1")
				return 0
			}
		}
		fmt.Fprintf(stderr, "Error: No such object: %s\n", docker[len(docker)-1])
		return 1
	case "exec":
		_, _ = io.Copy(io.Discard, stdin)
		_, _ = stdout.Write([]byte("faketar"))
		return 0
	case "rm":
		return 0
	case "run":
		return fakeDockerRun(stdin, stdout)
	}
	return 2
}

// fakeDockerRun speaks the hubctl exec-terminal frame protocol: a started
// frame, then behavior keyed on the requested command. "quick" finishes
// immediately; "interactive" echoes send frames until stdin closes; anything
// else emits a non-JSON line to prove the parser fails closed.
func fakeDockerRun(stdin io.Reader, stdout io.Writer) int {
	br := bufio.NewReader(stdin)
	line, err := br.ReadBytes('\n')
	if err != nil {
		return 1
	}
	var start terminalStart
	if json.Unmarshal(line, &start) != nil {
		return 1
	}
	fmt.Fprintln(stdout, `{"action":"started"}`)
	switch start.Command {
	case "quick":
		fmt.Fprintln(stdout, `{"action":"stdout","data":"hello"}`)
		fmt.Fprintln(stdout, `{"action":"stderr","data":"warn"}`)
		fmt.Fprintln(stdout, `{"action":"done","exit_code":0}`)
		return 0
	case "garbage":
		fmt.Fprintln(stdout, "this is not a frame")
		fmt.Fprintln(stdout, `{"action":"done","exit_code":1}`)
		return 0
	}
	// Interactive: answer send frames, block on stdin so kill can cancel us.
	for {
		l, rerr := br.ReadBytes('\n')
		if len(l) > 0 {
			var f terminalFrame
			if json.Unmarshal(l, &f) == nil && f.Action == "send" {
				out, _ := json.Marshal(terminalFrame{Action: "stdout", Data: "echo:" + f.Data})
				fmt.Fprintln(stdout, string(out))
			}
		}
		if rerr != nil {
			break
		}
	}
	fmt.Fprintln(stdout, `{"action":"done","exit_code":0}`)
	return 0
}

// terminalFixture wires an executor whose docker argv re-execs this test binary.
func terminalFixture(t *testing.T) *terminalExecutor {
	t.Helper()
	t.Setenv("HUB_FAKE_DOCKER", "1")
	return &terminalExecutor{
		docker:  []string{os.Args[0], "-test.run=TestFakeDockerHelper"},
		resolve: func(EffectiveBinding) (string, error) { return "hermes-context-aa", nil },
		jobs:    map[string]*terminalJob{},
	}
}

func terminalOwner() EffectiveBinding {
	owner := EffectiveBinding{}
	owner.Binding.PrincipalID = "alice"
	owner.Binding.ContextID = "private"
	owner.Binding.RuntimeID = "runtime-a"
	owner.Binding.ToolBindingID = "binding-a"
	return owner
}

func terminalFence() ScratchFenceFunc {
	return func(context.Context, EffectiveBinding) error { return nil }
}

func TestTerminalExecCompletesOverFakeDocker(t *testing.T) {
	e := terminalFixture(t)
	result, err := e.call(context.Background(), terminalOwner(),
		AgentExecRequest{Tool: "terminal_exec", Arguments: map[string]any{"cmd": "quick"}, TimeoutSeconds: 60}, terminalFence())
	if err != nil {
		t.Fatal(err)
	}
	snap, _ := result.Result.(map[string]any)
	if snap["state"] != "done" || snap["exit_code"] != 0 || snap["stdout"] != "hello" || snap["stderr"] != "warn" {
		t.Fatalf("unexpected snapshot: %v", snap)
	}
	if snap["tree_stopped"] != true {
		t.Fatal("confirmed removal must mark the tree stopped")
	}
}

func TestTerminalExecRejectsMalformedStart(t *testing.T) {
	e := terminalFixture(t)
	owner := terminalOwner()
	for _, args := range []map[string]any{
		{"cmd": "   "},
		{"cmd": strings.Repeat("x", 8193)},
		{"cmd": "ok\x00bad"},
		{"cmd": "ls", "shell": "zsh"},
		{"cmd": "ls", "timeout": float64(0)},
		{"cmd": "ls", "timeout": float64(1.5)},
		{"cmd": "ls", "timeout": float64(999)},
		{"cmd": "ls", "stdin": strings.Repeat("s", 64<<10+1)},
	} {
		if _, err := e.call(context.Background(), owner, AgentExecRequest{Tool: "terminal_exec", Arguments: args}, terminalFence()); !errors.Is(err, ErrInvalid) {
			t.Fatalf("args %v: want ErrInvalid, got %v", args, err)
		}
	}
}

func TestTerminalExecPacksScopedPath(t *testing.T) {
	e := terminalFixture(t)
	owner := terminalOwner()
	result, err := e.call(context.Background(), owner, AgentExecRequest{
		Tool:           "terminal_exec",
		Arguments:      map[string]any{"cmd": "quick", "path": "allowed/dir"},
		TimeoutSeconds: 60,
		Scopes:         []CapabilityScope{{Resource: "files", PathArgument: "path", PathPrefix: "allowed"}},
	}, terminalFence())
	if err != nil {
		t.Fatal(err)
	}
	if snap, _ := result.Result.(map[string]any); snap["state"] != "done" {
		t.Fatalf("scoped exec did not finish: %v", snap)
	}
}

func TestTerminalJobSendPollKill(t *testing.T) {
	e := terminalFixture(t)
	owner := terminalOwner()
	ctx := context.Background()
	start, err := e.call(ctx, owner,
		AgentExecRequest{Tool: "terminal_exec", Arguments: map[string]any{"cmd": "interactive", "background": true}, TimeoutSeconds: 60}, terminalFence())
	if err != nil {
		t.Fatal(err)
	}
	jobID, _ := start.Result.(map[string]any)["job_id"].(string)
	if jobID == "" {
		t.Fatalf("no job id: %v", start.Result)
	}
	control := func(args map[string]any) map[string]any {
		res, cerr := e.call(ctx, owner, AgentExecRequest{Tool: "terminal_job", Arguments: args}, terminalFence())
		if cerr != nil {
			t.Fatalf("control %v: %v", args, cerr)
		}
		return res.Result.(map[string]any)
	}
	jobs, _ := control(map[string]any{"action": "list"})["jobs"].([]map[string]any)
	found := false
	for _, j := range jobs {
		if j["job_id"] == jobID && j["state"] == "running" {
			found = true
		}
	}
	if !found {
		t.Fatalf("job missing from list: %v", jobs)
	}
	control(map[string]any{"action": "send", "job": jobID, "data": "ping"})
	deadline := time.Now().Add(5 * time.Second)
	var snap map[string]any
	for time.Now().Before(deadline) {
		snap = control(map[string]any{"action": "poll", "job": jobID})
		if s, _ := snap["stdout"].(string); strings.Contains(s, "echo:ping") {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	if s, _ := snap["stdout"].(string); !strings.Contains(s, "echo:ping") {
		t.Fatalf("send frame never echoed: %v", snap)
	}
	killed := control(map[string]any{"action": "kill", "job": jobID})
	if killed["state"] != "done" || killed["exit_code"] != -1 {
		t.Fatalf("kill must resolve as a canceled tree: %v", killed)
	}
}

func TestTerminalControlGuards(t *testing.T) {
	e := terminalFixture(t)
	owner := terminalOwner()
	if _, err := e.call(context.Background(), owner,
		AgentExecRequest{Tool: "terminal_job", Arguments: map[string]any{"action": "explode"}}, terminalFence()); !errors.Is(err, ErrInvalid) {
		t.Fatalf("unknown action: %v", err)
	}
	if _, err := e.call(context.Background(), owner,
		AgentExecRequest{Tool: "terminal_job", Arguments: map[string]any{"action": "poll", "job": "terminal-missing"}}, terminalFence()); !errors.Is(err, ErrNotFound) {
		t.Fatalf("unknown job: %v", err)
	}
	job := &terminalJob{id: "terminal-held", owner: owner.Binding.PrincipalID, contextID: owner.Binding.ContextID,
		runtimeID: owner.Binding.RuntimeID, bindingID: owner.Binding.ToolBindingID, effective: owner,
		state: "running", done: make(chan struct{}), cancel: func() {}}
	e.jobs[job.id] = job
	if _, err := e.call(context.Background(), owner,
		AgentExecRequest{Tool: "terminal_job", Arguments: map[string]any{"action": "send", "job": job.id, "data": ""}}, terminalFence()); !errors.Is(err, ErrInvalid) {
		t.Fatalf("empty send: %v", err)
	}
	if _, err := e.call(context.Background(), owner,
		AgentExecRequest{Tool: "terminal_job", Arguments: map[string]any{"action": "send", "job": job.id, "data": strings.Repeat("s", 32<<10+1)}}, terminalFence()); !errors.Is(err, ErrInvalid) {
		t.Fatalf("oversize send: %v", err)
	}
	job.state = "done"
	if _, err := e.call(context.Background(), owner,
		AgentExecRequest{Tool: "terminal_job", Arguments: map[string]any{"action": "send", "job": job.id, "data": "x"}}, terminalFence()); !errors.Is(err, ErrInvalid) {
		t.Fatalf("send to finished job: %v", err)
	}
}

// A malformed frame poisons the job with ErrIsolation, which cancels the
// workload. Whether the failure lands before or after start reads job.err is a
// scheduling race, so the contract is either a direct ErrIsolation or a
// snapshot of a torn-down tree — never a successful completion with
// attacker-controlled output.
func TestTerminalExecFailsClosedOnBadFrames(t *testing.T) {
	e := terminalFixture(t)
	result, err := e.call(context.Background(), terminalOwner(),
		AgentExecRequest{Tool: "terminal_exec", Arguments: map[string]any{"cmd": "garbage"}, TimeoutSeconds: 60}, terminalFence())
	if err != nil {
		if !errors.Is(err, ErrIsolation) {
			t.Fatalf("poisoned job may only fail with ErrIsolation: %v", err)
		}
		return
	}
	snap, _ := result.Result.(map[string]any)
	if snap["state"] != "done" || snap["exit_code"] != -1 || snap["tree_stopped"] != true {
		t.Fatalf("poisoned job must resolve as a torn-down tree: %v", snap)
	}
	if snap["stdout"] != "" {
		t.Fatalf("poisoned output must not reach the result: %v", snap)
	}
}

func TestTerminalJobLimitIsBounded(t *testing.T) {
	e := terminalFixture(t)
	owner := terminalOwner()
	for i := 0; i < 8; i++ {
		e.jobs[fmt.Sprintf("terminal-open-%d", i)] = &terminalJob{owner: owner.Binding.PrincipalID, done: make(chan struct{})}
	}
	e.jobs["terminal-evictable"] = &terminalJob{owner: "someone-else", done: closedChan()}
	_, err := e.call(context.Background(), owner,
		AgentExecRequest{Tool: "terminal_exec", Arguments: map[string]any{"cmd": "quick"}, TimeoutSeconds: 60}, terminalFence())
	if !errors.Is(err, ErrIsolation) {
		t.Fatalf("over-limit start: %v", err)
	}
}

func closedChan() chan struct{} {
	ch := make(chan struct{})
	close(ch)
	return ch
}
