//go:build !windows

package proctree

import "syscall"

// alive reports whether pid still names a live process.
func alive(pid int) bool {
	err := syscall.Kill(pid, 0)
	return err == nil || err == syscall.EPERM
}
