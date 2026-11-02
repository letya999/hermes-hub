package sqlitedb

import (
	"database/sql"
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"testing"
)

func TestOpenCreatesSchemaAndRefusesForeignVersion(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "s.db")
	db, err := Open(path, 2, func(tx *sql.Tx) error {
		_, err := tx.Exec("CREATE TABLE rec(id TEXT PRIMARY KEY)")
		return err
	})
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	var n int
	if err := db.QueryRow("SELECT count(*) FROM sqlite_master WHERE name='rec'").Scan(&n); err != nil || n != 1 {
		t.Fatalf("migration not applied: n=%d err=%v", n, err)
	}
	var v int
	if err := db.QueryRow("PRAGMA user_version").Scan(&v); err != nil || v != 2 {
		t.Fatalf("user_version=%d err=%v", v, err)
	}
	if runtime.GOOS != "windows" {
		info, err := os.Stat(path)
		if err != nil || info.Mode().Perm() != 0600 {
			t.Fatalf("mode %v err %v", info.Mode(), err)
		}
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
	// Foreign schema version refuses instead of repairing.
	if _, err := Open(path, 3, nil); !errors.Is(err, ErrSchema) {
		t.Fatalf("expected schema refusal, got %v", err)
	}
	// Same version reopens cleanly and the migration does not re-run.
	db, err = Open(path, 2, func(tx *sql.Tx) error {
		return errors.New("must not run")
	})
	if err != nil {
		t.Fatalf("reopen: %v", err)
	}
	_ = db.Close()
}

func TestOpenEnablesWALAndFullSync(t *testing.T) {
	path := filepath.Join(t.TempDir(), "s.db")
	db, err := Open(path, 1, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	var mode, sync int
	var jm string
	if err := db.QueryRow("PRAGMA journal_mode").Scan(&jm); err != nil || jm != "wal" {
		t.Fatalf("journal_mode=%q err=%v", jm, err)
	}
	if err := db.QueryRow("PRAGMA synchronous").Scan(&sync); err != nil || sync < 2 {
		t.Fatalf("synchronous=%d err=%v", sync, err)
	}
	_ = mode
}

// A failed migration rolls back: the store keeps no user_version and no
// partial table, so the next Open re-runs the migration cleanly.
func TestOpenRollsBackFailedMigration(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "s.db")
	sentinel := errors.New("boom")
	if _, err := Open(path, 1, func(tx *sql.Tx) error {
		if _, err := tx.Exec("CREATE TABLE rec(id TEXT)"); err != nil {
			return err
		}
		return sentinel
	}); !errors.Is(err, sentinel) {
		t.Fatalf("expected migration error, got %v", err)
	}
	db, err := Open(path, 1, func(tx *sql.Tx) error {
		_, err := tx.Exec("CREATE TABLE rec(id TEXT)")
		return err
	})
	if err != nil {
		t.Fatalf("reopen after rollback: %v", err)
	}
	var n int
	if err := db.QueryRow("SELECT count(*) FROM sqlite_master WHERE name='rec'").Scan(&n); err != nil || n != 1 {
		t.Fatalf("rollback left a partial migration: n=%d err=%v", n, err)
	}
	_ = db.Close()
}

// A store path inside a missing directory fails at Ping — no db leaks.
func TestOpenFailsOnUnreachablePath(t *testing.T) {
	if _, err := Open(filepath.Join(t.TempDir(), "missing", "s.db"), 1, nil); err == nil {
		t.Fatal("unreachable store path opened")
	}
}
