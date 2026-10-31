//go:build !windows

package audit

import "os"

func syncAuditDirectory(path string) error {
	f, err := os.Open(path) // #nosec G304 -- caller-selected absolute ledger directory.
	if err != nil {
		return err
	}
	defer f.Close()
	return f.Sync()
}
