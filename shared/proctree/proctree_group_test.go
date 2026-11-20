//go:build !windows

package proctree

import (
	"os/exec"
	"syscall"
	"testing"
)

// TestChildRunsInOwnProcessGroup covers the Unix half of the bound on every
// POSIX target including ones without Pdeathsig: the child leads its own
// process group, so a group kill reaches it and its children without
// touching the launcher.
func TestChildRunsInOwnProcessGroup(t *testing.T) {
	cmd := exec.Command("sleep", "30")
	Configure(cmd)
	if err := cmd.Start(); err != nil {
		t.Fatalf("start sleep: %v", err)
	}
	t.Cleanup(func() {
		_ = cmd.Process.Kill()
		_ = cmd.Wait()
	})
	pgid, err := syscall.Getpgid(cmd.Process.Pid)
	if err != nil {
		t.Fatalf("getpgid: %v", err)
	}
	if pgid != cmd.Process.Pid {
		t.Fatalf("child pgid %d != pid %d: Setpgid did not take", pgid, cmd.Process.Pid)
	}
}
