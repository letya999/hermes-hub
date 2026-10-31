package journal

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestRecoveryRejectsOlderOrDivergentJournal(t *testing.T) {
	for _, divergent := range []bool{false, true} {
		primary, key := setup(t)
		recovery, _ := setup(t)
		j, err := Open(primary, key, 8<<20)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := j.Append("mutation.v1", map[string]string{"status": "revoked"}); err != nil {
			t.Fatal(err)
		}
		if err := j.Close(); err != nil {
			t.Fatal(err)
		}
		if divergent {
			other, err := Open(recovery, key, 8<<20)
			if err != nil {
				t.Fatal(err)
			}
			if _, err := other.Append("mutation.v1", map[string]string{"status": "active"}); err != nil {
				t.Fatal(err)
			}
			if err := other.Close(); err != nil {
				t.Fatal(err)
			}
		}
		before, err := os.ReadFile(filepath.Join(primary, "ledger.bin"))
		if err != nil {
			t.Fatal(err)
		}
		opened, err := OpenReplicated(primary, recovery, key, key, 8<<20)
		if err == nil {
			_ = opened.Close()
			t.Errorf("accepted unsafe recovery journal: divergent=%v", divergent)
		}
		after, err := os.ReadFile(filepath.Join(primary, "ledger.bin"))
		if err != nil || !bytes.Equal(before, after) {
			t.Fatal("primary changed on rejected recovery")
		}
	}
}

func TestRecoveryJournalRestoresAcknowledgedRevocationAfterPrimaryLoss(t *testing.T) {
	primary, primaryKey := setup(t)
	recovery, _ := setup(t)
	recoveryKey := bytes.Repeat([]byte{9}, 32)
	j, err := OpenReplicated(primary, recovery, primaryKey, recoveryKey, 8<<20)
	if err != nil {
		t.Fatal(err)
	}
	for _, status := range []string{"active", "revoked"} {
		if _, err := j.Append("mutation.v1", map[string]string{"owner": "alice", "status": status, "canary": "private-metadata"}); err != nil {
			t.Fatal(err)
		}
	}
	if err := j.Close(); err != nil {
		t.Fatal(err)
	}
	backup, err := os.ReadFile(filepath.Join(recovery, "ledger.bin"))
	if err != nil {
		t.Fatalf("acknowledged changes have no recovery journal: %v", err)
	}
	if bytes.Contains(backup, []byte("private-metadata")) {
		t.Fatal("plaintext recovery metadata")
	}
	if err := os.Remove(filepath.Join(primary, "ledger.bin")); err != nil {
		t.Fatal(err)
	}
	j, err = OpenReplicated(primary, recovery, primaryKey, recoveryKey, 8<<20)
	if err != nil {
		t.Fatal(err)
	}
	defer j.Close()
	records := j.Records()
	if len(records) != 2 || !bytes.Contains(records[1].Data, []byte("revoked")) {
		t.Fatalf("latest revocation not recovered: count=%d", len(records))
	}
	if seq, err := j.Append("mutation.v1", map[string]string{"status": "deleted"}); err != nil || seq != 3 {
		t.Fatalf("append after recovery: sequence=%d err=%v", seq, err)
	}
}

func TestAnchoredRecoveryRejectsStaleBackupAfterRevocation(t *testing.T) {
	primary, primaryKey := setup(t)
	recovery, _ := setup(t)
	anchor, _ := setup(t)
	recoveryKey := bytes.Repeat([]byte{9}, 32)
	j, err := OpenReplicatedAnchored(primary, recovery, anchor, primaryKey, recoveryKey, 8<<20)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := j.Append("mutation.v1", map[string]string{"status": "active"}); err != nil {
		t.Fatal(err)
	}
	if err := j.Close(); err != nil {
		t.Fatal(err)
	}
	stale, err := os.ReadFile(filepath.Join(recovery, "ledger.bin"))
	if err != nil {
		t.Fatal(err)
	}
	j, err = OpenReplicatedAnchored(primary, recovery, anchor, primaryKey, recoveryKey, 8<<20)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := j.Append("mutation.v1", map[string]string{"status": "revoked"}); err != nil {
		t.Fatal(err)
	}
	if err := j.Close(); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(recovery, "ledger.bin"), stale, 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(filepath.Join(primary, "ledger.bin")); err != nil {
		t.Fatal(err)
	}
	j, err = OpenReplicatedAnchored(primary, recovery, anchor, primaryKey, recoveryKey, 8<<20)
	if err == nil {
		_ = j.Close()
		t.Fatal("stale backup restored revoked authority")
	}
}

