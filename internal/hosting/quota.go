package hosting

import (
	"io/fs"
	"strings"
)

func (s *Service) usage() (int64, int, error) {
	var total int64
	entries := 0
	err := fs.WalkDir(s.root.FS(), ".", func(p string, d fs.DirEntry, e error) error {
		if e != nil {
			return ErrUnavailable
		}
		if p == "." {
			return nil
		}
		if strings.HasPrefix(p, "audit") || p == "writer.lock" {
			return nil
		}
		info, err := d.Info()
		if err != nil || info.Mode()&fs.ModeSymlink != 0 {
			return ErrUnavailable
		}
		entries++
		if info.IsDir() {
			total += 4096
		} else if info.Mode().IsRegular() {
			total += info.Size()
		} else {
			return ErrUnavailable
		}
		return nil
	})
	return total, entries, err
}
