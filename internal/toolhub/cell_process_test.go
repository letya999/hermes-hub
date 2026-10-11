package toolhub

import (
	"bytes"
	"context"
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"testing"
)

func TestCellProcessReturnsOutputAndExitStatus(t *testing.T) {
	command, args := "sh", []string{"-c", "printf cell-output; exit 7"}
	if runtime.GOOS == "windows" {
		command, args = "cmd", []string{"/c", "echo cell-output & exit /b 7"}
	}
	var output bytes.Buffer
	code, err := localCommandExit(context.Background(), &output, command, args...)
	if err != nil || code != 7 || !bytes.Contains(output.Bytes(), []byte("cell-output")) {
		t.Fatalf("process outcome lost: %d %v %q", code, err, output.String())
	}
	if runtime.GOOS == "windows" {
		args = []string{"/c", "exit /b 0"}
	} else {
		args = []string{"-c", "exit 0"}
	}
	if code, err := localCommandExit(context.Background(), nil, command, args...); err != nil || code != 0 {
		t.Fatalf("successful empty outcome: %d %v", code, err)
	}
	if code, err := localCommandExit(context.Background(), nil, filepath.Join(t.TempDir(), "missing")); err == nil || code != -1 {
		t.Fatal("missing executable accepted")
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := localCommandExit(ctx, nil, command, args...); !errors.Is(err, context.Canceled) {
		t.Fatalf("cancellation lost: %v", err)
	}
}

func TestCLIHelperCopyPreservesBytesAndFailsWithoutSource(t *testing.T) {
	dir := t.TempDir()
	source, target := filepath.Join(dir, "source"), filepath.Join(dir, "cellinit")
	if err := copyFileMode(source, target, 0755); !errors.Is(err, os.ErrNotExist) {
		t.Fatal(err)
	}
	if _, err := os.Stat(target); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("missing source created helper")
	}
	if err := os.WriteFile(source, []byte("verified-helper"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := copyFileMode(source, target, 0755); err != nil {
		t.Fatal(err)
	}
	body, _ := os.ReadFile(target)
	if string(body) != "verified-helper" {
		t.Fatal("helper bytes changed")
	}
	if err := copyFileMode(source, dir, 0755); err == nil {
		t.Fatal("directory replaced")
	}
}
