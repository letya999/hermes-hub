package api

import (
	"context"
	"errors"
	"slices"
	"strings"
	"testing"
)

// CallEnv propagates the cell's receipts and errors: output-limit passes
// through unwrapped, exec failures are isolation faults, exit codes carry the
// redacted tail, and brokered credentials never enter argv env.
func TestCLIRunnerErrorAndBrokeredPaths(t *testing.T) {
	t.Parallel()
	def := cliExecDefinition()
	def.Credentials = []CredentialInput{{Name: "API_TOKEN", Delivery: "brokered", Target: "api.example", Prefix: "Bearer"}}

	var got CliExecRequest
	runner := CLIRunner{Exec: func(ctx context.Context, req CliExecRequest) (CliExecResponse, error) {
		got = req
		return CliExecResponse{Receipt: cliExecReceipt(def), Output: "ok", ExitCode: 0}, nil
	}}
	effective := cliExecEffective(def)
	_, err := runner.CallEnv(context.Background(), effective, def.Tools[0],
		map[string]any{"query": "x"}, map[string]string{"API_TOKEN": "secret", "LOCALE": "en"})
	if err != nil {
		t.Fatal(err)
	}
	if slices.Contains(got.Env, "API_TOKEN=secret") {
		t.Fatalf("brokered credential must not enter env pairs: %v", got.Env)
	}
	if len(got.Brokered) != 1 || got.Brokered[0].EnvName != "API_TOKEN" || got.Brokered[0].Host != "api.example" || got.Brokered[0].Value != "secret" {
		t.Fatalf("brokered request: %+v", got.Brokered)
	}

	fails := []struct {
		exec func(context.Context, CliExecRequest) (CliExecResponse, error)
		want error
	}{
		{func(context.Context, CliExecRequest) (CliExecResponse, error) {
			return CliExecResponse{}, ErrOutputLimit
		}, ErrOutputLimit},
		{func(context.Context, CliExecRequest) (CliExecResponse, error) {
			return CliExecResponse{}, errors.New("cell died")
		}, ErrIsolation},
		{func(context.Context, CliExecRequest) (CliExecResponse, error) {
			return CliExecResponse{Receipt: AdmissionReceipt{WorkloadID: "other"}}, nil
		}, ErrIsolation},
		{func(context.Context, CliExecRequest) (CliExecResponse, error) {
			return CliExecResponse{Receipt: cliExecReceipt(def), Error: "output-limit"}, nil
		}, ErrOutputLimit},
		{func(context.Context, CliExecRequest) (CliExecResponse, error) {
			return CliExecResponse{Receipt: cliExecReceipt(def), Error: "policy"}, nil
		}, ErrIsolation},
	}
	for i, c := range fails {
		r := CLIRunner{Exec: c.exec}
		if _, err := r.CallEnv(context.Background(), effective, def.Tools[0], map[string]any{"query": "x"}, nil); !errors.Is(err, c.want) {
			t.Fatalf("case %d: %v, want %v", i, err, c.want)
		}
	}

	badExit := CLIRunner{Exec: func(context.Context, CliExecRequest) (CliExecResponse, error) {
		return CliExecResponse{Receipt: cliExecReceipt(def), Output: "boom secret", ExitCode: 3}, nil
	}}
	_, err = badExit.CallEnv(context.Background(), effective, def.Tools[0], map[string]any{"query": "x"}, map[string]string{"SECRETV": "secret"})
	if err == nil || !strings.Contains(err.Error(), "exit code 3") {
		t.Fatalf("exit code must surface with redacted output: %v", err)
	}
}

// A binding-scoped workload opens its workspace and hands the canonical path
// to the cell.
func TestCLIRunnerBindingWorkspace(t *testing.T) {
	t.Parallel()
	def := cliExecDefinition()
	def.Workload.Class = PerUser
	def.Workload.WorkspaceScope = "binding"
	def.Workload.WorkspaceAccess = "ro"
	var got CliExecRequest
	runner := CLIRunner{
		Root: t.TempDir(),
		Exec: func(ctx context.Context, req CliExecRequest) (CliExecResponse, error) {
			got = req
			return CliExecResponse{Receipt: cliExecReceipt(def), ExitCode: 0}, nil
		},
	}
	_, err := runner.CallEnv(context.Background(), cliExecEffective(def), def.Tools[0], map[string]any{"query": "x"}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if got.Workspace.Path == "" || got.Workspace.Scope != "binding" || got.Workspace.Access != "ro" {
		t.Fatalf("workspace handoff: %+v", got.Workspace)
	}
}
