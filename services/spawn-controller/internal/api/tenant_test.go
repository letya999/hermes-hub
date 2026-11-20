package api_test

import (
	"testing"
	"time"

	"github.com/letya999/hermes-hub/services/spawn-controller/internal/spawn"
	"github.com/letya999/hermes-hub/services/spawn-controller/internal/spawn/migrations"
	"github.com/letya999/hermes-hub/shared/fsm"
	"github.com/letya999/hermes-hub/shared/store"
	"github.com/letya999/hermes-hub/shared/store/storetest"
)

// The contract adapter reaches spaces, passport_revisions (+history) and
// effective_specs — each table keeps the tenant invariants here.
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
	revision := storetest.TenantCase{
		Table:      spawn.PassportRevisionMachine.Table,
		Migrations: migrations.FS,
		Create: func(q store.Scoped) (string, error) {
			id := "t1"
			return id, fsm.Create(q, spawn.PassportRevisionMachine, id, time.Unix(1_800_000_000, 0))
		},
		Read:  readID(spawn.PassportRevisionMachine.Table),
		Write: write(spawn.PassportRevisionMachine.Table, "state", "probe"),
	}
	history := storetest.TenantCase{
		Table:      spawn.PassportRevisionMachine.History,
		Migrations: migrations.FS,
		Create: func(q store.Scoped) (string, error) {
			id := "t1"
			return id, fsm.Create(q, spawn.PassportRevisionMachine, id, time.Unix(1_800_000_000, 0))
		},
		Read:  readID(spawn.PassportRevisionMachine.History),
		Write: write(spawn.PassportRevisionMachine.History, "event", "probe"),
	}
	storetest.TenantInvariants(t,
		plain("spaces", []string{"created_at"}, now),
		plain("effective_specs", []string{"space_id", "documents", "created_at"}, "s1", "{}", now),
		revision,
		history,
	)
}
