package api

import (
	"bytes"
	"context"
	"os"
	"strings"
	"testing"
)

func TestIsolatedAndLocalCommandEnv(t *testing.T) {
	// isolated env keeps locality keys and caller extras, drops other host vars
	t.Setenv("PATH", os.Getenv("PATH"))
	t.Setenv("SECRET_HOST_VAR", "must-not-leak")
	env := IsolatedToolHiveEnviron(map[string]string{"WORKLOAD_TOKEN": "x", "CUSTOM_PATH_MARKER": "y"})
	joined := strings.Join(env, "\n")
	if strings.Contains(joined, "SECRET_HOST_VAR") {
		t.Fatal("host credential leaked into isolated env")
	}
	if !strings.Contains(joined, "WORKLOAD_TOKEN=x") || !strings.Contains(joined, "CUSTOM_PATH_MARKER=y") {
		t.Fatalf("extras: %v", env)
	}
	// caller extras override host locality keys
	env = IsolatedToolHiveEnviron(map[string]string{"HOME": "/isolated"})
	for _, kv := range env {
		if strings.HasPrefix(strings.ToUpper(kv), "HOME=") && kv != "HOME=/isolated" {
			t.Fatalf("duplicate host HOME: %q", kv)
		}
	}
	// IsolatedCommandEnv captures combined output; failure includes stderr
	out, err := IsolatedCommandEnv(context.Background(), nil, "go", "version")
	if err != nil || !strings.Contains(string(out), "go version") {
		t.Fatalf("isolated: %q %v", out, err)
	}
	if _, err := IsolatedCommandEnv(context.Background(), nil, "go", "this-subcommand-does-not-exist"); err == nil || !strings.Contains(err.Error(), "isolated ToolHive command failed") {
		t.Fatalf("failure: %v", err)
	}
	// LocalCommandEnv runs with host env plus extras; output goes to stdout only
	out, err = LocalCommandEnv(context.Background(), map[string]string{"X": "1"}, "go", "version")
	if err != nil || !strings.Contains(string(out), "go version") {
		t.Fatalf("local: %q %v", out, err)
	}
	// LocalCommandExit surfaces the tool's own exit code
	var sink bytes.Buffer
	code, err := LocalCommandExit(context.Background(), &sink, "go", "this-subcommand-does-not-exist")
	if err != nil || code <= 0 {
		t.Fatalf("exit: %d %v", code, err)
	}
	code, err = LocalCommandExit(context.Background(), nil, "go", "version")
	if err != nil || code != 0 {
		t.Fatalf("success exit: %d %v", code, err)
	}
	// a missing binary reports -1 and an error
	if code, err := LocalCommandExit(context.Background(), nil, "definitely-not-a-binary-xyz"); err == nil || code != -1 {
		t.Fatalf("missing binary: %d %v", code, err)
	}
}
