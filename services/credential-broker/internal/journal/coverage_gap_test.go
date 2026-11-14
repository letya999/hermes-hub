//go:build linux && cgo

package journal

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/letya999/credential-broker/internal/seal"
	"github.com/letya999/credential-broker/internal/strictjson"
)

// activatedStore returns an open replicated SQLCipher journal over n legacy
// records — the shared setup every retention/open error path needs.
func activatedStore(t *testing.T, n int) (primary, recovery, anchorDir string, j *Journal) {
	t.Helper()
	var pk, rk []byte
	primary, recovery, anchorDir, pk, rk = sqlSetup(t)
	seedLegacy(t, primary, recovery, anchorDir, pk, rk, n)
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
	return primary, recovery, anchorDir, j
}

// occupiedPath returns a path that exists as a file — securefs.Dir and the
// archive writers must refuse it rather than treating it as a directory.
func occupiedPath(t *testing.T) string {
	t.Helper()
	p := filepath.Join(t.TempDir(), "not-a-dir")
	if err := os.WriteFile(p, []byte("x"), 0600); err != nil {
		t.Fatal(err)
	}
	return p
}

// archiveTarget is a not-yet-created subdirectory path — securefs.Dir accepts
// it while an existing t.TempDir root is refused as unsafe.
func archiveTarget(t *testing.T) string {
	t.Helper()
	return filepath.Join(t.TempDir(), "archive")
}

// archiveDir returns a pre-created archive directory under t.TempDir — the
// temp root itself is refused by securefs.Dir, a self-created subdir is not.
func archiveDir(t *testing.T) string {
	t.Helper()
	dir := filepath.Join(t.TempDir(), "archive")
	if err := os.MkdirAll(dir, 0700); err != nil {
		t.Fatal(err)
	}
	return dir
}

func auditRow(t *testing.T, j *Journal, tag string) {
	t.Helper()
	_, err := j.AppendWith("mutation.v1", map[string]string{"op": tag}, &AuxWrites{
		Audit: &AuditWrite{Kind: "k." + tag, Principal: "p1", Context: "c1", Body: []byte(`{"op":"` + tag + `"}`)},
	})
	if err != nil {
		t.Fatalf("audit append %s: %v", tag, err)
	}
}

func legacyJournal(t *testing.T) *Journal {
	t.Helper()
	dir := filepath.Join(t.TempDir(), "legacy")
	if err := os.MkdirAll(dir, 0700); err != nil {
		t.Fatal(err)
	}
	j, err := Open(dir, bytes_(1), 1<<20)
	if err != nil {
		t.Fatal(err)
	}
	return j
}

// The audit retention pass validates its policy before touching tables: an
// unbounded cap, a missing archive directory and an archive path that is not
// a directory are all visible refusals (SPEC-0087 R5.3).
func TestRetainAuditsPolicyRefusals(t *testing.T) {
	requireCipher(t)
	_, _, _, j := activatedStore(t, 0)
	defer func() { _ = j.Close() }()
	if _, err := j.RetainAudits(); err == nil {
		t.Fatal("unbounded hot cap accepted")
	}
	j.SetAuditRetention(AuditRetention{HotMaxBytes: 200})
	if _, err := j.RetainAudits(); err == nil {
		t.Fatal("missing archive directory accepted")
	}
	j.SetAuditRetention(AuditRetention{HotMaxBytes: 200, ArchiveDir: occupiedPath(t)})
	if _, err := j.RetainAudits(); err == nil {
		t.Fatal("archive path occupied by a file accepted")
	}
	// A valid policy over an empty audit table exports nothing but stays clean.
	j.SetAuditRetention(AuditRetention{HotMaxBytes: 200, ArchiveDir: archiveTarget(t)})
	rep, err := j.RetainAudits()
	if err != nil || rep.Exported || rep.Rows != 0 {
		t.Fatalf("empty retention = %+v %v", rep, err)
	}
}

// Retention reads fail closed when the hot table is gone.
func TestRetainAuditsDroppedTable(t *testing.T) {
	requireCipher(t)
	_, _, _, j := activatedStore(t, 0)
	defer func() { _ = j.Close() }()
	auditRow(t, j, "a")
	j.SetAuditRetention(AuditRetention{HotMaxBytes: 200, ArchiveDir: archiveTarget(t)})
	if _, err := j.sql.db.Exec("DROP TABLE r.audit"); err != nil {
		t.Fatal(err)
	}
	if _, err := j.RetainAudits(); err == nil {
		t.Fatal("retention served a dropped audit table")
	}
}

// The archive writer must not proceed when the target seal path is blocked.
func TestRetainAuditsBlockedSealPath(t *testing.T) {
	requireCipher(t)
	_, _, _, j := activatedStore(t, 0)
	defer func() { _ = j.Close() }()
	auditRow(t, j, "a")
	dir := archiveDir(t)
	j.SetAuditRetention(AuditRetention{HotMaxBytes: 200, ArchiveDir: dir})
	if err := os.Mkdir(filepath.Join(dir, "audit-1-1.seal"), 0700); err != nil {
		t.Fatal(err)
	}
	if _, err := j.RetainAudits(); err == nil {
		t.Fatal("archive write over an occupied seal path succeeded")
	}
}

// A manifest path blocked by a directory fails the archive write after the
// seal landed — no partial pair may authorize anything.
func TestRetainAuditsBlockedManifestPath(t *testing.T) {
	requireCipher(t)
	_, _, _, j := activatedStore(t, 0)
	defer func() { _ = j.Close() }()
	auditRow(t, j, "a")
	dir := archiveDir(t)
	j.SetAuditRetention(AuditRetention{HotMaxBytes: 200, ArchiveDir: dir})
	if err := os.Mkdir(filepath.Join(dir, "audit-1-1.manifest.json"), 0700); err != nil {
		t.Fatal(err)
	}
	if _, err := j.RetainAudits(); err == nil {
		t.Fatal("archive write over an occupied manifest path succeeded")
	}
}

