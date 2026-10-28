// Package journal provides a bounded, encrypted, hash-chained append journal.
// Single writer, fsync before acknowledgement, strict replay; no silent repair.
package journal

import (
	"bufio"
	"bytes"
	"compress/zlib"
	"crypto/rand"
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
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
	lock     *os.File
	legacy   bool
	box      *seal.Box
	seq      uint64
	prev     string
	size     int64
	limit    int64
	poisoned bool
	records  []Record
	offsets  []int64
	recovery *Journal
	anchor   *recoveryAnchor
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
	lock, err := securefs.Open(dir, "ledger.lock", syscall.O_RDWR|syscall.O_CREAT, 0600)
	if err != nil {
		return nil, err
	}
	if err := syscall.Flock(int(lock.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
		_ = lock.Close()
		return nil, ErrUnavailable
	}
	f, err := securefs.Open(dir, "ledger.bin", syscall.O_RDWR|syscall.O_CREAT, 0600)
	if err != nil {
		_ = lock.Close()
		return nil, err
	}
	fail := func(err error) (*Journal, error) { _ = f.Close(); _ = lock.Close(); return nil, err }
	// The ledger lock also fences an older process during a mixed rollout.
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
	j := &Journal{file: f, lock: lock, box: box, limit: limit}
	for {
		var hdr [4]byte
		n, err := io.ReadFull(f, hdr[:])
		if err == io.EOF && n == 0 {
			break
		}
		if err != nil {
			return fail(ErrUnavailable)
		}
		frameSize := binary.BigEndian.Uint32(hdr[:])
		compressed := frameSize&(1<<31) != 0
		size := frameSize &^ (1 << 31)
		if !compressed {
			j.legacy = true
		}
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
		if compressed {
			reader, err := zlib.NewReader(bytes.NewReader(plain))
			if err != nil {
				return fail(ErrUnavailable)
			}
			decoded, readErr := io.ReadAll(io.LimitReader(reader, MaxRecord+1))
			closeErr := reader.Close()
			if readErr != nil || closeErr != nil || len(decoded) > MaxRecord-28 {
				return fail(ErrUnavailable)
			}
			plain = decoded
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
		j.offsets = append(j.offsets, j.size)
	}
	if err := securefs.SyncDir(dir); err != nil {
		return fail(err)
	}
	if j.legacy && j.size > j.limit/3 {
		if err := j.compactLocked(); err != nil {
			_ = j.file.Close()
			_ = lock.Close()
			return nil, err
		}
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
	buf, compressed, err := j.frame(plain)
	if err != nil {
		return 0, err
	}
	if j.size+int64(len(buf)) > j.limit && j.legacy {
		if err := j.compactLocked(); err != nil {
			j.poisoned = true
			return 0, ErrUnavailable
		}
	}
	if j.size+int64(len(buf)) > j.limit {
		j.poisoned = true
		return 0, ErrUnavailable
	}
	// Persist recovery first so acknowledged revocations survive primary loss.
	if j.recovery != nil {
		if _, err := j.recovery.Append(kind, json.RawMessage(raw)); err != nil {
			j.poisoned = true
			return 0, ErrUnavailable
		}
	}
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
	if j.anchor != nil {
		if err := j.anchor.commit(r.Sequence, hex.EncodeToString(h[:])); err != nil {
			j.poisoned = true
			return 0, ErrUnavailable
		}
	}
	j.prev = hex.EncodeToString(h[:])
	j.seq = r.Sequence
	j.size += int64(n)
	j.records = append(j.records, r)
	j.offsets = append(j.offsets, j.size)
	if !compressed {
		j.legacy = true
	}
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
	if j.lock != nil {
		err = errors.Join(err, j.lock.Close())
		j.lock = nil
	}
	if j.recovery != nil {
		err = errors.Join(err, j.recovery.Close())
	}
	return err
}

// frame compresses the plaintext before encryption; the logical hash chain and
// independent recovery anchor still cover the exact original record bytes.
func (j *Journal) frame(plain []byte) ([]byte, bool, error) {
	var payload bytes.Buffer
	writer := zlib.NewWriter(&payload)
	if _, err := writer.Write(plain); err != nil {
		return nil, false, err
	}
	if err := writer.Close(); err != nil {
		return nil, false, err
	}
	compressed := payload.Len() < len(plain)
	source := plain
	if compressed {
		source = payload.Bytes()
	}
	enc, err := j.box.Seal(source, []byte("ledger/v1"))
	if err != nil {
		return nil, false, err
	}
	if len(enc) > MaxRecord {
		return nil, false, ErrUnavailable
	}
	buf := make([]byte, 4, len(enc)+4)
	length := uint32(len(enc)) // #nosec G115 -- MaxRecord bounds the encrypted frame above.
	if compressed {
		length |= 1 << 31
	}
	binary.BigEndian.PutUint32(buf, length)
	return append(buf, enc...), compressed, nil
}

// compactLocked losslessly rewrites legacy frames under the stable lock file.
// The replacement is fsynced before rename; a crash exposes either complete
// old or complete new history, never a partially rewritten audit.
func (j *Journal) compactLocked() error {
	dir := filepath.Dir(j.file.Name())
	var nonce [8]byte
	if _, err := rand.Read(nonce[:]); err != nil {
		return err
	}
	name := "ledger-" + hex.EncodeToString(nonce[:]) + ".compact"
	temp, err := securefs.Open(dir, name, syscall.O_RDWR|syscall.O_CREAT|syscall.O_EXCL, 0600)
	if err != nil {
		return err
	}
	defer func() { _ = os.Remove(filepath.Join(dir, name)) }()
	if err := syscall.Flock(int(temp.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
		_ = temp.Close()
		return err
	}
	writer := bufio.NewWriterSize(temp, 256<<10)
	offsets := make([]int64, 0, len(j.records))
	var size int64
	prev := ""
	legacy := false
	for _, record := range j.records {
		plain, err := json.Marshal(record)
		if err != nil {
			_ = temp.Close()
			return err
		}
		if record.Previous != prev {
			_ = temp.Close()
			return ErrUnavailable
		}
		sum := sha256.Sum256(plain)
		prev = hex.EncodeToString(sum[:])
		frame, compressed, err := j.frame(plain)
		if err != nil {
			_ = temp.Close()
			return err
		}
		if !compressed {
			legacy = true
		}
		if _, err := writer.Write(frame); err != nil {
			_ = temp.Close()
			return err
		}
		size += int64(len(frame))
		offsets = append(offsets, size)
	}
	if prev != j.prev {
		_ = temp.Close()
		return ErrUnavailable
	}
	if size >= j.size {
		_ = temp.Close()
		return nil
	}
	if err := writer.Flush(); err != nil {
		_ = temp.Close()
		return err
	}
	if err := temp.Sync(); err != nil {
		_ = temp.Close()
		return err
	}
	if _, err := temp.Seek(size, io.SeekStart); err != nil {
		_ = temp.Close()
		return err
	}
	if err := os.Rename(filepath.Join(dir, name), filepath.Join(dir, "ledger.bin")); err != nil {
		_ = temp.Close()
		return err
	}
	old := j.file
	j.file, j.size, j.offsets, j.legacy = temp, size, offsets, legacy
	_ = old.Close()
	if err := securefs.SyncDir(dir); err != nil {
		j.poisoned = true
		return err
	}
	return nil
}
