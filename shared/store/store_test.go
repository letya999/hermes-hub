package store_test

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"sync"
	"testing"
	"testing/fstest"

	"github.com/letya999/hermes-hub/shared/store"
	"github.com/letya999/hermes-hub/shared/store/storetest"
)

var itemsSchema = fstest.MapFS{
	"0001_items.sql": {Data: []byte(`CREATE TABLE items (org TEXT NOT NULL, owner TEXT NOT NULL, id TEXT NOT NULL, v TEXT NOT NULL, PRIMARY KEY (org, owner, id));`)},
}

var alice = store.Tenant{Org: "org-a", Owner: "alice"}

func insertItem(t *testing.T, s *store.Store, tenant store.Tenant, id, v string) {
	t.Helper()
	err := s.Tx(t.Context(), func(tx *store.Tx) error {
		return tx.Scoped(tenant).Insert("items", []string{"id", "v"}, id, v)
	})
	if err != nil {
		t.Fatalf("insert %s: %v", id, err)
	}
}

func readItem(s *store.Store, tenant store.Tenant, id string) (string, error) {
	var v string
	err := s.Tx(context.Background(), func(tx *store.Tx) error {
		return tx.Scoped(tenant).QueryRow("SELECT v FROM items WHERE {tenant} AND id = ?", id).Scan(&v)
	})
	return v, err
}

func TestStoreRefusesUnscopedQuery(t *testing.T) {
	storetest.ForEach(t, func(t *testing.T, url string) {
		s := storetest.Open(t, url, itemsSchema)
		insertItem(t, s, alice, "i1", "one")
		insertItem(t, s, store.Tenant{Org: "org-b", Owner: "bob"}, "i1", "foreign")
		refused := map[string]func(store.Scoped) error{
			"select without marker": func(q store.Scoped) error {
				var v string
				return q.QueryRow("SELECT v FROM items WHERE id = ?", "i1").Scan(&v)
			},
			"update without marker": func(q store.Scoped) error {
				_, err := q.Exec("UPDATE items SET v = ? WHERE id = ?", "hijacked", "i1")
				return err
			},
			"delete without marker": func(q store.Scoped) error {
				_, err := q.Exec("DELETE FROM items")
				return err
			},
			"or widens the tenant predicate": func(q store.Scoped) error {
				_, err := q.Exec("UPDATE items SET v = ? WHERE {tenant} OR id = ?", "hijacked", "i1")
				return err
			},
			"query rows without marker": func(q store.Scoped) error {
				rows, err := q.Query("SELECT v FROM items")
				if err == nil {
					_ = rows.Close()
				}
				return err
			},
		}

		for name, query := range map[string]string{
			"marker in a literal":         "SELECT '{tenant}' FROM items",
			"marker in a comment":         "SELECT v FROM items WHERE 1=1 /* {tenant} */",
			"union escapes scope":         "SELECT v FROM items WHERE {tenant} UNION SELECT v FROM items",
			"negated scope":               "SELECT v FROM items WHERE NOT {tenant}",
			"nested select escapes scope": "SELECT (SELECT v FROM items LIMIT 1) FROM items WHERE {tenant}",
		} {
			refused[name] = func(q store.Scoped) error { var v string; return q.QueryRow(query).Scan(&v) }
		}
		for name, call := range refused {
			err := s.Tx(t.Context(), func(tx *store.Tx) error { return call(tx.Scoped(alice)) })
			if !errors.Is(err, store.ErrUnscoped) {
				t.Errorf("%s: want ErrUnscoped, got %v", name, err)
			}
		}
		for _, tenant := range []store.Tenant{{Org: "org-a"}, {Owner: "alice"}, {}} {
			if _, err := readItem(s, tenant, "i1"); !errors.Is(err, store.ErrUnscoped) {
				t.Errorf("incomplete tenant %+v: want ErrUnscoped, got %v", tenant, err)
			}
			err := s.Tx(t.Context(), func(tx *store.Tx) error {
				return tx.Scoped(tenant).Insert("items", []string{"id", "v"}, "i2", "x")
			})
			if !errors.Is(err, store.ErrUnscoped) {
				t.Errorf("insert with incomplete tenant %+v: want ErrUnscoped, got %v", tenant, err)
			}
		}
		// Nothing above reached the database: both tenants keep their rows.
		if v, err := readItem(s, alice, "i1"); err != nil || v != "one" {
			t.Fatalf("own row after refusals: %q %v", v, err)
		}
		if v, err := readItem(s, store.Tenant{Org: "org-b", Owner: "bob"}, "i1"); err != nil || v != "foreign" {
			t.Fatalf("foreign row after refusals: %q %v", v, err)
		}
		if _, err := readItem(s, store.Tenant{Org: "org-a", Owner: "bob"}, "i1"); !errors.Is(err, store.ErrNotFound) {
			t.Fatalf("other owner in same org: want not_found, got %v", err)
		}
	})
}

