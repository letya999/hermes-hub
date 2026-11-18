//go:build linux

package main

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// feedTerminal swaps os.Stdin for a file carrying the given frame lines.
func feedTerminal(t *testing.T, lines string) func() {
	t.Helper()
	name := filepath.Join(t.TempDir(), "stdin.json")
	if err := os.WriteFile(name, []byte(lines), 0o600); err != nil {
		t.Fatal(err)
	}
	in, err := os.Open(name)
	if err != nil {
		t.Fatal(err)
	}
	old := os.Stdin
	os.Stdin = in
	return func() { os.Stdin = old; _ = in.Close() }
}

// The start frame is validated before any process is spawned: malformed
// streams and invalid fields are rejected on the wire.
func TestRunExecTerminalValidation(t *testing.T) {
	for _, tc := range []struct{ name, frame string }{
		{"empty", ""},
		{"non-json", "nope\n"},
		{"no-command", "{\"cwd\":\"x\"}\n"},
		{"nul-command", "{\"command\":\"a\\u0000b\"}\n"},
		{"abs-cwd", "{\"command\":\"ls\",\"cwd\":\"/etc\"}\n"},
		{"traversal-cwd", "{\"command\":\"ls\",\"cwd\":\"../escape\"}\n"},
		{"bad-timeout", "{\"command\":\"ls\",\"timeout\":0}\n"},
		{"long-timeout", "{\"command\":\"ls\",\"timeout\":700}\n"},
		{"bad-shell", "{\"command\":\"ls\",\"shell\":\"zsh\"}\n"},
	} {
		restore := feedTerminal(t, tc.frame)
		err := runExecTerminal(context.Background())
		restore()
		if err == nil {
			t.Fatalf("%s accepted", tc.name)
		}
	}
}

// A real spawn drives the frame protocol end to end: started, stdout/stderr
// frames, done with the shell's exit code. Skipped where /scratch is not
// writable — the workload contract assumes the disposable image layout.
func TestRunExecTerminalSpawn(t *testing.T) {
	if err := os.MkdirAll("/scratch/workspace", 0o700); err != nil {
		t.Skip("scratch root unavailable outside the workload image")
	}
	restore := feedTerminal(t, `{"command":"echo out-$MARKER; echo err-line >&2; exit 4","cwd":".","shell":"sh","timeout":30,"env":{"MARKER":"yes","PATH":"evil"}}`+"\n")
	defer restore()

	out := captureOutput(t, func() error { return runExecTerminal(context.Background()) })
	var sawStart, sawStdout, sawStderr bool
	var doneFrame terminalFrame
	for _, line := range strings.Split(out, "\n") {
		var f terminalFrame
		if err := json.Unmarshal([]byte(line), &f); err != nil {
			t.Fatalf("frame decode %q: %v", line, err)
		}
		switch f.Action {
		case "started":
			sawStart = true
		case "stdout":
			if strings.Contains(f.Data, "out-yes") {
				sawStdout = true
			}
		case "stderr":
			if strings.Contains(f.Data, "err-line") {
				sawStderr = true
			}
		case "done":
			doneFrame = f
		}
	}
	if !sawStart || !sawStdout || !sawStderr {
		t.Fatalf("missing frames: start=%v stdout=%v stderr=%v", sawStart, sawStdout, sawStderr)
	}
	if doneFrame.ExitCode != 4 {
		t.Fatalf("done frame: %+v", doneFrame)
	}
}
