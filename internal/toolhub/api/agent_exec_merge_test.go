package api

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"os"

	"testing"
	"time"
)

// fakeToolsDaemon echoes each request frame back as a reply so the pool's
// register/write/readReplies path runs against a real child process.
func TestFakeToolsDaemonChild(t *testing.T) {
	helper := false
	for _, a := range os.Args {
		if a == "tools-daemon" {
			helper = true
		}
	}
	if !helper {
		t.Skip("helper process")
	}
	reader := bufio.NewReader(os.Stdin)
	for {
		line, err := reader.ReadBytes('\n')
		if err != nil {
			return
		}
		var frame AgentExecFrame
		if json.Unmarshal(line, &frame) != nil || frame.Cancel {
			continue
		}
		reply := AgentExecReply{ID: frame.ID}
		reply.Result = map[string]any{"echo": frame.Tool}
		_ = json.NewEncoder(os.Stdout).Encode(reply)
	}
}

func TestAgentExecPoolContract(t *testing.T) {
	t.Parallel()
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	// resolve failure and spawn failure fail closed.
	pool := NewAgentExecPool(nil, func(EffectiveBinding) (string, error) {
		return "", errors.New("no container")
	})
	if _, err := pool.Exec(ctx, EffectiveBinding{}, AgentExecRequest{}); err == nil || err.Error() != "no container" {
		t.Fatalf("resolve error must propagate: %v", err)
	}
	bad := NewAgentExecPool([]string{"definitely-no-docker-bin"}, func(EffectiveBinding) (string, error) {
		return "c1", nil
	})
	if _, err := bad.Exec(ctx, EffectiveBinding{}, AgentExecRequest{Tool: "x"}); err == nil {
		t.Fatal("spawn failure must error")
	}

	// A live fake daemon answers with the echoed tool name; Close kills it.
	live := NewAgentExecPool([]string{os.Args[0], "-test.run=TestFakeToolsDaemonChild"}, func(EffectiveBinding) (string, error) {
		return "c1", nil
	})
	defer live.Close()
	res, err := live.Exec(ctx, EffectiveBinding{}, AgentExecRequest{Tool: "files_list"})
	if err != nil {
		t.Fatalf("fake daemon call: %v", err)
	}
	m, _ := res.Result.(map[string]any)
	if m["echo"] != "files_list" {
		t.Fatalf("reply: %+v", res)
	}

	buf := &bytes.Buffer{}
	w := &BoundedWriter{W: buf, Max: 4}
	if _, err := w.Write([]byte("0123456789")); err != nil || buf.Len() != 4 || buf.String() != "0123" {
		t.Fatalf("bounded writer: %v %q", err, buf)
	}
}

// TestFakeToolsExecChild answers the one-shot `tools-exec` invocation with a
// JSON AgentExecResult on stdout; argv carries "tools-exec".
func TestFakeToolsExecChild(t *testing.T) {
	helper := false
	for _, a := range os.Args {
		if a == "tools-exec" {
			helper = true
		}
	}
	if !helper {
		t.Skip("helper process")
	}
	var request AgentExecRequest
	if err := json.NewDecoder(os.Stdin).Decode(&request); err != nil {
		return
	}
	_ = json.NewEncoder(os.Stdout).Encode(AgentExecResult{Result: map[string]any{"echo": request.Tool}})
}

// DockerAgentExec marshals the request into `docker exec hubctl tools-exec`
// and decodes the bounded reply; container resolution and spawn failures fail
// closed.
func TestDockerAgentExecContract(t *testing.T) {
	t.Parallel()
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	binding := EffectiveBinding{Binding: ToolBinding{PrincipalID: "alice", ContextID: "ctx-1"}}

	execFn := DockerAgentExec([]string{os.Args[0], "-test.run=TestFakeToolsExecChild"}, FixedAgentContainer("c1"))
	res, err := execFn(ctx, binding, AgentExecRequest{Tool: "files_list"})
	if err != nil {
		t.Fatalf("tools-exec: %v", err)
	}
	m, _ := res.Result.(map[string]any)
	if m["echo"] != "files_list" {
		t.Fatalf("result: %+v", res)
	}

	if _, err := DockerAgentExec(nil, FixedAgentContainer(""))(ctx, binding, AgentExecRequest{}); err == nil {
		t.Fatal("unconfigured container must fail")
	}
	if _, err := DockerAgentExec([]string{"definitely-no-docker-bin"}, FixedAgentContainer("c1"))(ctx, binding, AgentExecRequest{}); err == nil {
		t.Fatal("spawn failure must error")
	}
	if _, err := ManagedAgentContainer(EffectiveBinding{}); !errors.Is(err, ErrIsolation) {
		t.Fatalf("incomplete binding: %v", err)
	}
	name, err := ManagedAgentContainer(binding)
	if err != nil || name == "" || name == "c1" {
		t.Fatalf("managed container: %q %v", name, err)
	}
	again, _ := ManagedAgentContainer(binding)
	if name != again {
		t.Fatal("container name must be deterministic")
	}

	// DaemonAgentExec is the pooled variant of the same channel; a resolver
	// error surfaces without spawning.
	daemon := DaemonAgentExec(nil, func(EffectiveBinding) (string, error) {
		return "", errors.New("none")
	})
	if _, err := daemon(ctx, binding, AgentExecRequest{}); err == nil || err.Error() != "none" {
		t.Fatalf("daemon resolve: %v", err)
	}
}
