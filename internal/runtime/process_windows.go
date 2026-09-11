package runtime

import (
	"os"
	"os/exec"
	"os/signal"

	"golang.org/x/sys/windows"
)

func configureProcess(*exec.Cmd)       {}
func setUmask()                        {}
func chownPath(string, int, int) error { return nil }
func stopProcess(cmd *exec.Cmd)        { _ = cmd.Process.Kill() }
func processAlive(pid int) bool {
	if pid <= 0 || uint64(pid) > uint64(^uint32(0)) {
		return false
	}
	handle, err := windows.OpenProcess(windows.PROCESS_QUERY_LIMITED_INFORMATION, false, uint32(pid))
	if err != nil {
		return false
	}
	return windows.CloseHandle(handle) == nil
}
func notifySignals(ch chan os.Signal) { signal.Notify(ch, os.Interrupt) }
func stopSignals(ch chan os.Signal)   { signal.Stop(ch) }
