package identityhub

import (
	"bytes"
	"context"
	"database/sql"
	"errors"
	"os"
	"path/filepath"
	"testing"

	contract "github.com/letya999/hermes-hub/contracts/identity/v1"
	"github.com/letya999/hermes-hub/internal/commodel"
	"github.com/letya999/hermes-hub/internal/sqlitedb"
)

func TestRegistryRefusesForeignSQLiteWithoutMutation(t *testing.T) {
	path := filepath.Join(t.TempDir(), "foreign.sqlite")
	db, err := sqlitedb.Open(path, 1, func(tx *sql.Tx) error { _, err := tx.Exec("CREATE TABLE unrelated(value TEXT)"); return err })
	if err != nil {
		t.Fatal(err)
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
	before, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	r, err := Open(path)
	if r != nil {
		_ = r.Close()
	}
	if !errors.Is(err, contract.ErrInvalid) {
		t.Fatalf("foreign database accepted: %v", err)
	}
	after, err := os.ReadFile(path)
	if err != nil || !bytes.Equal(before, after) {
		t.Fatal("foreign database was changed")
	}
}

func TestRegistrySchemaOneUpgradePreservesIdentityAndImportReceipt(t *testing.T) {
	path := filepath.Join(t.TempDir(), "identity.sqlite")
	r, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	users := []commodel.User{{ID: "alice", Enabled: true, TelegramIDs: []int64{7}}}
	if _, err := r.ImportLegacy(context.Background(), "original", "personal", users); err != nil {
		t.Fatal(err)
	}
	if _, err := r.db.Exec("DROP TABLE space_members; DROP TABLE shared_spaces; DROP TABLE subjects; PRAGMA user_version=1"); err != nil {
		t.Fatal(err)
	}
	if err := r.Close(); err != nil {
		t.Fatal(err)
	}
	r, err = Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer r.Close()
	if _, err := r.ImportLegacy(context.Background(), "original", "personal", users); err != nil {
		t.Fatal("old receipt changed during schema upgrade")
	}
	if principal, err := r.Resolve(context.Background(), contract.ChannelIdentity{Channel: contract.Telegram, ID: "7"}); err != nil || principal.ID != "alice" {
		t.Fatal("upgrade lost identity")
	}
	var version int
	if err := r.db.QueryRow("PRAGMA user_version").Scan(&version); err != nil || version != schema {
		t.Fatal("schema upgrade incomplete")
	}
}

func TestRegistryRefusesSymlink(t *testing.T) {
	root := t.TempDir()
	path := filepath.Join(root, "identity.sqlite")
	r, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := r.Close(); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(root, "link.sqlite")
	if err := os.Symlink(path, link); err != nil {
		t.Skipf("symlinks unavailable: %v", err)
	}
	if r, err := Open(link); !errors.Is(err, contract.ErrInvalid) {
		if r != nil {
			_ = r.Close()
		}
		t.Fatalf("symlink accepted: %v", err)
	}
}