// Under the destructive policy a failed leg delete or floor write poisons the
// journal — partial pruning must never acknowledge.
func TestRetainAuditsDestructiveLegFailure(t *testing.T) {
	requireCipher(t)
	_, _, _, j := activatedStore(t, 0)
	defer func() { _ = j.Close() }()
	auditRow(t, j, "a")
	j.SetAuditRetention(AuditRetention{HotMaxBytes: 200, ArchiveDir: archiveTarget(t), Destructive: true})
	if _, err := j.sql.db.Exec("DROP TABLE p.audit"); err != nil {
		t.Fatal(err)
	}
	if _, err := j.RetainAudits(); err == nil {
		t.Fatal("prune acknowledged a failed leg delete")
	}
	if j.Healthy() {
		t.Fatal("journal stayed healthy after a poisoned prune")
	}
}

// Same destructive guard, one failure later: the meta floor write must roll
// the whole transaction back.
func TestRetainAuditsDestructiveMetaFailure(t *testing.T) {
	requireCipher(t)
	_, _, _, j := activatedStore(t, 0)
	defer func() { _ = j.Close() }()
	auditRow(t, j, "a")
	j.SetAuditRetention(AuditRetention{HotMaxBytes: 200, ArchiveDir: archiveTarget(t), Destructive: true})
	if _, err := j.sql.db.Exec("DROP TABLE p.meta"); err != nil {
		t.Fatal(err)
	}
	if _, err := j.RetainAudits(); err == nil {
		t.Fatal("prune acknowledged a failed floor write")
	}
	if j.Healthy() {
		t.Fatal("journal stayed healthy after a poisoned floor write")
	}
}

// Records retention validates policy and backend the same way: no ledger
// backend, no archive dir, no keep bound, no real directory — all refuse.
func TestRetainRecordsPolicyRefusals(t *testing.T) {
	requireCipher(t)
	legacy := legacyJournal(t)
	defer func() { _ = legacy.Close() }()
	if _, err := legacy.RetainRecords(1024); !errors.Is(err, ErrUnavailable) {
		t.Fatalf("framed backend retention = %v, want ErrUnavailable", err)
	}
	_, _, _, j := activatedStore(t, 0)
	defer func() { _ = j.Close() }()
	if _, err := j.RetainRecords(1024); err == nil {
		t.Fatal("missing archive directory accepted")
	}
	j.SetRecordsRetention(RecordsRetention{ArchiveDir: archiveTarget(t)})
	if _, err := j.RetainRecords(0); err == nil {
		t.Fatal("zero keep bound accepted")
	}
	j.SetRecordsRetention(RecordsRetention{ArchiveDir: occupiedPath(t)})
	if _, err := j.RetainRecords(1024); err == nil {
		t.Fatal("archive path occupied by a file accepted")
	}
}

// Records retention fails closed when the chained table is gone.
func TestRetainRecordsDroppedTable(t *testing.T) {
	requireCipher(t)
	_, _, _, j := activatedStore(t, 3)
	defer func() { _ = j.Close() }()
	j.SetRecordsRetention(RecordsRetention{ArchiveDir: archiveTarget(t)})
	if _, err := j.sql.db.Exec("DROP TABLE r.records"); err != nil {
		t.Fatal(err)
	}
	if _, err := j.RetainRecords(1024); err == nil {
		t.Fatal("retention served a dropped records table")
	}
}

