//go:build linux

package proctree

import (
	"os"
	"os/exec"
	"syscall"
)

// Configure puts the child in its own process group and arms Pdeathsig:
// if the launcher dies — even by SIGKILL — the kernel delivers SIGTERM to
// the child, and the child's own Pdeathsig'd children cascade after it.
func Configure(cmd *exec.Cmd) {
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true, Pdeathsig: syscall.SIGTERM}
}

// Interrupt delivers SIGTERM so the child can shut down children of its
// own before the WaitDelay deadline forces a kill.
func Interrupt(p *os.Process) error { return p.Signal(syscall.SIGTERM) }

func (g *Group) open() error            { return nil }
func (g *Group) assign(*exec.Cmd) error { return nil }
func (g *Group) close()                 {}
