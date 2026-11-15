// Package sqlitedb opens per-service embedded SQLite stores for the root
// control plane (ADR-0091). Pure-Go driver keeps root binaries CGO-free;
// SQLCipher stays confined to the credential-broker module (ADR-0087 D1).
package sqlitedb

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"net/url"
	"os"
	"runtime"

	_ "modernc.org/sqlite"
)

// ErrSchema refuses stores whose recorded schema differs from this binary's.
var ErrSchema = errors.New("sqlitedb: schema")

var chmodStore = os.Chmod

// Open creates or opens a service DB, running migrate once inside one
// transaction when the store predates this schema version.
func Open(path string, schema int, migrate func(*sql.Tx) error) (*sql.DB, error) {
	return open(path, schema, migrate, "WAL")
}

// OpenRollback opens the store in rollback-journal mode. The shared ToolHub
// store has writers on both sides of a Docker Desktop bind mount — a WAL's
// shared-memory index cannot propagate there, so every connection's view
// freezes at map time and blind writes physically erase foreign frames.
// The rollback journal needs no shared index: every reader sees the
// authoritative file and file locks serialize the writers.
func OpenRollback(path string, schema int, migrate func(*sql.Tx) error) (*sql.DB, error) {
	return open(path, schema, migrate, "DELETE")
}

func open(path string, schema int, migrate func(*sql.Tx) error, journalMode string) (*sql.DB, error) {
	v := url.Values{}
	v.Add("_pragma", "journal_mode("+journalMode+")")
	v.Add("_pragma", "synchronous(FULL)")
	db, err := sql.Open("sqlite", "file:"+path+"?"+v.Encode())
	if err != nil {
		return nil, err
	}
	fail := func(err error) (*sql.DB, error) { _ = db.Close(); return nil, err }
	if err := db.Ping(); err != nil {
		return fail(err)
	}
	info, err := os.Stat(path)
	if err != nil {
		return fail(err)
	}
	if info.Mode().Perm() != 0600 {
		if err := chmodStore(path, 0600); err != nil {
			return fail(err)
		}
		info, err = os.Stat(path)
		if err != nil {
			return fail(err)
		}
		if runtime.GOOS != "windows" && info.Mode().Perm() != 0600 {
			return fail(errors.New("sqlitedb: private file mode unavailable"))
		}
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