func TestAnchoredRecoveryRejectsSameSequenceReplacement(t *testing.T) {
	primary, primaryKey := setup(t)
	recovery, _ := setup(t)
	anchor, _ := setup(t)
	alternate, _ := setup(t)
	recoveryKey := bytes.Repeat([]byte{9}, 32)
	j, err := OpenReplicatedAnchored(primary, recovery, anchor, primaryKey, recoveryKey, 8<<20)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := j.Append("mutation.v1", map[string]string{"status": "active"}); err != nil {
		t.Fatal(err)
	}
	if _, err := j.Append("mutation.v1", map[string]string{"status": "revoked"}); err != nil {
		t.Fatal(err)
	}
	if err := j.Close(); err != nil {
		t.Fatal(err)
	}
	other, err := Open(alternate, recoveryKey, 8<<20)
	if err != nil {
		t.Fatal(err)
	}
	for range 2 {
		if _, err := other.Append("mutation.v1", map[string]string{"status": "active"}); err != nil {
			t.Fatal(err)
		}
	}
	if err := other.Close(); err != nil {
		t.Fatal(err)
	}
	replacement, err := os.ReadFile(filepath.Join(alternate, "ledger.bin"))
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(recovery, "ledger.bin"), replacement, 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(filepath.Join(primary, "ledger.bin")); err != nil {
		t.Fatal(err)
	}
	j, err = OpenReplicatedAnchored(primary, recovery, anchor, primaryKey, recoveryKey, 8<<20)
	if err == nil {
		_ = j.Close()
		t.Fatal("same-sequence replacement restored revoked authority")
	}
}

func TestAnchoredRecoveryRequiresExistingCommitEvidence(t *testing.T) {
	primary, primaryKey := setup(t)
	recovery, _ := setup(t)
	anchor, _ := setup(t)
	recoveryKey := bytes.Repeat([]byte{9}, 32)
	j, err := OpenReplicated(primary, recovery, primaryKey, recoveryKey, 8<<20)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := j.Append("mutation.v1", map[string]string{"status": "active"}); err != nil {
		t.Fatal(err)
	}
	if err := j.Close(); err != nil {
		t.Fatal(err)
	}
	j, err = OpenReplicatedAnchored(primary, recovery, anchor, primaryKey, recoveryKey, 8<<20)
	if err == nil {
		_ = j.Close()
		t.Fatal("existing journal silently adopted without commit evidence")
	}
}

func TestAnchoredRecoveryDiscardsUncommittedBackupTail(t *testing.T) {
	primary, primaryKey := setup(t)
	recovery, _ := setup(t)
	anchor, _ := setup(t)
	recoveryKey := bytes.Repeat([]byte{9}, 32)
	j, err := OpenReplicatedAnchored(primary, recovery, anchor, primaryKey, recoveryKey, 8<<20)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := j.Append("mutation.v1", map[string]string{"status": "active"}); err != nil {
		t.Fatal(err)
	}
	if err := j.Close(); err != nil {
		t.Fatal(err)
	}
	backup, err := Open(recovery, recoveryKey, 8<<20)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := backup.Append("mutation.v1", map[string]string{"status": "unconfirmed-grant"}); err != nil {
		t.Fatal(err)
	}
	if err := backup.Close(); err != nil {
		t.Fatal(err)
	}
	j, err = OpenReplicatedAnchored(primary, recovery, anchor, primaryKey, recoveryKey, 8<<20)
	if err != nil {
		t.Fatal(err)
	}
	if len(j.Records()) != 1 {
		_ = j.Close()
		t.Fatal("unconfirmed recovery tail was replayed")
	}
	if seq, err := j.Append("mutation.v1", map[string]string{"status": "revoked"}); err != nil || seq != 2 {
		_ = j.Close()
		t.Fatalf("recovery did not resume at confirmed head: seq=%d err=%v", seq, err)
	}
	if err := j.Close(); err != nil {
		t.Fatal(err)
	}
}

