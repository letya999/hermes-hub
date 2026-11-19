package commops

import (
	"database/sql"
	"encoding/json"
	"errors"
	"io/fs"
	"net/url"
	"os"
	"path/filepath"
	"strings"

	_ "modernc.org/sqlite"
)

// LegacySpoolReader reads the retired gateway substrate without mutation.
type LegacySpoolReader struct{ Root string }

// The gateway may already have moved its records into spool.db. Keep its
// current substrate authoritative; substrate-backup is never a read source.
func (b LegacySpoolReader) Database() (*sql.DB, error) {
	marker, err := os.Lstat(filepath.Join(b.Root, "records.activated"))
	if os.IsNotExist(err) {
		return nil, nil
	}
	if err != nil || !marker.Mode().IsRegular() {
		return nil, errors.New("automation: unsafe spool activation marker")
	}
	path := filepath.Join(b.Root, "spool.db")
	info, err := os.Lstat(path)
	if err != nil || !info.Mode().IsRegular() {
		return nil, errors.New("automation: activated spool database unavailable")
	}
	uriPath := filepath.ToSlash(path)
	if filepath.VolumeName(path) != "" {
		uriPath = "/" + uriPath
	}
	u := url.URL{Scheme: "file", Path: uriPath, RawQuery: "mode=ro&_pragma=query_only(1)&_pragma=busy_timeout(5000)"}
	db, err := sql.Open("sqlite", u.String())
	if err != nil {
		return nil, err
	}
	db.SetMaxOpenConns(1)
	var version int
	if err := db.QueryRow("PRAGMA user_version").Scan(&version); err != nil || version != 2 {
		_ = db.Close()
		return nil, errors.New("automation: unsupported spool database schema")
	}
	return db, nil
}

func (b LegacySpoolReader) Entries(path string) ([]string, error) {
	db, err := b.Database()
	if err != nil {
		return nil, err
	}
	if db == nil {
		entries, err := os.ReadDir(path)
		if err != nil {
			return nil, err
		}
		names := make([]string, 0, len(entries))
		for _, entry := range entries {
			names = append(names, entry.Name())
		}
		return names, nil
	}
	defer db.Close()
	rel, err := filepath.Rel(b.Root, path)
	if err != nil || !filepath.IsLocal(rel) {
		return nil, errors.New("automation: invalid spool directory")
	}
	prefix := filepath.ToSlash(rel) + "/"
	rows, err := db.Query("SELECT path FROM rec WHERE substr(path, 1, ?) = ? ORDER BY path", len(prefix), prefix)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var names []string
	seen := map[string]bool{}
	for rows.Next() {
		var key string
		if err := rows.Scan(&key); err != nil {
			return nil, err
		}
		name := strings.TrimPrefix(key, prefix)
		name, _, _ = strings.Cut(name, "/")
		if name != "" && !seen[name] {
			names = append(names, name)
			seen[name] = true
		}
	}
	return names, rows.Err()
}

func (b LegacySpoolReader) ReadJSON(path string, out any) error {
	db, err := b.Database()
	if err != nil {
		return err
	}
	if db == nil {
		info, err := os.Lstat(path)
		if err != nil {
			return err
		}
		if !info.Mode().IsRegular() || info.Size() > 1<<20 {
			return errors.New("automation: unsafe spool record")
		}
		body, err := os.ReadFile(path) // #nosec G703 -- bounded local read-only spool
		if err != nil {
			return err
		}
		return json.Unmarshal(body, out)
	}
	defer db.Close()
	rel, err := filepath.Rel(b.Root, path)
	if err != nil || !filepath.IsLocal(rel) {
		return errors.New("automation: invalid spool record path")
	}
	var body []byte
	err = db.QueryRow("SELECT body FROM rec WHERE path = ? AND length(body) <= ?", filepath.ToSlash(rel), 1<<20).Scan(&body)
	if errors.Is(err, sql.ErrNoRows) {
		return &os.PathError{Op: "read", Path: path, Err: fs.ErrNotExist}
	}
	if err != nil {
		return err
	}
	return json.Unmarshal(body, out)
}
