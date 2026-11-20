//go:build !windows && !linux

package proctree

import (
	"os"
	"os/exec"
	"syscall"
)

// Configure puts the child in its own process group. Pdeathsig is a
// Linux-only facility; on other Unix targets the launcher's own lifecycle
// (cancel + WaitDelay) is the orphan bound.
func Configure(cmd *exec.Cmd) {
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
}

// Interrupt delivers SIGTERM so the child can shut down children of its
// own before the WaitDelay deadline forces a kill.
func Interrupt(p *os.Process) error { return p.Signal(syscall.SIGTERM) }

func (g *Group) open() error            { return nil }
func (g *Group) assign(*exec.Cmd) error { return nil }
func (g *Group) close()                 {}
