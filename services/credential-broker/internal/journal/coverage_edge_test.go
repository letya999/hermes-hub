package journal

import (
	"bytes"
	"compress/zlib"
	"crypto/rand"
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
)

func TestOpenRejectsUnsafeFileShapes(t *testing.T) {
	for _, name := range []string{"ledger.lock", "ledger.bin"} {
		dir, key := setup(t)
		if err := os.Mkdir(filepath.Join(dir, name), 0700); err != nil {
			t.Fatal(err)
		}
		if j, err := Open(dir, key, 4<<20); err == nil {
			_ = j.Close()
			t.Fatalf("%s accepted as journal file", name)
		}
	}
	if _, err := Open("relative-journal", bytes.Repeat([]byte{6}, 32), 4<<20); err == nil {
		t.Fatal("relative journal dir accepted")
	}
}

func TestOpenRejectsExternallyLockedLedger(t *testing.T) {
	dir, key := setup(t)
	f, err := os.OpenFile(filepath.Join(dir, "ledger.bin"), os.O_RDWR|os.O_CREATE, 0600)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	if err := syscall.Flock(int(f.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
		t.Fatal(err)
	}
	if j, err := Open(dir, key, 4<<20); err == nil {
		_ = j.Close()
		t.Fatal("externally locked ledger accepted")
	}
}

func writeSealedFrame(t *testing.T, dir string, payload []byte, compressed bool) {
	t.Helper()
	j, err := Open(dir, bytes.Repeat([]byte{6}, 32), 8<<20)
	if err != nil {
		t.Fatal(err)
	}
	box := j.box
	if err := j.Close(); err != nil {
		t.Fatal(err)
	}
	enc, err := box.Seal(payload, []byte("ledger/v1"))
	if err != nil {
		t.Fatal(err)
	}
	header := uint32(len(enc))
	if compressed {
		header |= 1 << 31
	}
	buf := make([]byte, 4)
	binary.BigEndian.PutUint32(buf, header)
	buf = append(buf, enc...)
	if err := os.WriteFile(filepath.Join(dir, "ledger.bin"), buf, 0600); err != nil {
		t.Fatal(err)
	}
}

func TestOpenRejectsMalformedCompressedAudit(t *testing.T) {
	dir, key := setup(t)
	writeSealedFrame(t, dir, []byte("not a zlib stream"), true)
	if j, err := Open(dir, key, 8<<20); err == nil {
		_ = j.Close()
		t.Fatal("non-zlib compressed frame accepted")
	}
	dir, key = setup(t)
	var inflated bytes.Buffer
	writer := zlib.NewWriter(&inflated)
	if _, err := writer.Write(make([]byte, MaxRecord)); err != nil {
		t.Fatal(err)
	}
	if err := writer.Close(); err != nil {
		t.Fatal(err)
	}
	writeSealedFrame(t, dir, inflated.Bytes(), true)
	if j, err := Open(dir, key, 8<<20); err == nil {
		_ = j.Close()
		t.Fatal("decompression bomb accepted")
	}
}

func TestAppendFailsClosedWhenSyncFails(t *testing.T) {
	dir, key := setup(t)
	j, err := Open(dir, key, 8<<20)
	if err != nil {
		t.Fatal(err)
	}
	reader, writer, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	original := j.file
	j.file = writer
	if _, err := j.Append("mutation.v1", "payload"); err == nil || j.Healthy() {
		t.Fatal("append acknowledged without a durable sync")
	}
	j.file = original
	_ = writer.Close()
	_ = reader.Close()
	if err := j.Close(); err != nil {
		t.Fatal(err)
	}
}

func TestAppendFailsClosedWhenAuditCompactionFails(t *testing.T) {
	dir, key := setup(t)
	j, err := Open(dir, key, MaxRecord)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := j.Append("audit", "seed"); err != nil {
		t.Fatal(err)
	}
	j.legacy = true
	j.records[0].Previous = "corrupted"
	chunk := make([]byte, 64<<10)
	if _, err := rand.Read(chunk); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 20 && err == nil; i++ {
		_, err = j.Append("big", chunk)
	}
	if err == nil || j.Healthy() {
		t.Fatal("mutation acknowledged while the audit chain could not be rewritten")
	}
	if err := j.Close(); err != nil {
		t.Fatal(err)
	}
}

func TestCompactLockedFailsClosedOnChainHeadDrift(t *testing.T) {
	dir, key := setup(t)
	j, err := Open(dir, key, 8<<20)
	if err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 3; i++ {
		if _, err := j.Append("audit", strings.Repeat("a", 256)); err != nil {
			t.Fatal(err)
		}
	}
	j.records[len(j.records)-1].Data = json.RawMessage(`{"forged":true}`)
	if err := j.compactLocked(); err == nil {
		t.Fatal("chain head drift rewritten")
	}
	if err := j.Close(); err != nil {
		t.Fatal(err)
	}
}

func TestCompactLockedKeepsLedgerWhenRewriteDoesNotShrink(t *testing.T) {
	dir, key := setup(t)
	j, err := Open(dir, key, 8<<20)
	if err != nil {
		t.Fatal(err)
	}
	data := make([]byte, 512)
	if _, err := rand.Read(data); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 2; i++ {
		if _, err := j.Append("audit", data); err != nil {
			t.Fatal(err)
		}
	}
	size := j.size
	if err := j.compactLocked(); err != nil {
		t.Fatal(err)
	}
	if j.size != size {
		t.Fatal("rewrite swapped a ledger that did not shrink")
	}
	if err := j.Close(); err != nil {
		t.Fatal(err)
	}
}

func TestCompactLockedFailsClosedWhenLedgerPathIsOccupied(t *testing.T) {
	dir, key := setup(t)
	j, err := Open(dir, key, 8<<20)
	if err != nil {
		t.Fatal(err)
	}
	prev := ""
	for i := 0; i < 4; i++ {
		record := Record{Sequence: uint64(i + 1), Previous: prev, Kind: "audit", Data: json.RawMessage(fmt.Sprintf(`{"payload":%q}`, strings.Repeat("b", 1024)))}
		plain, _ := json.Marshal(record)
		enc, err := j.box.Seal(plain, []byte("ledger/v1"))
		if err != nil {
			t.Fatal(err)
		}
		var header [4]byte
		binary.BigEndian.PutUint32(header[:], uint32(len(enc)))
		if _, err := j.file.Write(header[:]); err != nil {
			t.Fatal(err)
		}
		if _, err := j.file.Write(enc); err != nil {
			t.Fatal(err)
		}
		sum := sha256.Sum256(plain)
		prev = hex.EncodeToString(sum[:])
	}
	if err := j.Close(); err != nil {
		t.Fatal(err)
	}
	j, err = Open(dir, key, 8<<20)
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, "ledger.bin")
	if err := os.Remove(path); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(path, 0700); err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(path)
	if err := j.compactLocked(); err == nil {
		t.Fatal("rewrite acknowledged over an occupied ledger path")
	}
	_ = j.Close()
}

