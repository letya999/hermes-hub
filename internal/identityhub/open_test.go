package identityhub

import (
	"bytes"
	"database/sql"
	"errors"
	"os"
	"path/filepath"
	"testing"

	contract "github.com/letya999/hermes-hub/contracts/identity/v1"
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
