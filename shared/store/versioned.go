package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"os"
	"runtime"
)

// The versioned openers keep the v0 single-file stores (identity registry,
// communication spool, ToolHub store) on their PRAGMA user_version schema
// until each store moves into its service and onto Open with numbered
// migrations (M40.3+). New code uses Open.

var chmodStore = os.Chmod

// OpenVersioned creates or opens a v0 SQLite store, running migrate once
// inside one transaction when the store predates this schema version.
func OpenVersioned(path string, schema int, migrate func(*sql.Tx) error) (*sql.DB, error) {
	return openVersioned(path, schema, migrate, "WAL")
}

// OpenVersionedRollback opens the store in rollback-journal mode. The shared
// ToolHub store has writers on both sides of a Docker Desktop bind mount — a
// WAL's shared-memory index cannot propagate there, so every connection's view
// freezes at map time and blind writes physically erase foreign frames.
// The rollback journal needs no shared index: every reader sees the
// authoritative file and file locks serialize the writers.
func OpenVersionedRollback(path string, schema int, migrate func(*sql.Tx) error) (*sql.DB, error) {
	return openVersioned(path, schema, migrate, "DELETE")
}

func openVersioned(path string, schema int, migrate func(*sql.Tx) error, journalMode string) (*sql.DB, error) {
	db, err := sql.Open("sqlite", sqliteDSN(path, journalMode, false))
	if err != nil {
		return nil, err
	}
	fail := func(err error) (*sql.DB, error) { _ = db.Close(); return nil, err }
	if err := db.Ping(); err != nil {
		return fail(err)
	}
	if err := ensurePrivate(path); err != nil {
		return fail(err)
	}
	var have int
	if err := db.QueryRow("PRAGMA user_version").Scan(&have); err != nil {
		return fail(err)
	}
	if have != 0 && have != schema {
		return fail(fmt.Errorf("%w: store schema %d != %d", ErrSchema, have, schema))
	}
	if have != schema {
		tx, err := db.BeginTx(context.Background(), nil)
		if err != nil {
			return fail(err)
		}
		if migrate != nil {
			if err := migrate(tx); err != nil {
				_ = tx.Rollback()
				return fail(err)
			}
		}
		if _, err := tx.Exec(fmt.Sprintf("PRAGMA user_version=%d", schema)); err != nil {
			_ = tx.Rollback()
			return fail(err)
		}
		if err := tx.Commit(); err != nil {
			return fail(err)
		}
	}
	return db, nil
}

// ensurePrivate keeps a SQLite file owner-only (0600). A file that is already
// private passes even where chmod is unavailable; a permissive one fails
// closed.
func ensurePrivate(path string) error {
	info, err := os.Stat(path)
	if err != nil {
		return err
	}
	if info.Mode().Perm() == 0600 {
		return nil
	}
	if err := chmodStore(path, 0600); err != nil {
		return err
	}
	info, err = os.Stat(path)
	if err != nil {
		return err
	}
	if runtime.GOOS != "windows" && info.Mode().Perm() != 0600 {
		return errors.New("store: private file mode unavailable")
	}
	return nil
}
