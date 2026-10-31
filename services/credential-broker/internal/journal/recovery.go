package journal

import (
	"bytes"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"github.com/letya999/credential-broker/internal/seal"
	"github.com/letya999/credential-broker/internal/securefs"
	"github.com/letya999/credential-broker/internal/strictjson"
)

type recoveryAnchor struct {
	dir   string
	box   *seal.Box
	store string
}

type currentHead struct {
	Format   int    `json:"format"`
	Store    string `json:"store"`
	Sequence uint64 `json:"sequence"`
	Digest   string `json:"digest"`
}

func newAnchor(dir string, primaryIdentity, recoveryKey []byte) (*recoveryAnchor, error) {
	box, err := seal.New(seal.Derive(recoveryKey, "journal-anchor"))
	if err != nil {
		return nil, err
	}
	sum := sha256.Sum256(recoveryKey)
	id := seal.Derive(primaryIdentity, "journal-store/v2/"+hex.EncodeToString(sum[:]))
	return &recoveryAnchor{dir: dir, box: box, store: hex.EncodeToString(id)}, nil
}

func (a *recoveryAnchor) marker() (string, error) {
	enc, err := securefs.Read(a.dir, "initialized", 256)
	if err != nil {
		return "", err
	}
	plain, err := a.box.Open(enc, []byte("initialized"))
	if err != nil {
		return "", ErrUnavailable
	}
	return string(plain), nil
}

// Persist the sealed temporary file before atomically replacing the current head.
func (a *recoveryAnchor) replace(name string, data []byte) error {
	var nonce [16]byte
	if _, err := rand.Read(nonce[:]); err != nil {
		return err
	}
	temp := "head-" + hex.EncodeToString(nonce[:]) + ".tmp"
	if err := securefs.Create(a.dir, temp, data, 0600); err != nil {
		return err
	}
	defer func() { _ = os.Remove(filepath.Join(a.dir, temp)) }()
	if err := os.Rename(filepath.Join(a.dir, temp), filepath.Join(a.dir, name)); err != nil {
		return err
	}
	return securefs.SyncDir(a.dir)
}

func (a *recoveryAnchor) markInitialized() error {
	enc, err := a.box.Seal([]byte("v2:"+a.store), []byte("initialized"))
	if err != nil {
		return err
	}
	return a.replace("initialized", enc)
}

func (a *recoveryAnchor) writeHead(seq uint64, hash string) error {
	plain, err := json.Marshal(currentHead{2, a.store, seq, hash})
	if err != nil {
		return err
	}
	enc, err := a.box.Seal(plain, []byte("journal-current-head/v2/"+a.store))
	if err != nil {
		return err
	}
	return a.replace("current-head", enc)
}

func (a *recoveryAnchor) current() (uint64, string, error) {
	enc, err := securefs.Read(a.dir, "current-head", 1024)
	if err != nil {
		return 0, "", ErrUnavailable
	}
	plain, err := a.box.Open(enc, []byte("journal-current-head/v2/"+a.store))
	if err != nil {
		return 0, "", ErrUnavailable
	}
	var h currentHead
	if err := strictjson.Decode(plain, &h); err != nil || h.Format != 2 || h.Store != a.store {
		return 0, "", ErrUnavailable
	}
	if h.Sequence == 0 {
		if h.Digest != "" {
			return 0, "", ErrUnavailable
		}
	} else {
		if len(h.Digest) != 64 {
			return 0, "", ErrUnavailable
		}
		if _, err := hex.DecodeString(h.Digest); err != nil {
			return 0, "", ErrUnavailable
		}
	}
	return h.Sequence, h.Digest, nil
}

func (j *Journal) truncateTo(seq uint64) error {
	if seq >= j.seq {
		return ErrUnavailable
	}
	size := int64(0)
	prev := ""
	if seq > 0 {
		size = j.offsets[seq-1]
		prev = j.records[seq].Previous
	}
	if err := j.file.Truncate(size); err != nil {
		return err
	}
	if err := j.file.Sync(); err != nil {
		return err
	}
	if _, err := j.file.Seek(size, io.SeekStart); err != nil {
		return err
	}
	j.records = j.records[:seq]
	j.offsets = j.offsets[:seq]
	j.seq, j.prev, j.size = seq, prev, size
	return nil
}

