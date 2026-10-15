package api

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestCLIRunnerFromEnv(t *testing.T) {
	// no controller endpoint → runner with a closed exec channel
	t.Setenv("HUB_TOOLHIVE_ADMISSION_ENDPOINT", "")
	runner, err := CLIRunnerFromEnv()
	if err != nil {
		t.Fatal(err)
	}
	if runner.Exec != nil || runner.Root != "/state" {
		t.Fatalf("runner: %+v", runner)
	}
	// endpoint configured → the runner gets a live exec channel
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(409) }))
	defer srv.Close()
	t.Setenv("HUB_TOOLHIVE_ADMISSION_ENDPOINT", srv.URL)
	t.Setenv("HUB_STATE", t.TempDir())
	runner, err = CLIRunnerFromEnv()
	if err != nil {
		t.Fatal(err)
	}
	if runner.Exec == nil || runner.Root == "" {
		t.Fatalf("runner: %+v", runner)
	}
	// Call delegates through CallEnv
	def := cliExecDefinition()
	var hit bool
	runner = CLIRunner{Exec: func(ctx context.Context, req CliExecRequest) (CliExecResponse, error) {
		hit = true
		return CliExecResponse{Receipt: cliExecReceipt(def), ExitCode: 0}, nil
	}}
	if _, err := runner.Call(t.Context(), cliExecEffective(def), def.Tools[0], map[string]any{"query": "x"}); err != nil || !hit {
		t.Fatalf("call: %v hit=%v", err, hit)
	}
}
