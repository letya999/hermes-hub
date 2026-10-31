package journal

import (
	"bytes"
	"os"
	"path/filepath"
	"syscall"

	"github.com/letya999/credential-broker/internal/seal"
	"github.com/letya999/credential-broker/internal/securefs"
)

// MigrateHead upgrades only a quiesced, matching legacy set. Ledgers and old
// commit evidence remain unchanged; the operator must retain a verified backup.
func MigrateHead(primaryDir, recoveryDir, anchorDir string, primaryKey, recoveryKey []byte, limit int64) error {
	// Unlike normal recovery, migration must not create a missing source ledger.
	for _, dir := range []string{primaryDir, recoveryDir, anchorDir} {
		if info, err := os.Lstat(dir); err != nil || !info.IsDir() {
			return ErrUnavailable
		}
		if err := securefs.Dir(dir); err != nil {
			return err
		}
	}
	for _, dir := range []string{primaryDir, recoveryDir} {
		f, err := securefs.Open(dir, "ledger.bin", syscall.O_RDONLY, 0)
		if err != nil {
			return ErrUnavailable
		}
		if err = f.Close(); err != nil {
			return err
		}
	}
	recovery, err := openJournal(recoveryDir, recoveryKey, limit, false)
	if err != nil {
		return err
	}
	defer recovery.Close()
	primary, err := openJournal(primaryDir, primaryKey, limit, false)
	if err != nil {
		return err
	}
	defer primary.Close()
	if primary.seq != recovery.seq || primary.prev != recovery.prev {
		return ErrUnavailable
	}
	for i, p := range primary.records {
		r := recovery.records[i]
		if p.Sequence != r.Sequence || p.Previous != r.Previous || p.Kind != r.Kind || !bytes.Equal(p.Data, r.Data) {
			return ErrUnavailable
		}
	}
	anchor, err := newAnchor(anchorDir, seal.Derive(primaryKey, "journal-store-key/v2"), recoveryKey)
	if err != nil {
		return err
	}
	marker, markerErr := anchor.marker()
	if markerErr == nil && marker == "v2:"+anchor.store {
		seq, hash, err := anchor.current()
		if err != nil || seq != primary.seq || hash != primary.prev {
			return ErrUnavailable
		}
		return nil
	}
	if markerErr != nil && !os.IsNotExist(markerErr) || markerErr == nil && marker != "v1" {
		return ErrUnavailable
	}
	seq, hash, err := anchor.latest()
	if err != nil || seq != primary.seq || hash != primary.prev || seq == 0 && marker != "v1" {
		return ErrUnavailable
	}
	// Resume only a fully verified staged head, never a conflicting candidate.
	if _, err := os.Lstat(filepath.Join(anchorDir, "current-head")); err == nil {
		seq, hash, err := anchor.current()
		if err != nil || seq != primary.seq || hash != primary.prev {
			return ErrUnavailable
		}
	} else if !os.IsNotExist(err) {
		return ErrUnavailable
	}
	if err := anchor.writeHead(primary.seq, primary.prev); err != nil {
		return err
	}
	if seq, hash, err := anchor.current(); err != nil || seq != primary.seq || hash != primary.prev {
		return ErrUnavailable
	}
	return anchor.markInitialized()
}