func (a *recoveryAnchor) latest() (uint64, string, error) {
	entries, err := os.ReadDir(a.dir)
	if err != nil {
		return 0, "", err
	}
	var latest uint64
	for _, entry := range entries {
		if !strings.HasPrefix(entry.Name(), "commit-") {
			continue
		}
		var seq uint64
		if entry.IsDir() || len(entry.Name()) != len("commit-00000000000000000000") {
			return 0, "", ErrUnavailable
		}
		if _, err := fmt.Sscanf(entry.Name(), "commit-%020d", &seq); err != nil || fmt.Sprintf("commit-%020d", seq) != entry.Name() {
			return 0, "", ErrUnavailable
		}
		if seq > latest {
			latest = seq
		}
	}
	if latest == 0 {
		return 0, "", nil
	}
	name := fmt.Sprintf("commit-%020d", latest)
	enc, err := securefs.Read(a.dir, name, 256)
	if err != nil {
		return 0, "", ErrUnavailable
	}
	plain, err := a.box.Open(enc, []byte(name))
	if err != nil || len(plain) != 64 {
		return 0, "", ErrUnavailable
	}
	if _, err := hex.DecodeString(string(plain)); err != nil {
		return 0, "", ErrUnavailable
	}
	return latest, string(plain), nil
}

// OpenReplicated keeps an independently encrypted durable recovery journal.
func OpenReplicated(primaryDir, recoveryDir string, primaryKey, recoveryKey []byte, limit int64) (*Journal, error) {
	return openReplicated(primaryDir, recoveryDir, primaryKey, recoveryKey, limit, nil)
}

func openReplicated(primaryDir, recoveryDir string, primaryKey, recoveryKey []byte, limit int64, anchor *recoveryAnchor) (*Journal, error) {
	recovery, err := openJournal(recoveryDir, recoveryKey, limit, false)
	if err != nil {
		return nil, err
	}
	primary, err := openJournal(primaryDir, primaryKey, limit, false)
	if err != nil {
		_ = recovery.Close()
		return nil, err
	}
	fail := func(err error) (*Journal, error) { _ = primary.Close(); _ = recovery.Close(); return nil, err }
	if anchor != nil {
		marker, markerErr := anchor.marker()
		if os.IsNotExist(markerErr) && primary.seq == 0 && recovery.seq == 0 {
			entries, err := os.ReadDir(anchor.dir)
			if err != nil || len(entries) != 0 {
				return fail(ErrUnavailable)
			}
			if err = anchor.writeHead(0, ""); err == nil {
				err = anchor.markInitialized()
			}
			if err != nil {
				return fail(ErrUnavailable)
			}
			marker, markerErr = anchor.marker()
		}
		latest, hash, err := anchor.current()
		if markerErr != nil || marker != "v2:"+anchor.store || err != nil || latest > recovery.seq {
			return fail(ErrUnavailable)
		}
		if latest > 0 {
			plain, err := json.Marshal(recovery.records[latest-1])
			if err != nil {
				return fail(ErrUnavailable)
			}
			sum := sha256.Sum256(plain)
			if hex.EncodeToString(sum[:]) != hash {
				return fail(ErrUnavailable)
			}
		}
		// Validate committed prefixes before any destructive tail recovery.
		upto := primary.seq
		if upto > latest {
			upto = latest
		}
		for i := uint64(0); i < upto; i++ {
			p, r := primary.records[i], recovery.records[i]
			if p.Sequence != r.Sequence || p.Previous != r.Previous || p.Kind != r.Kind || !bytes.Equal(p.Data, r.Data) {
				return fail(ErrUnavailable)
			}
		}
		if recovery.seq > latest {
			if err := recovery.truncateTo(latest); err != nil {
				return fail(ErrUnavailable)
			}
		}
		if primary.seq > latest {
			if err := primary.truncateTo(latest); err != nil {
				return fail(ErrUnavailable)
			}
		}
	}
	validPrefix := len(primary.records) <= len(recovery.records)
	if validPrefix {
		for i, record := range primary.records {
			other := recovery.records[i]
			if record.Sequence != other.Sequence || record.Previous != other.Previous || record.Kind != other.Kind || !bytes.Equal(record.Data, other.Data) {
				validPrefix = false
				break
			}
		}
	}
	if !validPrefix {
		return fail(ErrUnavailable)
	}
	for _, record := range recovery.records {
		if record.Sequence <= primary.seq {
			continue
		}
		if _, err := primary.Append(record.Kind, record.Data); err != nil {
			return fail(err)
		}
	}
	primary.recovery = recovery
	primary.anchor = anchor
	return primary, nil
}

