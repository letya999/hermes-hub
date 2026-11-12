package migration

import (
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/gofrs/flock"
	"github.com/letya999/hermes-hub/internal/toolhub"
	"github.com/letya999/hermes-hub/internal/toolhub/api"

	_ "modernc.org/sqlite"
)

// StoreSubstrateReport records a verified journal/JSON-to-SQLite migration:
// the source head, the new DB digest and where the manifest and backup live.
type StoreSubstrateReport struct {
	FromPath     string    `json:"from_path"`
	ToPath       string    `json:"to_path"`
	BackupPath   string    `json:"backup_path"`
	ManifestPath string    `json:"manifest_path"`
	SourceSHA256 string    `json:"source_sha256"`
	DBDigest     string    `json:"db_digest"`
	Collections  int       `json:"collections"`
	MigratedAt   time.Time `json:"migrated_at"`
}

// MigrateStoreSubstrate copies a ToolHub store.json into store.db. The
// source is only read; a rollback copy and a manifest are written so the
// two stores stay linked until the service is switched over. The import
// verifies byte-equivalence: the DB's stored digest covers the canonical
// snapshot, so it must equal the source file's sha256.
func MigrateStoreSubstrate(from, to string) (StoreSubstrateReport, error) {
	var report StoreSubstrateReport
	lock := flock.New(from + ".lock")
	locked, err := lock.TryLock()
	if err != nil {
		return report, err
	}
	if !locked {
		_ = lock.Close()
		return report, fmt.Errorf("%w: store writer active", api.ErrConflict)
	}
	defer func() { _ = lock.Close() }()
	source, err := os.ReadFile(from) // #nosec G304 -- operator-supplied store path
	if err != nil {
		return report, err
	}
	if _, err := os.Lstat(to); err == nil {
		return report, fmt.Errorf("%w: destination store exists", api.ErrConflict)
	} else if !os.IsNotExist(err) {
		return report, err
	}
	if err := os.MkdirAll(filepath.Dir(to), 0700); err != nil {
		return report, err
	}
	backup := from + ".substrate-backup"
	if err := writePrivateFile(backup, source); err != nil {
		return report, err
	}
	store, err := toolhub.Load(from)
	if err != nil {
		return report, err
	}
	if err := store.Save(to); err != nil {
		return report, err
	}
	if err := store.CloseDB(); err != nil {
		return report, err
	}
	migrated, err := toolhub.Load(to)
	if err != nil {
		return report, err
	}
	if err := migrated.CloseDB(); err != nil {
		return report, err
	}
	canonicalDigest, err := migrated.CanonicalSnapshotDigest()
	if err != nil {
		return report, err
	}
	db, err := sql.Open("sqlite", "file:"+to)
	if err != nil {
		return report, err
	}
	var dbDigest string
	var collections int
	err = db.QueryRow(`SELECT (SELECT COUNT(*) FROM ent) + (SELECT COUNT(*) FROM kvmap) + (SELECT COUNT(*) FROM listrec)`).Scan(&collections)
	if err == nil {
		err = db.QueryRow(`SELECT v FROM meta WHERE k = 'digest'`).Scan(&dbDigest)
	}
	if closeErr := db.Close(); err == nil {
		err = closeErr
	}
	if err != nil {
		return report, err
	}
	sourceDigest := sha256.Sum256(source)
	if hex.EncodeToString(sourceDigest[:]) != canonicalDigest {
		return report, fmt.Errorf("%w: migrated digest does not match source head", api.ErrInvalid)
	}
	report = StoreSubstrateReport{
		FromPath:     from,
		ToPath:       to,
		BackupPath:   backup,
		ManifestPath: to + ".manifest.json",
		SourceSHA256: hex.EncodeToString(sourceDigest[:]),
		DBDigest:     dbDigest,
		Collections:  collections,
		MigratedAt:   time.Now().UTC(),
	}
	body, err := json.MarshalIndent(report, "", "  ")
	if err != nil {
		return report, err
	}
	if err := writePrivateFile(report.ManifestPath, append(body, '\n')); err != nil {
		return report, err
	}
	return report, nil
}

// ActivateStoreSubstrate retires the legacy JSON store after the verified
// import: the source moves aside atomically so a stale writer meets the
// initialized-missing fence instead of forking state.
func ActivateStoreSubstrate(from, to string) (StoreSubstrateReport, error) {
	var report StoreSubstrateReport
	body, err := os.ReadFile(to + ".manifest.json") // #nosec G304 -- derived from the operator path
	if err != nil {
		return report, fmt.Errorf("%w: verified migration manifest missing", api.ErrInvalid)
	}
	if err := json.Unmarshal(body, &report); err != nil {
		return report, err
	}
	if _, err := os.Lstat(from); err != nil {
		return report, err
	}
	if err := os.Rename(from, from+".legacy"); err != nil {
		return report, err
	}
	return report, nil
}

func writePrivateFile(path string, body []byte) error {
	file, err := os.OpenFile(path, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600) // #nosec G304 -- derived from the operator path
	if err != nil {
		return err
	}
	if _, err := file.Write(body); err != nil {
		_ = file.Close()
		return err
	}
	if err := file.Sync(); err != nil {
		_ = file.Close()
		return err
	}
	return file.Close()
}
