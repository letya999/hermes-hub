//go:build windows

package main

import (
	"os"
	"os/exec"

	"golang.org/x/sys/windows"
)

// setSysProcAttr gives the child its own console process group so a
// CTRL_BREAK event reaches only it, not the controller console.
func setSysProcAttr(cmd *exec.Cmd) {
	cmd.SysProcAttr = &windows.SysProcAttr{CreationFlags: windows.CREATE_NEW_PROCESS_GROUP}
}

// interruptProcess delivers CTRL_BREAK_EVENT, which Go maps to os.Interrupt
// inside the child: a graceful stop Windows supports instead of a bare kill.
func interruptProcess(p *os.Process) error {
	return windows.GenerateConsoleCtrlEvent(windows.CTRL_BREAK_EVENT, uint32(p.Pid)) // #nosec G115 -- pid fits uint32 on Windows
}
