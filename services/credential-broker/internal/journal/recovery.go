package journal

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"strings"

	"github.com/letya999/credential-broker/internal/seal"
	"github.com/letya999/credential-broker/internal/securefs"
)

type recoveryAnchor struct {
	dir string
	box *seal.Box
}

func (a *recoveryAnchor) initialized() (bool, error) {
	enc, err := securefs.Read(a.dir, "initialized", 128)
	if os.IsNotExist(err) {
		return false, nil
	}
	if err != nil {
		return false, ErrUnavailable
	}
	plain, err := a.box.Open(enc, []byte("initialized"))
	if err != nil || string(plain) != "v1" {
		return false, ErrUnavailable
	}
	return true, nil
}

func (a *recoveryAnchor) markInitialized() error {
	enc, err := a.box.Seal([]byte("v1"), []byte("initialized"))
	if err != nil {
		return err
	}
	return securefs.Create(a.dir, "initialized", enc, 0600)
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

func (a *recoveryAnchor) commit(seq uint64, hash string) error {
	name := fmt.Sprintf("commit-%020d", seq)
	enc, err := a.box.Seal([]byte(hash), []byte(name))
	if err != nil {
		return err
	}
	return securefs.Create(a.dir, name, enc, 0600)
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
	recovery, err := Open(recoveryDir, recoveryKey, limit)
	if err != nil {
		return nil, err
	}
	primary, err := Open(primaryDir, primaryKey, limit)
	if err != nil {
		_ = recovery.Close()
		return nil, err
	}
	if anchor != nil {
		latest, hash, err := anchor.latest()
		initialized, initErr := anchor.initialized()
		if err != nil || initErr != nil || latest > recovery.seq || (latest == 0 && !initialized && (primary.seq > 0 || recovery.seq > 0)) {
			_ = primary.Close()
			_ = recovery.Close()
			return nil, ErrUnavailable
		}
		if latest == 0 && !initialized {
			if err := anchor.markInitialized(); err != nil {
				_ = primary.Close()
				_ = recovery.Close()
				return nil, ErrUnavailable
			}
		}
		if latest > 0 {
			plain, err := json.Marshal(recovery.records[latest-1])
			if err != nil {
				_ = primary.Close()
				_ = recovery.Close()
				return nil, ErrUnavailable
			}
			sum := sha256.Sum256(plain)
			if hex.EncodeToString(sum[:]) != hash {
				_ = primary.Close()
				_ = recovery.Close()
				return nil, ErrUnavailable
			}
		}
		if recovery.seq > latest {
			if err := recovery.truncateTo(latest); err != nil {
				_ = primary.Close()
				_ = recovery.Close()
				return nil, ErrUnavailable
			}
		}
		if primary.seq > latest {
			if err := primary.truncateTo(latest); err != nil {
				_ = primary.Close()
				_ = recovery.Close()
				return nil, ErrUnavailable
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
		_ = primary.Close()
		_ = recovery.Close()
		return nil, ErrUnavailable
	}
	for _, record := range recovery.records {
		if record.Sequence <= primary.seq {
			continue
		}
		if _, err := primary.Append(record.Kind, record.Data); err != nil {
			_ = primary.Close()
			_ = recovery.Close()
			return nil, err
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
	box, err := seal.New(seal.Derive(recoveryKey, "journal-anchor"))
	if err != nil {
		return nil, err
	}
	return openReplicated(primaryDir, recoveryDir, primaryKey, recoveryKey, limit, &recoveryAnchor{dir: anchorDir, box: box})
}

func SeedReplicated(primaryDir, recoveryDir, anchorDir string, primaryKey, recoveryKey []byte, limit int64) error {
	primary, err := Open(primaryDir, primaryKey, limit)
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
	box, err := seal.New(seal.Derive(recoveryKey, "journal-anchor"))
	if err != nil {
		return err
	}
	anchor := &recoveryAnchor{dir: anchorDir, box: box}
	recovery, err := Open(recoveryDir, recoveryKey, limit)
	if err != nil {
		return err
	}
	defer recovery.Close()
	latest, hash, err := anchor.latest()
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
	if primary.seq > latest {
		return anchor.commit(primary.seq, primary.prev)
	}
	return nil
}
