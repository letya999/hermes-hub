//go:build !linux

package stack

import (
	"errors"
	"os"
	"path/filepath"
)

// Linux Docker performs the ownership check; non-Linux renderers still parse
// and validate the reviewed config and referenced files at their own access.
func runtimeConnectionReadable(root, relative string, _, _ int) error {
	info, err := os.Lstat(filepath.Join(root, filepath.FromSlash(relative)))
	if err != nil {
		return err
	}
	if !info.Mode().IsRegular() {
		return errors.New("runtime connection file is not regular")
	}
	return nil
}
