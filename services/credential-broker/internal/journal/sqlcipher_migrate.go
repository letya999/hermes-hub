//go:build linux && cgo

package journal

import (
	"bytes"
	"crypto/rand"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"

	_ "github.com/mattn/go-sqlite3"

	"github.com/letya999/credential-broker/internal/seal"
	"github.com/letya999/credential-broker/internal/securefs"
	"github.com/letya999/credential-broker/internal/strictjson"
)

func sqlKeyPass(key []byte, purpose string) string {
	return hex.EncodeToString(seal.Derive(key, purpose))
}

func sqlQuote(s string) (string, error) {
	for _, c := range s {
		if c != '_' && c != '-' && c != '.' && c != '/' && (c < '0' || c > '9') && (c < 'a' || c > 'z') && (c < 'A' || c > 'Z') {
			return "", errors.New("unsafe sql literal")
		}
	}
	return "'" + s + "'", nil
}

// sqlOpenDB opens the session connection. mattn's Open() unconditionally
// runs PRAGMA synchronous, and any pager-level pragma fails on an encrypted
// file before a key could ever be set — so the connection's own schema is
// :memory: and every real database file comes in through ATTACH ... KEY,
// which arms the codec for that file at attach time.
func sqlOpenDB() (*sql.DB, error) {
	db, err := sql.Open("sqlite3", ":memory:")
	if err != nil {
		return nil, err
	}
	db.SetMaxOpenConns(1)
	var cipher string
	if err := db.QueryRow("PRAGMA cipher_version").Scan(&cipher); err != nil || cipher == "" {
		_ = db.Close()
		return nil, errNoCipher
	}
	if _, err := db.Exec("PRAGMA busy_timeout=5000"); err != nil {
		_ = db.Close()
		return nil, ErrUnavailable
	}
	return db, nil
}

func sqlMustExec(db *sql.DB, query string) error {
	if _, err := db.Exec(query); err != nil {
		return ErrUnavailable
	}
	return nil
}

func sqlAttach(db *sql.DB, alias, path, pass string) error {
	a, err := sqlQuote(alias)
	if err != nil {
		return err
	}
	p, err := sqlQuote(path)
	if err != nil {
		return err
	}
	k, err := sqlQuote(pass)
	if err != nil {
		return err
	}
	if err := sqlMustExec(db, "ATTACH DATABASE "+p+" AS "+a+" KEY "+k); err != nil {
		return err
	}
	if err := sqlMustExec(db, "PRAGMA "+a+".journal_mode=DELETE"); err != nil {
		return err
	}
	return sqlMustExec(db, "PRAGMA "+a+".synchronous=EXTRA")
}

// sqlReplay streams and verifies the record chain. A rotated store starts at
// its floor: the first retained row must be the floor sequence and must carry
// the floor_prev hash — the sealed link into archived history.
func sqlReplay(db *sql.DB, schema string) ([]Record, string, int64, uint64, error) {
	floor, floorPrev := sqlRecordsFloor(db, schema)
	// #nosec G202 -- schema is a fixed "r"/"p" literal from call sites, never
	// user input; the whitelisted sqlAttach aliases enforce it.
	rows, err := db.Query("SELECT seq, prev, kind, body FROM " + schema + ".records ORDER BY seq")
	if err != nil {
		return nil, "", 0, 0, ErrUnavailable
	}
	defer rows.Close()
	var out []Record
	prev := floorPrev
	var size int64
	expect := floor
	for rows.Next() {
		var seq uint64
		var p, kind string
		var body []byte
		if err := rows.Scan(&seq, &p, &kind, &body); err != nil {
			return nil, "", 0, 0, ErrUnavailable
		}
		if seq != expect || p != prev || kind == "" || len(body) == 0 || len(body) > MaxRecord {
			return nil, "", 0, 0, ErrUnavailable
		}
		if !strictjson.Valid(body) {
			return nil, "", 0, 0, ErrUnavailable
		}
		plain, err := json.Marshal(Record{Sequence: seq, Previous: p, Kind: kind, Data: json.RawMessage(body)})
		if err != nil {
			return nil, "", 0, 0, err
		}
		sum := sha256.Sum256(plain)
		prev = hex.EncodeToString(sum[:])
		out = append(out, Record{Sequence: seq, Previous: p, Kind: kind, Data: append(json.RawMessage(nil), body...)})
		size += int64(len(body)) + 64
		expect++
	}
	if err := rows.Err(); err != nil {
		return nil, "", 0, 0, ErrUnavailable
	}
	return out, prev, size, floor, nil
}

func sqlEqual(a, b []Record) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i].Sequence != b[i].Sequence || a[i].Previous != b[i].Previous || a[i].Kind != b[i].Kind || !bytes.Equal(a[i].Data, b[i].Data) {
			return false
		}
	}
	return true
}

