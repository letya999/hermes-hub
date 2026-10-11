//go:build !windows

package main

import "syscall"

// reapOrphans re-parents and reaps orphaned tool processes when cellinit is
// pid1. Without it each killed exec child would linger as a zombie and trip
// the warm-reuse procs canary forever.
func reapOrphans() {
	for {
		var status syscall.WaitStatus
		if _, err := syscall.Wait4(-1, &status, 0, nil); err != nil {
			return
		}
	}
}

func killProc(pid int) { _ = syscall.Kill(pid, syscall.SIGKILL) }
