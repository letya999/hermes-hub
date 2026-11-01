//go:build linux && cgo

package journal

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// requireCipher skips engine tests on builds without the codec (stub, CGO=0
// or missing the libsqlite3 tag). Engine presence is proven by the pinned
// toolchain image and the container gate, not by every developer host.
func requireCipher(t *testing.T) {
	t.Helper()
	if _, err := SQLCipherVersion(); err != nil {
		t.Skip("sqlcipher engine unavailable in this build")
	}
}

func sqlSetup(t *testing.T) (primary, recovery, anchorDir string, pk, rk []byte) {
	t.Helper()
	base := t.TempDir()
	primary = filepath.Join(base, "state")
	recovery = filepath.Join(base, "recovery", "journal")
	anchorDir = filepath.Join(base, "anchor")
	for _, d := range []string{primary, recovery, anchorDir} {
		if err := os.MkdirAll(d, 0700); err != nil {
			t.Fatal(err)
		}
	}
	return primary, recovery, anchorDir, bytes.Repeat([]byte{6}, 32), bytes.Repeat([]byte{9}, 32)
}

func seedLegacy(t *testing.T, primary, recovery, anchorDir string, pk, rk []byte, n int) {
	t.Helper()
	j, err := OpenReplicatedAnchored(primary, recovery, anchorDir, pk, rk, 8<<20)
	if err != nil {
		t.Fatal(err)
	}
	for i := 0; i < n; i++ {
		if _, err := j.Append("mutation.v1", map[string]string{"op": fmt.Sprintf("op-%d", i)}); err != nil {
			t.Fatal(err)
		}
	}
	if err := j.Close(); err != nil {
		t.Fatal(err)
	}
}

func TestSQLCipherBuildIdentity(t *testing.T) {
	requireCipher(t)
	v, err := SQLCipherVersion()
	if err != nil {
		t.Fatal(err)
	}
	if v == "" {
		t.Fatal("engine reports no cipher_version")
	}
}

func TestSQLCipherMarkerRequired(t *testing.T) {
	requireCipher(t)
	base := t.TempDir()
	primary := filepath.Join(base, "state")
	recovery := filepath.Join(base, "recovery", "journal")
	anchorDir := filepath.Join(base, "anchor")
	for _, d := range []string{primary, recovery, anchorDir} {
		if err := os.MkdirAll(d, 0700); err != nil {
			t.Fatal(err)
		}
	}
	pk := bytes.Repeat([]byte{6}, 32)
	rk := bytes.Repeat([]byte{9}, 32)
	// Database files without the activation marker must never be served:
	// an unverified import cannot distinguish itself from corruption.
	for _, pair := range [][2]string{{primary, "primary.db"}, {recovery, "recovery.db"}} {
		if err := os.WriteFile(filepath.Join(pair[0], pair[1]), []byte("not-a-database"), 0600); err != nil {
			t.Fatal(err)
		}
	}
	if j, err := OpenReplicatedAnchored(primary, recovery, anchorDir, pk, rk, 8<<20); err == nil {
		_ = j.Close()
		t.Fatal("unactivated sqlcipher store accepted")
	}
}

func TestSQLCipherImportRefusesLiveWriter(t *testing.T) {
	requireCipher(t)
	primary, recovery, anchorDir, pk, rk := sqlSetup(t)
	j, err := OpenReplicatedAnchored(primary, recovery, anchorDir, pk, rk, 8<<20)
	if err != nil {
		t.Fatal(err)
	}
	defer j.Close()
	if _, err := ImportSQLCipher(primary, recovery, anchorDir, pk, rk, 8<<20); err == nil {
		t.Fatal("import proceeded under a live writer")
	}
}

