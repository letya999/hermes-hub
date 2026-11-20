package migrations_test

import (
	"testing"

	"github.com/letya999/hermes-hub/services/spawn-controller/internal/spawn/migrations"
	"github.com/letya999/hermes-hub/shared/store"
	"github.com/letya999/hermes-hub/shared/store/storetest"
)

// Schema-level coverage: every table the migration creates is checked with
// raw scoped statements, independent of the machines in package spawn.
func TestTenantInvariants(t *testing.T) {
	var now int64 = 1_800_000_000
	cases := []storetest.TenantCase{}
	add := func(table string, columns []string, values ...any) {
		cases = append(cases, storetest.TenantCase{
			Table:      table,
			Migrations: migrations.FS,
			Create: func(q store.Scoped) (string, error) {
				id := "t1"
				return id, q.Insert(table, append([]string{"id"}, columns...), append([]any{id}, values...)...)
			},
			Read: func(q store.Scoped, id string) error {
				var v string
				return q.QueryRow("SELECT id FROM "+table+" WHERE {tenant} AND id = ?", id).Scan(&v)
			},
			Write: func(q store.Scoped, id string) (bool, error) {
				result, err := q.Exec("UPDATE "+table+" SET "+columns[0]+" = ? WHERE {tenant} AND id = ?", values[0], id)
				if err != nil {
					return false, err
				}
				n, err := result.RowsAffected()
				return n > 0, err
			},
		})
	}
	add("spaces", []string{"created_at"}, now)
	add("effective_specs", []string{"space_id", "documents", "created_at"}, "s1", "{}", now)
	add("passport_revisions", []string{"state"}, "draft")
	add("passport_revisions_history", []string{"seq", "at", "from_state", "event", "to_state"}, 1, now, "", "create", "draft")
	add("runtimes", []string{"generation", "state"}, 1, "starting")
	add("runtimes_history", []string{"seq", "at", "from_state", "event", "to_state"}, 1, now, "", "create", "starting")
	add("leases", []string{"runtime_id", "created_at"}, "rt", now)
	add("workers", []string{"generation", "state"}, 1, "starting")
	add("workers_history", []string{"seq", "at", "from_state", "event", "to_state"}, 1, now, "", "create", "starting")
	add("image_pins", []string{"digest", "created_at"}, "sha256:x", now)
	add("backups", []string{"last_success_at", "last_error", "last_archive", "retained", "last_drill_at", "drill_result", "drill_checked"}, now, "", "", 0, 0, "", 0)
	storetest.TenantInvariants(t, cases...)
}
