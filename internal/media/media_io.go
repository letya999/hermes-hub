package media

import (
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path"
	"strings"
	"unicode/utf8"
)

func (s *Session) readRegular(rel string, limit int) ([]byte, error) {
	if s.Workspace == nil {
		return nil, errors.New("workspace is not configured")
	}
	if err := relPath(rel); err != nil {
		return nil, err
	}
	info, err := s.Workspace.Lstat(rel)
	if err != nil {
		return nil, err
	}
	if !info.Mode().IsRegular() || info.Mode()&os.ModeSymlink != 0 {
		return nil, errors.New("regular file required")
	}
	f, err := s.Workspace.Open(rel)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	body, err := io.ReadAll(io.LimitReader(f, int64(limit)+1))
	if err != nil {
		return nil, err
	}
	if len(body) > limit {
		return nil, fmt.Errorf("file exceeds %d bytes", limit)
	}
	return body, nil
}

func (s *Session) writeNew(rel string, body []byte) error {
	s.writeMu.Lock()
	defer s.writeMu.Unlock()
	if err := s.prepareWrite(rel, body); err != nil {
		return err
	}
	if _, err := s.Workspace.Lstat(rel); err == nil {
		return errors.New("artifact already exists")
	} else if !os.IsNotExist(err) {
		return err
	}
	return s.commitNew(rel, body)
}

func (s *Session) writeReplacing(rel string, body []byte) error {
	s.writeMu.Lock()
	defer s.writeMu.Unlock()
	if err := s.prepareWrite(rel, body); err != nil {
		return err
	}
	info, err := s.Workspace.Lstat(rel)
	if err != nil {
		return err
	}
	if !info.Mode().IsRegular() || info.Mode()&os.ModeSymlink != 0 {
		return errors.New("regular file required")
	}
	f, err := s.Workspace.OpenFile(rel, os.O_WRONLY|os.O_TRUNC, 0o600)
	if err != nil {
		return err
	}
	if _, err = f.Write(body); err != nil {
		_ = f.Close()
		return err
	}
	if err = f.Sync(); err != nil {
		_ = f.Close()
		return err
	}
	return f.Close()
}

func (s *Session) prepareWrite(rel string, body []byte) error {
	if s.Workspace == nil {
		return errors.New("workspace is not configured")
	}
	if containsSecretBytes(body) {
		return errors.New("refusing to store credential material")
	}
	return s.Workspace.MkdirAll(path.Dir(rel), 0o700)
}

func (s *Session) commitNew(rel string, body []byte) error {
	tmp := path.Join(path.Dir(rel), ".hub-"+randomToken())
	if err := s.writeTemp(tmp, body); err != nil {
		return err
	}
	if err := s.Workspace.Rename(tmp, rel); err != nil {
		_ = s.Workspace.Remove(tmp)
		return err
	}
	return nil
}

func (s *Session) writeTemp(tmp string, body []byte) error {
	f, err := s.Workspace.OpenFile(tmp, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
	if err != nil {
		return err
	}
	ok := false
	defer func() {
		_ = f.Close()
		if !ok {
			_ = s.Workspace.Remove(tmp)
		}
	}()
	if _, err = f.Write(body); err != nil {
		return err
	}
	if err = f.Sync(); err != nil {
		return err
	}
	if err = f.Close(); err != nil {
		return err
	}
	ok = true
	return nil
}

func relPath(p string) error {
	if p == "" || p == "." || !fs.ValidPath(p) || strings.Contains(p, "\\") || strings.ContainsRune(p, 0) || strings.ContainsRune(p, ':') || path.IsAbs(p) {
		return errors.New("relative path required")
	}
	return nil
}

func validName(name string) bool {
	if name == "" || len(name) > 64 {
		return false
	}
	for i, r := range name {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9':
		case (r == '-' || r == '_') && i > 0:
		default:
			return false
		}
	}
	return true
}

func pageCount(text string) int {
	n := utf8.RuneCountInString(text)
	if n == 0 {
		return 0
	}
	return (n + RunesPerPage - 1) / RunesPerPage
}

func slicesContains(list []string, value string) bool {
	for _, item := range list {
		if item == value {
			return true
		}
	}
	return false
}

func randomToken() string {
	var b [8]byte
	if _, err := rand.Read(b[:]); err != nil {
		return "tmp"
	}
	return hex.EncodeToString(b[:])
}
