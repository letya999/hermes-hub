package communication

import (
	"database/sql"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/letya999/hermes-hub/internal/sqlitedb"
)

// spoolFS is the narrow filesystem surface the durable spool needs. The os
// backend keeps the historical behavior; the SQLite backend absorbs record
// classes into spool.db while payloads keep landing on disk (SPEC-0090 R3).
type spoolFS interface {
	ReadFile(path string) ([]byte, error)
	WriteFile(path string, body []byte) error
	ReadDir(dir string) ([]string, error)
	Rename(from, to string) error
	Remove(path string) error
	MkdirAll(dir string) error
	Lstat(path string) (os.FileInfo, error)
	Chtimes(path string, atime, mtime time.Time) error
	Close() error
}

// spoolActivated reports the post-migration substrate: the operator marker
// retires the record classes and hands them to spool.db.
func spoolActivated(root string) bool {
	info, err := os.Lstat(filepath.Join(root, "records.activated"))
	return err == nil && info.Mode().IsRegular()
}

// osFS is the file backend.
type osFS struct{}

func (osFS) ReadFile(path string) ([]byte, error)     { return os.ReadFile(path) }
func (osFS) WriteFile(path string, body []byte) error { return atomicBytes(path, body) }
func (osFS) ReadDir(dir string) ([]string, error)     { return readDirNames(dir) }
func (osFS) Rename(from, to string) error             { return os.Rename(from, to) }
func (osFS) Remove(path string) error                 { return os.Remove(path) }
func (osFS) MkdirAll(dir string) error                { return os.MkdirAll(dir, 0700) }
func (osFS) Lstat(path string) (os.FileInfo, error)   { return os.Lstat(path) }
func (osFS) Chtimes(path string, atime, mtime time.Time) error {
	return os.Chtimes(path, atime, mtime)
}
func (osFS) Close() error { return nil }

const spoolDBSchema = 2

// openSpoolDB opens the hybrid backend: records in <root>/spool.db,
// payloads on disk.
func openSpoolDB(root string, readOnly bool) (spoolFS, error) {
	db, err := sqlitedb.Open(filepath.Join(root, "spool.db"), spoolDBSchema, func(tx *sql.Tx) error {
		_, err := tx.Exec(`CREATE TABLE IF NOT EXISTS rec (
			path TEXT PRIMARY KEY,
			body BLOB NOT NULL,
			mtime INTEGER NOT NULL
		) WITHOUT ROWID`)
		return err
	})
	if err != nil {
		return nil, err
	}
	db.SetMaxOpenConns(1)
	return &dbFS{db: db, root: root}, nil
}

// dbFS stores records as rows keyed by their slash-separated spool-relative
// path; anything the router sends to disk bypasses the table entirely.
type dbFS struct {
	db   *sql.DB
	root string
}

func (d *dbFS) key(path string) string {
	rel, err := filepath.Rel(d.root, path)
	if err != nil {
		return ""
	}
	return filepath.ToSlash(rel)
}

func notExist(path string) error {
	return &os.PathError{Op: "open", Path: path, Err: os.ErrNotExist}
}

func (d *dbFS) ReadFile(path string) ([]byte, error) {
	if !spoolRecordPath(d.root, path) {
		return os.ReadFile(path)
	}
	var body []byte
	err := d.db.QueryRow(`SELECT body FROM rec WHERE path = ?`, d.key(path)).Scan(&body)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, notExist(path)
	}
	return body, err
}

func (d *dbFS) WriteFile(path string, body []byte) error {
	if !spoolRecordPath(d.root, path) {
		if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil { // #nosec G703 -- non-record paths are caller-joined under the spool root
			return err
		}
		return atomicBytes(path, body)
	}
	_, err := d.db.Exec(`INSERT INTO rec(path, body, mtime) VALUES(?, ?, ?)
		ON CONFLICT(path) DO UPDATE SET body = excluded.body, mtime = excluded.mtime`,
		d.key(path), body, time.Now().UTC().UnixNano())
	return err
}

func (d *dbFS) ReadDir(dir string) ([]string, error) {
	rel := d.key(dir)
	if rel == "" || !spoolRecordDirs[strings.SplitN(rel, "/", 2)[0]] {
		return readDirNames(dir)
	}
	rows, err := d.db.Query(`SELECT path FROM rec WHERE path LIKE ? || '/%'`, rel)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	seen := map[string]bool{}
	names := []string{}
	for rows.Next() {
		var p string
		if err := rows.Scan(&p); err != nil {
			return nil, err
		}
		rest := strings.TrimPrefix(p, rel+"/")
		name, _, _ := strings.Cut(rest, "/")
		if !seen[name] {
			seen[name] = true
			names = append(names, name)
		}
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	// Payload files keep living under record dirs (inbound blobs, occurrence
	// sidecars): the listing is the union of rows and real entries.
	real, err := readDirNames(dir)
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return nil, err
	}
	for _, name := range real {
		if !seen[name] {
			seen[name] = true
			names = append(names, name)
		}
	}
	return names, nil
}

func (d *dbFS) Rename(from, to string) error {
	if spoolRecordPath(d.root, from) && spoolRecordPath(d.root, to) {
		_, err := d.db.Exec(`UPDATE rec SET path = ? WHERE path = ?`, d.key(to), d.key(from))
		return err
	}
	return os.Rename(from, to)
}

func (d *dbFS) Remove(path string) error {
	if !spoolRecordPath(d.root, path) {
		return os.Remove(path)
	}
	res, err := d.db.Exec(`DELETE FROM rec WHERE path = ?`, d.key(path))
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return notExist(path)
	}
	return nil
}

func (d *dbFS) MkdirAll(dir string) error { return os.MkdirAll(dir, 0700) }

// rowInfo presents a record row as the regular file callers expect; mtime
// survives renames so retention aging keeps working on rows.
type rowInfo struct {
	name string
	size int64
	mod  time.Time
}

func (i rowInfo) Name() string       { return i.name }
func (i rowInfo) Size() int64        { return i.size }
func (i rowInfo) Mode() fs.FileMode  { return 0600 }
func (i rowInfo) ModTime() time.Time { return i.mod }
func (i rowInfo) IsDir() bool        { return false }
func (i rowInfo) Sys() interface{}   { return nil }

func (d *dbFS) Lstat(path string) (os.FileInfo, error) {
	if !spoolRecordPath(d.root, path) {
		return os.Lstat(path)
	}
	var size, mtime int64
	err := d.db.QueryRow(`SELECT length(body), mtime FROM rec WHERE path = ?`, d.key(path)).Scan(&size, &mtime)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, &os.PathError{Op: "stat", Path: path, Err: os.ErrNotExist}
	}
	if err != nil {
		return nil, err
	}
	return rowInfo{name: filepath.Base(path), size: size, mod: time.Unix(0, mtime)}, nil
}

func (d *dbFS) Chtimes(path string, _ time.Time, mtime time.Time) error {
	if !spoolRecordPath(d.root, path) {
		return os.Chtimes(path, time.Now(), mtime)
	}
	res, err := d.db.Exec(`UPDATE rec SET mtime = ? WHERE path = ?`, mtime.UTC().UnixNano(), d.key(path))
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return &os.PathError{Op: "chtimes", Path: path, Err: os.ErrNotExist}
	}
	return nil
}

func (d *dbFS) Close() error { return d.db.Close() }

func readDirNames(dir string) ([]string, error) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil, err
	}
	names := make([]string, 0, len(entries))
	for _, e := range entries {
		names = append(names, e.Name())
	}
	return names, nil
}