func TestScopedQueryBindsTenantAndArguments(t *testing.T) {
	storetest.ForEach(t, func(t *testing.T, url string) {
		s := storetest.Open(t, url, itemsSchema)
		insertItem(t, s, alice, "i1", "a'?{x}")
		insertItem(t, s, alice, "i2", "two")
		// A literal containing ? and quotes is not a placeholder.
		var n int
		err := s.Tx(t.Context(), func(tx *store.Tx) error {
			return tx.Scoped(alice).QueryRow("SELECT count(*) FROM items WHERE {tenant} AND v <> 'it''s ?' AND (id = ? OR id = ?)", "i1", "i2").Scan(&n)
		})
		if err != nil || n != 2 {
			t.Fatalf("count: %d %v", n, err)
		}
		err = s.Tx(t.Context(), func(tx *store.Tx) error {
			_, err := tx.Scoped(alice).Exec("UPDATE items SET v = ? WHERE {tenant} AND id = ?", "x")
			return err
		})
		if err == nil || !strings.Contains(err.Error(), "placeholders") {
			t.Fatalf("argument count mismatch: %v", err)
		}
		err = s.Tx(t.Context(), func(tx *store.Tx) error {
			return tx.Scoped(alice).Insert("items; DROP TABLE items", []string{"id", "v"}, "i3", "x")
		})
		if err == nil {
			t.Fatal("insert accepted a non-identifier table")
		}
		err = s.Tx(t.Context(), func(tx *store.Tx) error {
			return tx.Scoped(alice).Insert("items", []string{"org", "id", "v"}, "org-b", "i3", "x")
		})
		if !errors.Is(err, store.ErrUnscoped) {
			t.Fatalf("insert overriding org: want ErrUnscoped, got %v", err)
		}
	})
}

func TestTxRollsBackOnError(t *testing.T) {
	storetest.ForEach(t, func(t *testing.T, url string) {
		s := storetest.Open(t, url, itemsSchema)
		boom := errors.New("boom")
		err := s.Tx(t.Context(), func(tx *store.Tx) error {
			if err := tx.Scoped(alice).Insert("items", []string{"id", "v"}, "i1", "x"); err != nil {
				return err
			}
			return boom
		})
		if !errors.Is(err, boom) {
			t.Fatalf("tx error: %v", err)
		}
		if _, err := readItem(s, alice, "i1"); !errors.Is(err, store.ErrNotFound) {
			t.Fatalf("rolled back row visible: %v", err)
		}
	})
}

func TestQueryAllTenantsIsReadOnly(t *testing.T) {
	storetest.ForEach(t, func(t *testing.T, url string) {
		s := storetest.Open(t, url, itemsSchema)
		insertItem(t, s, alice, "i1", "x")
		insertItem(t, s, store.Tenant{Org: "org-b", Owner: "bob"}, "i1", "y")
		err := s.Tx(t.Context(), func(tx *store.Tx) error {
			rows, err := tx.QueryAllTenants("SELECT org, owner FROM items")
			if err != nil {
				return err
			}
			defer rows.Close()
			n := 0
			for rows.Next() {
				n++
			}
			if n != 2 {
				return fmt.Errorf("rows %d", n)
			}
			return rows.Err()
		})
		if err != nil {
			t.Fatal(err)
		}
		for _, query := range []string{"DELETE FROM items", "SELECT 1; DELETE FROM items", " update items set v = 'z'"} {
			err := s.Tx(t.Context(), func(tx *store.Tx) error {
				rows, err := tx.QueryAllTenants(query)
				if err == nil {
					_ = rows.Close()
				}
				return err
			})
			if !errors.Is(err, store.ErrUnscoped) {
				t.Errorf("%q: want ErrUnscoped, got %v", query, err)
			}
		}
	})
}

