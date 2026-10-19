package api

import (
	"context"
	"errors"
	"slices"
	"strings"
	"testing"
)

func cliExecDefinition() ToolDefinition {
	return ToolDefinition{
		Schema: SchemaVersion, DefinitionID: "grep", Version: "1.0.0",
		Transport: BoundedCLI,
		Source:    DefinitionSource{Command: "grep", Args: []string{"--color=never"}},
		Tools: []ToolSpec{{Name: "grep", Effect: ReadEffect, Arguments: []CLIArgument{
			{Name: "query", Type: "string", Flag: "--query", Required: true},
			{Name: "count", Type: "integer", Flag: "--count"},
			{Name: "verbose", Type: "boolean", Flag: "--verbose"},
		}}},
		Workload:  WorkloadPolicy{Class: PerJob, WorkspaceScope: "none", Rationale: "searches local text"},
		Execution: ExecutionPolicy{TimeoutSeconds: 10, OutputBytes: 4096, CPUMillis: 250, MemoryMiB: 64, MaxPIDs: 8, Egress: []string{"proxy.internal"}},
		Health:    HealthProbe{Kind: "exec", Value: "grep --version", TimeoutSeconds: 5},
	}
}

func cliExecEffective(def ToolDefinition) EffectiveBinding {
	return EffectiveBinding{
		WorkloadID: "wl-1",
		Binding:    ToolBinding{PrincipalID: "alice", ContextID: "ctx-1", ToolBindingID: "bind-1"},
		Definition: def,
	}
}

func cliExecReceipt(def ToolDefinition) AdmissionReceipt {
	return AdmissionReceipt{
		WorkloadID: "wl-1", State: "running", Enforced: true,
		CellID: "cell-1", Runtime: "runc",
		Isolation: []string{"filesystem", "network", "pid"},
		Execution: def.Execution,
	}
}

func TestCLIRunnerCallEnvContract(t *testing.T) {
	def := cliExecDefinition()
	var got CliExecRequest
	runner := CLIRunner{
		AllowedEnvironment: map[string]bool{"LOCALE": true, "API_TOKEN": true},
		Exec: func(ctx context.Context, req CliExecRequest) (CliExecResponse, error) {
			got = req
			return CliExecResponse{Receipt: cliExecReceipt(def), Output: "hit secret-xyz\n", ExitCode: 0}, nil
		},
	}
	if _, err := runner.CallEnv(context.Background(), cliExecEffective(ToolDefinition{Transport: RemoteMCP}), ToolSpec{}, nil, nil); !errors.Is(err, ErrInvalid) {
		t.Fatalf("non-cli transport: %v", err)
	}
	result, err := runner.CallEnv(context.Background(), cliExecEffective(def), def.Tools[0],
		map[string]any{"query": "needle", "count": 2, "verbose": true},
		map[string]string{"API_TOKEN": "secret-xyz", "LOCALE": "en"})
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(result.Text, "secret-xyz") || !strings.Contains(result.Text, "[REDACTED]") {
		t.Fatalf("output not redacted: %q", result.Text)
	}
	if !slices.Contains(got.Args, "--query") || !slices.Contains(got.Args, "needle") ||
		!slices.Contains(got.Args, "--count") || !slices.Contains(got.Args, "--verbose") ||
		!slices.Contains(got.Args, "--color=never") {
		t.Fatalf("argv: %v", got.Args)
	}
	if !slices.Contains(got.Env, "API_TOKEN=secret-xyz") || !slices.Contains(got.Env, "LOCALE=en") {
		t.Fatalf("env: %v", got.Env)
	}
	if got.Principal != "alice" || got.BindingID != "bind-1" || got.Workspace.Scope != "none" {
		t.Fatalf("request identity: %+v", got)
	}
	if result.Receipt != "wl-1@filesystem,network,pid@cell=cell-1,rt=runc" {
		t.Fatalf("receipt: %q", result.Receipt)
	}
	if _, err := runner.CallEnv(context.Background(), cliExecEffective(def), def.Tools[0],
		map[string]any{"query": "x"}, map[string]string{"EVIL_KEY": "x"}); !errors.Is(err, ErrUnauthorized) {
		t.Fatalf("unlisted env passed: %v", err)
	}
	quiet := CLIRunner{}
	if _, err := quiet.CallEnv(context.Background(), cliExecEffective(def), def.Tools[0], map[string]any{"query": "x"}, nil); !errors.Is(err, ErrIsolation) {
		t.Fatalf("nil exec ran: %v", err)
	}
}

func TestCliArgumentsAndBoundedOutput(t *testing.T) {
	tool := ToolSpec{Arguments: []CLIArgument{
		{Name: "query", Type: "string", Flag: "--query", Required: true},
		{Name: "count", Type: "integer", Flag: "--count"},
		{Name: "pos", Type: "string", Pattern: "^[a-z]+$"},
		{Name: "verbose", Type: "boolean", Flag: "--verbose"},
	}}
	got, err := CliArguments([]string{"--base"}, tool, map[string]any{"query": "x", "count": float64(3), "pos": "abc", "verbose": false})
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"--base", "--query", "x", "--count", "3", "abc", "--verbose=false"}
	if !slices.Equal(got, want) {
		t.Fatalf("argv %v, want %v", got, want)
	}
	rejects := []map[string]any{
		{"ghost": "x"},
		{"query": "x", "count": 1.5},
		{"query": "x", "pos": "-rf"},
		{"query": "x", "pos": "ABC"},
		{"count": 1},
	}
	for i, args := range rejects {
		if _, err := CliArguments(nil, tool, args); err == nil {
			t.Fatalf("reject case %d accepted", i)
		}
	}
	badTypes := ToolSpec{Arguments: []CLIArgument{{Name: "a", Type: "string", Flag: "--a"}}}
	if _, err := CliArguments(nil, badTypes, map[string]any{"a": 5}); err == nil {
		t.Fatal("non-string string arg accepted")
	}
	t.Setenv("HUB_CLI_ALLOWLIST", "grep, also-ok, /bad/path, bad name,")
	allow := CLIAllowlistFromEnv()
	if !allow["grep"] || !allow["also-ok"] || len(allow) != 2 {
		t.Fatalf("allowlist: %v", allow)
	}
	t.Setenv("HUB_CLI_ALLOWLIST", "")
	if CLIAllowlistFromEnv() != nil {
		t.Fatal("empty allowlist non-nil")
	}
	out := &BoundedOutput{Limit: 5, Exceeded: make(chan struct{})}
	if n, err := out.Write([]byte("hello")); n != 5 || err != nil {
		t.Fatalf("write: %d %v", n, err)
	}
	if n, err := out.Write([]byte("ab")); n != 0 || !errors.Is(err, ErrOutputLimit) {
		t.Fatalf("overflow: %d %v", n, err)
	}
	if !out.Overflowed() || out.String() != "hello" {
		t.Fatalf("buffer: %v %q", out.Overflowed(), out.String())
	}
	partial := &BoundedOutput{Limit: 3, Exceeded: make(chan struct{})}
	if n, err := partial.Write([]byte("abcde")); n != 3 || !errors.Is(err, ErrOutputLimit) || partial.String() != "abc" {
		t.Fatalf("partial: %d %v %q", n, err, partial.String())
	}
}