func TestAnchoredRecoveryDiscardsUncommittedPrimaryTail(t *testing.T) {
	primary, primaryKey := setup(t)
	recovery, _ := setup(t)
	anchor, _ := setup(t)
	recoveryKey := bytes.Repeat([]byte{9}, 32)
	j, err := OpenReplicatedAnchored(primary, recovery, anchor, primaryKey, recoveryKey, 8<<20)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := j.Append("mutation.v1", map[string]string{"status": "active"}); err != nil {
		t.Fatal(err)
	}
	if err := j.Close(); err != nil {
		t.Fatal(err)
	}
	for _, target := range []struct {
		dir string
		key []byte
	}{{recovery, recoveryKey}, {primary, primaryKey}} {
		unconfirmed, err := Open(target.dir, target.key, 8<<20)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := unconfirmed.Append("mutation.v1", map[string]string{"status": "unconfirmed-grant"}); err != nil {
			t.Fatal(err)
		}
		if err := unconfirmed.Close(); err != nil {
			t.Fatal(err)
		}
	}
	j, err = OpenReplicatedAnchored(primary, recovery, anchor, primaryKey, recoveryKey, 8<<20)
	if err != nil {
		t.Fatal(err)
	}
	defer j.Close()
	if len(j.Records()) != 1 {
		t.Fatal("unconfirmed primary mutation survived recovery")
	}
}

func TestSeedReplicatedMigratesExistingJournalIdempotently(t *testing.T) {
	primary, primaryKey := setup(t)
	recovery, _ := setup(t)
	anchor, _ := setup(t)
	recoveryKey := bytes.Repeat([]byte{9}, 32)
	j, err := Open(primary, primaryKey, 8<<20)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := j.Append("mutation.v1", map[string]string{"status": "active"}); err != nil {
		t.Fatal(err)
	}
	if _, err := j.Append("mutation.v1", map[string]string{"status": "revoked"}); err != nil {
		t.Fatal(err)
	}
	if err := j.Close(); err != nil {
		t.Fatal(err)
	}
	for range 2 {
		if err := SeedReplicated(primary, recovery, anchor, primaryKey, recoveryKey, 8<<20); err != nil {
			t.Fatalf("migration failed or was not repeatable: %v", err)
		}
	}
	j, err = OpenReplicatedAnchored(primary, recovery, anchor, primaryKey, recoveryKey, 8<<20)
	if err != nil {
		t.Fatal(err)
	}
	defer j.Close()
	if records := j.Records(); len(records) != 2 || !bytes.Contains(records[1].Data, []byte("revoked")) {
		t.Fatal("migrated journal lost the revocation")
	}
}

func TestSeedReplicatedRejectsDivergentOrLongerBackup(t *testing.T) {
	for _, longer := range []bool{false, true} {
		primary, primaryKey := setup(t)
		recovery, _ := setup(t)
		anchor, _ := setup(t)
		recoveryKey := bytes.Repeat([]byte{9}, 32)
		first, err := Open(primary, primaryKey, 8<<20)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := first.Append("mutation.v1", map[string]string{"status": "active"}); err != nil {
			t.Fatal(err)
		}
		if err := first.Close(); err != nil {
			t.Fatal(err)
		}
		other, err := Open(recovery, recoveryKey, 8<<20)
		if err != nil {
			t.Fatal(err)
		}
		status := "revoked"
		if longer {
			status = "active"
		}
		if _, err := other.Append("mutation.v1", map[string]string{"status": status}); err != nil {
			t.Fatal(err)
		}
		if longer {
			if _, err := other.Append("mutation.v1", map[string]string{"status": "revoked"}); err != nil {
				t.Fatal(err)
			}
		}
		if err := other.Close(); err != nil {
			t.Fatal(err)
		}
		if err := SeedReplicated(primary, recovery, anchor, primaryKey, recoveryKey, 8<<20); err == nil {
			t.Fatalf("unsafe backup accepted: longer=%v", longer)
		}
	}
}

