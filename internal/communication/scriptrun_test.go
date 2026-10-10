package communication

import (
	"context"
	"strings"
	"testing"
	"time"
)

// A scheduled script is the LLM-free workhorse: deterministic Starlark, no
// fs/net/random/clock beyond the injected bindings, hard step and wall caps,
// and only emit() output ever leaves the sandbox.
func TestRunScriptEmit(t *testing.T) {
	emitted, err := RunScript(context.Background(), `
emit("hello " + "world")
emit(time_now())
def total():
    x = 0
    for i in range(100):
        x += i
    return x
emit(str(total()))
`, time.Date(2026, 10, 10, 12, 0, 0, 0, time.UTC))
	if err != nil {
		t.Fatal(err)
	}
	if len(emitted) != 3 || emitted[0] != "hello world" || emitted[1] != "2026-10-10T12:00:00Z" || emitted[2] != "4950" {
		t.Fatalf("emitted: %v", emitted)
	}
	// No ambient authority: nothing beyond the declared builtins exists.
	for _, name := range []string{"open", "socket", "exec", "read_file"} {
		if _, err := RunScript(context.Background(), name+"()", time.Now()); err == nil {
			t.Fatalf("%s reachable inside the sandbox", name)
		}
	}
	// Runaway computation is cut by the step budget, not by luck.
	if _, err := RunScript(context.Background(), "x = 0\nfor i in range(10**9):\n    x += i", time.Now()); err == nil {
		t.Fatal("unbounded loop was not cut")
	}
	// emit is capped.
	huge := "emit(" + strings.Repeat("'x'", 1) + " * 99999)"
	if emitted, err := RunScript(context.Background(), huge, time.Now()); err == nil || len(emitted) != 0 {
		t.Fatalf("oversized emit accepted: %v %v", emitted, err)
	}
	// A cancelled context kills the run — the wall-time bound the dispatcher
	// relies on must actually reach the interpreter.
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := RunScript(ctx, "for i in range(10**9):\n    pass", time.Now()); err == nil {
		t.Fatal("cancelled context did not stop the script")
	}
}
