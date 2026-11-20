package spawn_test

import (
	"testing"
	"time"

	"github.com/letya999/hermes-hub/services/spawn-controller/internal/spawn"
	"github.com/letya999/hermes-hub/services/spawn-controller/internal/spawn/migrations"
	"github.com/letya999/hermes-hub/shared/fsm"
	"github.com/letya999/hermes-hub/shared/store"
	"github.com/letya999/hermes-hub/shared/store/storetest"
)

// Every new table is covered: entity tables are created through the same
// fsm.Create the service uses, history tables get their row from the same
// call, and the plain tables insert through Scoped. Read and Write run the
// scoped surface a foreign tenant would hit.
func TestTenantInvariants(t *testing.T) {
	readID := func(table string) func(store.Scoped, string) error {
		return func(q store.Scoped, id string) error {
			var v string
			return q.QueryRow("SELECT id FROM "+table+" WHERE {tenant} AND id = ?", id).Scan(&v)
		}
	}
	write := func(table, column string, value any) func(store.Scoped, string) (bool, error) {
		return func(q store.Scoped, id string) (bool, error) {
			result, err := q.Exec("UPDATE "+table+" SET "+column+" = ? WHERE {tenant} AND id = ?", value, id)
			if err != nil {
				return false, err
			}
			n, err := result.RowsAffected()
			return n > 0, err
		}
	}
	machine := func(m *fsm.Machine) storetest.TenantCase {
		return storetest.TenantCase{
			Table:      m.Table,
			Migrations: migrations.FS,
			Create: func(q store.Scoped) (string, error) {
				id := "t1"
				return id, fsm.Create(q, m, id, t0)
			},
			Read:  readID(m.Table),
			Write: write(m.Table, "state", "probe"),
		}
	}
	history := func(m *fsm.Machine) storetest.TenantCase {
		return storetest.TenantCase{
			Table:      m.History,
			Migrations: migrations.FS,
			Create: func(q store.Scoped) (string, error) {
				id := "t1"
				return id, fsm.Create(q, m, id, t0)
			},
			Read:  readID(m.History),
			Write: write(m.History, "event", "probe"),
		}
	}
	plain := func(table string, columns []string, values ...any) storetest.TenantCase {
		return storetest.TenantCase{
			Table:      table,
			Migrations: migrations.FS,
			Create: func(q store.Scoped) (string, error) {
				id := "t1"
				return id, q.Insert(table, append([]string{"id"}, columns...), append([]any{id}, values...)...)
			},
			Read:  readID(table),
			Write: write(table, columns[0], values[0]),
		}
	}
	now := time.Unix(1_800_000_000, 0).Unix()
	storetest.TenantInvariants(t,
		plain("spaces", []string{"created_at"}, now),
		machine(spawn.PassportRevisionMachine),
		history(spawn.PassportRevisionMachine),
		machine(spawn.RuntimeMachine),
		history(spawn.RuntimeMachine),
		plain("leases", []string{"runtime_id", "created_at"}, "rt", now),
		machine(spawn.WorkerMachine),
		history(spawn.WorkerMachine),
		plain("image_pins", []string{"digest", "created_at"}, "sha256:x", now),
	)
}
