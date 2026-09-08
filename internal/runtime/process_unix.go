//go:build !windows

package runtime

import (
	"os"
	"os/exec"
	"os/signal"
	"syscall"
)

func configureProcess(cmd *exec.Cmd)            { cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true} }
func setUmask()                                 { syscall.Umask(0007) }
func chownPath(path string, uid, gid int) error { return os.Chown(path, uid, gid) }
func stopProcess(cmd *exec.Cmd) {
	if cmd.ProcessState == nil {
		_ = syscall.Kill(-cmd.Process.Pid, syscall.SIGTERM)
	}
}
func processAlive(pid int) bool       { return syscall.Kill(pid, 0) == nil }
func notifySignals(ch chan os.Signal) { signal.Notify(ch, syscall.SIGINT, syscall.SIGTERM) }
func stopSignals(ch chan os.Signal)   { signal.Stop(ch) }
