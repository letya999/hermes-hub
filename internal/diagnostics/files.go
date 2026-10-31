package diagnostics

import (
	"bytes"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strconv"
	"time"
)

const (
	maxDiagnosticLine   = 32 << 10
	maxDiagnosticBatch  = 4 << 20
	rotatedLogFiles     = 3
	diagnosticRetention = 7 * 24 * time.Hour
)

func syncedReplace(path string, data []byte) error {
	dir := filepath.Dir(path)
	f, err := os.CreateTemp(dir, ".diagnostic-*")
	if err != nil {
		return err
	}
	name := f.Name()
	defer os.Remove(name)
	if err = f.Chmod(0660); err == nil {
		_, err = f.Write(data)
	}
	if err == nil {
		err = f.Sync()
	}
	closeErr := f.Close()
	if err != nil {
		return err
	}
	if closeErr != nil {
		return closeErr
	}
	root, err := os.OpenRoot(dir)
	if err != nil {
		return err
	}
	defer root.Close()
	if err = root.Rename(filepath.Base(name), filepath.Base(path)); err != nil {
		return err
	}
	return syncDiagnosticDirectory(dir)
}

// Rotation never truncates the active inode. Generation changes before new data.
func appendDiagnostic(path string, data []byte, limit int64) error {
	if int64(len(data)) > limit {
		return errors.New("diagnostic batch exceeds file bound")
	}
	dir := filepath.Dir(path)
	root, err := os.OpenRoot(dir)
	if err != nil {
		return err
	}
	defer root.Close()
	base := filepath.Base(path)
	for i := 1; i <= rotatedLogFiles; i++ {
		name := base + "." + strconv.Itoa(i)
		info, err := root.Lstat(name)
		if errors.Is(err, os.ErrNotExist) {
			continue
		}
		if err != nil {
			return err
		}
		if !info.Mode().IsRegular() {
			return errors.New("unsafe rotated diagnostic file")
		}
		if time.Since(info.ModTime()) > diagnosticRetention {
			if err = root.Remove(name); err != nil {
				return err
			}
		}
	}
	info, err := root.Lstat(base)
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	if err == nil {
		if !info.Mode().IsRegular() {
			return errors.New("unsafe diagnostic file")
		}
		aged := time.Since(info.ModTime()) > diagnosticRetention
		if info.Size()+int64(len(data)) > limit || aged {
			if err = root.Remove(base + "." + strconv.Itoa(rotatedLogFiles)); err != nil && !errors.Is(err, os.ErrNotExist) {
				return err
			}
			for i := rotatedLogFiles - 1; i >= 0; i-- {
				from := base
				if i > 0 {
					from += "." + strconv.Itoa(i)
				}
				if err = root.Rename(from, base+"."+strconv.Itoa(i+1)); err != nil && !errors.Is(err, os.ErrNotExist) {
					return err
				}
			}
			if aged {
				if err = root.Remove(base + ".1"); err != nil {
					return err
				}
			}
			var id [16]byte
			if _, err = rand.Read(id[:]); err != nil {
				return err
			}
			if err = syncedReplace(path+".generation", []byte(hex.EncodeToString(id[:]))); err != nil {
				return err
			}
		}
	}
	f, err := root.OpenFile(base, os.O_CREATE|os.O_EXCL|os.O_APPEND|os.O_WRONLY, 0660)
	created := err == nil
	if errors.Is(err, os.ErrExist) {
		f, err = root.OpenFile(base, os.O_APPEND|os.O_WRONLY, 0660)
	}
	if err != nil {
		return err
	}
	if created {
		if err = f.Chmod(0660); err != nil {
			_ = f.Close()
			return err
		}
	}
	_, writeErr := f.Write(data)
	if writeErr == nil {
		writeErr = f.Sync()
	}
	closeErr := f.Close()
	if writeErr != nil {
		return writeErr
	}
	if closeErr != nil {
		return closeErr
	}
	return syncDiagnosticDirectory(dir)
}

// ReadDiagnosticTail reads the newest owned generations within one byte budget.
func ReadDiagnosticTail(path string, limit int64) ([]byte, bool, error) {
	if limit <= 0 {
		return nil, false, errors.New("invalid diagnostic tail bound")
	}
	root, err := os.OpenRoot(filepath.Dir(path))
	if err != nil {
		return nil, false, err
	}
	defer root.Close()
	base := filepath.Base(path)
	remaining := limit
	truncated := false
	found := false
	var parts [][]byte
	for i := 0; i <= rotatedLogFiles; i++ {
		name := base
		if i > 0 {
			name += "." + strconv.Itoa(i)
		}
		info, err := root.Lstat(name)
		if errors.Is(err, os.ErrNotExist) {
			continue
		}
		if err != nil {
			return nil, false, err
		}
		found = true
		if !info.Mode().IsRegular() {
			return nil, false, errors.New("unsafe diagnostic segment")
		}
		if remaining == 0 {
			if info.Size() > 0 {
				truncated = true
			}
			continue
		}
		if len(parts) > 0 {
			if remaining <= 1 {
				truncated = truncated || info.Size() > 0
				continue
			}
			remaining-- // Reserve the segment separator within the same byte budget.
		}
		f, err := root.Open(name)
		if err != nil {
			return nil, false, err
		}
		offset := int64(0)
		if info.Size() > remaining {
			offset = info.Size() - remaining
			truncated = true
		}
		if _, err = f.Seek(offset, io.SeekStart); err != nil {
			_ = f.Close()
			return nil, false, err
		}
		data, readErr := io.ReadAll(io.LimitReader(f, remaining))
		closeErr := f.Close()
		if readErr != nil {
			return nil, false, readErr
		}
		if closeErr != nil {
			return nil, false, closeErr
		}
		remaining -= int64(len(data))
		if offset > 0 {
			if cut := bytes.IndexByte(data, '\n'); cut >= 0 {
				data = data[cut+1:]
			} else {
				data = nil
			}
		}
		if len(data) > 0 {
			parts = append(parts, data)
		}
	}
	if !found {
		return nil, false, os.ErrNotExist
	}
	for left, right := 0, len(parts)-1; left < right; left, right = left+1, right-1 {
		parts[left], parts[right] = parts[right], parts[left]
	}
	return bytes.Join(parts, []byte("\n")), truncated, nil
}