func TestSQLCipherImportAndActivate(t *testing.T) {
	requireCipher(t)
	primary, recovery, anchorDir, pk, rk := sqlSetup(t)
	seedLegacy(t, primary, recovery, anchorDir, pk, rk, 5)
	manifest, err := ImportSQLCipher(primary, recovery, anchorDir, pk, rk, 8<<20)
	if err != nil {
		t.Fatal(err)
	}
	if manifest.Records != 5 || manifest.HeadSequence != 5 || len(manifest.HeadDigest) != 64 {
		t.Fatalf("manifest %+v", manifest)
	}
	// Import is non-destructive: legacy frames still serve until activation.
	j, err := OpenReplicatedAnchored(primary, recovery, anchorDir, pk, rk, 8<<20)
	if err != nil {
		t.Fatal(err)
	}
	if len(j.Records()) != 5 {
		t.Fatal("import corrupted source")
	}
	_ = j.Close()
	if err := ActivateSQLCipher(primary, recovery, anchorDir, pk, rk); err != nil {
		t.Fatal(err)
	}
	for _, dir := range []string{primary, recovery} {
		if _, err := os.Lstat(filepath.Join(dir, "ledger.bin")); !os.IsNotExist(err) {
			t.Fatal("legacy ledger still active")
		}
		if _, err := os.Lstat(filepath.Join(dir, "substrate-backup", "ledger.bin")); err != nil {
			t.Fatal("legacy ledger not retained")
		}
	}
	j, err = OpenReplicatedAnchored(primary, recovery, anchorDir, pk, rk, 8<<20)
	if err != nil {
		t.Fatal(err)
	}
	defer j.Close()
	if len(j.Records()) != 5 || j.Records()[4].Sequence != 5 {
		t.Fatal("activated store lost records")
	}
	if _, err := j.Append("mutation.v1", map[string]string{"op": "post-activate"}); err != nil {
		t.Fatal(err)
	}
	_ = j.Close()
	j, err = OpenReplicatedAnchored(primary, recovery, anchorDir, pk, rk, 8<<20)
	if err != nil {
		t.Fatal(err)
	}
	defer j.Close()
	if len(j.Records()) != 6 {
		t.Fatal("append after activation not durable")
	}
}

