// Package storetest runs store-backed tests on every database backend and
// holds the tenant invariant runner every service package calls from its
// TestTenantInvariants (run first by `just test`).
package storetest

import (
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/hex"
	"errors"
	"io/fs"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/letya999/hermes-hub/shared/store"
	_ "github.com/letya999/hermes-hub/shared/store/postgres" // pgx for the HUB_TEST_POSTGRES_URL leg
)

// PostgresEnv names a Postgres URL (a database the test may create and drop
// schemas in). Unset, the Postgres leg of every store suite is skipped; the
// CI job postgres-store sets it.
const PostgresEnv = "HUB_TEST_POSTGRES_URL"

// ForEach runs fn once per backend with the URL of a fresh, empty database:
// SQLite in a temp dir always, Postgres in a throwaway schema when
// HUB_TEST_POSTGRES_URL is set.
func ForEach(t *testing.T, fn func(t *testing.T, url string)) {
	t.Helper()
	t.Run("sqlite", func(t *testing.T) { fn(t, SQLiteURL(t)) })
	t.Run("postgres", func(t *testing.T) {
		base := os.Getenv(PostgresEnv)
		if base == "" {
			t.Skip(PostgresEnv + " unset; the postgres-store CI job runs this leg")
		}
		fn(t, PostgresURL(t, base))
	})
}

// SQLiteURL is a fresh SQLite database URL in the test's temp dir.
func SQLiteURL(t *testing.T) string {
	t.Helper()
	return "sqlite://" + filepath.ToSlash(filepath.Join(t.TempDir(), "store.db"))
}

// PostgresURL creates a uniquely named schema in base, drops it after the
// test and returns base pinned to it through search_path.
func PostgresURL(t *testing.T, base string) string {
	t.Helper()
	raw := make([]byte, 8)
	if _, err := rand.Read(raw); err != nil {
		t.Fatal(err)
	}
	schema := "t_" + hex.EncodeToString(raw)
	admin, err := sql.Open("pgx", base)
	if err != nil {
		t.Fatalf("postgres: %v", err)
	}
	t.Cleanup(func() { _ = admin.Close() })
	if _, err := admin.ExecContext(context.Background(), "CREATE SCHEMA "+schema); err != nil {
		t.Fatalf("postgres: create schema: %v", err)
	}
	t.Cleanup(func() {
		_, _ = admin.ExecContext(context.Background(), "DROP SCHEMA "+schema+" CASCADE")
	})
	u, err := url.Parse(base)
	if err != nil {
		t.Fatalf("postgres: %s is not a URL", PostgresEnv)
	}
	q := u.Query()
	q.Set("search_path", schema)
	u.RawQuery = q.Encode()
	return u.String()
}

// Open opens the store at url with migrations and closes it after the test.
func Open(t *testing.T, url string, migrations fs.FS) *store.Store {
	t.Helper()
	s, err := store.Open(t.Context(), url, migrations)
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	t.Cleanup(func() { _ = s.Close() })
	return s
}

// AdminValue runs one read-only aggregate outside the tenant surface — test
// assertions on the store's own bookkeeping tables (schema_migrations,
// migration counters) that carry no tenant columns. It opens a second
// connection to the same database URL; never use it for tenant data.
func AdminValue(t *testing.T, url, query string) int {
	t.Helper()
	var db *sql.DB
	var err error
	switch {
	case strings.HasPrefix(url, "sqlite://"):
		db, err = sql.Open("sqlite", "file:"+strings.TrimPrefix(url, "sqlite://"))
	case strings.HasPrefix(url, "postgres://"), strings.HasPrefix(url, "postgresql://"):
		db, err = sql.Open("pgx", url)
	default:
		t.Fatalf("admin value: unsupported url scheme")
	}
	if err != nil {
		t.Fatalf("admin value: open: %v", err)
	}
	defer db.Close()
	var n int
	if err := db.QueryRowContext(context.Background(), query).Scan(&n); err != nil {
		t.Fatalf("admin value %q: %v", query, err)
	}
	return n
}

// TenantCase describes one tenant-owned table: how to create a row as a
// tenant, read it back by id and attempt the service's normal write on it.
type TenantCase struct {
	Table      string
	Migrations fs.FS
	Create     func(q store.Scoped) (id string, err error)
	Read       func(q store.Scoped, id string) error
	// Write performs the service's update path on id. It reports wrote=true
	// only when foreign data actually changed; a scoped statement that
	// matches no rows is a safe no-op, not a breach.
	Write func(q store.Scoped, id string) (wrote bool, err error)
}

// Owner, a colleague in the same org and a stranger in another org.
var (
	Owner     = store.Tenant{Org: "org-a", Owner: "alice"}
	Colleague = store.Tenant{Org: "org-a", Owner: "bob"}
	Stranger  = store.Tenant{Org: "org-b", Owner: "alice"}
)

// TenantInvariants checks every table on a fresh store per table and per
// backend: the owner reads its row back; the same id read or written as a
// colleague or a stranger is not_found (reads) or rejected/a no-op
// (writes), never data and never another error class; and an org/owner
// move through a scoped UPDATE is refused before it reaches the database.
func TenantInvariants(t *testing.T, cases ...TenantCase) {
	t.Helper()
	if len(cases) == 0 {
		t.Fatal("tenant invariants: no tables")
	}
	for _, c := range cases {
		if c.Write == nil {
			t.Fatalf("tenant invariants for %s: Write is required — foreign records and tenant moves need a verdict", c.Table)
		}
		t.Run(c.Table, func(t *testing.T) {
			ForEach(t, func(t *testing.T, url string) {
				s := Open(t, url, c.Migrations)
				var id string
				err := s.Tx(t.Context(), func(tx *store.Tx) error {
					var err error
					id, err = c.Create(tx.Scoped(Owner))
					return err
				})
				if err != nil {
					t.Fatalf("create as owner: %v", err)
				}
				read := func(tenant store.Tenant) error {
					return s.Tx(t.Context(), func(tx *store.Tx) error { return c.Read(tx.Scoped(tenant), id) })
				}
				if err := read(Owner); err != nil {
					t.Fatalf("owner read: %v", err)
				}
				for name, tenant := range map[string]store.Tenant{"colleague": Colleague, "stranger": Stranger} {
					if err := read(tenant); !errors.Is(err, store.ErrNotFound) {
						t.Errorf("%s read of %s/%s: want not_found, got %v", name, c.Table, id, err)
					}
					var wrote bool
					err := s.Tx(t.Context(), func(tx *store.Tx) error {
						var werr error
						wrote, werr = c.Write(tx.Scoped(tenant), id)
						return werr
					})
					if err == nil && wrote {
						t.Errorf("%s write of %s/%s changed foreign data", name, c.Table, id)
					}
					// A scoped UPDATE that assigns org or owner is a tenant
					// move; the binder must refuse it outright.
					err = s.Tx(t.Context(), func(tx *store.Tx) error {
						_, err := tx.Scoped(tenant).Exec("UPDATE "+c.Table+" SET org = ?, owner = ? WHERE {tenant} AND id = ?", "org-b", "bob", id)
						return err
					})
					if !errors.Is(err, store.ErrUnscoped) {
						t.Errorf("%s tenant move on %s/%s: want unscoped, got %v", name, c.Table, id, err)
					}
				}
				// Foreign attempts left the owner's row intact.
				if err := read(Owner); err != nil {
					t.Fatalf("owner read after foreign writes: %v", err)
				}
			})
		})
	}
}
