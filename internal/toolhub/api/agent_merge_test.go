package api

import (
	"bytes"
	"context"
	"errors"
	"strings"
	"testing"
)

func TestAgentExecBackendDispatch(t *testing.T) {
	eff := EffectiveBinding{
		Definition:       ToolDefinition{Transport: AgentTools, Execution: ExecutionPolicy{TimeoutSeconds: 30}},
		Binding:          ToolBinding{ToolBindingID: "bind-1"},
		CapabilityScopes: []CapabilityScope{{PathArgument: "path", PathPrefix: "docs"}},
	}
	var gotRequest AgentExecRequest
	backend := AgentExecBackend{Exec: func(_ context.Context, _ EffectiveBinding, req AgentExecRequest) (AgentExecResult, error) {
		gotRequest = req
		return AgentExecResult{Result: map[string]any{"ok": true}}, nil
	}}
	// wrong transport and missing executor fail closed
	wrong := eff
	wrong.Definition.Transport = ContainerMCP
	if _, err := backend.Call(context.Background(), wrong, ToolSpec{Name: "t"}, nil); !errors.Is(err, ErrInvalid) {
		t.Fatalf("transport: %v", err)
	}
	if _, err := (AgentExecBackend{}).Call(context.Background(), eff, ToolSpec{Name: "t"}, nil); !errors.Is(err, ErrIsolation) {
		t.Fatalf("no exec: %v", err)
	}
	if _, err := backend.Call(context.Background(), eff, ToolSpec{Name: "t", Sandboxed: true}, nil); !errors.Is(err, ErrIsolation) {
		t.Fatalf("no scratch: %v", err)
	}
	// a plain call routes the admitted scopes through verbatim
	out, err := backend.CallEnv(context.Background(), eff, ToolSpec{Name: "file_read"}, map[string]any{"path": "a"}, nil)
	if err != nil || out.Structured == nil {
		t.Fatalf("call: %v %v", out, err)
	}
	if gotRequest.Tool != "file_read" || gotRequest.TimeoutSeconds != 30 || len(gotRequest.Scopes) != 1 {
		t.Fatalf("request: %+v", gotRequest)
	}
	// sandboxed tools route through Scratch with the fence, executor error maps to IsError
	backend.Scratch = func(_ context.Context, _ EffectiveBinding, req AgentExecRequest, fence ScratchFenceFunc) (AgentExecResult, error) {
		if fence == nil {
			t.Fatal("fence missing")
		}
		return AgentExecResult{Error: "boom"}, nil
	}
	backend.Fence = func(context.Context, EffectiveBinding) error { return nil }
	res, err := backend.Call(context.Background(), eff, ToolSpec{Name: "s", Sandboxed: true}, nil)
	if err != nil || !res.IsError || res.Text != "boom" {
		t.Fatalf("sandbox: %v %+v", err, res)
	}
	// transport failure wraps in ErrIsolation
	backend.Exec = func(context.Context, EffectiveBinding, AgentExecRequest) (AgentExecResult, error) {
		return AgentExecResult{}, errors.New("dead")
	}
	if _, err := backend.Call(context.Background(), eff, ToolSpec{Name: "t"}, nil); !errors.Is(err, ErrIsolation) {
		t.Fatalf("channel error: %v", err)
	}
}

func TestAgentExecHelpers(t *testing.T) {
	// fixed container resolves one name or fails closed
	if name, err := FixedAgentContainer("rt-1")(EffectiveBinding{}); err != nil || name != "rt-1" {
		t.Fatalf("fixed: %v %v", name, err)
	}
	if _, err := FixedAgentContainer("")(EffectiveBinding{}); !errors.Is(err, ErrIsolation) {
		t.Fatalf("empty fixed: %v", err)
	}
	// managed container derives deterministically and requires full identity
	e := EffectiveBinding{Binding: ToolBinding{PrincipalID: "p1", ContextID: "c1"}}
	name, err := ManagedAgentContainer(e)
	if err != nil || !strings.HasPrefix(name, "hermes-context-") || len(name) != len("hermes-context-")+16 {
		t.Fatalf("managed: %q %v", name, err)
	}
	again, _ := ManagedAgentContainer(e)
	if name != again {
		t.Fatal("nondeterministic")
	}
	if _, err := ManagedAgentContainer(EffectiveBinding{}); !errors.Is(err, ErrIsolation) {
		t.Fatalf("incomplete: %v", err)
	}
	// bounded writer truncates
	buf := &bytes.Buffer{}
	w := &BoundedWriter{W: buf, Max: 4}
	w.Write([]byte("abcdef"))
	w.Write([]byte("zz"))
	if buf.String() != "abcd" {
		t.Fatalf("bounded: %q", buf.String())
	}
}
