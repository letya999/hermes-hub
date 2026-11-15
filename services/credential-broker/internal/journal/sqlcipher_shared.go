package journal

import (
	"os"
	"path/filepath"
)

const (
	sqlMarkerFile = "sqlcipher.activated"
	sqlRecoveryDB = "recovery.db"
	sqlPrimaryDB  = "primary.db"
	sqlSingleDB   = "ledger.db"
)

func sqlFileExists(path string) bool {
	_, err := os.Lstat(path)
	return err == nil
}

func sqlActivated(dir string) bool {
	return sqlFileExists(filepath.Join(dir, sqlMarkerFile))
}