func TestMigrationLockSerializesConcurrentStart(t *testing.T) {
	// Non-idempotent migrations: a second application fails (CREATE TABLE
	// without IF NOT EXISTS) or is visible (the counter row).
	migrations := fstest.MapFS{
		"0001_counter.sql": {Data: []byte("CREATE TABLE applied (n INTEGER NOT NULL);\nINSERT INTO applied (n) VALUES (1);")},
		"0002_items.sql":   {Data: []byte("CREATE TABLE items (org TEXT NOT NULL, owner TEXT NOT NULL, id TEXT NOT NULL, PRIMARY KEY (org, owner, id));\nINSERT INTO applied (n) VALUES (2);")},
	}
	storetest.ForEach(t, func(t *testing.T, url string) {
		const copies = 8
		var wg sync.WaitGroup
		stores := make([]*store.Store, copies)
		errs := make([]error, copies)
		start := make(chan struct{})
		for i := range copies {
			wg.Add(1)
			go func() {
				defer wg.Done()
				<-start
				stores[i], errs[i] = store.Open(t.Context(), url, migrations)
			}()
		}
		close(start)
		wg.Wait()
		for i, err := range errs {
			if err != nil {
				t.Errorf("copy %d: %v", i, err)
				continue
			}
			defer stores[i].Close()
		}
		if t.Failed() {
			return
		}
		var applied, versions int
		err := stores[0].Tx(t.Context(), func(tx *store.Tx) error {
			if err := tx.QueryAllTenantsRow("SELECT count(*) FROM applied").Scan(&applied); err != nil {
				return err
			}
			return tx.QueryAllTenantsRow("SELECT count(*) FROM schema_migrations").Scan(&versions)
		})
		if err != nil || applied != 2 || versions != 2 {
			t.Fatalf("migrations applied %d times, %d versions recorded, err %v", applied, versions, err)
		}
	})
}

func TestMigrationsRejectGapsAndNewerSchema(t *testing.T) {
	storetest.ForEach(t, func(t *testing.T, url string) {
		for name, files := range map[string]fstest.MapFS{
			"gap":       {"0001_a.sql": {Data: []byte("SELECT 1;")}, "0003_c.sql": {Data: []byte("SELECT 1;")}},
			"duplicate": {"0001_a.sql": {Data: []byte("SELECT 1;")}, "0001_b.sql": {Data: []byte("SELECT 1;")}},
			"bad name":  {"init.sql": {Data: []byte("SELECT 1;")}},
		} {
			if _, err := store.Open(t.Context(), url, files); !errors.Is(err, store.ErrMigration) {
				t.Errorf("%s: want ErrMigration, got %v", name, err)
			}
		}
		s := storetest.Open(t, url, itemsSchema)
		s.Close()
		if _, err := store.Open(t.Context(), url, fstest.MapFS{}); !errors.Is(err, store.ErrSchema) {
			t.Fatalf("binary older than the database: want ErrSchema, got %v", err)
		}
		reopened, err := store.Open(t.Context(), url, itemsSchema)
		if err != nil {
			t.Fatalf("reopen at same version: %v", err)
		}
		reopened.Close()
	})
}

func TestOpenRejectsUnknownURLWithoutEchoingIt(t *testing.T) {
	for _, raw := range []string{"", "mysql://user:secret@host/db", "sqlite://", "file:x.db"} {
		_, err := store.Open(t.Context(), raw, nil)
		if !errors.Is(err, store.ErrURL) {
			t.Errorf("%q: want ErrURL, got %v", raw, err)
		}
		if err != nil && strings.Contains(err.Error(), "secret") {
			t.Errorf("%q: error echoes the URL: %v", raw, err)
		}
	}
}

func TestNoRowsIsNotFound(t *testing.T) {
	storetest.ForEach(t, func(t *testing.T, url string) {
		s := storetest.Open(t, url, itemsSchema)
		_, err := readItem(s, alice, "missing")
		if !errors.Is(err, store.ErrNotFound) || errors.Is(err, sql.ErrNoRows) {
			t.Fatalf("missing row: %v", err)
		}
	})
}

func TestTenantInvariants(t *testing.T) {
	storetest.TenantInvariants(t, storetest.TenantCase{
		Table:      "items",
		Migrations: itemsSchema,
		Create: func(q store.Scoped) (string, error) {
			return "i1", q.Insert("items", []string{"id", "v"}, "i1", "x")
		},
		Read: func(q store.Scoped, id string) error {
			var v string
			return q.QueryRow("SELECT v FROM items WHERE {tenant} AND id = ?", id).Scan(&v)
		},
	})
}
