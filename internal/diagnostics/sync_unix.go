//go:build !windows

package diagnostics

import "os"

func syncDiagnosticDirectory(path string) error {
	f, err := os.Open(path)
	if err != nil {
		return err
	}
	defer f.Close()
	return f.Sync()
}
