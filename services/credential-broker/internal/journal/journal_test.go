package journal

import (
	"bytes"
	"encoding/binary"
	"encoding/json"
	"os"
	"path/filepath"
	"sync"
	"testing"
)

func setup(t *testing.T) (string, []byte) {
	t.Helper()
	d := t.TempDir()
	_ = os.Chmod(d, 0700)
	return d, bytes.Repeat([]byte{6}, 32)
}
func TestDurabilityAndExclusiveWriter(t *testing.T) {
	d, k := setup(t)
	j, e := Open(d, k, 8<<20)
	if e != nil {
		t.Fatal(e)
	}
	if _, e := Open(d, k, 8<<20); e == nil {
		t.Fatal("two writers")
	}
	var wg sync.WaitGroup
	for i := 0; i < 20; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			if _, e := j.Append("test", map[string]int{"i": i}); e != nil {
				t.Error(e)
			}
		}(i)
	}
	wg.Wait()
	if len(j.Records()) != 20 || !j.Healthy() {
		t.Fatal("records")
	}
	r := j.Records()
	r[0].Data[0] = 'X'
	if j.Records()[0].Data[0] == 'X' {
		t.Fatal("mutable replay view")
	}
	if _, e := j.Append("bad", make(chan int)); e == nil {
		t.Fatal("marshal")
	}
	if _, e := j.Append("", 1); e == nil {
		t.Fatal("empty kind")
	}
	if _, e := j.Append("big", string(bytes.Repeat([]byte{'a'}, MaxRecord))); e == nil {
		t.Fatal("record bound")
	}
	if e = j.Close(); e != nil {
		t.Fatal(e)
	}
	if j.Healthy() {
		t.Fatal("closed health")
	}
	if j.Close() != nil {
		t.Fatal("double close")
	}
	if _, e = j.Append("closed", 1); e == nil {
		t.Fatal("closed append")
	}
	j, e = Open(d, k, 8<<20)
	if e != nil {
		t.Fatal(e)
	}
	defer j.Close()
	if len(j.Records()) != 20 {
		t.Fatal("lost acknowledged records")
	}
	if _, e = j.Append("next", map[string]string{"secret": "canary-only"}); e != nil {
		t.Fatal(e)
	}
	disk, _ := os.ReadFile(filepath.Join(d, "ledger.bin"))
	if bytes.Contains(disk, []byte("canary-only")) {
		t.Fatal("plaintext")
	}
}
func TestTamperAndTruncation(t *testing.T) {
	d, k := setup(t)
	j, e := Open(d, k, 4<<20)
	if e != nil {
		t.Fatal(e)
	}
	_, _ = j.Append("test", map[string]int{"x": 1})
	_ = j.Close()
	original, _ := os.ReadFile(filepath.Join(d, "ledger.bin"))
	for _, cut := range []int{1, 2, 3, 4, 10, len(original) - 1} {
		t.Run(string(rune(cut)), func(t *testing.T) {
			dir := t.TempDir()
			_ = os.Chmod(dir, 0700)
			_ = os.WriteFile(filepath.Join(dir, "ledger.bin"), original[:cut], 0600)
			if j, e := Open(dir, k, 4<<20); e == nil {
				_ = j.Close()
				t.Fatal("truncated record accepted")
			}
		})
	}
	variants := [][]byte{append([]byte(nil), original...), append([]byte(nil), original...)}
	variants[0][len(original)-1] ^= 2
	binary.BigEndian.PutUint32(variants[1], MaxRecord+1)
	for _, v := range variants {
		dir := t.TempDir()
		_ = os.Chmod(dir, 0700)
		_ = os.WriteFile(filepath.Join(dir, "ledger.bin"), v, 0600)
		if j, e := Open(dir, k, 4<<20); e == nil {
			_ = j.Close()
			t.Fatal("tamper accepted")
		}
	}
	if j, e := Open(d, bytes.Repeat([]byte{9}, 32), 4<<20); e == nil {
		_ = j.Close()
		t.Fatal("wrong key")
	}
	// AEAD-valid but noncontiguous sequence also fails replay.
	raw, _ := json.Marshal(Record{Sequence: 9, Kind: "test", Data: json.RawMessage(`{}`)})
	box := j.box
	enc, _ := box.Seal(raw, []byte("ledger/v1"))
	frame := make([]byte, 4)
	binary.BigEndian.PutUint32(frame, uint32(len(enc)))
	frame = append(frame, enc...)
	dir := t.TempDir()
	_ = os.Chmod(dir, 0700)
	_ = os.WriteFile(filepath.Join(dir, "ledger.bin"), frame, 0600)
	if v, e := Open(dir, k, 4<<20); e == nil {
		_ = v.Close()
		t.Fatal("bad sequence")
	}
}
func TestFailClosedOnStorageFailure(t *testing.T) {
	d, k := setup(t)
	for _, limit := range []int64{0, 1, 1 << 31} {
		if _, e := Open(d, k, limit); e == nil {
			t.Fatal("limit")
		}
	}
	if _, e := Open(d, []byte{1}, 2<<20); e == nil {
		t.Fatal("key")
	}
	j, e := Open(d, k, MaxRecord)
	if e != nil {
		t.Fatal(e)
	}
	_, _ = j.Append("fills", string(bytes.Repeat([]byte{'x'}, MaxRecord-400)))
	if _, e = j.Append("over", string(bytes.Repeat([]byte{'x'}, 1000))); e == nil {
		t.Fatal("size cap")
	}
	if j.Healthy() {
		t.Fatal("capacity failure must poison")
	}
	_ = j.Close()
	dir, k := setup(t)
	j, e = Open(dir, k, 4<<20)
	if e != nil {
		t.Fatal(e)
	}
	_ = j.file.Close()
	if _, e = j.Append("diskfail", 1); e == nil || j.Healthy() {
		t.Fatal("write failure")
	}
	_ = j.Close()
	dir, k = setup(t)
	_ = os.WriteFile(filepath.Join(dir, "ledger.bin"), nil, 0644)
	if _, e = Open(dir, k, 4<<20); e == nil {
		t.Fatal("unsafe permissions")
	}
}