func sqlSealReplace(dir, name string, data []byte) error {
	var nonce [16]byte
	if _, err := rand.Read(nonce[:]); err != nil {
		return err
	}
	temp := "sql-" + hex.EncodeToString(nonce[:]) + ".tmp"
	if err := securefs.Create(dir, temp, data, 0600); err != nil {
		return err
	}
	defer func() { _ = os.Remove(filepath.Join(dir, temp)) }()
	if err := os.Rename(filepath.Join(dir, temp), filepath.Join(dir, name)); err != nil {
		return err
	}
	return securefs.SyncDir(dir)
}

// ImportSQLCipher copies a quiesced, mutually consistent legacy triad into
// fresh SQLCipher databases and seals a manifest that the activation gate
// later verifies. Legacy frames are never modified; a live writer holds the
// ledger locks and makes the leg opens fail closed.
func ImportSQLCipher(primaryDir, recoveryDir, anchorDir string, primaryKey, recoveryKey []byte, limit int64) (*SQLCipherManifest, error) {
	if v, err := SQLCipherVersion(); err != nil || v == "" {
		return nil, errNoCipher
	}
	if sqlFileExists(filepath.Join(primaryDir, sqlPrimaryDB)) || sqlFileExists(filepath.Join(recoveryDir, sqlRecoveryDB)) || sqlActivated(primaryDir) || sqlActivated(recoveryDir) {
		return nil, ErrUnavailable
	}
	if err := securefs.Dir(anchorDir); err != nil {
		return nil, err
	}
	anchor, err := newAnchor(anchorDir, seal.Derive(primaryKey, "journal-store-key/v2"), recoveryKey)
	if err != nil {
		return nil, err
	}
	recovery, err := openJournal(recoveryDir, recoveryKey, limit, false)
	if err != nil {
		return nil, err
	}
	defer recovery.Close()
	primary, err := openJournal(primaryDir, primaryKey, limit, false)
	if err != nil {
		return nil, err
	}
	defer primary.Close()
	marker, markerErr := anchor.marker()
	if os.IsNotExist(markerErr) && primary.seq == 0 && recovery.seq == 0 {
		entries, err := os.ReadDir(anchorDir)
		if err != nil || len(entries) != 0 {
			return nil, ErrUnavailable
		}
		if err = anchor.writeHead(0, ""); err == nil {
			err = anchor.markInitialized()
		}
		if err != nil {
			return nil, ErrUnavailable
		}
		marker, markerErr = anchor.marker()
	}
	if markerErr != nil || marker != "v2:"+anchor.store {
		return nil, ErrUnavailable
	}
	latest, _, err := anchor.current()
	if err != nil || latest != primary.seq || latest != recovery.seq {
		return nil, ErrUnavailable
	}
	if !sqlEqual(primary.records, recovery.records) {
		return nil, ErrUnavailable
	}
	db, err := sqlOpenDB()
	if err != nil {
		return nil, err
	}
	fail := func(err error) (*SQLCipherManifest, error) { _ = db.Close(); return nil, err }
	if err := sqlAttach(db, "r", filepath.Join(recoveryDir, sqlRecoveryDB), sqlKeyPass(recoveryKey, "sqlcipher-recovery/v1")); err != nil {
		return fail(err)
	}
	if err := sqlAttach(db, "p", filepath.Join(primaryDir, sqlPrimaryDB), sqlKeyPass(primaryKey, "sqlcipher-ledger/v1")); err != nil {
		return fail(err)
	}
	tx, err := db.Begin()
	if err != nil {
		return fail(ErrUnavailable)
	}
	for _, schema := range []string{"r", "p"} {
		if err := sqlEnsureSchema(tx, schema); err != nil {
			_ = tx.Rollback()
			return fail(ErrUnavailable)
		}
	}
	for _, r := range primary.records {
		for _, schema := range []string{"r", "p"} {
			// #nosec G202 -- schema is a fixed "r"/"p" literal; row values stay
			// parameterized.
			if _, err := tx.Exec("INSERT INTO "+schema+".records(seq,prev,kind,body) VALUES(?,?,?,?)", r.Sequence, r.Previous, r.Kind, []byte(r.Data)); err != nil {
				_ = tx.Rollback()
				return fail(ErrUnavailable)
			}
		}
	}
	if err := tx.Commit(); err != nil {
		return fail(ErrUnavailable)
	}
	for _, check := range []struct {
		schema string
		want   []Record
	}{{"r", recovery.records}, {"p", primary.records}} {
		got, _, _, _, err := sqlReplay(db, check.schema)
		if err != nil || !sqlEqual(got, check.want) {
			return fail(ErrUnavailable)
		}
	}
	if err := db.Close(); err != nil {
		return nil, err
	}
	manifest := &SQLCipherManifest{Format: 1, Records: primary.seq, HeadSequence: primary.seq, HeadDigest: primary.prev, Store: anchor.store, StoreBytes: limit}
	plain, err := json.Marshal(manifest)
	if err != nil {
		return nil, err
	}
	enc, err := anchor.box.Seal(plain, []byte("sqlcipher-manifest/"+anchor.store))
	if err != nil {
		return nil, err
	}
	for _, dir := range []string{primaryDir, recoveryDir} {
		if err := sqlSealReplace(dir, "sqlcipher.manifest", enc); err != nil {
			return nil, err
		}
	}
	return manifest, nil
}

