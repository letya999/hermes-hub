//go:build linux

package stack

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"syscall"
)

func runtimeConnectionReadable(root, relative string, uid, gid int) error {
	parts := append([]string{"."}, strings.Split(relative, "/")...)
	current := root
	for i, part := range parts {
		if i > 0 {
			current = filepath.Join(current, part)
		}
		info, err := os.Lstat(current)
		if err != nil {
			return err
		}
		if info.Mode()&os.ModeSymlink != 0 {
			return errors.New("symlink in runtime connection path")
		}
		want := os.FileMode(0400)
		if i < len(parts)-1 {
			if !info.IsDir() {
				return errors.New("runtime connection parent is not a directory")
			}
			want = 0100
		} else if !info.Mode().IsRegular() {
			return errors.New("runtime connection file is not regular")
		}
		stat, ok := info.Sys().(*syscall.Stat_t)
		if !ok {
			return errors.New("runtime connection ownership unavailable")
		}
		allowed := info.Mode().Perm()&want != 0
		if int(stat.Uid) != uid {
			allowed = info.Mode().Perm()&(want>>3) != 0
			if int(stat.Gid) != gid {
				allowed = info.Mode().Perm()&(want>>6) != 0
			}
		}
		if !allowed {
			return os.ErrPermission
		}
	}
	return nil
}
