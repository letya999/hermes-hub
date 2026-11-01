//go:build linux && cgo

package journal

import (
	"bytes"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/letya999/credential-broker/internal/seal"
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

func TestSQLCipherImportRefusesExistingArtifacts(t *testing.T) {
	requireCipher(t)
	primary, recovery, anchorDir, pk, rk := sqlSetup(t)
	seedLegacy(t, primary, recovery, anchorDir, pk, rk, 1)
	// A staged database must not be silently overwritten.
	if err := os.WriteFile(filepath.Join(primary, sqlPrimaryDB), []byte("staged"), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := ImportSQLCipher(primary, recovery, anchorDir, pk, rk, 8<<20); err == nil {
		t.Fatal("import over an existing primary.db accepted")
	}
	if err := os.Remove(filepath.Join(primary, sqlPrimaryDB)); err != nil {
		t.Fatal(err)
	}
	// An already-activated leg must not be re-imported.
	if err := os.WriteFile(filepath.Join(recovery, sqlMarkerFile), []byte("v1"), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := ImportSQLCipher(primary, recovery, anchorDir, pk, rk, 8<<20); err == nil {
		t.Fatal("import over an activated recovery leg accepted")
	}
	if err := os.Remove(filepath.Join(recovery, sqlMarkerFile)); err != nil {
		t.Fatal(err)
	}
	// A missing anchor directory refuses instead of conjuring a store binding.
	if _, err := ImportSQLCipher(primary, recovery, filepath.Join(t.TempDir(), "gone"), pk, rk, 8<<20); err == nil {
		t.Fatal("import without an anchor directory accepted")
	}
}

func TestSQLCipherImportEmptyStoreInitializesAnchor(t *testing.T) {
	requireCipher(t)
	primary, recovery, anchorDir, pk, rk := sqlSetup(t)
	// Empty legs with a never-initialized anchor still migrate: the import
	// writes head 0 + the marker itself instead of refusing a virgin store.
	manifest, err := ImportSQLCipher(primary, recovery, anchorDir, pk, rk, 8<<20)
	if err != nil {
		t.Fatal(err)
	}
	if manifest.Records != 0 || manifest.HeadSequence != 0 {
		t.Fatalf("empty import manifest %+v", manifest)
	}
}

func TestSQLCipherImportSingleRefusals(t *testing.T) {
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
	if _, err := j.Append("mutation.v1", map[string]string{"op": "x"}); err != nil {
		t.Fatal(err)
	}
	// Live writer holds the lock; import must fail closed.
	if err := ImportSQLCipherSingle(dir, key, 8<<20); err == nil {
		t.Fatal("single import under a live writer accepted")
	}
	if err := j.Close(); err != nil {
		t.Fatal(err)
	}
	// An existing ledger.db is a staged import, not a source to overwrite.
	if err := os.WriteFile(filepath.Join(dir, sqlSingleDB), []byte("staged"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := ImportSQLCipherSingle(dir, key, 8<<20); err == nil {
		t.Fatal("single import over an existing ledger.db accepted")
	}
	if err := os.Remove(filepath.Join(dir, sqlSingleDB)); err != nil {
		t.Fatal(err)
	}
	// An activated directory has no framed source left to import.
	if err := os.WriteFile(filepath.Join(dir, sqlMarkerFile), []byte("v1"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := ImportSQLCipherSingle(dir, key, 8<<20); err == nil {
		t.Fatal("single import over an activated directory accepted")
	}
}

func TestSQLCipherActivateSingleRefusals(t *testing.T) {
	requireCipher(t)
	key := bytes.Repeat([]byte{4}, 32)
	// No ledger.db staged: ATTACH creates an empty file whose missing records
	// table fails the verify instead of activating nothing.
	dir := filepath.Join(t.TempDir(), "state")
	if err := os.MkdirAll(dir, 0700); err != nil {
		t.Fatal(err)
	}
	j, err := Open(dir, key, 8<<20)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := j.Append("mutation.v1", map[string]string{"op": "x"}); err != nil {
		t.Fatal(err)
	}
	if err := j.Close(); err != nil {
		t.Fatal(err)
	}
	if err := ActivateSQLCipherSingle(dir, key); err == nil {
		t.Fatal("activation with no staged ledger.db accepted")
	}
	// A staged database whose rows diverge from the live journal must not
	// activate: the verify compares replayed rows to the framed source.
	empty := filepath.Join(t.TempDir(), "empty")
	if err := os.MkdirAll(empty, 0700); err != nil {
		t.Fatal(err)
	}
	j2, err := Open(empty, key, 8<<20)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := j2.Append("mutation.v1", map[string]string{"op": "y"}); err != nil {
		t.Fatal(err)
	}
	if err := j2.Close(); err != nil {
		t.Fatal(err)
	}
	if err := ImportSQLCipherSingle(empty, key, 8<<20); err != nil {
		t.Fatal(err)
	}
	j3, err := Open(empty, key, 8<<20)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := j3.Append("mutation.v1", map[string]string{"op": "post-import"}); err != nil {
		t.Fatal(err)
	}
	if err := j3.Close(); err != nil {
		t.Fatal(err)
	}
	if err := ActivateSQLCipherSingle(empty, key); err == nil {
		t.Fatal("activation accepted rows stale relative to the journal")
	}
}

func TestSQLCipherDispatchRefusesSplitMarkers(t *testing.T) {
	requireCipher(t)
	primary, recovery, anchorDir, pk, rk := sqlSetup(t)
	seedLegacy(t, primary, recovery, anchorDir, pk, rk, 1)
	if _, err := ImportSQLCipher(primary, recovery, anchorDir, pk, rk, 8<<20); err != nil {
		t.Fatal(err)
	}
	// One leg activated, the other not — a torn activation must refuse, not
	// serve whichever leg happens to be readable.
	if err := sqlSealReplace(primary, sqlMarkerFile, []byte("v1")); err != nil {
		t.Fatal(err)
	}
	if j, err := OpenReplicatedAnchored(primary, recovery, anchorDir, pk, rk, 8<<20); err == nil {
		_ = j.Close()
		t.Fatal("split-marker activation accepted")
	}
	// Database files without any framed source are unverified imports.
	orphan := filepath.Join(t.TempDir(), "orphan")
	if err := os.MkdirAll(orphan, 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(orphan, sqlSingleDB), []byte("x"), 0600); err != nil {
		t.Fatal(err)
	}
	if j, err := Open(orphan, pk, 8<<20); err == nil {
		_ = j.Close()
		t.Fatal("orphan ledger.db without legacy source accepted")
	}
}

func TestSQLCipherTamperedManifestRefused(t *testing.T) {
	requireCipher(t)
	primary, recovery, anchorDir, pk, rk := sqlSetup(t)
	seedLegacy(t, primary, recovery, anchorDir, pk, rk, 1)
	if _, err := ImportSQLCipher(primary, recovery, anchorDir, pk, rk, 8<<20); err != nil {
		t.Fatal(err)
	}
	raw, err := os.ReadFile(filepath.Join(primary, "sqlcipher.manifest"))
	if err != nil {
		t.Fatal(err)
	}
	raw[len(raw)-8] ^= 0xff
	if err := os.WriteFile(filepath.Join(primary, "sqlcipher.manifest"), raw, 0600); err != nil {
		t.Fatal(err)
	}
	if err := ActivateSQLCipher(primary, recovery, anchorDir, pk, rk); err == nil {
		t.Fatal("tampered manifest accepted")
	}
}

func TestSQLReplayRejectsCorruptRows(t *testing.T) {
	requireCipher(t)
	dir := filepath.Join(t.TempDir(), "probe")
	if err := os.MkdirAll(dir, 0700); err != nil {
		t.Fatal(err)
	}
	insert := func(db *sql.DB, seq uint64, prev, kind string, body []byte) {
		t.Helper()
		tx, err := db.Begin()
		if err != nil {
			t.Fatal(err)
		}
		if _, err := tx.Exec("CREATE TABLE r.records(seq INTEGER PRIMARY KEY, prev TEXT NOT NULL, kind TEXT NOT NULL, body BLOB NOT NULL)"); err != nil {
			t.Fatal(err)
		}
		if _, err := tx.Exec("INSERT INTO r.records(seq,prev,kind,body) VALUES(?,?,?,?)", seq, prev, kind, body); err != nil {
			t.Fatal(err)
		}
		if err := tx.Commit(); err != nil {
			t.Fatal(err)
		}
	}
	// Every chain invariant is validated on replay: sequence contiguity, the
	// prev-digest link, kind/body presence, the record size bound and JSON.
	for i, row := range []struct {
		seq  uint64
		prev string
		kind string
		body []byte
	}{
		{2, "", "mutation.v1", []byte(`{"op":"x"}`)},
		{1, "foreign-head", "mutation.v1", []byte(`{"op":"x"}`)},
		{1, "", "", []byte(`{"op":"x"}`)},
		{1, "", "mutation.v1", []byte(`{not-json`)},
		{1, "", "mutation.v1", bytes.Repeat([]byte{1}, MaxRecord+1)},
	} {
		db, err := sqlOpenDB()
		if err != nil {
			t.Fatal(err)
		}
		if err := sqlAttach(db, "r", filepath.Join(dir, "probe.db"), sqlKeyPass(bytes.Repeat([]byte{4}, 32), "sqlcipher-ledger/v1")); err != nil {
			t.Fatal(err)
		}
		insert(db, row.seq, row.prev, row.kind, row.body)
		if _, _, _, err := sqlReplay(db, "r"); err == nil {
			t.Fatalf("corrupt row %d replayed", i)
		}
		if err := db.Close(); err != nil {
			t.Fatal(err)
		}
		if err := os.Remove(filepath.Join(dir, "probe.db")); err != nil {
			t.Fatal(err)
		}
	}
}

func TestSQLCipherAppendAfterCloseFails(t *testing.T) {
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
	if _, err := j.Append("mutation.v1", map[string]string{"op": "x"}); err != nil {
		t.Fatal(err)
	}
	if err := j.Close(); err != nil {
		t.Fatal(err)
	}
	if err := ImportSQLCipherSingle(dir, key, 8<<20); err != nil {
		t.Fatal(err)
	}
	if err := ActivateSQLCipherSingle(dir, key); err != nil {
		t.Fatal(err)
	}
	j, err = Open(dir, key, 8<<20)
	if err != nil {
		t.Fatal(err)
	}
	defer j.Close()
	// A store whose session dies mid-flight must refuse and poison, never
	// report success for a row that is not on disk.
	if err := j.sql.db.Close(); err != nil {
		t.Fatal(err)
	}
	if _, err := j.Append("mutation.v1", map[string]string{"op": "lost"}); err == nil {
		t.Fatal("append against a dead session reported success")
	}
	if _, err := j.Append("mutation.v1", map[string]string{"op": "lost"}); err == nil {
		t.Fatal("poisoned journal accepted an append")
	}
}

func TestSQLHelpersRefuseUnsafeInput(t *testing.T) {
	requireCipher(t)
	if _, err := sqlQuote("a;drop"); err == nil {
		t.Fatal("unsafe literal quoted")
	}
	if sqlEqual([]Record{}, []Record{{Sequence: 1}}) {
		t.Fatal("length-mismatched record sets compared equal")
	}
	if sqlEqual([]Record{{Sequence: 1}}, []Record{{Sequence: 2}}) {
		t.Fatal("divergent records compared equal")
	}
	db, err := sqlOpenDB()
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if err := sqlMustExec(db, "SELEC 1"); err == nil {
		t.Fatal("invalid statement executed")
	}
	if err := sqlSealReplace(filepath.Join(t.TempDir(), "gone"), "x", []byte("v1")); err == nil {
		t.Fatal("seal into a missing directory succeeded")
	}
}

func TestSQLCipherImportRefusesForeignAnchorMarker(t *testing.T) {
	requireCipher(t)
	primary, recovery, anchorDir, pk, rk := sqlSetup(t)
	seedLegacy(t, primary, recovery, anchorDir, pk, rk, 1)
	// An anchor marker bound to a different store id must stop the import:
	// accepting it would certify records under the wrong witness.
	foreign := filepath.Join(t.TempDir(), "anchor")
	if err := os.MkdirAll(foreign, 0700); err != nil {
		t.Fatal(err)
	}
	fa, err := newAnchor(foreign, seal.Derive(bytes.Repeat([]byte{1}, 32), "journal-store-key/v2"), bytes.Repeat([]byte{2}, 32))
	if err != nil {
		t.Fatal(err)
	}
	if err := fa.writeHead(0, ""); err != nil {
		t.Fatal(err)
	}
	if err := fa.markInitialized(); err != nil {
		t.Fatal(err)
	}
	entries, err := os.ReadDir(foreign)
	if err != nil || len(entries) == 0 {
		t.Fatal("foreign anchor not seeded")
	}
	for _, e := range entries {
		raw, err := os.ReadFile(filepath.Join(foreign, e.Name()))
		if err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(anchorDir, e.Name()), raw, 0600); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := ImportSQLCipher(primary, recovery, anchorDir, pk, rk, 8<<20); err == nil {
		t.Fatal("import accepted a marker bound to a foreign store")
	}
}

func TestSQLCipherOpenRefusesForgedHead(t *testing.T) {
	requireCipher(t)
	primary, recovery, anchorDir, pk, rk := sqlSetup(t)
	seedLegacy(t, primary, recovery, anchorDir, pk, rk, 2)
	if _, err := ImportSQLCipher(primary, recovery, anchorDir, pk, rk, 8<<20); err != nil {
		t.Fatal(err)
	}
	if err := ActivateSQLCipher(primary, recovery, anchorDir, pk, rk); err != nil {
		t.Fatal(err)
	}
	anchor, err := newAnchor(anchorDir, seal.Derive(pk, "journal-store-key/v2"), rk)
	if err != nil {
		t.Fatal(err)
	}
	// A witness head ahead of the stored rows acknowledges data that does not
	// exist — the open must refuse instead of truncating blindly.
	if err := anchor.writeHead(99, strings.Repeat("0", 64)); err != nil {
		t.Fatal(err)
	}
	if j, err := OpenReplicatedAnchored(primary, recovery, anchorDir, pk, rk, 8<<20); err == nil {
		_ = j.Close()
		t.Fatal("head beyond committed rows accepted")
	}
	// A head at a valid position but with a foreign digest breaks the binding
	// between witness and record bytes.
	if err := anchor.writeHead(1, strings.Repeat("0", 64)); err != nil {
		t.Fatal(err)
	}
	if j, err := OpenReplicatedAnchored(primary, recovery, anchorDir, pk, rk, 8<<20); err == nil {
		_ = j.Close()
		t.Fatal("head with foreign digest accepted")
	}
}

func TestSQLCipherOpenRefusesPrefixDivergence(t *testing.T) {
	requireCipher(t)
	primary, recovery, anchorDir, pk, rk := sqlSetup(t)
	seedLegacy(t, primary, recovery, anchorDir, pk, rk, 2)
	if _, err := ImportSQLCipher(primary, recovery, anchorDir, pk, rk, 8<<20); err != nil {
		t.Fatal(err)
	}
	if err := ActivateSQLCipher(primary, recovery, anchorDir, pk, rk); err != nil {
		t.Fatal(err)
	}
	// Forge p row 1 with a different body but a self-consistent chain: the
	// replay hash chain stays valid while the committed prefix diverges from
	// recovery — the open must compare the legs and refuse.
	db, err := sqlOpenDB()
	if err != nil {
		t.Fatal(err)
	}
	if err := sqlAttach(db, "p", filepath.Join(primary, sqlPrimaryDB), sqlKeyPass(pk, "sqlcipher-ledger/v1")); err != nil {
		t.Fatal(err)
	}
	recs, _, _, err := sqlReplay(db, "p")
	if err != nil || len(recs) != 2 {
		t.Fatalf("replay: %v", err)
	}
	forged := Record{Sequence: 1, Previous: "", Kind: "mutation.v1", Data: json.RawMessage(`{"op":"forged"}`)}
	plain, err := json.Marshal(forged)
	if err != nil {
		t.Fatal(err)
	}
	sum := sha256.Sum256(plain)
	tx, err := db.Begin()
	if err != nil {
		t.Fatal(err)
	}
	if _, err := tx.Exec("UPDATE p.records SET body=? WHERE seq=1", []byte(forged.Data)); err != nil {
		t.Fatal(err)
	}
	if _, err := tx.Exec("UPDATE p.records SET prev=? WHERE seq=2", hex.EncodeToString(sum[:])); err != nil {
		t.Fatal(err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
	if j, err := OpenReplicatedAnchored(primary, recovery, anchorDir, pk, rk, 8<<20); err == nil {
		_ = j.Close()
		t.Fatal("primary diverged inside the committed prefix accepted")
	}
}

func TestSQLCipherActivateRefusesBlockedSeal(t *testing.T) {
	requireCipher(t)
	primary, recovery, anchorDir, pk, rk := sqlSetup(t)
	seedLegacy(t, primary, recovery, anchorDir, pk, rk, 1)
	if _, err := ImportSQLCipher(primary, recovery, anchorDir, pk, rk, 8<<20); err != nil {
		t.Fatal(err)
	}
	// A directory squatting on the marker path fails the sealed write; the
	// activation must surface it instead of half-retiring the source.
	if err := os.Mkdir(filepath.Join(primary, sqlMarkerFile), 0700); err != nil {
		t.Fatal(err)
	}
	if err := ActivateSQLCipher(primary, recovery, anchorDir, pk, rk); err == nil {
		t.Fatal("activation wrote markers over a blocked path")
	}
}

func TestSQLCipherAppendRefusesConflictingRow(t *testing.T) {
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
	// A foreign writer landing a conflicting row under the session must fail
	// the insert and poison the journal — silently appending over it would
	// fork the chain.
	db, err := sqlOpenDB()
	if err != nil {
		t.Fatal(err)
	}
	if err := sqlAttach(db, "r", filepath.Join(recovery, sqlRecoveryDB), sqlKeyPass(rk, "sqlcipher-recovery/v1")); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec("INSERT INTO r.records(seq,prev,kind,body) VALUES(?,?,?,?)", 2, "foreign", "mutation.v1", []byte(`{"op":"foreign"}`)); err != nil {
		t.Fatal(err)
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
	if _, err := j.Append("mutation.v1", map[string]string{"op": "mine"}); err == nil {
		t.Fatal("append over a conflicting row reported success")
	}
	if _, err := j.Append("mutation.v1", map[string]string{"op": "mine"}); err == nil {
		t.Fatal("poisoned journal accepted an append")
	}
}

func TestSQLCipherImportRefusesStaleHead(t *testing.T) {
	requireCipher(t)
	primary, recovery, anchorDir, pk, rk := sqlSetup(t)
	seedLegacy(t, primary, recovery, anchorDir, pk, rk, 3)
	// A witness head behind the committed records certifies a shorter history
	// than the legs actually hold — import must not bake that in.
	anchor, err := newAnchor(anchorDir, seal.Derive(pk, "journal-store-key/v2"), rk)
	if err != nil {
		t.Fatal(err)
	}
	if err := anchor.writeHead(2, strings.Repeat("0", 64)); err != nil {
		t.Fatal(err)
	}
	if _, err := ImportSQLCipher(primary, recovery, anchorDir, pk, rk, 8<<20); err == nil {
		t.Fatal("import accepted a witness head behind the journal")
	}
}

func TestSQLCipherImportRefusesDivergedLegs(t *testing.T) {
	requireCipher(t)
	primary, recovery, anchorDir, pk, rk := sqlSetup(t)
	// Equal sequence counts but different record bytes between the legs: the
	// witness can only certify one of them, so the import must refuse.
	pj, err := openJournal(primary, pk, 8<<20, false)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := pj.Append("mutation.v1", map[string]string{"op": "primary-only"}); err != nil {
		t.Fatal(err)
	}
	if err := pj.Close(); err != nil {
		t.Fatal(err)
	}
	rj, err := openJournal(recovery, rk, 8<<20, false)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := rj.Append("mutation.v1", map[string]string{"op": "recovery-only"}); err != nil {
		t.Fatal(err)
	}
	if err := rj.Close(); err != nil {
		t.Fatal(err)
	}
	anchor, err := newAnchor(anchorDir, seal.Derive(pk, "journal-store-key/v2"), rk)
	if err != nil {
		t.Fatal(err)
	}
	if err := anchor.writeHead(1, strings.Repeat("0", 64)); err != nil {
		t.Fatal(err)
	}
	if err := anchor.markInitialized(); err != nil {
		t.Fatal(err)
	}
	if _, err := ImportSQLCipher(primary, recovery, anchorDir, pk, rk, 8<<20); err == nil {
		t.Fatal("import accepted diverged legs")
	}
}

func TestSQLAttachRefusesUnsafeNames(t *testing.T) {
	requireCipher(t)
	db, err := sqlOpenDB()
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	path := filepath.Join(t.TempDir(), "probe.db")
	// Alias, path and key are spliced into DDL — every one of them must pass
	// the literal whitelist, never reach the engine unescaped.
	if err := sqlAttach(db, "r;bad", path, "k"); err == nil {
		t.Fatal("unsafe schema alias attached")
	}
	if err := sqlAttach(db, "r", "bad'path", "k"); err == nil {
		t.Fatal("unsafe path attached")
	}
	if err := sqlAttach(db, "r", path, "bad'key"); err == nil {
		t.Fatal("unsafe key attached")
	}
}

func TestSQLCipherSingleActivateBlockedPaths(t *testing.T) {
	requireCipher(t)
	key := bytes.Repeat([]byte{4}, 32)
	dir := filepath.Join(t.TempDir(), "state")
	if err := os.MkdirAll(dir, 0700); err != nil {
		t.Fatal(err)
	}
	j, err := Open(dir, key, 8<<20)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := j.Append("mutation.v1", map[string]string{"op": "x"}); err != nil {
		t.Fatal(err)
	}
	if err := j.Close(); err != nil {
		t.Fatal(err)
	}
	if err := ImportSQLCipherSingle(dir, key, 8<<20); err != nil {
		t.Fatal(err)
	}
	// Marker path blocked by a directory: the sealed replace must fail and the
	// activation must surface it.
	if err := os.Mkdir(filepath.Join(dir, sqlMarkerFile), 0700); err != nil {
		t.Fatal(err)
	}
	if err := ActivateSQLCipherSingle(dir, key); err == nil {
		t.Fatal("activation wrote over a blocked marker path")
	}
	if err := os.Remove(filepath.Join(dir, sqlMarkerFile)); err != nil {
		t.Fatal(err)
	}
	// substrate-backup blocked by a regular file: retirement must not proceed.
	if err := os.WriteFile(filepath.Join(dir, "substrate-backup"), []byte("x"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := ActivateSQLCipherSingle(dir, key); err == nil {
		t.Fatal("activation retired over a blocked backup path")
	}
}

func TestSQLCipherSingleOpenRefusesCorruptDB(t *testing.T) {
	requireCipher(t)
	key := bytes.Repeat([]byte{4}, 32)
	dir := filepath.Join(t.TempDir(), "state")
	if err := os.MkdirAll(dir, 0700); err != nil {
		t.Fatal(err)
	}
	j, err := Open(dir, key, 8<<20)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := j.Append("mutation.v1", map[string]string{"op": "x"}); err != nil {
		t.Fatal(err)
	}
	if err := j.Close(); err != nil {
		t.Fatal(err)
	}
	if err := ImportSQLCipherSingle(dir, key, 8<<20); err != nil {
		t.Fatal(err)
	}
	if err := ActivateSQLCipherSingle(dir, key); err != nil {
		t.Fatal(err)
	}
	// A truncated chain in the activated database breaks replay — the open
	// must refuse rather than serve a partial history.
	db, err := sqlOpenDB()
	if err != nil {
		t.Fatal(err)
	}
	if err := sqlAttach(db, "r", filepath.Join(dir, sqlSingleDB), sqlKeyPass(key, "sqlcipher-ledger/v1")); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec("UPDATE r.records SET prev=? WHERE seq=1", "forged"); err != nil {
		t.Fatal(err)
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
	if j, err := Open(dir, key, 8<<20); err == nil {
		_ = j.Close()
		t.Fatal("corrupt activated ledger.db served")
	}
}

func TestSQLCipherAppendRefusesHeadWriteFailure(t *testing.T) {
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
	// Row committed but the witness head can no longer be written: the append
	// must fail rather than leave an unacknowledged row invisible forever.
	if err := os.RemoveAll(anchorDir); err != nil {
		t.Fatal(err)
	}
	if _, err := j.Append("mutation.v1", map[string]string{"op": "mine"}); err == nil {
		t.Fatal("append without a writable witness head reported success")
	}
}

func TestSQLCipherOpenRefusesHeldRecoveryLock(t *testing.T) {
	requireCipher(t)
	primary, recovery, anchorDir, pk, rk := sqlSetup(t)
	seedLegacy(t, primary, recovery, anchorDir, pk, rk, 1)
	if _, err := ImportSQLCipher(primary, recovery, anchorDir, pk, rk, 8<<20); err != nil {
		t.Fatal(err)
	}
	if err := ActivateSQLCipher(primary, recovery, anchorDir, pk, rk); err != nil {
		t.Fatal(err)
	}
	// A second writer holding only the recovery leg must still fence the
	// replicated open — serving one leg would split the journals.
	rlock, err := sqlLock(recovery)
	if err != nil {
		t.Fatal(err)
	}
	defer rlock.Close()
	if j, err := OpenReplicatedAnchored(primary, recovery, anchorDir, pk, rk, 8<<20); err == nil {
		_ = j.Close()
		t.Fatal("open proceeded while a foreign writer held the recovery lock")
	}
}
