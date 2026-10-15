package api

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"os"
	"testing"
	"time"
)

// TestFakeDockerChild emulates the docker CLI surface DockerScratchExec
// needs: image inspect, exec-pack, the scratch run, rm, and a final inspect
// that reports the executor gone.
func TestFakeDockerChild(t *testing.T) {
	kind := ""
	for _, a := range os.Args {
		switch a {
		case "exec-pack", "exec-scratch", "inspect", "rm":
			if kind == "" {
				kind = a
			}
		}
	}
	if kind == "" {
		t.Skip("helper process")
	}
	switch kind {
	case "inspect":
		// confirmStopped's plain `inspect <name>` must fail: the executor is
		// gone. `inspect -f <fmt> <runtime>` answers the image question.
		withFormat := false
		for _, a := range os.Args {
			if a == "-f" {
				withFormat = true
			}
		}
		if withFormat {
			os.Stdout.WriteString("img:1\n")
			os.Exit(0)
		}
		os.Exit(1)
	case "exec-pack":
		_, _ = io.Copy(io.Discard, os.Stdin)
		os.Stdout.WriteString("tarbytes")
		os.Exit(0)
	case "exec-scratch":
		_, _ = io.Copy(io.Discard, os.Stdin)
		_ = json.NewEncoder(os.Stdout).Encode(ScratchExecResult{ExitCode: 0, Stdout: "done", TreeStopped: true})
		os.Exit(0)
	case "rm":
		os.Exit(0)
	}
	os.Exit(0)
}

// DockerScratchExec drives inspect → exec-pack → isolated run → confirmed
// removal, all over the fixed docker verbs; guards fail closed before docker.
func TestDockerScratchExecContract(t *testing.T) {
	t.Parallel()
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	effective := EffectiveBinding{Binding: ToolBinding{ToolBindingID: "bind-scratch-1"}}
	scratch := DockerScratchExec([]string{os.Args[0], "-test.run=TestFakeDockerChild"}, FixedAgentContainer("rt-1"), nil)

	guards := []AgentExecRequest{
		{Arguments: map[string]any{}},
		{Arguments: map[string]any{"command": " "}},
		{Arguments: map[string]any{"command": "ls", "path": "../evil"}},
		{Arguments: map[string]any{"command": "ls", "path": "ok.txt"}, Scopes: []CapabilityScope{{PathArgument: "path", PathPrefix: "other"}}},
	}
	for i, req := range guards {
		if _, err := scratch(ctx, effective, req, nil); err == nil {
			t.Fatalf("guard %d accepted", i)
		}
	}
	badRuntime := DockerScratchExec(nil, func(EffectiveBinding) (string, error) { return "", errors.New("no runtime") }, nil)
	if _, err := badRuntime(ctx, effective, AgentExecRequest{Arguments: map[string]any{"command": "ls"}}, nil); err == nil {
		t.Fatal("runtime resolution failure must propagate")
	}

	res, err := scratch(ctx, effective, AgentExecRequest{
		Tool:           "code_exec",
		Arguments:      map[string]any{"command": "ls /scratch", "path": "ws"},
		Scopes:         []CapabilityScope{{PathArgument: "path", PathPrefix: "ws"}},
		TimeoutSeconds: 30,
	}, nil)
	if err != nil {
		t.Fatalf("scratch exec: %v", err)
	}
	summary, _ := res.Result.(map[string]any)
	if summary["exit_code"] != 0 || summary["stdout"] != "done" || summary["stopped"] != true || summary["tree_stopped"] != true {
		t.Fatalf("summary: %+v", summary)
	}
}