// A pre-existing archive over the range is verified, never rewritten — a
// tampered copy stops the pass.
func TestRetainRecordsCorruptExistingArchive(t *testing.T) {
	requireCipher(t)
	_, _, _, j := activatedStore(t, 4)
	defer func() { _ = j.Close() }()
	dir := archiveDir(t)
	j.SetRecordsRetention(RecordsRetention{ArchiveDir: dir})
	rep, err := j.RetainRecords(170)
	if err != nil || !rep.Exported {
		t.Fatalf("export = %+v %v", rep, err)
	}
	seal, manifest := recordsArchiveNames(rep.SeqLo, rep.SeqHi)
	if err := os.WriteFile(filepath.Join(dir, manifest), []byte("{"), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := j.RetainRecords(170); err == nil {
		t.Fatal("retention ran against a corrupt existing archive")
	}
	if _, err := os.Stat(filepath.Join(dir, seal)); err != nil {
		t.Fatal(err)
	}
}

// A manifest path blocked by a directory fails the records archive write
// after the seal landed.
func TestRetainRecordsBlockedManifestPath(t *testing.T) {
	requireCipher(t)
	_, _, _, j := activatedStore(t, 4)
	defer func() { _ = j.Close() }()
	dir := archiveDir(t)
	j.SetRecordsRetention(RecordsRetention{ArchiveDir: dir})
	rep, err := j.RetainRecords(170)
	if err != nil || !rep.Exported {
		t.Fatalf("export = %+v %v", rep, err)
	}
	seal, manifest := recordsArchiveNames(rep.SeqLo, rep.SeqHi)
	if err := os.Remove(filepath.Join(dir, seal)); err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(filepath.Join(dir, manifest)); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(filepath.Join(dir, manifest), 0700); err != nil {
		t.Fatal(err)
	}
	if _, err := j.RetainRecords(170); err == nil {
		t.Fatal("archive write over an occupied manifest path succeeded")
	}
}

// The maintenance tick collects errors from every stage — audit retain,
// records retain, both archive sweeps — persists the outcome marker and keeps
// running; a truncated long error still fits meta.
func TestRetainMaintenanceCollectsErrors(t *testing.T) {
	requireCipher(t)
	_, _, _, j := activatedStore(t, 2)
	defer func() { _ = j.Close() }()
	auditRow(t, j, "a")
	long := filepath.Join(t.TempDir(), strings.Repeat("segment-", 30))
	bad := occupiedPath(t)
	j.SetAuditRetention(AuditRetention{HotMaxBytes: 1, ArchiveDir: bad})
	j.SetRecordsRetention(RecordsRetention{ArchiveDir: long})
	j.limit, j.size = 100, 90
	rep := j.RetainMaintenance()
	if rep.LastError == "" {
		t.Fatal("failing retain stages reported no error")
	}
	if rep.Marker != "retain_last_error" {
		t.Fatalf("marker = %q, want retain_last_error", rep.Marker)
	}
	// Idle marker reuse: a second identical tick does not rewrite meta.
	rep = j.RetainMaintenance()
	if rep.Marker != "" {
		t.Fatalf("unchanged error marker rewritten: %q", rep.Marker)
	}
}

// A framed journal has no sql backend: the tick is a no-op.
func TestRetainMaintenanceNoSQLBackend(t *testing.T) {
	requireCipher(t)
	legacy := legacyJournal(t)
	defer func() { _ = legacy.Close() }()
	rep := legacy.RetainMaintenance()
	if rep.Audit != nil || rep.Records != nil || rep.LastError != "" {
		t.Fatalf("framed journal reported work: %+v", rep)
	}
}

// The archive sweep itself fails visibly when the directory is unreadable.
func TestRetainMaintenanceSweepErrors(t *testing.T) {
	requireCipher(t)
	_, _, _, j := activatedStore(t, 1)
	defer func() { _ = j.Close() }()
	bad := occupiedPath(t)
	j.SetRecordsRetention(RecordsRetention{ArchiveDir: bad, ArchiveMaxBytes: 1})
	j.SetAuditRetention(AuditRetention{HotMaxBytes: 1 << 20, ArchiveDir: bad, ArchiveMaxBytes: 1})
	rep := j.RetainMaintenance()
	if rep.LastError == "" {
		t.Fatal("unreadable archive sweep reported no error")
	}
}

// Insert/aux failures inside the commit poison the journal — a refused commit
// is a durable stop, not a retried silent loss.
func TestAppendSQLCommitFailures(t *testing.T) {
	requireCipher(t)
	_, _, _, j := activatedStore(t, 1)
	defer func() { _ = j.Close() }()
	if _, err := j.sql.db.Exec("DROP TABLE p.records"); err != nil {
		t.Fatal(err)
	}
	if _, err := j.Append("mutation.v1", map[string]string{"op": "x"}); !errors.Is(err, ErrUnavailable) {
		t.Fatalf("append into dropped leg = %v, want ErrUnavailable", err)
	}
	if j.Healthy() {
		t.Fatal("journal stayed healthy after a failed leg insert")
	}
}

// Aux writes commit atomically with the record — a dropped normalized table
// fails the whole append, not just the aux part.
func TestAppendSQLAuxTableDropped(t *testing.T) {
	requireCipher(t)
	_, _, _, j := activatedStore(t, 1)
	defer func() { _ = j.Close() }()
	if _, err := j.sql.db.Exec("DROP TABLE r.state"); err != nil {
		t.Fatal(err)
	}
	_, err := j.AppendWith("mutation.v1", map[string]string{"op": "x"}, &AuxWrites{
		States: []StateWrite{{Kind: "grant", ID: "g1", Body: []byte(`{"a":1}`)}},
	})
	if !errors.Is(err, ErrUnavailable) {
		t.Fatalf("append with aux into dropped table = %v, want ErrUnavailable", err)
	}
}

// Invalid aux payloads are refused before the commit is attempted.
func TestAppendRejectsInvalidAux(t *testing.T) {
	requireCipher(t)
	_, _, _, j := activatedStore(t, 0)
	defer func() { _ = j.Close() }()
	if _, err := j.AppendWith("mutation.v1", map[string]string{"op": "x"}, &AuxWrites{
		States: []StateWrite{{Kind: "", ID: "g1", Body: []byte(`{"a":1}`)}},
	}); err == nil {
		t.Fatal("empty state kind accepted")
	}
	if _, err := j.AppendWith("mutation.v1", map[string]string{"op": "x"}, &AuxWrites{
		Audit: &AuditWrite{Kind: "k", Principal: "p", Context: "c", Body: nil},
	}); err == nil {
		t.Fatal("empty audit body accepted")
	}
}

// Normalized reads fail closed on dropped tables rather than serving
// incomplete data.
func TestNormalizedReadsDroppedTables(t *testing.T) {
	requireCipher(t)
	_, _, _, j := activatedStore(t, 0)
	defer func() { _ = j.Close() }()
	auditRow(t, j, "a")
	if _, err := j.sql.db.Exec("DROP TABLE r.state"); err != nil {
		t.Fatal(err)
	}
	if _, err := j.StateRows(); err == nil {
		t.Fatal("state reads served a dropped table")
	}
	if _, err := j.sql.db.Exec("DROP TABLE r.audit"); err != nil {
		t.Fatal(err)
	}
	if _, err := j.AuditPage(0, 10, AuditScope{}); err == nil {
		t.Fatal("audit pages served a dropped table")
	}
}

// Replicated open refuses a witness head ahead of the durable records — the
// acknowledged sequence can never exceed the replayed tail.
func TestOpenReplicatedRejectsFutureAnchor(t *testing.T) {
	requireCipher(t)
	primary, recovery, anchorDir, j := activatedStore(t, 2)
	if err := j.anchor.writeHead(99, strings.Repeat("f", 64)); err != nil {
		t.Fatal(err)
	}
	if err := j.Close(); err != nil {
		t.Fatal(err)
	}
	if _, err := OpenReplicatedAnchored(primary, recovery, anchorDir, bytes_(6), bytes_(9), 8<<20); err == nil {
		t.Fatal("open accepted a head beyond the durable tail")
	}
}

// A head digest that does not bind the acknowledged record refuses open.
func TestOpenReplicatedRejectsDigestMismatch(t *testing.T) {
	requireCipher(t)
	primary, recovery, anchorDir, j := activatedStore(t, 2)
	if err := j.anchor.writeHead(2, strings.Repeat("0", 64)); err != nil {
		t.Fatal(err)
	}
	if err := j.Close(); err != nil {
		t.Fatal(err)
	}
	if _, err := OpenReplicatedAnchored(primary, recovery, anchorDir, bytes_(6), bytes_(9), 8<<20); err == nil {
		t.Fatal("open accepted a forged head digest")
	}
}

// A witness head below the retained floor means acknowledged history was
// rotated away — refuse instead of replaying from an impossible index.
func TestOpenReplicatedRejectsSunkAnchor(t *testing.T) {
	requireCipher(t)
	primary, recovery, anchorDir, j := activatedStore(t, 6)
	pad := strings.Repeat("x", 2000)
	for i := 0; i < 6; i++ {
		if _, err := j.AppendWith("mutation.v1", map[string]string{"op": fmt.Sprintf("p%d", i), "pad": pad}, nil); err != nil {
			t.Fatal(err)
		}
	}
	dir := archiveDir(t)
	j.SetRecordsRetention(RecordsRetention{ArchiveDir: dir, Destructive: true})
	rep, err := j.RetainRecords(1 << 10)
	if err != nil || rep.SeqHi < 2 {
		t.Fatalf("retention = %+v %v", rep, err)
	}
	if err := j.anchor.writeHead(rep.SeqLo, strings.Repeat("e", 64)); err != nil {
		t.Fatal(err)
	}
	if err := j.Close(); err != nil {
		t.Fatal(err)
	}
	if _, err := OpenReplicatedAnchored(primary, recovery, anchorDir, bytes_(6), bytes_(9), 8<<20); err == nil {
		t.Fatal("open accepted an anchor below the retained floor")
	}
}

// A corrupt recovery leg fails the attach/replay, never silently heals.
func TestOpenReplicatedRejectsCorruptLegs(t *testing.T) {
	requireCipher(t)
	primary, recovery, anchorDir, j := activatedStore(t, 2)
	if err := j.Close(); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(recovery, sqlRecoveryDB), []byte("garbage-not-a-db"), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := OpenReplicatedAnchored(primary, recovery, anchorDir, bytes_(6), bytes_(9), 8<<20); err == nil {
		t.Fatal("open accepted a corrupt recovery leg")
	}
	if err := os.WriteFile(filepath.Join(primary, sqlPrimaryDB), []byte("garbage-not-a-db"), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := OpenReplicatedAnchored(primary, recovery, anchorDir, bytes_(6), bytes_(9), 8<<20); err == nil {
		t.Fatal("open accepted corrupt legs")
	}
}

// Activation markers must agree on both legs — a split marker refuses open.
func TestOpenReplicatedRejectsSplitMarkers(t *testing.T) {
	requireCipher(t)
	primary, recovery, anchorDir, j := activatedStore(t, 1)
	if err := j.Close(); err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(filepath.Join(recovery, sqlMarkerFile)); err != nil {
		t.Fatal(err)
	}
	if _, err := OpenReplicatedAnchored(primary, recovery, anchorDir, bytes_(6), bytes_(9), 8<<20); err == nil {
		t.Fatal("open accepted split activation markers")
	}
}

// A stray primary.db without activation evidence or a legacy source refuses
// the replicated dispatch — an unverified import never serves.
func TestReplicatedDispatchRejectsStrayDB(t *testing.T) {
	requireCipher(t)
	primary, recovery, anchorDir, pk, rk := sqlSetup(t)
	if err := os.WriteFile(filepath.Join(primary, sqlPrimaryDB), []byte("stray"), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := OpenReplicatedAnchored(primary, recovery, anchorDir, pk, rk, 8<<20); err == nil {
		t.Fatal("open accepted an unverified stray database")
	}
}

// The single-directory lifecycle — import, activate, open, normalize — moves
// the framed ledger onto ledger.db and retires the frames.
func TestSingleModeLifecycle(t *testing.T) {
	requireCipher(t)
	dir := filepath.Join(t.TempDir(), "single")
	if err := os.MkdirAll(dir, 0700); err != nil {
		t.Fatal(err)
	}
	key := bytes_(3)
	j, err := Open(dir, key, 1<<20)
	if err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 3; i++ {
		if _, err := j.Append("mutation.v1", map[string]string{"op": fmt.Sprintf("s%d", i)}); err != nil {
			t.Fatal(err)
		}
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
	if _, err := j.Append("mutation.v1", map[string]string{"op": "s3"}); err != nil {
		t.Fatal(err)
	}
	if err := j.Close(); err != nil {
		t.Fatal(err)
	}
	norm := func(r Record) ([]StateWrite, *AuditWrite, error) {
		return []StateWrite{{Kind: "rec", ID: fmt.Sprint(r.Sequence), Body: r.Data}}, nil, nil
	}
	if err := NormalizeSQLCipherSingle(dir, key, 8<<20, norm); err != nil {
		t.Fatal(err)
	}
	j, err = Open(dir, key, 8<<20)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = j.Close() }()
	if len(j.Records()) != 4 {
		t.Fatalf("records after single lifecycle = %d, want 4", len(j.Records()))
	}
}

// Single-mode import and activation refuse every state that is not a quiesced
// legacy ledger with a verified import.
func TestSingleModeImportActivateRefusals(t *testing.T) {
	requireCipher(t)
	key := bytes_(3)
	// Activation without an import sees no ledger.db to verify.
	empty := filepath.Join(t.TempDir(), "empty")
	if err := os.MkdirAll(empty, 0700); err != nil {
		t.Fatal(err)
	}
	if err := ActivateSQLCipherSingle(empty, key); err == nil {
		t.Fatal("activation accepted a missing import")
	}
	dir := filepath.Join(t.TempDir(), "single")
	if err := os.MkdirAll(dir, 0700); err != nil {
		t.Fatal(err)
	}
	j, err := Open(dir, key, 1<<20)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := j.Append("mutation.v1", map[string]string{"op": "s"}); err != nil {
		t.Fatal(err)
	}
	if err := j.Close(); err != nil {
		t.Fatal(err)
	}
	if err := ImportSQLCipherSingle(dir, key, 8<<20); err != nil {
		t.Fatal(err)
	}
	// A second import refuses the existing database artifact.
	if err := ImportSQLCipherSingle(dir, key, 8<<20); err == nil {
		t.Fatal("import accepted an existing ledger.db")
	}
	if err := ActivateSQLCipherSingle(dir, key); err != nil {
		t.Fatal(err)
	}
	// Post-activation import refuses the marker too.
	if err := ImportSQLCipherSingle(dir, key, 8<<20); err == nil {
		t.Fatal("import accepted an activated store")
	}
}

// A stray ledger.db with no activation marker and no legacy source is an
// unverified import — the single dispatch refuses it.
func TestSingleDispatchRejectsStrayDB(t *testing.T) {
	requireCipher(t)
	dir := filepath.Join(t.TempDir(), "single")
	if err := os.MkdirAll(dir, 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, sqlSingleDB), []byte("stray"), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := Open(dir, bytes_(3), 1<<20); err == nil {
		t.Fatal("open accepted an unverified stray ledger.db")
	}
}

// A corrupt single database fails the open path cleanly.
func TestSingleOpenRejectsCorruptDB(t *testing.T) {
	requireCipher(t)
	dir := filepath.Join(t.TempDir(), "single")
	if err := os.MkdirAll(dir, 0700); err != nil {
		t.Fatal(err)
	}
	key := bytes_(3)
	j, err := Open(dir, key, 1<<20)
	if err != nil {
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
	if err := os.WriteFile(filepath.Join(dir, sqlSingleDB), []byte("garbage"), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := Open(dir, key, 8<<20); err == nil {
		t.Fatal("open accepted a corrupt single database")
	}
}

// Normalize over a store with no records is a clean no-op — the migration
// tool opens the database directly and has nothing to backfill.
func TestNormalizeSingleEmptyStore(t *testing.T) {
	requireCipher(t)
	dir := filepath.Join(t.TempDir(), "single")
	if err := os.MkdirAll(dir, 0700); err != nil {
		t.Fatal(err)
	}
	called := false
	norm := func(r Record) ([]StateWrite, *AuditWrite, error) { called = true; return nil, nil, nil }
	if err := NormalizeSQLCipherSingle(dir, bytes_(3), 1<<20, norm); err != nil {
		t.Fatalf("empty-store normalize = %v", err)
	}
	if called {
		t.Fatal("normalize ran the mapper over an empty store")
	}
}

// A records archive verifies strictly: missing manifest, foreign identity,
// broken chain and tip mismatch all fail closed.
func TestReadRecordsArchiveStrictVerification(t *testing.T) {
	requireCipher(t)
	_, _, _, j := activatedStore(t, 4)
	defer func() { _ = j.Close() }()
	dir := archiveDir(t)
	j.SetRecordsRetention(RecordsRetention{ArchiveDir: dir})
	rep, err := j.RetainRecords(170)
	if err != nil || !rep.Exported {
		t.Fatalf("export = %+v %v", rep, err)
	}
	seal, manifest := recordsArchiveNames(rep.SeqLo, rep.SeqHi)
	sealPath, manPath := filepath.Join(dir, seal), filepath.Join(dir, manifest)
	if _, err := ReadRecordsArchive(sealPath, manPath, nil); err == nil {
		t.Fatal("archive read accepted a missing identity")
	}
	if _, err := ReadRecordsArchive(sealPath, filepath.Join(dir, "missing.json"), j.identity); err == nil {
		t.Fatal("archive read accepted a missing manifest")
	}
	foreign := bytes_(7)
	if _, err := ReadRecordsArchive(sealPath, manPath, foreign); err == nil {
		t.Fatal("archive read accepted a foreign identity")
	}
	// A resealed manifest with a corrupted archive digest fails verification.
	raw, err := os.ReadFile(manPath)
	if err != nil {
		t.Fatal(err)
	}
	var d recordsManifestDoc
	if err := strictjson.Decode(raw, &d); err != nil {
		t.Fatal(err)
	}
	d.ArchiveSHA256 = "AAAA"
	d.Seal = recordsManifestHMAC(d, j.identity)
	forged, err := json.Marshal(d)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(manPath, forged, 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := ReadRecordsArchive(sealPath, manPath, j.identity); err == nil {
		t.Fatal("archive read accepted a corrupted archive digest")
	}
}

// The anchor head paths fail visibly when the witness files are directories.
func TestAnchorRejectsDirectoryHeads(t *testing.T) {
	requireCipher(t)
	_, _, _, j := activatedStore(t, 1)
	defer func() { _ = j.Close() }()
	headPath := filepath.Join(j.anchor.dir, "current-head")
	if err := os.Remove(headPath); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(headPath, 0700); err != nil {
		t.Fatal(err)
	}
	if _, _, err := j.anchor.current(); err == nil {
		t.Fatal("anchor served a directory head")
	}
	if err := j.anchor.writeHead(1, strings.Repeat("a", 64)); err == nil {
		t.Fatal("head write over an occupied path succeeded")
	}
}

// The initialized marker must decrypt — a directory placeholder refuses open.
func TestAnchorRejectsDirectoryMarker(t *testing.T) {
	requireCipher(t)
	primary, recovery, anchorDir, pk, rk := sqlSetup(t)
	if err := os.MkdirAll(filepath.Join(anchorDir, "initialized"), 0700); err != nil {
		t.Fatal(err)
	}
	if _, err := OpenReplicatedAnchored(primary, recovery, anchorDir, pk, rk, 8<<20); err == nil {
		t.Fatal("open accepted a directory marker")
	}
}

func bytes_(v byte) []byte {
	b := make([]byte, 32)
	for i := range b {
		b[i] = v
	}
	return b
}

// The framed replicated path anchors and reconciles both ledgers: a staged
// recovery copy verifies prefix-equal, accepts the open, and replays tail
// records appended to recovery while the primary was stopped.
func TestFramedAnchoredOpenReconciles(t *testing.T) {
	primary := filepath.Join(t.TempDir(), "primary")
	recovery := filepath.Join(t.TempDir(), "recovery")
	anchorDir := filepath.Join(t.TempDir(), "anchor")
	for _, d := range []string{primary, recovery, anchorDir} {
		if err := os.MkdirAll(d, 0700); err != nil {
			t.Fatal(err)
		}
	}
	pk, rk := bytes_(6), bytes_(9)
	j, err := Open(primary, pk, 8<<20)
	if err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 3; i++ {
		if _, err := j.Append("mutation.v1", map[string]string{"op": fmt.Sprintf("f%d", i)}); err != nil {
			t.Fatal(err)
		}
	}
	if err := j.Close(); err != nil {
		t.Fatal(err)
	}
	if err := SeedReplicated(primary, recovery, anchorDir, pk, rk, 8<<20); err != nil {
		t.Fatal(err)
	}
	// A committed recovery tail — anchored by the witness head — replays into
	// the primary on the next open.
	rj, err := Open(recovery, rk, 8<<20)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := rj.Append("mutation.v1", map[string]string{"op": "tail"}); err != nil {
		t.Fatal(err)
	}
	anchor, err := newAnchor(anchorDir, seal.Derive(pk, "journal-store-key/v2"), rk)
	if err != nil {
		t.Fatal(err)
	}
	if err := anchor.writeHead(rj.seq, rj.prev); err != nil {
		t.Fatal(err)
	}
	if err := rj.Close(); err != nil {
		t.Fatal(err)
	}
	j, err = OpenReplicatedAnchored(primary, recovery, anchorDir, pk, rk, 8<<20)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = j.Close() }()
	if len(j.Records()) != 4 {
		t.Fatalf("reconciled records = %d, want 4", len(j.Records()))
	}
}

// framedAnchoredStore seeds a framed primary, its recovery copy and the
// anchor head — the pre-SQLCipher triad the anchored open must verify.
func framedAnchoredStore(t *testing.T, n int) (primary, recovery, anchorDir string) {
	t.Helper()
	primary = filepath.Join(t.TempDir(), "primary")
	recovery = filepath.Join(t.TempDir(), "recovery")
	anchorDir = filepath.Join(t.TempDir(), "anchor")
	for _, d := range []string{primary, recovery, anchorDir} {
		if err := os.MkdirAll(d, 0700); err != nil {
			t.Fatal(err)
		}
	}
	pk, rk := bytes_(6), bytes_(9)
	j, err := Open(primary, pk, 8<<20)
	if err != nil {
		t.Fatal(err)
	}
	for i := 0; i < n; i++ {
		if _, err := j.Append("mutation.v1", map[string]string{"op": fmt.Sprintf("f%d", i)}); err != nil {
			t.Fatal(err)
		}
	}
	if err := j.Close(); err != nil {
		t.Fatal(err)
	}
	if err := SeedReplicated(primary, recovery, anchorDir, pk, rk, 8<<20); err != nil {
		t.Fatal(err)
	}
	return primary, recovery, anchorDir
}

// The framed anchored open refuses a witness head ahead of the recovery tail
// and a digest that does not bind the acknowledged record — never healing by
// silent truncation.
func TestFramedAnchoredOpenRefusals(t *testing.T) {
	pk, rk := bytes_(6), bytes_(9)
	primary, recovery, anchorDir := framedAnchoredStore(t, 3)
	anchor, err := newAnchor(anchorDir, seal.Derive(pk, "journal-store-key/v2"), rk)
	if err != nil {
		t.Fatal(err)
	}
	if err := anchor.writeHead(99, strings.Repeat("f", 64)); err != nil {
		t.Fatal(err)
	}
	if _, err := OpenReplicatedAnchored(primary, recovery, anchorDir, pk, rk, 8<<20); err == nil {
		t.Fatal("open accepted a head beyond the recovery tail")
	}
	primary, recovery, anchorDir = framedAnchoredStore(t, 3)
	anchor, err = newAnchor(anchorDir, seal.Derive(pk, "journal-store-key/v2"), rk)
	if err != nil {
		t.Fatal(err)
	}
	if err := anchor.writeHead(2, strings.Repeat("0", 64)); err != nil {
		t.Fatal(err)
	}
	if _, err := OpenReplicatedAnchored(primary, recovery, anchorDir, pk, rk, 8<<20); err == nil {
		t.Fatal("open accepted a forged head digest")
	}
}

// A recovery tail that diverges inside the committed prefix refuses the open
// — the anchor binds history, it cannot heal a forked ledger.
func TestFramedAnchoredOpenRejectsDivergentPrefix(t *testing.T) {
	pk, rk := bytes_(6), bytes_(9)
	primary, recovery, anchorDir := framedAnchoredStore(t, 3)
	rj, err := Open(recovery, rk, 8<<20)
	if err != nil {
		t.Fatal(err)
	}
	if err := rj.truncateTo(1); err != nil {
		t.Fatal(err)
	}
	if _, err := rj.Append("mutation.v1", map[string]string{"op": "fork"}); err != nil {
		t.Fatal(err)
	}
	if err := rj.Close(); err != nil {
		t.Fatal(err)
	}
	if _, err := OpenReplicatedAnchored(primary, recovery, anchorDir, pk, rk, 8<<20); err == nil {
		t.Fatal("open accepted a divergent committed prefix")
	}
}

// Import validates the triad before writing a byte: occupied anchor
// directory, corrupt leg, non-empty anchor without a marker, a witness head
// that disagrees with the tails and divergent legs all refuse.
func TestImportRefusals(t *testing.T) {
	requireCipher(t)
	// Occupied anchor path.
	primary, recovery, _, pk, rk := sqlSetup(t)
	j, err := Open(primary, pk, 8<<20)
	if err != nil {
		t.Fatal(err)
	}
	if err := j.Close(); err != nil {
		t.Fatal(err)
	}
	rj, err := Open(recovery, rk, 8<<20)
	if err != nil {
		t.Fatal(err)
	}
	if err := rj.Close(); err != nil {
		t.Fatal(err)
	}
	bad := filepath.Join(t.TempDir(), "occupied")
	if err := os.WriteFile(bad, []byte("x"), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := ImportSQLCipher(primary, recovery, bad, pk, rk, 8<<20); err == nil {
		t.Fatal("import accepted an occupied anchor path")
	}
	// Corrupt primary leg.
	primary, recovery, anchorDir, pk, rk := sqlSetup(t)
	seedLegacy(t, primary, recovery, anchorDir, pk, rk, 2)
	if err := os.WriteFile(filepath.Join(primary, "ledger.bin"), []byte("corrupt"), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := ImportSQLCipher(primary, recovery, anchorDir, pk, rk, 8<<20); err == nil {
		t.Fatal("import accepted a corrupt primary leg")
	}
	// Stray content in the anchor directory with empty ledgers.
	primary, recovery, anchorDir, pk, rk = sqlSetup(t)
	j, err = Open(primary, pk, 8<<20)
	if err != nil {
		t.Fatal(err)
	}
	if err := j.Close(); err != nil {
		t.Fatal(err)
	}
	rj, err = Open(recovery, rk, 8<<20)
	if err != nil {
		t.Fatal(err)
	}
	if err := rj.Close(); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(anchorDir, "stray"), []byte("x"), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := ImportSQLCipher(primary, recovery, anchorDir, pk, rk, 8<<20); err == nil {
		t.Fatal("import accepted a dirty anchor directory")
	}
	// Witness head behind the tails.
	primary, recovery, anchorDir, pk, rk = sqlSetup(t)
	seedLegacy(t, primary, recovery, anchorDir, pk, rk, 2)
	j, err = Open(primary, pk, 8<<20)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := j.Append("mutation.v1", map[string]string{"op": "extra"}); err != nil {
		t.Fatal(err)
	}
	if err := j.Close(); err != nil {
		t.Fatal(err)
	}
	if _, err := ImportSQLCipher(primary, recovery, anchorDir, pk, rk, 8<<20); err == nil {
		t.Fatal("import accepted a sunk witness head")
	}
	// Divergent legs.
	primary, recovery, anchorDir, pk, rk = sqlSetup(t)
	seedLegacy(t, primary, recovery, anchorDir, pk, rk, 2)
	rj, err = Open(recovery, rk, 8<<20)
	if err != nil {
		t.Fatal(err)
	}
	if err := rj.truncateTo(1); err != nil {
		t.Fatal(err)
	}
	if _, err := rj.Append("mutation.v1", map[string]string{"op": "fork"}); err != nil {
		t.Fatal(err)
	}
	if err := rj.Close(); err != nil {
		t.Fatal(err)
	}
	anchor, err := newAnchor(anchorDir, seal.Derive(pk, "journal-store-key/v2"), rk)
	if err != nil {
		t.Fatal(err)
	}
	if err := anchor.writeHead(2, strings.Repeat("a", 64)); err != nil {
		t.Fatal(err)
	}
	if _, err := ImportSQLCipher(primary, recovery, anchorDir, pk, rk, 8<<20); err == nil {
		t.Fatal("import accepted divergent legs")
	}
}

// The sealed manifest write refuses an occupied path — the import must not
// leave an unverifiable pair behind.
func TestImportBlockedManifestPath(t *testing.T) {
	requireCipher(t)
	primary, recovery, anchorDir, pk, rk := sqlSetup(t)
	seedLegacy(t, primary, recovery, anchorDir, pk, rk, 1)
	if err := os.MkdirAll(filepath.Join(primary, "sqlcipher.manifest"), 0700); err != nil {
		t.Fatal(err)
	}
	if _, err := ImportSQLCipher(primary, recovery, anchorDir, pk, rk, 8<<20); err == nil {
		t.Fatal("import wrote a manifest over an occupied path")
	}
}

// Activation verifies the staged manifest and the imported databases before
// retiring the frames: corrupt manifest, missing database, occupied marker or
// backup paths all refuse without touching the legacy ledger.
func TestActivateRefusals(t *testing.T) {
	requireCipher(t)
	setup := func(t *testing.T) (string, string, string, []byte, []byte) {
		primary, recovery, anchorDir, pk, rk := sqlSetup(t)
		seedLegacy(t, primary, recovery, anchorDir, pk, rk, 1)
		if _, err := ImportSQLCipher(primary, recovery, anchorDir, pk, rk, 8<<20); err != nil {
			t.Fatal(err)
		}
		return primary, recovery, anchorDir, pk, rk
	}
	// Corrupt manifest.
	primary, recovery, anchorDir, pk, rk := setup(t)
	if err := os.WriteFile(filepath.Join(primary, "sqlcipher.manifest"), []byte("garbage"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := ActivateSQLCipher(primary, recovery, anchorDir, pk, rk); err == nil {
		t.Fatal("activation accepted a corrupt manifest")
	}
	// Missing imported database.
	primary, recovery, anchorDir, pk, rk = setup(t)
	if err := os.Remove(filepath.Join(recovery, sqlRecoveryDB)); err != nil {
		t.Fatal(err)
	}
	if err := ActivateSQLCipher(primary, recovery, anchorDir, pk, rk); err == nil {
		t.Fatal("activation accepted a missing database")
	}
	// Occupied activation marker path.
	primary, recovery, anchorDir, pk, rk = setup(t)
	if err := os.MkdirAll(filepath.Join(primary, sqlMarkerFile), 0700); err != nil {
		t.Fatal(err)
	}
	if err := ActivateSQLCipher(primary, recovery, anchorDir, pk, rk); err == nil {
		t.Fatal("activation wrote a marker over an occupied path")
	}
	// Occupied substrate-backup path.
	primary, recovery, anchorDir, pk, rk = setup(t)
	if err := os.WriteFile(filepath.Join(primary, "substrate-backup"), []byte("x"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := ActivateSQLCipher(primary, recovery, anchorDir, pk, rk); err == nil {
		t.Fatal("activation accepted an occupied backup path")
	}
	// Missing framed ledger at retirement.
	primary, recovery, anchorDir, pk, rk = setup(t)
	if err := os.Remove(filepath.Join(primary, "ledger.bin")); err != nil {
		t.Fatal(err)
	}
	if err := ActivateSQLCipher(primary, recovery, anchorDir, pk, rk); err == nil {
		t.Fatal("activation accepted a missing framed ledger")
	}
}

// The single-mode import and activation verify the same gates: corrupt
// source and a database replaying different history both refuse.
func TestSingleImportActivateRefusals(t *testing.T) {
	requireCipher(t)
	key := bytes_(3)
	dir := filepath.Join(t.TempDir(), "single")
	if err := os.MkdirAll(dir, 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "ledger.bin"), []byte("corrupt"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "ledger.lock"), []byte(""), 0600); err != nil {
		t.Fatal(err)
	}
	if err := ImportSQLCipherSingle(dir, key, 8<<20); err == nil {
		t.Fatal("single import accepted a corrupt source")
	}
	if err := ActivateSQLCipherSingle(dir, key); err == nil {
		t.Fatal("single activation accepted a corrupt source")
	}
	// A database imported elsewhere replays different history.
	other := filepath.Join(t.TempDir(), "other")
	if err := os.MkdirAll(other, 0700); err != nil {
		t.Fatal(err)
	}
	oj, err := Open(other, key, 8<<20)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := oj.Append("mutation.v1", map[string]string{"op": "other"}); err != nil {
		t.Fatal(err)
	}
	if err := oj.Close(); err != nil {
		t.Fatal(err)
	}
	if err := ImportSQLCipherSingle(other, key, 8<<20); err != nil {
		t.Fatal(err)
	}
	dir = filepath.Join(t.TempDir(), "single2")
	if err := os.MkdirAll(dir, 0700); err != nil {
		t.Fatal(err)
	}
	j, err := Open(dir, key, 8<<20)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := j.Append("mutation.v1", map[string]string{"op": "a"}); err != nil {
		t.Fatal(err)
	}
	if _, err := j.Append("mutation.v1", map[string]string{"op": "b"}); err != nil {
		t.Fatal(err)
	}
	if err := j.Close(); err != nil {
		t.Fatal(err)
	}
	if err := os.Rename(filepath.Join(other, sqlSingleDB), filepath.Join(dir, sqlSingleDB)); err != nil {
		t.Fatal(err)
	}
	if err := ActivateSQLCipherSingle(dir, key); err == nil {
		t.Fatal("single activation accepted a divergent database")
	}
}

// The audit archive reads back under the same strict manifest checks as the
// records archive — foreign identity and tampered digests fail closed.
func TestReadAuditArchiveStrictVerification(t *testing.T) {
	requireCipher(t)
	_, _, _, j := activatedStore(t, 0)
	defer func() { _ = j.Close() }()
	auditRow(t, j, "a")
	dir := archiveDir(t)
	j.SetAuditRetention(AuditRetention{HotMaxBytes: 200, ArchiveDir: dir})
	rep, err := j.RetainAudits()
	if err != nil || !rep.Exported {
		t.Fatalf("export = %+v %v", rep, err)
	}
	sealPath := filepath.Join(dir, "audit-1-1.seal")
	manPath := filepath.Join(dir, "audit-1-1.manifest.json")
	if _, err := ReadAuditArchive(sealPath, manPath, bytes_(7)); err == nil {
		t.Fatal("audit archive accepted a foreign identity")
	}
	raw, err := os.ReadFile(manPath)
	if err != nil {
		t.Fatal(err)
	}
	var d auditManifestDoc
	if err := strictjson.Decode(raw, &d); err != nil {
		t.Fatal(err)
	}
	d.Rows++
	d.Seal = auditManifestHMAC(d, j.identity)
	forged, err := json.Marshal(d)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(manPath, forged, 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := ReadAuditArchive(sealPath, manPath, j.identity); err == nil {
		t.Fatal("audit archive accepted a forged row count")
	}
}

// The archive sweep ignores unknown names, orphan manifests and broken pairs
// — only verified seal+manifest sets count toward the byte budget, and only
// verified-but-old pairs are dropped.
func TestSweepArchivesSkipsJunkAndDropsOld(t *testing.T) {
	requireCipher(t)
	_, _, _, j := activatedStore(t, 4)
	defer func() { _ = j.Close() }()
	dir := archiveDir(t)
	j.SetRecordsRetention(RecordsRetention{ArchiveDir: dir})
	first, err := j.RetainRecords(170)
	if err != nil || !first.Exported {
		t.Fatalf("first export = %+v %v", first, err)
	}
	for i := 0; i < 4; i++ {
		if _, err := j.Append("mutation.v1", map[string]string{"op": fmt.Sprintf("s%d", i)}); err != nil {
			t.Fatal(err)
		}
	}
	second, err := j.RetainRecords(340)
	if err != nil || !second.Exported {
		t.Fatalf("second export = %+v %v", second, err)
	}
	// Plant junk: a stray file, a directory placeholder, an orphan manifest
	// and a corrupt pair — none of it may be treated as a verified archive.
	if err := os.WriteFile(filepath.Join(dir, "records-7-8.seal"), []byte("junk"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "records-7-8.manifest.json"), []byte("{"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "audit-5-9.manifest.json"), []byte("{}"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "anything.txt"), []byte("x"), 0600); err != nil {
		t.Fatal(err)
	}
	j.SetRecordsRetention(RecordsRetention{ArchiveDir: dir, ArchiveMaxBytes: 1})
	rep := j.RetainMaintenance()
	if rep.LastError != "" {
		t.Fatalf("sweep over junk errored: %s", rep.LastError)
	}
	oldSeal, oldManifest := recordsArchiveNames(first.SeqLo, first.SeqHi)
	if _, err := os.Stat(filepath.Join(dir, oldSeal)); !os.IsNotExist(err) {
		t.Fatal("old verified archive survived the byte cap")
	}
	if _, err := os.Stat(filepath.Join(dir, oldManifest)); !os.IsNotExist(err) {
		t.Fatal("old verified manifest survived the byte cap")
	}
	if _, err := os.Stat(filepath.Join(dir, "records-7-8.seal")); err != nil {
		t.Fatal("sweep touched an unverified pair")
	}
	newSeal, _ := recordsArchiveNames(second.SeqLo, second.SeqHi)
	if _, err := os.Stat(filepath.Join(dir, newSeal)); err != nil {
		t.Fatal("newest verified archive dropped under the byte cap")
	}
}
