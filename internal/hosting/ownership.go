package hosting

import (
	"encoding/json"
	"errors"
	"io"
	"io/fs"
	"os"
	"strings"
)

func validRelease(v string) bool { return len(v) == 64 && strings.Trim(v, "0123456789abcdef") == "" }
func (s *Service) claimStore() error {
	const filename = ".hosting-owner.json"
	file, err := s.root.Open(filename)
	if errors.Is(err, os.ErrNotExist) {
		entries, err := fs.ReadDir(s.root.FS(), ".")
		if err != nil {
			return ErrUnavailable
		}
		for _, entry := range entries {
			if entry.Name() != "writer.lock" {
				return ErrDenied
			}
		}
		out, err := s.root.OpenFile(filename, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
		if err != nil {
			return ErrUnavailable
		}
		body, _ := json.Marshal(s.Binding())
		_, writeErr := out.Write(body)
		syncErr := out.Sync()
		closeErr := out.Close()
		if writeErr != nil || syncErr != nil || closeErr != nil || syncDirectory(s.cfg.Store) != nil {
			return ErrUnavailable
		}
		return nil
	}
	if err != nil {
		return ErrUnavailable
	}
	defer file.Close()
	var owner Binding
	decoder := json.NewDecoder(io.LimitReader(file, 1024))
	decoder.DisallowUnknownFields()
	if decoder.Decode(&owner) != nil || decoder.Decode(new(any)) != io.EOF || owner != s.Binding() {
		return ErrDenied
	}
	return nil
}