// ActivateSQLCipher flips the store onto the imported databases: it opens
// them through the full replicated path (which verifies the witness head and
// chain), writes the activation markers and retires the framed ledgers into
// substrate-backup.
func ActivateSQLCipher(primaryDir, recoveryDir, anchorDir string, primaryKey, recoveryKey []byte) error {
	anchor, err := newAnchor(anchorDir, seal.Derive(primaryKey, "journal-store-key/v2"), recoveryKey)
	if err != nil {
		return err
	}
	enc, err := securefs.Read(primaryDir, "sqlcipher.manifest", 4096)
	if err != nil {
		return ErrUnavailable
	}
	plain, err := anchor.box.Open(enc, []byte("sqlcipher-manifest/"+anchor.store))
	if err != nil {
		return ErrUnavailable
	}
	var manifest SQLCipherManifest
	if strictjson.Decode(plain, &manifest) != nil || manifest.Format != 1 {
		return ErrUnavailable
	}
	activated, err := openSQLReplicated(primaryDir, recoveryDir, anchor, primaryKey, recoveryKey, manifest.StoreBytes)
	if err != nil {
		return err
	}
	if err := activated.Close(); err != nil {
		return err
	}
	for _, dir := range []string{primaryDir, recoveryDir} {
		if err := sqlSealReplace(dir, sqlMarkerFile, []byte("v1")); err != nil {
			return err
		}
	}
	for _, dir := range []string{primaryDir, recoveryDir} {
		backup := filepath.Join(dir, "substrate-backup")
		if err := securefs.Dir(backup); err != nil {
			return err
		}
		if err := os.Rename(filepath.Join(dir, "ledger.bin"), filepath.Join(backup, "ledger.bin")); err != nil {
			return err
		}
		if err := securefs.SyncDir(dir); err != nil {
			return err
		}
		if err := securefs.SyncDir(backup); err != nil {
			return err
		}
	}
	return nil
}

// ImportSQLCipherSingle copies a quiesced single ledger into a fresh
// ledger.db beside it; activation retires the frames into substrate-backup.
// A live writer holds the ledger lock and makes the source open fail closed.
func ImportSQLCipherSingle(dir string, key []byte, limit int64) error {
	if v, err := SQLCipherVersion(); err != nil || v == "" {
		return errNoCipher
	}
	if sqlFileExists(filepath.Join(dir, sqlSingleDB)) || sqlActivated(dir) {
		return ErrUnavailable
	}
	j, err := openJournal(dir, key, limit, false)
	if err != nil {
		return err
	}
	defer j.Close()
	db, err := sqlOpenDB()
	if err != nil {
		return err
	}
	fail := func(err error) error { _ = db.Close(); return err }
	if err := sqlAttach(db, "r", filepath.Join(dir, sqlSingleDB), sqlKeyPass(key, "sqlcipher-ledger/v1")); err != nil {
		return fail(err)
	}
	tx, err := db.Begin()
	if err != nil {
		return fail(ErrUnavailable)
	}
	if err := sqlEnsureSchema(tx, "r"); err != nil {
		_ = tx.Rollback()
		return fail(ErrUnavailable)
	}
	for _, r := range j.records {
		if _, err := tx.Exec("INSERT INTO r.records(seq,prev,kind,body) VALUES(?,?,?,?)", r.Sequence, r.Previous, r.Kind, []byte(r.Data)); err != nil {
			_ = tx.Rollback()
			return fail(ErrUnavailable)
		}
	}
	if err := tx.Commit(); err != nil {
		return fail(ErrUnavailable)
	}
	got, _, _, _, err := sqlReplay(db, "r")
	if err != nil || !sqlEqual(got, j.records) {
		return fail(ErrUnavailable)
	}
	return db.Close()
}

// ActivateSQLCipherSingle verifies the imported ledger.db against the live
// legacy journal, then writes the activation marker and retires the frames.
func ActivateSQLCipherSingle(dir string, key []byte) error {
	j, err := openJournal(dir, key, 1<<30, false)
	if err != nil {
		return err
	}
	defer j.Close()
	db, err := sqlOpenDB()
	if err != nil {
		return err
	}
	fail := func(err error) error { _ = db.Close(); return err }
	if err := sqlAttach(db, "r", filepath.Join(dir, sqlSingleDB), sqlKeyPass(key, "sqlcipher-ledger/v1")); err != nil {
		return fail(err)
	}
	got, _, _, _, err := sqlReplay(db, "r")
	if err != nil || !sqlEqual(got, j.records) {
		return fail(ErrUnavailable)
	}
	if err := db.Close(); err != nil {
		return err
	}
	if err := sqlSealReplace(dir, sqlMarkerFile, []byte("v1")); err != nil {
		return err
	}
	backup := filepath.Join(dir, "substrate-backup")
	if err := securefs.Dir(backup); err != nil {
		return err
	}
	if err := os.Rename(filepath.Join(dir, "ledger.bin"), filepath.Join(backup, "ledger.bin")); err != nil {
		return err
	}
	if err := securefs.SyncDir(dir); err != nil {
		return err
	}
	return securefs.SyncDir(backup)
}
