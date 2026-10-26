//go:build !windows

package hosting

import (
	"os"
	"syscall"
)

func readFlags() int { return os.O_RDONLY | syscall.O_NONBLOCK | syscall.O_NOFOLLOW }
func multipleLinks(i os.FileInfo) bool {
	st, ok := i.Sys().(*syscall.Stat_t)
	return !ok || st.Nlink != 1
}
func syncDirectory(p string) error {
	f, err := os.Open(p)
	if err != nil {
		return ErrUnavailable
	}
	defer f.Close()
	if f.Sync() != nil {
		return ErrUnavailable
	}
	return nil
}

func openedMultipleLinks(f *os.File) bool { i, err := f.Stat(); return err != nil || multipleLinks(i) }