func TestSeedReplicatedRejectsMismatchedAnchor(t *testing.T) {
	primary, primaryKey := setup(t)
	recovery, _ := setup(t)
	anchor, _ := setup(t)
	recoveryKey := bytes.Repeat([]byte{9}, 32)
	j, err := OpenReplicatedAnchored(primary, recovery, anchor, primaryKey, recoveryKey, 8<<20)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := j.Append("mutation.v1", map[string]string{"status": "active"}); err != nil {
		t.Fatal(err)
	}
	if err := j.Close(); err != nil {
		t.Fatal(err)
	}
	alternate, _ := setup(t)
	other, err := Open(alternate, primaryKey, 8<<20)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := other.Append("mutation.v1", map[string]string{"status": "revoked"}); err != nil {
		t.Fatal(err)
	}
	if err := other.Close(); err != nil {
		t.Fatal(err)
	}
	replacement, err := os.ReadFile(filepath.Join(alternate, "ledger.bin"))
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(primary, "ledger.bin"), replacement, 0600); err != nil {
		t.Fatal(err)
	}
	if err := SeedReplicated(primary, recovery, anchor, primaryKey, recoveryKey, 8<<20); err == nil {
		t.Fatal("mismatched anchor accepted")
	}
}

func TestSeedReplicatedFromOpenKeepsPrimaryLocked(t *testing.T) {
	primary, primaryKey := setup(t)
	recovery, _ := setup(t)
	anchor, _ := setup(t)
	recoveryKey := bytes.Repeat([]byte{9}, 32)
	j, err := Open(primary, primaryKey, 8<<20)
	if err != nil {
		t.Fatal(err)
	}
	defer j.Close()
	if _, err := j.Append("mutation.v1", map[string]string{"status": "active"}); err != nil {
		t.Fatal(err)
	}
	if err := SeedReplicatedFromOpen(j, recovery, anchor, recoveryKey, 8<<20); err != nil {
		t.Fatal(err)
	}
	backup, err := Open(recovery, recoveryKey, 8<<20)
	if err != nil {
		t.Fatal(err)
	}
	if len(backup.Records()) != 1 {
		_ = backup.Close()
		t.Fatal("migration did not copy locked primary")
	}
	if err := backup.Close(); err != nil {
		t.Fatal(err)
	}
	other, err := Open(primary, primaryKey, 8<<20)
	if err == nil {
		_ = other.Close()
		t.Fatal("migration released primary lock before cutover")
	}
}

func TestAnchoredRecoveryRejectsWrongKeyAndCorruptBackup(t *testing.T) {
	primary, primaryKey := setup(t)
	recovery, _ := setup(t)
	anchor, _ := setup(t)
	recoveryKey := bytes.Repeat([]byte{9}, 32)
	j, err := OpenReplicatedAnchored(primary, recovery, anchor, primaryKey, recoveryKey, 8<<20)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := j.Append("mutation.v1", map[string]string{"status": "revoked"}); err != nil {
		t.Fatal(err)
	}
	if err := j.Close(); err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(filepath.Join(primary, "ledger.bin")); err != nil {
		t.Fatal(err)
	}
	if opened, err := OpenReplicatedAnchored(primary, recovery, anchor, primaryKey, bytes.Repeat([]byte{8}, 32), 8<<20); err == nil {
		_ = opened.Close()
		t.Fatal("wrong recovery key accepted")
	}
	if err := os.WriteFile(filepath.Join(recovery, "ledger.bin"), []byte("corrupt"), 0600); err != nil {
		t.Fatal(err)
	}
	if opened, err := OpenReplicatedAnchored(primary, recovery, anchor, primaryKey, recoveryKey, 8<<20); err == nil {
		_ = opened.Close()
		t.Fatal("corrupt backup accepted")
	}
}

