package api

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

func SafeStorePath(path string) error {
	if path == "" || !filepath.IsAbs(path) {
		return fmt.Errorf("%w: store path must be absolute", ErrInvalid)
	}
	abs, err := filepath.Abs(path)
	if err != nil {
		return fmt.Errorf("%w: store path: %v", ErrInvalid, err)
	}
	volume := filepath.VolumeName(abs)
	root := volume + string(filepath.Separator)
	current := root
	for _, part := range strings.Split(strings.TrimPrefix(abs, root), string(filepath.Separator)) {
		if part == "" {
			continue
		}
		current = filepath.Join(current, part)
		info, err := os.Lstat(current)
		if errors.Is(err, os.ErrNotExist) {
			continue
		}
		if err != nil {
			return fmt.Errorf("%w: store path: %v", ErrInvalid, err)
		}
		if info.Mode()&os.ModeSymlink != 0 {
			return fmt.Errorf("%w: store path contains symlink", ErrInvalid)
		}
	}
	return nil
}
