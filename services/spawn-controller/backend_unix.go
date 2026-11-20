//go:build !windows

package main

import (
	"os"
	"os/exec"
	"syscall"
)

// setSysProcAttr is a no-op on Unix: signal delivery needs no process setup.
func setSysProcAttr(*exec.Cmd) {}

// interruptProcess delivers SIGTERM so the child can finish in-flight work
// before the WaitDelay deadline forces a kill.
func interruptProcess(p *os.Process) error { return p.Signal(syscall.SIGTERM) }
