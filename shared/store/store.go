// Package store opens one service's own database by URL (sqlite://path or
// postgres://...), applies its embedded numbered migrations under a lock and
// hands out only tenant-scoped query handles: every row carries org and owner,
// and a statement without the tenant predicate is refused before it reaches
// the database.
package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"io/fs"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	_ "github.com/jackc/pgx/v5/stdlib" // Postgres driver "pgx" (ADR-0115)
	"github.com/letya999/hermes-hub/shared/httpapi"
	"modernc.org/sqlite" // pure-Go SQLite driver keeps binaries CGO-free
)

// Dialect is the SQL flavour behind a store.
type Dialect string

const (
	SQLite   Dialect = "sqlite"
	Postgres Dialect = "postgres"
)

var (
	// ErrNotFound is the contract not_found error, so a missing or foreign
	// row crosses a service boundary unchanged.
	ErrNotFound = httpapi.ErrNotFound
	// ErrConflict is the contract conflict error for lost compare-and-swap
	// writes and refused transitions.
	ErrConflict = httpapi.ErrConflict
	// ErrUnscoped refuses a statement or tenant that could reach another
	// tenant's rows.
	ErrUnscoped = errors.New("store: statement without tenant scope")
	// ErrURL refuses an unsupported or malformed database URL. The URL is
	// never echoed: it may carry a password.
	ErrURL = errors.New("store: unsupported database URL")
	// ErrSchema refuses a database whose recorded schema is not this binary's.
	ErrSchema = errors.New("store: schema")
	// ErrMigration refuses a malformed migration set.
	ErrMigration = errors.New("store: migration set")
)

// Store is one service's database.
type Store struct {
	db      *sql.DB
	dialect Dialect
}

// Open connects to rawURL, verifies it and applies the pending migrations
// from migrations (files NNNN_name.sql numbered from 0001 without gaps; nil
// means none) in one transaction under the migration lock, so N copies
// starting together apply each migration exactly once.
func Open(ctx context.Context, rawURL string, migrations fs.FS) (*Store, error) {
	dialect, driver, dsn, path, err := parseURL(rawURL)
	if err != nil {
		return nil, err
	}
	if path != "" {
		if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
			return nil, fmt.Errorf("store: data directory: %w", err)
		}
	}
	db, err := sql.Open(driver, dsn)
	if err != nil {
		return nil, fmt.Errorf("store: open %s: %w", dialect, err)
	}
	s := &Store{db: db, dialect: dialect}
	fail := func(err error) (*Store, error) { _ = db.Close(); return nil, err }
	if err := connect(ctx, db); err != nil {
		if ctx.Err() != nil {
			return fail(ctx.Err())
		}
		// Driver parse errors may echo connection options or credentials.
		return fail(fmt.Errorf("store: connect %s: %w", dialect, httpapi.ErrUnavailable))
	}
	if path != "" {
		if err := ensurePrivate(path); err != nil {
			return fail(err)
		}
	}
	if err := s.migrate(ctx, migrations); err != nil {
		return fail(err)
	}
	return s, nil
}

func parseURL(rawURL string) (dialect Dialect, driver, dsn, path string, err error) {
	switch {
	case strings.HasPrefix(rawURL, "sqlite://"):
		path = strings.TrimPrefix(rawURL, "sqlite://")
		if path == "" || strings.ContainsAny(path, "?#") {
			return "", "", "", "", ErrURL
		}
		return SQLite, "sqlite", sqliteDSN(path, "WAL", true), filepath.FromSlash(path), nil
	case strings.HasPrefix(rawURL, "postgres://"), strings.HasPrefix(rawURL, "postgresql://"):
		if _, perr := url.Parse(rawURL); perr != nil {
			return "", "", "", "", ErrURL
		}
		return Postgres, "pgx", rawURL, "", nil
	default:
		return "", "", "", "", ErrURL
	}
}

// connect pings the database. A fresh SQLite file switching to WAL needs an
// exclusive lock that the journal-mode pragma does not wait for, so copies
// starting together retry a busy connect within the busy-timeout window.
func connect(ctx context.Context, db *sql.DB) error {
	deadline := time.Now().Add(busyTimeout)
	for {
		err := db.PingContext(ctx)
		var busy *sqlite.Error
		if err == nil || !errors.As(err, &busy) || busy.Code()&0xff != sqliteBusy || time.Now().After(deadline) {
			return err
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(20 * time.Millisecond):
		}
	}
}

const (
	busyTimeout = 10 * time.Second
	sqliteBusy  = 5
)

