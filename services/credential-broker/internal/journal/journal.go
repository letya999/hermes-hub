// Package journal provides a bounded, encrypted, hash-chained append journal.
// Single writer, fsync before acknowledgement, strict replay; no silent repair.
package journal

import (
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"os"
	"sync"
	"syscall"

	"github.com/letya999/credential-broker/internal/seal"
	"github.com/letya999/credential-broker/internal/securefs"
	"github.com/letya999/credential-broker/internal/strictjson"
)

const MaxRecord = 1 << 20

var ErrUnavailable = errors.New("journal unavailable")

type Record struct {
	Sequence uint64          `json:"sequence"`
	Previous string          `json:"previous"`
	Kind     string          `json:"kind"`
	Data     json.RawMessage `json:"data"`
}
type Journal struct {
	mu       sync.Mutex
	file     *os.File
	box      *seal.Box
	seq      uint64
	prev     string
	size     int64
	limit    int64
	poisoned bool
	records  []Record
}

func Open(dir string, key []byte, limit int64) (*Journal, error) {
	if limit < MaxRecord || limit > 1<<30 {
		return nil, errors.New("invalid journal limit")
	}
	if err := securefs.Dir(dir); err != nil {
		return nil, err
	}
	box, err := seal.New(key)
	if err != nil {
		return nil, err
	}
	f, err := securefs.Open(dir, "ledger.bin", syscall.O_RDWR|syscall.O_CREAT, 0600)
	if err != nil {
		return nil, err
	}
	fail := func(err error) (*Journal, error) { _ = f.Close(); return nil, err }
	if err := syscall.Flock(int(f.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
		return fail(ErrUnavailable)
	}
	fi, err := f.Stat()
	if err != nil {
		return fail(err)
	}
	if fi.Mode().Perm()&0077 != 0 || fi.Size() > limit {
		return fail(ErrUnavailable)
	}
	j := &Journal{file: f, box: box, limit: limit}
	for {
		var hdr [4]byte
		n, err := io.ReadFull(f, hdr[:])
		if err == io.EOF && n == 0 {
			break
		}
		if err != nil {
			return fail(ErrUnavailable)
		}
		size := binary.BigEndian.Uint32(hdr[:])
		if size < 28 || size > MaxRecord {
			return fail(ErrUnavailable)
		}
		data := make([]byte, size)
		if _, err := io.ReadFull(f, data); err != nil {
			return fail(ErrUnavailable)
		}
		plain, err := box.Open(data, []byte("ledger/v1"))
		if err != nil {
			return fail(ErrUnavailable)
		}
		var r Record
		if err := strictjson.Decode(plain, &r); err != nil || r.Sequence != j.seq+1 || r.Previous != j.prev || r.Kind == "" {
			return fail(ErrUnavailable)
		}
		h := sha256.Sum256(plain)
		j.prev = hex.EncodeToString(h[:])
		j.seq = r.Sequence
		j.size += int64(size) + 4
		j.records = append(j.records, r)
	}
	if err := securefs.SyncDir(dir); err != nil {
		return fail(err)
	}
	return j, nil
}

func (j *Journal) Records() []Record {
	j.mu.Lock()
	defer j.mu.Unlock()
	out := make([]Record, len(j.records))
	for i, r := range j.records {
		out[i] = r
		out[i].Data = append(json.RawMessage(nil), r.Data...)
	}
	return out
}

func (j *Journal) Append(kind string, data any) (uint64, error) {
	j.mu.Lock()
	defer j.mu.Unlock()
	if j.poisoned || j.file == nil || kind == "" {
		return 0, ErrUnavailable
	}
	raw, err := json.Marshal(data)
	if err != nil {
		return 0, err
	}
	r := Record{j.seq + 1, j.prev, kind, raw}
	plain, err := json.Marshal(r)
	if err != nil {
		return 0, err
	}
	if len(plain)+28 > MaxRecord {
		return 0, errors.New("record too large")
	}
	enc, err := j.box.Seal(plain, []byte("ledger/v1"))
	if err != nil {
		return 0, err
	}
	if j.size+int64(len(enc))+4 > j.limit {
		j.poisoned = true
		return 0, ErrUnavailable
	}
	buf := make([]byte, 4, len(enc)+4)
	binary.BigEndian.PutUint32(buf, uint32(len(enc)))
	buf = append(buf, enc...)
	n, err := j.file.Write(buf)
	if err != nil || n != len(buf) {
		j.poisoned = true
		return 0, ErrUnavailable
	}
	if err := j.file.Sync(); err != nil {
		j.poisoned = true
		return 0, ErrUnavailable
	}
	h := sha256.Sum256(plain)
	j.prev = hex.EncodeToString(h[:])
	j.seq = r.Sequence
	j.size += int64(n)
	j.records = append(j.records, r)
	return r.Sequence, nil
}
func (j *Journal) Healthy() bool {
	j.mu.Lock()
	defer j.mu.Unlock()
	return !j.poisoned && j.file != nil
}
func (j *Journal) Close() error {
	j.mu.Lock()
	defer j.mu.Unlock()
	if j.file == nil {
		return nil
	}
	err := j.file.Close()
	j.file = nil
	return err
}
