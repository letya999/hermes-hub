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

// ErrFull is the visible admission stop: an audit write would push the hot
// audit table past its configured cap. It refuses the commit without
// poisoning the journal — reads keep working and the store resumes once a
// retention pass frees headroom (SPEC-0087 R5.3).
var ErrFull = errors.New("journal full")

// maxAuditHotCap bounds the hot-audit policy: retention is always finite and
// never accepts an unlimited value (SPEC-0087 R5).
const maxAuditHotCap = 1 << 30

type Record struct {
	Sequence uint64          `json:"sequence"`
	Previous string          `json:"previous"`
	Kind     string          `json:"kind"`
	Data     json.RawMessage `json:"data"`
}
type Journal struct {
	retention  AuditRetention
	retainR    RecordsRetention
	auditBytes int64
	mu         sync.Mutex
	file       *os.File
	lock       *os.File
	legacy     bool
	box        *seal.Box
	identity   []byte
	seq        uint64
	prev       string
	size       int64
	limit      int64
	poisoned   bool
	records    []Record
	offsets    []int64
	recovery   *Journal
	anchor     *recoveryAnchor
	sql        *sqlStore
}

func Open(dir string, key []byte, limit int64) (*Journal, error) {
	if j, handled, err := sqlDispatchSingle(dir, key, limit); handled {
		return j, err
	}
	return openJournal(dir, key, limit, true)
}

func openJournal(dir string, key []byte, limit int64, compact bool) (*Journal, error) {
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
	j := &Journal{file: f, lock: lock, box: box, identity: seal.Derive(key, "journal-store-key/v2"), limit: limit}
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
	if compact && j.legacy && j.size > j.limit/3 {
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
	if j.sql != nil {
		return j.sqlRecords()
	}
	out := make([]Record, len(j.records))
	for i, r := range j.records {
		out[i] = r
		out[i].Data = append(json.RawMessage(nil), r.Data...)
	}
	return out
}

func (j *Journal) Append(kind string, data any) (uint64, error) {
	return j.append(kind, data, nil)
}

func (j *Journal) append(kind string, data any, aux *AuxWrites) (uint64, error) {
	j.mu.Lock()
	defer j.mu.Unlock()
	if j.poisoned || (j.file == nil && j.sql == nil) || kind == "" {
		return 0, ErrUnavailable
	}
	if err := sqlValidateAux(aux); err != nil {
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
	if j.sql != nil {
		return j.appendSQL(r, plain, aux)
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
		if err := j.anchor.writeHead(r.Sequence, hex.EncodeToString(h[:])); err != nil {
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

// AppendWith commits one record plus its normalized-table writes (state
// upserts and an audit row) in a single transaction on the sql backend. On a
// framed store the aux writes are dropped — there are no tables to receive
// them — and the append behaves exactly like Append.
func (j *Journal) AppendWith(kind string, data any, aux *AuxWrites) (uint64, error) {
	return j.append(kind, data, aux)
}

// SetAuditRetention wires the hot-audit policy onto the journal and reloads
// the live audit byte count so the admission gate starts accurate.
func (j *Journal) SetAuditRetention(r AuditRetention) {
	j.mu.Lock()
	defer j.mu.Unlock()
	j.retention = r
	j.auditBytes = j.sqlAuditBytes()
}

// SetRecordsRetention wires the history-rotation policy onto the journal.
// Destructive is the explicit authorization for prune+compact; without it
// rotation only exports sealed archives.
func (j *Journal) SetRecordsRetention(r RecordsRetention) {
	j.mu.Lock()
	defer j.mu.Unlock()
	j.retainR = r
}

// RecordsFloor returns the smallest record sequence still stored — the point
// replay re-anchors at after history rotation.
func (j *Journal) RecordsFloor() (uint64, error) {
	j.mu.Lock()
	defer j.mu.Unlock()
	floor, _ := sqlRecordsFloor(j.sql.db, "r")
	return floor, nil
}

// StoreIdentity returns the journal's derived store identity — the value
// archive keys and manifest seals are bound to.
func (j *Journal) StoreIdentity() []byte {
	j.mu.Lock()
	defer j.mu.Unlock()
	return append([]byte(nil), j.identity...)
}

// Normalized reports whether the normalized state/audit tables provably cover
// the journal head. Always false on framed stores.
func (j *Journal) Normalized() bool {
	j.mu.Lock()
	defer j.mu.Unlock()
	return j.sql != nil && j.sqlNormalized()
}

func (j *Journal) Healthy() bool {
	j.mu.Lock()
	defer j.mu.Unlock()
	return !j.poisoned && (j.file != nil || j.sql != nil)
}
func (j *Journal) Close() error {
	j.mu.Lock()
	defer j.mu.Unlock()
	if j.file == nil && j.sql == nil {
		return nil
	}
	var err error
	if j.file != nil {
		err = j.file.Close()
		j.file = nil
	}
	if j.sql != nil {
		err = errors.Join(err, j.sql.db.Close())
		if j.sql.recLock != nil {
			err = errors.Join(err, j.sql.recLock.Close())
		}
		j.sql = nil
	}
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
	fail := func(err error) error {
		_ = temp.Close()
		return err
	}
	defer func() { _ = os.Remove(filepath.Join(dir, name)) }()
	if err := syscall.Flock(int(temp.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
		return fail(err)
	}
	writer := bufio.NewWriterSize(temp, 256<<10)
	offsets := make([]int64, 0, len(j.records))
	var size int64
	prev := ""
	legacy := false
	for _, record := range j.records {
		plain, err := json.Marshal(record)
		if err != nil {
			return fail(err)
		}
		if record.Previous != prev {
			return fail(ErrUnavailable)
		}
		sum := sha256.Sum256(plain)
		prev = hex.EncodeToString(sum[:])
		frame, compressed, err := j.frame(plain)
		if err != nil {
			return fail(err)
		}
		if !compressed {
			legacy = true
		}
		if _, err := writer.Write(frame); err != nil {
			return fail(err)
		}
		size += int64(len(frame))
		offsets = append(offsets, size)
	}
	if prev != j.prev {
		_ = temp.Close()
		return ErrUnavailable
	}
	if size >= j.size {
		return fail(nil)
	}
	if err := writer.Flush(); err != nil {
		return fail(err)
	}
	if err := temp.Sync(); err != nil {
		return fail(err)
	}
	if _, err := temp.Seek(size, io.SeekStart); err != nil {
		return fail(err)
	}
	if err := os.Rename(filepath.Join(dir, name), filepath.Join(dir, "ledger.bin")); err != nil {
		return fail(err)
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
