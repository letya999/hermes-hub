//go:build !linux || !cgo

package journal

import (
	"database/sql"
	"errors"
	"os"
	"path/filepath"
)

// sqlStore is a placeholder on builds without the SQLCipher engine. The
// activation marker is never produced there; if one appears anyway the open
// path refuses rather than silently serving the legacy frames.
type sqlStore struct {
	db         *sql.DB
	replicated bool
	recLock    *os.File
}

// appendSQL is unreachable on non-CGO builds: an activated marker can only be
// produced by a cipher build, and the dispatch above refuses one it finds.
func (j *Journal) appendSQL(r Record, plain []byte, aux *AuxWrites) (uint64, error) {
	return 0, errNoCipher
}

var errNoCipher = errors.New("sqlcipher engine unavailable")

func sqlDispatchReplicated(primaryDir, recoveryDir string, anchor *recoveryAnchor, primaryKey, recoveryKey []byte, limit int64) (*Journal, bool, error) {
	for _, dir := range []string{primaryDir, recoveryDir} {
		for _, name := range []string{sqlMarkerFile, sqlRecoveryDB, sqlPrimaryDB, sqlSingleDB} {
			if sqlFileExists(filepath.Join(dir, name)) {
				return nil, true, ErrUnavailable
			}
		}
	}
	return nil, false, nil
}

func sqlDispatchSingle(dir string, key []byte, limit int64) (*Journal, bool, error) {
	for _, name := range []string{sqlMarkerFile, sqlSingleDB} {
		if sqlFileExists(filepath.Join(dir, name)) {
			return nil, true, ErrUnavailable
		}
	}
	return nil, false, nil
}

type SQLCipherManifest struct {
	Format       int    `json:"format"`
	Records      uint64 `json:"records"`
	HeadSequence uint64 `json:"head_sequence"`
	HeadDigest   string `json:"head_digest"`
	Store        string `json:"store"`
	StoreBytes   int64  `json:"store_bytes"`
}

func SQLCipherVersion() (string, error) {
	return "", errNoCipher
}

func (j *Journal) sqlAuditBytes() int64 { return 0 }

func (j *Journal) AuditFloor() (uint64, error) {
	return 0, errNoCipher
}

func (j *Journal) RetainAudits() (RetainReport, error) {
	return RetainReport{}, errNoCipher
}

func ReadAuditArchive(_, _ string, _ []byte) ([]AuditRow, error) {
	return nil, errNoCipher
}

func ImportSQLCipher(primaryDir, recoveryDir, anchorDir string, primaryKey, recoveryKey []byte, limit int64) (*SQLCipherManifest, error) {
	return nil, errNoCipher
}

func ActivateSQLCipher(primaryDir, recoveryDir, anchorDir string, primaryKey, recoveryKey []byte) error {
	return errNoCipher
}

func ImportSQLCipherSingle(dir string, key []byte, limit int64) error {
	return errNoCipher
}

func ActivateSQLCipherSingle(dir string, key []byte) error {
	return errNoCipher
}

func (j *Journal) sqlNormalized() bool { return false }

func (j *Journal) sqlRecords() []Record { return nil }

func (j *Journal) StateRows() ([]StateRow, error) { return nil, ErrUnavailable }

func (j *Journal) AuditPage(after uint64, limit int, scope AuditScope) ([]AuditRow, error) {
	return nil, ErrUnavailable
}

func NormalizeSQLCipher(primaryDir, recoveryDir, anchorDir string, primaryKey, recoveryKey []byte, limit int64, norm NormalizeFunc) error {
	return errNoCipher
}

func NormalizeSQLCipherSingle(dir string, key []byte, limit int64, norm NormalizeFunc) error {
	return errNoCipher
}
