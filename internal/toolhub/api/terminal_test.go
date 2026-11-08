package api

import (
	"context"
	"errors"
	"testing"
)

func TestTerminalRejectsPathsAndForeignJobsBeforeDocker(t *testing.T) {
	e := &terminalExecutor{resolve: func(EffectiveBinding) (string, error) { t.Fatal("unexpected runtime lookup"); return "", nil }, jobs: map[string]*terminalJob{}}
	fence := func(context.Context, EffectiveBinding) error { return nil }
	owner := EffectiveBinding{}
	owner.Binding.PrincipalID, owner.Binding.ContextID, owner.Binding.RuntimeID, owner.Binding.ToolBindingID = "alice", "private", "runtime-a", "binding-a"
	for _, args := range []map[string]any{
		{"cmd": "pwd", "cwd": "../outside"},
		{"cmd": "pwd", "cwd": "/root"},
		{"cmd": "pwd", "cwd": "allowed/../outside"},
		{"cmd": "pwd", "cwd": "elsewhere", "path": "allowed"},
	} {
		_, err := e.call(context.Background(), owner, AgentExecRequest{Tool: "terminal_exec", Arguments: args,
			Scopes: []CapabilityScope{{Resource: "files", PathArgument: "path", PathPrefix: "allowed"}}}, fence)
		if !errors.Is(err, ErrInvalid) {
			t.Fatalf("path %v: %v", args, err)
		}
	}
	job := &terminalJob{id: "terminal-known", owner: "alice", contextID: "private", runtimeID: "runtime-a", bindingID: "binding-a", effective: owner, state: "done"}
	e.jobs[job.id] = job
	for _, changed := range []func(*EffectiveBinding){
		func(e *EffectiveBinding) { e.Binding.PrincipalID = "bob" },
		func(e *EffectiveBinding) { e.Binding.ContextID = "group" },
		func(e *EffectiveBinding) { e.Binding.RuntimeID = "runtime-b" },
		func(e *EffectiveBinding) { e.Binding.ToolBindingID = "binding-b" },
	} {
		foreign := owner
		changed(&foreign)
		if _, err := e.call(context.Background(), foreign, AgentExecRequest{Tool: "terminal_job", Arguments: map[string]any{"action": "poll", "job": job.id}}, fence); !errors.Is(err, ErrNotFound) {
			t.Fatalf("foreign job admitted: %v", err)
		}
	}
	if _, err := e.call(context.Background(), owner, AgentExecRequest{Tool: "terminal_job", Arguments: map[string]any{"action": "poll", "job": job.id}},
		func(context.Context, EffectiveBinding) error { return ErrIsolation }); !errors.Is(err, ErrIsolation) {
		t.Fatalf("revoked job admitted: %v", err)
	}
}
