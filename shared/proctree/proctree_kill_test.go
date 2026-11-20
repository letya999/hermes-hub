//go:build linux || windows

package proctree

import (
	"testing"
	"time"
)

// TestParentDeathKillsChild is the orphan bound itself: a parent helper
// spawns a grandchild through Configure + Assign, then dies by SIGKILL.
// Linux Pdeathsig and the Windows Job Object must take the grandchild down
// with it. The test builds only where a real bound exists: POSIX targets
// without Pdeathsig (darwin, the BSDs) keep only the group isolation that
// TestChildRunsInOwnProcessGroup covers — a SIGKILLed launcher's children
// survive there by design and are bounded by cancel + WaitDelay instead.
func TestParentDeathKillsChild(t *testing.T) {
	parent, childPID := spawnParent(t)
	if !alive(childPID) {
		t.Fatalf("grandchild %d not alive before the kill", childPID)
	}
	if err := parent.Process.Kill(); err != nil {
		t.Fatalf("kill parent: %v", err)
	}
	_ = parent.Wait()
	if !waitDead(t, childPID, 15*time.Second) {
		t.Fatalf("grandchild %d survived SIGKILLed parent — orphan bound broken", childPID)
	}
}