func OpenReplicatedAnchored(primaryDir, recoveryDir, anchorDir string, primaryKey, recoveryKey []byte, limit int64) (*Journal, error) {
	if err := securefs.Dir(anchorDir); err != nil {
		return nil, err
	}
	anchor, err := newAnchor(anchorDir, seal.Derive(primaryKey, "journal-store-key/v2"), recoveryKey)
	if err != nil {
		return nil, err
	}
	// Refuse a lost/corrupt initialized head before opening or creating ledgers.
	marker, markerErr := anchor.marker()
	if markerErr == nil {
		if marker != "v2:"+anchor.store {
			return nil, ErrUnavailable
		}
		if _, _, err := anchor.current(); err != nil {
			return nil, ErrUnavailable
		}
	} else if !os.IsNotExist(markerErr) {
		return nil, ErrUnavailable
	}

	return openReplicated(primaryDir, recoveryDir, primaryKey, recoveryKey, limit, anchor)
}

func SeedReplicated(primaryDir, recoveryDir, anchorDir string, primaryKey, recoveryKey []byte, limit int64) error {
	primary, err := openJournal(primaryDir, primaryKey, limit, false)
	if err != nil {
		return err
	}
	defer primary.Close()
	return SeedReplicatedFromOpen(primary, recoveryDir, anchorDir, recoveryKey, limit)
}

func SeedReplicatedFromOpen(primary *Journal, recoveryDir, anchorDir string, recoveryKey []byte, limit int64) error {
	primary.mu.Lock()
	defer primary.mu.Unlock()
	if err := securefs.Dir(anchorDir); err != nil {
		return err
	}
	// Seeding installs new recovery copies; legacy-head adoption is separate.
	anchor, err := newAnchor(anchorDir, primary.identity, recoveryKey)
	if err != nil {
		return err
	}
	marker, markerErr := anchor.marker()
	fresh := os.IsNotExist(markerErr)
	if fresh {
		entries, err := os.ReadDir(anchorDir)
		if err != nil {
			return ErrUnavailable
		}
		if len(entries) != 0 {
			seq, hash, err := anchor.current()
			if err != nil || len(entries) != 1 || entries[0].Name() != "current-head" || seq != primary.seq || hash != primary.prev {
				return ErrUnavailable
			}
		}
	} else if markerErr != nil || marker != "v2:"+anchor.store {
		return ErrUnavailable
	}
	recovery, err := openJournal(recoveryDir, recoveryKey, limit, false)
	if err != nil {
		return err
	}
	defer recovery.Close()
	latest, hash := uint64(0), ""
	if !fresh {
		latest, hash, err = anchor.current()
	}
	if err != nil || latest > primary.seq || len(recovery.records) > len(primary.records) {
		return ErrUnavailable
	}
	if latest > 0 {
		plain, err := json.Marshal(primary.records[latest-1])
		if err != nil {
			return ErrUnavailable
		}
		sum := sha256.Sum256(plain)
		if hex.EncodeToString(sum[:]) != hash {
			return ErrUnavailable
		}
	}
	for i, record := range recovery.records {
		other := primary.records[i]
		if record.Sequence != other.Sequence || record.Previous != other.Previous || record.Kind != other.Kind || !bytes.Equal(record.Data, other.Data) {
			return ErrUnavailable
		}
	}
	for _, record := range primary.records[len(recovery.records):] {
		if _, err := recovery.Append(record.Kind, record.Data); err != nil {
			return err
		}
	}
	if fresh || primary.seq > latest {
		if err := anchor.writeHead(primary.seq, primary.prev); err != nil {
			return err
		}
		if fresh {
			return anchor.markInitialized()
		}
	}
	return nil
}
