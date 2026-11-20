package store

import (
	"context"
	"fmt"
	"io/fs"
	"regexp"
	"slices"
	"strconv"
)

// migrationLockKey is the Postgres advisory lock held for the whole
// check-and-apply transaction (an arbitrary constant: "hubmigr" in ASCII).
const migrationLockKey = 0x6875626d696772

var migrationName = regexp.MustCompile(`^(\d{4})_[a-z0-9_]+\.sql$`)

type migration struct {
	version int
	name    string
	body    string
}

func loadMigrations(files fs.FS) ([]migration, error) {
	if files == nil {
		return nil, nil
	}
	entries, err := fs.ReadDir(files, ".")
	if err != nil {
		return nil, fmt.Errorf("%w: %v", ErrMigration, err)
	}
	var out []migration
	for _, entry := range entries {
		if entry.IsDir() {
			continue
		}
		m := migrationName.FindStringSubmatch(entry.Name())
		if m == nil {
			return nil, fmt.Errorf("%w: file %q is not NNNN_name.sql", ErrMigration, entry.Name())
		}
		version, _ := strconv.Atoi(m[1])
		body, err := fs.ReadFile(files, entry.Name())
		if err != nil {
			return nil, fmt.Errorf("%w: %v", ErrMigration, err)
		}
		out = append(out, migration{version: version, name: entry.Name(), body: string(body)})
	}
	slices.SortFunc(out, func(a, b migration) int { return a.version - b.version })
	for i, m := range out {
		if m.version != i+1 {
			return nil, fmt.Errorf("%w: expected version %04d, found %s", ErrMigration, i+1, m.name)
		}
	}
	return out, nil
}

// migrate applies every pending migration in one transaction. The lock is
// taken before schema_migrations is read: SQLite stores begin IMMEDIATE (the
// write lock, waited for under the busy timeout), Postgres takes a
// transaction-scoped advisory lock. A copy that waited sees the versions the
// first copy recorded and applies nothing.
func (s *Store) migrate(ctx context.Context, files fs.FS) error {
	migrations, err := loadMigrations(files)
	if err != nil {
		return err
	}
	return s.Tx(ctx, func(tx *Tx) error {
		if s.dialect == Postgres {
			if _, err := tx.tx.ExecContext(ctx, "SELECT pg_advisory_xact_lock($1)", int64(migrationLockKey)); err != nil {
				return fmt.Errorf("store: migration lock: %w", err)
			}
		}
		if _, err := tx.tx.ExecContext(ctx, "CREATE TABLE IF NOT EXISTS schema_migrations (version INTEGER PRIMARY KEY, name TEXT NOT NULL)"); err != nil {
			return fmt.Errorf("store: migration table: %w", err)
		}
		rows, err := tx.tx.QueryContext(ctx, "SELECT version FROM schema_migrations")
		if err != nil {
			return fmt.Errorf("store: read migrations: %w", err)
		}
		applied := map[int]bool{}
		for rows.Next() {
			var version int
			if err := rows.Scan(&version); err != nil {
				_ = rows.Close()
				return fmt.Errorf("store: read migrations: %w", err)
			}
			if version > len(migrations) {
				_ = rows.Close()
				return fmt.Errorf("%w: database at version %d, binary knows %d", ErrSchema, version, len(migrations))
			}
			applied[version] = true
		}
		if err := rows.Err(); err != nil {
			_ = rows.Close()
			return fmt.Errorf("store: read migrations: %w", err)
		}
		if err := rows.Close(); err != nil {
			return fmt.Errorf("store: read migrations: %w", err)
		}
		for _, m := range migrations {
			if applied[m.version] {
				continue
			}
			if _, err := tx.tx.ExecContext(ctx, m.body); err != nil {
				return fmt.Errorf("store: apply %s: %w", m.name, err)
			}
			bound, args, err := bind(s.dialect, "INSERT INTO schema_migrations (version, name) VALUES (?, ?)", []any{m.version, m.name}, Tenant{}, false)
			if err != nil {
				return err
			}
			if _, err := tx.tx.ExecContext(ctx, bound, args...); err != nil {
				return fmt.Errorf("store: record %s: %w", m.name, err)
			}
		}
		return nil
	})
}