func TestSQLCipherUncommittedTailDropped(t *testing.T) {
	requireCipher(t)
	primary, recovery, anchorDir, pk, rk := sqlSetup(t)
	seedLegacy(t, primary, recovery, anchorDir, pk, rk, 2)
	if _, err := ImportSQLCipher(primary, recovery, anchorDir, pk, rk, 8<<20); err != nil {
		t.Fatal(err)
	}
	if err := ActivateSQLCipher(primary, recovery, anchorDir, pk, rk); err != nil {
		t.Fatal(err)
	}
	// Forge a crash: commit a chained row into both databases but never write
	// the witness head, so the tail is unacknowledged and must be dropped.
	db, err := sqlOpenDB()
	if err != nil {
		t.Fatal(err)
	}
	if err := sqlAttach(db, "r", filepath.Join(recovery, "recovery.db"), sqlKeyPass(rk, "sqlcipher-recovery/v1")); err != nil {
		t.Fatal(err)
	}
	if err := sqlAttach(db, "p", filepath.Join(primary, "primary.db"), sqlKeyPass(pk, "sqlcipher-ledger/v1")); err != nil {
		t.Fatal(err)
	}
	recs, prev, _, err := sqlReplay(db, "r")
	if err != nil || len(recs) != 2 {
		t.Fatal("replay")
	}
	r := Record{Sequence: 3, Previous: prev, Kind: "mutation.v1", Data: json.RawMessage(`{"op":"crashed"}`)}
	tx, err := db.Begin()
	if err != nil {
		t.Fatal(err)
	}
	for _, schema := range []string{"r", "p"} {
		if _, err := tx.Exec("INSERT INTO "+schema+".records(seq,prev,kind,body) VALUES(?,?,?,?)", r.Sequence, r.Previous, r.Kind, []byte(r.Data)); err != nil {
			t.Fatal(err)
		}
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
	j, err := OpenReplicatedAnchored(primary, recovery, anchorDir, pk, rk, 8<<20)
	if err != nil {
		t.Fatal(err)
	}
	defer j.Close()
	if len(j.Records()) != 2 {
		t.Fatalf("uncommitted tail survived: %d", len(j.Records()))
	}
}

func TestSQLCipherWrongKeyRefused(t *testing.T) {
	requireCipher(t)
	primary, recovery, anchorDir, pk, rk := sqlSetup(t)
	seedLegacy(t, primary, recovery, anchorDir, pk, rk, 2)
	if _, err := ImportSQLCipher(primary, recovery, anchorDir, pk, rk, 8<<20); err != nil {
		t.Fatal(err)
	}
	if err := ActivateSQLCipher(primary, recovery, anchorDir, pk, rk); err != nil {
		t.Fatal(err)
	}
	if j, err := OpenReplicatedAnchored(primary, recovery, anchorDir, bytes.Repeat([]byte{7}, 32), rk, 8<<20); err == nil {
		_ = j.Close()
		t.Fatal("wrong primary key accepted")
	}
	if j, err := OpenReplicatedAnchored(primary, recovery, anchorDir, pk, bytes.Repeat([]byte{7}, 32), 8<<20); err == nil {
		_ = j.Close()
		t.Fatal("wrong recovery key accepted")
	}
}

func TestSQLCipherNoPlaintextRecords(t *testing.T) {
	requireCipher(t)
	primary, recovery, anchorDir, pk, rk := sqlSetup(t)
	seedLegacy(t, primary, recovery, anchorDir, pk, rk, 1)
	if _, err := ImportSQLCipher(primary, recovery, anchorDir, pk, rk, 8<<20); err != nil {
		t.Fatal(err)
	}
	if err := ActivateSQLCipher(primary, recovery, anchorDir, pk, rk); err != nil {
		t.Fatal(err)
	}
	j, err := OpenReplicatedAnchored(primary, recovery, anchorDir, pk, rk, 8<<20)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := j.Append("mutation.v1", map[string]string{"secret": "canary-only-token"}); err != nil {
		t.Fatal(err)
	}
	_ = j.Close()
	for _, db := range []string{filepath.Join(primary, "primary.db"), filepath.Join(recovery, "recovery.db")} {
		raw, err := os.ReadFile(db)
		if err != nil {
			t.Fatal(err)
		}
		if len(raw) < 16 || string(raw[:15]) == "SQLite format 3" {
			t.Fatalf("plaintext sqlite header in %s", db)
		}
		if bytes.Contains(raw, []byte("canary-only-token")) {
			t.Fatalf("plaintext record body in %s", db)
		}
	}
}

func TestSQLCipherSingleOpen(t *testing.T) {
	requireCipher(t)
	dir := filepath.Join(t.TempDir(), "state")
	if err := os.MkdirAll(dir, 0700); err != nil {
		t.Fatal(err)
	}
	key := bytes.Repeat([]byte{4}, 32)
	j, err := Open(dir, key, 8<<20)
	if err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 3; i++ {
		if _, err := j.Append("mutation.v1", map[string]string{"op": fmt.Sprintf("op-%d", i)}); err != nil {
			t.Fatal(err)
		}
	}
	if err := j.Close(); err != nil {
		t.Fatal(err)
	}
	if err := ImportSQLCipherSingle(dir, key, 8<<20); err != nil {
		t.Fatal(err)
	}
	// Before activation the framed ledger still serves.
	j, err = Open(dir, key, 8<<20)
	if err != nil {
		t.Fatal(err)
	}
	if len(j.Records()) != 3 {
		t.Fatal("import corrupted source")
	}
	_ = j.Close()
	if err := ActivateSQLCipherSingle(dir, key); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Lstat(filepath.Join(dir, "ledger.bin")); !os.IsNotExist(err) {
		t.Fatal("legacy ledger still active")
	}
	j, err = Open(dir, key, 8<<20)
	if err != nil {
		t.Fatal(err)
	}
	if len(j.Records()) != 3 {
		t.Fatalf("records lost: %d", len(j.Records()))
	}
	if _, err := j.Append("mutation.v1", map[string]string{"op": "post"}); err != nil {
		t.Fatal(err)
	}
	_ = j.Close()
	j, err = Open(dir, key, 8<<20)
	if err != nil {
		t.Fatal(err)
	}
	if len(j.Records()) != 4 {
		t.Fatalf("append not durable: %d", len(j.Records()))
	}
	_ = j.Close()
	if j, err := Open(dir, bytes.Repeat([]byte{7}, 32), 8<<20); err == nil {
		_ = j.Close()
		t.Fatal("wrong key accepted")
	}
	raw, err := os.ReadFile(filepath.Join(dir, sqlSingleDB))
	if err != nil {
		t.Fatal(err)
	}
	if len(raw) < 16 || string(raw[:15]) == "SQLite format 3" || bytes.Contains(raw, []byte("op-1")) {
		t.Fatal("plaintext ledger.db")
	}
}

func TestSQLCipherRecoveryCatchUp(t *testing.T) {
	requireCipher(t)
	primary, recovery, anchorDir, pk, rk := sqlSetup(t)
	seedLegacy(t, primary, recovery, anchorDir, pk, rk, 3)
	if _, err := ImportSQLCipher(primary, recovery, anchorDir, pk, rk, 8<<20); err != nil {
		t.Fatal(err)
	}
	if err := ActivateSQLCipher(primary, recovery, anchorDir, pk, rk); err != nil {
		t.Fatal(err)
	}
	// A crash between the two journal commits leaves the acknowledged tail in
	// r but missing from p; the open path must replay it into primary.
	db, err := sqlOpenDB()
	if err != nil {
		t.Fatal(err)
	}
	if err := sqlAttach(db, "p", filepath.Join(primary, "primary.db"), sqlKeyPass(pk, "sqlcipher-ledger/v1")); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec("DELETE FROM p.records WHERE seq=3"); err != nil {
		t.Fatal(err)
	}
	_ = db.Close()
	j, err := OpenReplicatedAnchored(primary, recovery, anchorDir, pk, rk, 8<<20)
	if err != nil {
		t.Fatal(err)
	}
	defer j.Close()
	if len(j.Records()) != 3 {
		t.Fatalf("acknowledged record not recovered: %d", len(j.Records()))
	}
	db, err = sqlOpenDB()
	if err != nil {
		t.Fatal(err)
	}
	if err := sqlAttach(db, "p", filepath.Join(primary, "primary.db"), sqlKeyPass(pk, "sqlcipher-ledger/v1")); err != nil {
		t.Fatal(err)
	}
	var n int
	if err := db.QueryRow("SELECT count(*) FROM p.records").Scan(&n); err != nil {
		t.Fatal(err)
	}
	_ = db.Close()
	if n != 3 {
		t.Fatalf("primary not caught up: %d rows", n)
	}
}

func TestSQLCipherDivergenceRefused(t *testing.T) {
	requireCipher(t)
	primary, recovery, anchorDir, pk, rk := sqlSetup(t)
	seedLegacy(t, primary, recovery, anchorDir, pk, rk, 3)
	if _, err := ImportSQLCipher(primary, recovery, anchorDir, pk, rk, 8<<20); err != nil {
		t.Fatal(err)
	}
	if err := ActivateSQLCipher(primary, recovery, anchorDir, pk, rk); err != nil {
		t.Fatal(err)
	}
	db, err := sqlOpenDB()
	if err != nil {
		t.Fatal(err)
	}
	if err := sqlAttach(db, "p", filepath.Join(primary, "primary.db"), sqlKeyPass(pk, "sqlcipher-ledger/v1")); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec("UPDATE p.records SET body=? WHERE seq=2", []byte(`{"forged":true}`)); err != nil {
		t.Fatal(err)
	}
	_ = db.Close()
	if j, err := OpenReplicatedAnchored(primary, recovery, anchorDir, pk, rk, 8<<20); err == nil {
		_ = j.Close()
		t.Fatal("diverged primary accepted")
	}
}

func TestSQLCipherWriterFenced(t *testing.T) {
	requireCipher(t)
	primary, recovery, anchorDir, pk, rk := sqlSetup(t)
	seedLegacy(t, primary, recovery, anchorDir, pk, rk, 1)
	if _, err := ImportSQLCipher(primary, recovery, anchorDir, pk, rk, 8<<20); err != nil {
		t.Fatal(err)
	}
	if err := ActivateSQLCipher(primary, recovery, anchorDir, pk, rk); err != nil {
		t.Fatal(err)
	}
	j, err := OpenReplicatedAnchored(primary, recovery, anchorDir, pk, rk, 8<<20)
	if err != nil {
		t.Fatal(err)
	}
	defer j.Close()
	if j2, err := OpenReplicatedAnchored(primary, recovery, anchorDir, pk, rk, 8<<20); err == nil {
		_ = j2.Close()
		t.Fatal("second writer accepted")
	}
}

func TestSQLCipherAppendBound(t *testing.T) {
	requireCipher(t)
	primary, recovery, anchorDir, pk, rk := sqlSetup(t)
	seedLegacy(t, primary, recovery, anchorDir, pk, rk, 1)
	if _, err := ImportSQLCipher(primary, recovery, anchorDir, pk, rk, 1<<20); err != nil {
		t.Fatal(err)
	}
	if err := ActivateSQLCipher(primary, recovery, anchorDir, pk, rk); err != nil {
		t.Fatal(err)
	}
	j, err := OpenReplicatedAnchored(primary, recovery, anchorDir, pk, rk, 1<<20)
	if err != nil {
		t.Fatal(err)
	}
	defer j.Close()
	// Fill to just under the limit, then one more append must cross the byte
	// bound - and poison the journal against further writes.
	big := strings.Repeat("x", (1<<20)-300)
	if _, err := j.Append("mutation.v1", map[string]string{"blob": big}); err != nil {
		t.Fatal(err)
	}
	if _, err := j.Append("mutation.v1", map[string]string{"blob": big}); err == nil {
		t.Fatal("over-limit append accepted")
	}
	if _, err := j.Append("mutation.v1", map[string]string{"op": "small"}); err == nil {
		t.Fatal("poisoned journal accepted an append")
	}
}

func TestSQLCipherActivateWithoutImport(t *testing.T) {
	requireCipher(t)
	primary, recovery, anchorDir, pk, rk := sqlSetup(t)
	seedLegacy(t, primary, recovery, anchorDir, pk, rk, 1)
	if err := ActivateSQLCipher(primary, recovery, anchorDir, pk, rk); err == nil {
		t.Fatal("activation without verified import accepted")
	}
}
