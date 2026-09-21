//go:build linux

// Package securefs confines broker files to owned, non-symlink directories.
// Production deployment is Linux. The administrator and host root are trusted.
package securefs

import (
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"syscall"
)

var ErrUnsafe = errors.New("unsafe filesystem object")

// Dir refuses symlink components and group/world-accessible final directories.
func Dir(path string) error {
	if !filepath.IsAbs(path) || filepath.Clean(path) != path {
		return ErrUnsafe
	}
	current := string(os.PathSeparator)
	for _, part := range strings.Split(strings.TrimPrefix(path, "/"), "/") {
		if part == "" {
			continue
		}
		current = filepath.Join(current, part)
		fi, err := os.Lstat(current)
		if errors.Is(err, os.ErrNotExist) {
			if err = os.Mkdir(current, 0700); err != nil {
				return err
			}
			fi, err = os.Lstat(current)
		}
		if err != nil {
			return err
		}
		if !fi.IsDir() || fi.Mode()&os.ModeSymlink != 0 {
			return ErrUnsafe
		}
	}
	fi, err := os.Lstat(path)
	if err != nil {
		return err
	}
	st, ok := fi.Sys().(*syscall.Stat_t)
	if !ok || int(st.Uid) != os.Geteuid() || fi.Mode().Perm()&0077 != 0 {
		return ErrUnsafe
	}
	return nil
}

func Name(name string) bool {
	return name != "" && name != "." && name != ".." && len(name) <= 128 && !strings.ContainsAny(name, "/\\\x00")
}

// Open uses a directory descriptor and O_NOFOLLOW; it never follows a final symlink.
func Open(dir, name string, flags int, mode uint32) (*os.File, error) {
	if !Name(name) {
		return nil, ErrUnsafe
	}
	d, err := syscall.Open(dir, syscall.O_RDONLY|syscall.O_DIRECTORY|syscall.O_NOFOLLOW|syscall.O_CLOEXEC, 0)
	if err != nil {
		return nil, err
	}
	defer syscall.Close(d)
	fd, err := syscall.Openat(d, name, flags|syscall.O_NOFOLLOW|syscall.O_CLOEXEC|syscall.O_NONBLOCK, mode)
	if err != nil {
		return nil, err
	}
	f := os.NewFile(uintptr(fd), filepath.Join(dir, name))
	st, err := f.Stat()
	if err != nil || !st.Mode().IsRegular() {
		_ = f.Close()
		return nil, ErrUnsafe
	}
	if stat, ok := st.Sys().(*syscall.Stat_t); !ok || stat.Nlink != 1 || int(stat.Uid) != os.Geteuid() {
		_ = f.Close()
		return nil, ErrUnsafe
	}
	return f, nil
}

func Read(dir, name string, limit int64) ([]byte, error) {
	f, err := Open(dir, name, syscall.O_RDONLY, 0)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	fi, err := f.Stat()
	if err != nil || fi.Size() > limit {
		return nil, ErrUnsafe
	}
	b, err := io.ReadAll(io.LimitReader(f, limit+1))
	if err != nil {
		return nil, err
	}
	if int64(len(b)) > limit {
		return nil, ErrUnsafe
	}
	return b, nil
}

// Create writes an immutable object with fsync on file and containing directory.
func Create(dir, name string, data []byte, mode uint32) error {
	f, err := Open(dir, name, syscall.O_WRONLY|syscall.O_CREAT|syscall.O_EXCL, mode)
	if err != nil {
		return err
	}
	_, err = f.Write(data)
	if err == nil {
		err = f.Sync()
	}
	closeErr := f.Close()
	if err == nil {
		err = closeErr
	}
	if err != nil {
		_ = os.Remove(filepath.Join(dir, name))
		return err
	}
	return SyncDir(dir)
}
func SyncDir(dir string) error {
	d, err := os.Open(dir)
	if err != nil {
		return err
	}
	defer d.Close()
	return d.Sync()
}
func Tmpfs(path string) error {
	var st syscall.Statfs_t
	if err := syscall.Statfs(path, &st); err != nil {
		return err
	}
	if uint64(st.Type) != 0x01021994 {
		return fmt.Errorf("runtime root must be tmpfs")
	}
	return nil
}

func ReadKey(path string) ([]byte, error) {
	if !filepath.IsAbs(path) {
		return nil, ErrUnsafe
	}
	// Parent and file must not be symlinks. Parent ownership is deployment-controlled.
	if err := Dir(filepath.Dir(path)); err != nil {
		return nil, err
	}
	f, err := Open(filepath.Dir(path), filepath.Base(path), syscall.O_RDONLY, 0)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	st, err := f.Stat()
	if err != nil || st.Mode().Perm()&0077 != 0 {
		return nil, ErrUnsafe
	}
	b, err := io.ReadAll(io.LimitReader(f, 33))
	if err != nil {
		return nil, err
	}
	if len(b) != 32 {
		return nil, errors.New("key file must contain 32 raw bytes")
	}
	return b, nil
}