func TestTruncateToRefusesUnsafeRequests(t *testing.T) {
	dir, key := setup(t)
	j, err := Open(dir, key, 8<<20)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := j.Append("mutation.v1", "active"); err != nil {
		t.Fatal(err)
	}
	if err := j.truncateTo(j.seq); err == nil {
		t.Fatal("truncate at the head accepted")
	}
	readOnly, err := os.Open(filepath.Join(dir, "ledger.bin"))
	if err != nil {
		t.Fatal(err)
	}
	original := j.file
	j.file = readOnly
	if err := j.truncateTo(0); err == nil {
		t.Fatal("truncate succeeded on a read-only ledger")
	}
	j.file = original
	_ = readOnly.Close()
	if err := j.Close(); err != nil {
		t.Fatal(err)
	}
}

func TestRecoveryAnchorRejectsUnsafeState(t *testing.T) {
	dir, key := setup(t)
	j, err := Open(dir, key, 8<<20)
	if err != nil {
		t.Fatal(err)
	}
	if err := j.Close(); err != nil {
		t.Fatal(err)
	}
	anchor, _ := setup(t)
	a := &recoveryAnchor{dir: anchor, box: j.box}
	if err := os.RemoveAll(anchor); err != nil {
		t.Fatal(err)
	}
	if _, _, err := a.latest(); err == nil {
		t.Fatal("anchor state read from a missing directory")
	}
	anchor, _ = setup(t)
	a = &recoveryAnchor{dir: anchor, box: j.box}
	if err := os.WriteFile(filepath.Join(anchor, "initialized"), make([]byte, 256), 0600); err != nil {
		t.Fatal(err)
	}
	if initialized, err := a.initialized(); err == nil || initialized {
		t.Fatal("oversized initialization marker accepted")
	}
	if err := os.Remove(filepath.Join(anchor, "initialized")); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"commit-abcdefghijklmnopqrst", "commit-00000000000000000001"} {
		if err := os.WriteFile(filepath.Join(anchor, name), make([]byte, 300), 0600); err != nil {
			t.Fatal(err)
		}
		if _, _, err := a.latest(); err == nil {
			t.Fatalf("unsafe commit entry %q accepted", name)
		}
		if err := os.Remove(filepath.Join(anchor, name)); err != nil {
			t.Fatal(err)
		}
	}
	enc, err := a.box.Seal([]byte(strings.Repeat("z", 64)), []byte("commit-00000000000000000001"))
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(anchor, "commit-00000000000000000001"), enc, 0600); err != nil {
		t.Fatal(err)
	}
	if _, _, err := a.latest(); err == nil {
		t.Fatal("non-hex commit hash accepted")
	}
}

func TestOpenReplicatedFailsClosedWhenPrimaryUnreadable(t *testing.T) {
	primary, primaryKey := setup(t)
	recovery, _ := setup(t)
	recoveryKey := bytes.Repeat([]byte{9}, 32)
	r, err := Open(recovery, recoveryKey, 8<<20)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := r.Append("mutation.v1", "active"); err != nil {
		t.Fatal(err)
	}
	if err := r.Close(); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(primary, "ledger.bin"), []byte("corrupt"), 0600); err != nil {
		t.Fatal(err)
	}
	if j, err := OpenReplicated(primary, recovery, primaryKey, recoveryKey, 8<<20); err == nil {
		_ = j.Close()
		t.Fatal("replication resumed over an unreadable primary")
	}
}

func TestSeedReplicatedFromOpenRejectsUnsafeAnchorDir(t *testing.T) {
	primary, primaryKey := setup(t)
	recovery, _ := setup(t)
	j, err := Open(primary, primaryKey, 8<<20)
	if err != nil {
		t.Fatal(err)
	}
	defer j.Close()
	if _, err := j.Append("mutation.v1", "active"); err != nil {
		t.Fatal(err)
	}
	if err := SeedReplicatedFromOpen(j, recovery, "relative-anchor", bytes.Repeat([]byte{9}, 32), 8<<20); err == nil {
		t.Fatal("migration accepted a relative anchor dir")
	}
}
