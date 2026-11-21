//go:build windows

package proctree

import (
	"fmt"
	"os"
	"os/exec"
	"sync"
	"unsafe"

	"golang.org/x/sys/windows"
)

// Configure gives the child its own console process group so a CTRL_BREAK
// event reaches only it, not the launcher's console.
func Configure(cmd *exec.Cmd) {
	cmd.SysProcAttr = &windows.SysProcAttr{CreationFlags: windows.CREATE_NEW_PROCESS_GROUP}
}

// Interrupt delivers CTRL_BREAK_EVENT, which Go maps to os.Interrupt inside
// the child: a graceful stop Windows supports instead of a bare kill.
func Interrupt(p *os.Process) error {
	return windows.GenerateConsoleCtrlEvent(windows.CTRL_BREAK_EVENT, uint32(p.Pid)) // #nosec G115 -- pid fits uint32 on Windows
}

// Group carries the Job Object handle Windows binds children to.
type Group struct {
	mu     sync.Mutex
	handle uintptr
}

func (g *Group) open() error {
	job, err := windows.CreateJobObject(nil, nil)
	if err != nil {
		return fmt.Errorf("create job object: %w", err)
	}
	info := windows.JOBOBJECT_EXTENDED_LIMIT_INFORMATION{}
	info.BasicLimitInformation.LimitFlags = windows.JOB_OBJECT_LIMIT_KILL_ON_JOB_CLOSE
	if _, err := windows.SetInformationJobObject(job, windows.JobObjectExtendedLimitInformation, uintptr(unsafe.Pointer(&info)), uint32(unsafe.Sizeof(info))); err != nil { // #nosec G103 -- the Win32 job-object API requires passing the struct pointer.
		_ = windows.CloseHandle(job)
		return fmt.Errorf("configure job object: %w", err)
	}
	g.handle = uintptr(job)
	return nil
}

func (g *Group) assign(cmd *exec.Cmd) error {
	if g.handle == 0 {
		return nil
	}
	proc, err := windows.OpenProcess(windows.PROCESS_SET_QUOTA|windows.PROCESS_TERMINATE, false, uint32(cmd.Process.Pid)) // #nosec G115 -- pid fits uint32 on Windows
	if err != nil {
		return fmt.Errorf("open child %d: %w", cmd.Process.Pid, err)
	}
	defer windows.CloseHandle(proc)
	if err := windows.AssignProcessToJobObject(windows.Handle(g.handle), proc); err != nil {
		return fmt.Errorf("assign child %d to job: %w", cmd.Process.Pid, err)
	}
	return nil
}

func (g *Group) close() {
	if g.handle != 0 {
		_ = windows.CloseHandle(windows.Handle(g.handle))
		g.handle = 0
	}
}
