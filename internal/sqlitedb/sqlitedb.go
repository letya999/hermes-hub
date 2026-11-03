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

	_ "modernc.org/sqlite"
)

// ErrSchema refuses stores whose recorded schema differs from this binary's.
var ErrSchema = errors.New("sqlitedb: schema")

// Open creates or opens a service DB, running migrate once inside one
// transaction when the store predates this schema version.
func Open(path string, schema int, migrate func(*sql.Tx) error) (*sql.DB, error) {
	v := url.Values{}
	v.Add("_pragma", "journal_mode(WAL)")
	v.Add("_pragma", "synchronous(FULL)")
	db, err := sql.Open("sqlite", "file:"+path+"?"+v.Encode())
	if err != nil {
		return nil, err
	}
	fail := func(err error) (*sql.DB, error) { _ = db.Close(); return nil, err }
	if err := db.Ping(); err != nil {
		return fail(err)
	}
	if err := os.Chmod(path, 0600); err != nil {
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
