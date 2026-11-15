package toolhub

import (
	"encoding/hex"
	"errors"
	"path/filepath"
	"strconv"
	"testing"

	_ "modernc.org/sqlite"

	"github.com/letya999/hermes-hub/internal/toolhub/api"
)

// TestReadDBSnapshotCorruptStates drives the fail-closed read branches: a
// valid digest is impossible for doctored rows, so corruption must surface
// as ErrInvalid/ErrConflict before any compare passes.
func TestReadDBSnapshotCorruptStates(t *testing.T) {
	path := filepath.Join(t.TempDir(), "corrupt.db")
	db, err := openStoreDB(path)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	exec := func(q string) {
		t.Helper()
		if _, err := db.Exec(q); err != nil {
			t.Fatal(err)
		}
	}
	clearRows := func() {
		t.Helper()
		for _, table := range []string{"ent", "kvmap", "listrec"} {
			exec(`DELETE FROM ` + table)
		}
	}
	read := func() error {
		t.Helper()
		_, _, _, err := readDBSnapshot(db)
		return err
	}

	// Empty meta: the store was never initialized.
	if err := read(); !errors.Is(err, api.ErrInvalid) {
		t.Fatalf("uninitialized: %v", err)
	}
	exec(`INSERT INTO meta (k,v) VALUES ('initialized','1'),('schema','999')`)
	if err := read(); !errors.Is(err, api.ErrInvalid) {
		t.Fatalf("bad schema: %v", err)
	}
	exec(`UPDATE meta SET v='` + strconv.Itoa(api.SchemaVersion) + `' WHERE k='schema'`)

	for _, c := range []struct{ name, q string }{
		{"ent-unknown-collection", `INSERT INTO ent (collection,id,seq,body) VALUES ('bogus','x',0,X'7b7d')`},
		{"ent-bad-json", `INSERT INTO ent (collection,id,seq,body) VALUES ('connection','x',0,'not json')`},
		{"ent-strict-fields", `INSERT INTO ent (collection,id,seq,body) VALUES ('connection','x',0,'{"connection_id":"x","unknown_field":1}')`},
		{"kvmap-bad-revision", `INSERT INTO kvmap (collection,k,v) VALUES ('projection_revision','x','nan')`},
		{"kvmap-bad-digest", `INSERT INTO kvmap (collection,k,v) VALUES ('restart_digest','x','not json')`},
		{"kvmap-unknown", `INSERT INTO kvmap (collection,k,v) VALUES ('mystery','x','1')`},
		{"list-bad-json", `INSERT INTO listrec (collection,seq,body) VALUES ('capability_change',0,'nope')`},
		{"list-bad-binding", `INSERT INTO listrec (collection,seq,body) VALUES ('binding_change',0,'nope')`},
		{"list-unknown", `INSERT INTO listrec (collection,seq,body) VALUES ('mystery',0,X'7b7d')`},
	} {
		clearRows()
		exec(c.q)
		if err := read(); !errors.Is(err, api.ErrInvalid) {
			t.Fatalf("%s: %v", c.name, err)
		}
	}
	clearRows()

	// Non-hex stored digest, then an unknown digest format.
	exec(`INSERT INTO meta (k,v) VALUES ('digest','zz')`)
	if err := read(); !errors.Is(err, api.ErrConflict) {
		t.Fatalf("non-hex digest: %v", err)
	}
	exec(`UPDATE meta SET v='` + hex.EncodeToString(make([]byte, 32)) + `' WHERE k='digest'`)
	exec(`INSERT INTO meta (k,v) VALUES ('digest_format','bogus')`)
	if err := read(); !errors.Is(err, api.ErrInvalid) {
		t.Fatalf("unknown digest format: %v", err)
	}
	// Under rows-v1 the compare runs against row hashes; a store whose rows
	// were never hashed by a real save must conflict.
	exec(`UPDATE meta SET v='rows-v1' WHERE k='digest_format'`)
	exec(`INSERT INTO kvmap (collection,k,v) VALUES ('projection_revision','x','1')`)
	if err := read(); !errors.Is(err, api.ErrConflict) {
		t.Fatalf("rows-v1 compare: %v", err)
	}
}
