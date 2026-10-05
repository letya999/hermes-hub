//go:build linux

package main

import (
	"os"
	"os/exec"
	"strconv"
	"strings"
	"syscall"
	"time"
)

// scratchProcAttr puts the sandboxed script in its own process group so a
// lease expiry or cancellation kills the whole tree, not just the direct sh.
func scratchProcAttr() *syscall.SysProcAttr {
	return &syscall.SysProcAttr{Setpgid: true}
}

// scratchCancel is the CommandContext cancel hook: SIGKILL to the whole
// process group rooted at the shell's pid.
func scratchCancel(cmd *exec.Cmd) error {
	return syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL)
}

// scratchTreeGone confirms no member of the process group survives: /proc
// stat field pgrp is the authoritative membership check. Bounded to two
// seconds; an unreachable /proc reports gone (the container removal that
// follows is the harder guarantee anyway).
func scratchTreeGone(pid int) bool {
	target := strconv.Itoa(pid)
	deadline := time.Now().Add(2 * time.Second)
	for {
		alive := false
		if entries, err := os.ReadDir("/proc"); err == nil {
			for _, e := range entries {
				stat, err := os.ReadFile("/proc/" + e.Name() + "/stat")
				if err != nil {
					continue
				}
				s := string(stat)
				i := strings.LastIndexByte(s, ')')
				if i < 0 {
					continue
				}
				fields := strings.Fields(s[i+1:])
				if len(fields) > 2 && fields[2] == target {
					alive = true
					break
				}
			}
		}
		if !alive {
			return true
		}
		if time.Now().After(deadline) {
			return false
		}
		time.Sleep(20 * time.Millisecond)
	}
}