// sqliteDSN builds the modernc DSN. The busy timeout comes first so the
// journal-mode switch of a concurrently starting copy waits instead of failing;
// immediate transactions take the write lock at BEGIN, which serializes the
// migration check-and-apply across processes.
func sqliteDSN(path, journalMode string, immediate bool) string {
	v := url.Values{}
	if immediate {
		v.Add("_pragma", "busy_timeout("+strconv.Itoa(int(busyTimeout.Milliseconds()))+")")
	}
	v.Add("_pragma", "journal_mode("+journalMode+")")
	v.Add("_pragma", "synchronous(FULL)")
	if immediate {
		v.Add("_pragma", "foreign_keys(1)")
		v.Add("_txlock", "immediate")
	}
	return "file:" + path + "?" + v.Encode()
}

// Dialect reports the SQL flavour.
func (s *Store) Dialect() Dialect { return s.dialect }

// Close releases the connection pool.
func (s *Store) Close() error { return s.db.Close() }

// Tx runs fn in one transaction: committed when fn returns nil, rolled back
// otherwise (also on panic).
func (s *Store) Tx(ctx context.Context, fn func(*Tx) error) error {
	sqlTx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("store: begin: %w", err)
	}
	committed := false
	defer func() {
		if !committed {
			_ = sqlTx.Rollback()
		}
	}()
	if err := fn(&Tx{ctx: ctx, tx: sqlTx, dialect: s.dialect}); err != nil {
		return err
	}
	if err := sqlTx.Commit(); err != nil {
		return fmt.Errorf("store: commit: %w", err)
	}
	committed = true
	return nil
}

// ReadTx runs fn in a transaction that is read-only on Postgres: the
// sanctioned cross-tenant sweep read can never accidentally write there.
// SQLite keeps the ordinary transaction — its read already holds no lock
// another writer waits on.
func (s *Store) ReadTx(ctx context.Context, fn func(*Tx) error) error {
	sqlTx, err := s.db.BeginTx(ctx, &sql.TxOptions{ReadOnly: s.dialect == Postgres})
	if err != nil {
		return fmt.Errorf("store: begin: %w", err)
	}
	committed := false
	defer func() {
		if !committed {
			_ = sqlTx.Rollback()
		}
	}()
	if err := fn(&Tx{ctx: ctx, tx: sqlTx, dialect: s.dialect}); err != nil {
		return err
	}
	if err := sqlTx.Commit(); err != nil {
		return fmt.Errorf("store: commit: %w", err)
	}
	committed = true
	return nil
}

// Tx is one open transaction. Statements go through Scoped handles; the only
// unscoped read is DueRows, the fixed-column projection background sweeps use.
type Tx struct {
	ctx     context.Context
	tx      *sql.Tx
	dialect Dialect
}

// Dialect reports the SQL flavour of the transaction's store.
func (tx *Tx) Dialect() Dialect { return tx.dialect }

// DueRow is one expired entity row's tenant key and state. Sweepers collect
// these across tenants, then act scoped to each row's own tenant.
type DueRow struct {
	Org, Owner, ID, State string
}

// sweepRowLimit caps one DueRows batch; a sweeper loops until a batch comes
// back short instead of reading the whole table at once.
const sweepRowLimit = 1000

// DueRows returns up to limit expired rows of table (a validated identifier)
// in deadline order: fixed columns org/owner/id/state, deadline IS NOT NULL
// and deadline <= deadline. It is the only cross-tenant read the store
// exposes; every write that follows must go through Scoped with the row's
// own tenant.
func (tx *Tx) DueRows(table string, deadline int64, limit int) ([]DueRow, error) {
	if !identifier.MatchString(table) || limit <= 0 || limit > sweepRowLimit {
		return nil, fmt.Errorf("%w: sweep read arguments", ErrUnscoped)
	}
	query := "SELECT org, owner, id, state FROM " + table + " WHERE deadline IS NOT NULL AND deadline <= ? ORDER BY deadline LIMIT ?"
	bound, values, err := bind(tx.dialect, query, []any{deadline, limit}, Tenant{}, false)
	if err != nil {
		return nil, err
	}
	rows, err := tx.tx.QueryContext(tx.ctx, bound, values...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var due []DueRow
	for rows.Next() {
		var d DueRow
		if err := rows.Scan(&d.Org, &d.Owner, &d.ID, &d.State); err != nil {
			return nil, err
		}
		due = append(due, d)
	}
	return due, rows.Err()
}

// Row is the result of a single-row query; a missing row is ErrNotFound.
type Row struct {
	row *sql.Row
	err error
}

// Scan copies the row into dest.
func (r Row) Scan(dest ...any) error {
	if r.err != nil {
		return r.err
	}
	err := r.row.Scan(dest...)
	if errors.Is(err, sql.ErrNoRows) {
		return ErrNotFound
	}
	return err
}
