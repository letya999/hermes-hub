//go:build !linux

package main

import (
	"os/exec"
	"syscall"
)

// Process-group kill is Linux-only; exec-scratch is a container entrypoint,
// so non-Linux builds keep the plain process kill and report the tree as
// gone (CommandContext's Kill already covers the direct child).
func scratchProcAttr() *syscall.SysProcAttr { return nil }

func scratchCancel(cmd *exec.Cmd) error { return cmd.Process.Kill() }

func scratchTreeGone(int) bool { return true }
