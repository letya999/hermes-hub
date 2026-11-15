//go:build !windows

package api

import "os"

func replaceGovernanceFile(from, to string) error {
	return os.Rename(from, to) // #nosec G703 -- validated policy path and its same-directory temporary file
}
