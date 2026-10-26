//go:build !windows

package toolhub

import "os"

func syncHostingFleetDirectory(path string) error {
	dir, err := os.Open(path)
	if err != nil {
		return err
	}
	defer dir.Close()
	return dir.Sync()
}
