//go:build !windows

package communication

import "os"

func syncSpoolDirectory(path string) error {
	f, err := os.Open(path) // #nosec G703 -- spool dir path from filepath.Dir of an owned spool file
	if err != nil {
		return err
	}
	defer f.Close()
	return f.Sync()
}