func TestAnchoredRecoveryRejectsCorruptCommitAnchor(t *testing.T) {
	primary, primaryKey := setup(t)
	recovery, _ := setup(t)
	anchor, _ := setup(t)
	recoveryKey := bytes.Repeat([]byte{9}, 32)
	j, err := OpenReplicatedAnchored(primary, recovery, anchor, primaryKey, recoveryKey, 8<<20)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := j.Append("mutation.v1", map[string]string{"status": "revoked"}); err != nil {
		t.Fatal(err)
	}
	if err := j.Close(); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(anchor, "commit-00000000000000000001"), []byte("corrupt"), 0600); err != nil {
		t.Fatal(err)
	}
	if opened, err := OpenReplicatedAnchored(primary, recovery, anchor, primaryKey, recoveryKey, 8<<20); err == nil {
		_ = opened.Close()
		t.Fatal("corrupt commit anchor accepted")
	}
}

func TestAnchoredAppendFailsClosedWhenBackupUnavailable(t *testing.T) {
	primary, primaryKey := setup(t)
	recovery, _ := setup(t)
	anchor, _ := setup(t)
	j, err := OpenReplicatedAnchored(primary, recovery, anchor, primaryKey, bytes.Repeat([]byte{9}, 32), 8<<20)
	if err != nil {
		t.Fatal(err)
	}
	if err := j.recovery.Close(); err != nil {
		t.Fatal(err)
	}
	if _, err := j.Append("mutation.v1", map[string]string{"status": "active"}); err == nil || j.Healthy() || len(j.Records()) != 0 {
		t.Fatal("primary accepted mutation while backup unavailable")
	}
	if err := j.Close(); err != nil {
		t.Fatal(err)
	}
}

func TestAnchoredRecoveryDiscardsInterruptedFirstWrite(t *testing.T) {
	primary, primaryKey := setup(t)
	recovery, _ := setup(t)
	anchor, _ := setup(t)
	recoveryKey := bytes.Repeat([]byte{9}, 32)
	j, err := OpenReplicatedAnchored(primary, recovery, anchor, primaryKey, recoveryKey, 8<<20)
	if err != nil {
		t.Fatal(err)
	}
	if err := j.Close(); err != nil {
		t.Fatal(err)
	}
	backup, err := Open(recovery, recoveryKey, 8<<20)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := backup.Append("mutation.v1", map[string]string{"status": "unconfirmed-grant"}); err != nil {
		t.Fatal(err)
	}
	if err := backup.Close(); err != nil {
		t.Fatal(err)
	}
	j, err = OpenReplicatedAnchored(primary, recovery, anchor, primaryKey, recoveryKey, 8<<20)
	if err != nil {
		t.Fatalf("interrupted first write did not resume: %v", err)
	}
	defer j.Close()
	if len(j.Records()) != 0 {
		t.Fatal("unconfirmed first write was replayed")
	}
}

func TestAnchoredRecoveryRejectsCorruptInitializationMarker(t *testing.T) {
	primary, primaryKey := setup(t)
	recovery, _ := setup(t)
	anchor, _ := setup(t)
	recoveryKey := bytes.Repeat([]byte{9}, 32)
	j, err := OpenReplicatedAnchored(primary, recovery, anchor, primaryKey, recoveryKey, 8<<20)
	if err != nil {
		t.Fatal(err)
	}
	if err := j.Close(); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(anchor, "initialized"), []byte("corrupt"), 0600); err != nil {
		t.Fatal(err)
	}
	if opened, err := OpenReplicatedAnchored(primary, recovery, anchor, primaryKey, recoveryKey, 8<<20); err == nil {
		_ = opened.Close()
		t.Fatal("corrupt initialization marker accepted")
	}
}

func TestAnchoredRecoveryRejectsMalformedCommitEntry(t *testing.T) {
	primary, primaryKey := setup(t)
	recovery, _ := setup(t)
	anchor, _ := setup(t)
	recoveryKey := bytes.Repeat([]byte{9}, 32)
	j, err := OpenReplicatedAnchored(primary, recovery, anchor, primaryKey, recoveryKey, 8<<20)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := j.Append("mutation.v1", map[string]string{"status": "active"}); err != nil {
		t.Fatal(err)
	}
	if err := j.Close(); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(anchor, "commit-malformed"), nil, 0600); err != nil {
		t.Fatal(err)
	}
	if opened, err := OpenReplicatedAnchored(primary, recovery, anchor, primaryKey, recoveryKey, 8<<20); err == nil {
		_ = opened.Close()
		t.Fatal("malformed commit entry accepted")
	}
}

