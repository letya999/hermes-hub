//go:build linux

package stack

import (
	"os"
	"syscall"
)

func dockerSocketGID(path string) int {
	info, err := os.Stat(path)
	if err == nil && info.Mode()&os.ModeSocket != 0 {
		if stat, ok := info.Sys().(*syscall.Stat_t); ok {
			return int(stat.Gid)
		}
	}
	return max(0, os.Getgid())
}
