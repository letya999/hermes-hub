//go:build !windows

package stack

import "os"

func replaceRenderedFile(from, to string) error {
	return os.Rename(from, to) // #nosec G703 -- rendered paths are under the operator-owned space
}