func TestAnchoredAppendFailsClosedWhenAnchorUnavailable(t *testing.T) {
	primary, primaryKey := setup(t)
	recovery, _ := setup(t)
	anchor, _ := setup(t)
	j, err := OpenReplicatedAnchored(primary, recovery, anchor, primaryKey, bytes.Repeat([]byte{9}, 32), 8<<20)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.RemoveAll(anchor); err != nil {
		t.Fatal(err)
	}
	if _, err := j.Append("mutation.v1", map[string]string{"status": "active"}); err == nil || j.Healthy() {
		t.Fatal("mutation acknowledged without independent anchor")
	}
	if err := j.Close(); err != nil {
		t.Fatal(err)
	}
}

func TestAnchoredRecoveryRejectsUnsafeConfiguration(t *testing.T) {
	primary, primaryKey := setup(t)
	recovery, _ := setup(t)
	anchor, _ := setup(t)
	if opened, err := OpenReplicatedAnchored(primary, recovery, "relative-anchor", primaryKey, bytes.Repeat([]byte{9}, 32), 8<<20); err == nil {
		_ = opened.Close()
		t.Fatal("relative anchor accepted")
	}
	if opened, err := OpenReplicatedAnchored(primary, recovery, anchor, primaryKey, []byte("short"), 8<<20); err == nil {
		_ = opened.Close()
		t.Fatal("invalid recovery key accepted")
	}
}

func TestSeedReplicatedRefusesRunningPrimary(t *testing.T) {
	primary, primaryKey := setup(t)
	recovery, _ := setup(t)
	anchor, _ := setup(t)
	j, err := Open(primary, primaryKey, 8<<20)
	if err != nil {
		t.Fatal(err)
	}
	defer j.Close()
	if _, err := j.Append("mutation.v1", map[string]string{"status": "active"}); err != nil {
		t.Fatal(err)
	}
	if err := SeedReplicated(primary, recovery, anchor, primaryKey, bytes.Repeat([]byte{9}, 32), 8<<20); err == nil {
		t.Fatal("migration ran alongside primary writer")
	}
}

func TestSeedReplicatedRejectsWrongRecoveryKey(t *testing.T) {
	primary, primaryKey := setup(t)
	recovery, _ := setup(t)
	anchor, _ := setup(t)
	j, err := Open(primary, primaryKey, 8<<20)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := j.Append("mutation.v1", map[string]string{"status": "active"}); err != nil {
		t.Fatal(err)
	}
	if err := j.Close(); err != nil {
		t.Fatal(err)
	}
	key := bytes.Repeat([]byte{9}, 32)
	if err := SeedReplicated(primary, recovery, anchor, primaryKey, key, 8<<20); err != nil {
		t.Fatal(err)
	}
	if err := SeedReplicated(primary, recovery, anchor, primaryKey, bytes.Repeat([]byte{8}, 32), 8<<20); err == nil {
		t.Fatal("wrong recovery key accepted during migration retry")
	}
}

func TestReplicatedAnchorAcceptsCompressedAuditOnRestart(t *testing.T) {
	primary, key := setup(t)
	recovery, _ := setup(t)
	anchor, _ := setup(t)
	recoveryKey := bytes.Repeat([]byte{9}, 32)
	payload := strings.Repeat("audit", 1024)
	j, err := OpenReplicatedAnchored(primary, recovery, anchor, key, recoveryKey, 2<<20)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := j.Append("audit", map[string]string{"payload": payload}); err != nil {
		t.Fatal(err)
	}
	if err := j.Close(); err != nil {
		t.Fatal(err)
	}
	j, err = OpenReplicatedAnchored(primary, recovery, anchor, key, recoveryKey, 2<<20)
	if err != nil {
		t.Fatal(err)
	}
	defer j.Close()
	if len(j.Records()) != 1 || !bytes.Contains(j.Records()[0].Data, []byte(payload)) {
		t.Fatal("compressed audit missing after anchored replay")
	}
}
