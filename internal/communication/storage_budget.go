package communication

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strconv"

	"github.com/letya999/hermes-hub/internal/storagebudget"
)

var errReadOnlySpool = errors.New("spool inspection cannot mutate storage")

// StorageStatus reports the owned-byte aggregates and the limits they are
// measured against — read-only accounting for operator surfaces.
func (s *Spool) StorageStatus() (storagebudget.Snapshot, storagebudget.Limits, error) {
	limits, err := spoolStorageLimits(s.limits)
	if err != nil {
		return storagebudget.Snapshot{}, storagebudget.Limits{}, err
	}
	if s.storage == nil {
		return storagebudget.Snapshot{}, limits, errors.New("spool storage unavailable")
	}
	return s.storage.Snapshot(), limits, nil
}

func spoolStorageLimits(s spoolLimits) (storagebudget.Limits, error) {
	for _, name := range []string{"HUB_SPOOL_MAX_BYTES", "HUB_SPOOL_MAX_OWNED_FILES", "HUB_SPOOL_MAX_NEXT_WRITE_BYTES", "HUB_SPOOL_LIFECYCLE_RESERVE_BYTES", "HUB_SPOOL_LIFECYCLE_RESERVE_FILES"} {
		if raw, ok := os.LookupEnv(name); ok {
			value, err := strconv.ParseInt(raw, 10, 64)
			if err != nil || value <= 0 {
				return storagebudget.Limits{}, storagebudget.ErrLimits
			}
		}
	}
	limits := storagebudget.Limits{Bytes: s.MaxBytes, Files: s.MaxOwnedFiles, NextWrite: s.MaxNextWrite, LifecycleBytes: s.LifecycleBytes, LifecycleFiles: s.LifecycleFiles}
	return limits, limits.Validate()
}

func (s *Spool) writeJSON(path string, value any) error {
	return s.writeJSONClass(path, value, true)
}

// storagePath returns the counter-accounted path for a write: record rows
// live inside spool.db, so their bytes debit the real file they occupy.
func (s *Spool) storagePath(path string) string {
	if _, ok := s.fsys.(*dbFS); ok && spoolRecordPath(s.root, path) {
		return filepath.Join(s.root, "spool.db")
	}
	return path
}

func (s *Spool) writeJSONClass(path string, value any, lifecycle bool) error {
	if s.readOnly {
		return errReadOnlySpool
	}
	if s.storage == nil {
		return atomicJSON(path, value)
	}
	body, err := json.Marshal(value)
	if err != nil {
		return err
	}
	return s.storage.Write(s.storagePath(path), int64(len(body)), lifecycle, func() error { return s.fsys.WriteFile(path, body) })
}
func (s *Spool) writeRunReceipt(path string, r streamReceipt) error {
	if s.readOnly {
		return errReadOnlySpool
	}
	if s.storage == nil {
		return writeRunReceipt(path, r)
	}
	body, err := json.Marshal(r)
	if err != nil {
		return err
	}
	if len(body) > 4<<20 {
		return errors.New("run receipt exceeds its record limit")
	}
	return s.storage.Write(s.storagePath(path), int64(len(body)), true, func() error { return s.fsys.WriteFile(path, body) })
}
func (s *Spool) writeBlob(path string, data []byte, lifecycle bool) error {
	if s.readOnly {
		return errReadOnlySpool
	}
	if s.storage == nil {
		return atomicBytes(path, data)
	}
	return s.storage.Write(s.storagePath(path), int64(len(data)), lifecycle, func() error { return s.fsys.WriteFile(path, data) })
}
func (s *Spool) mkdirOwned(path string) error {
	return s.mkdirOwnedClass(path, true)
}
func (s *Spool) mkdirOwnedClass(path string, lifecycle bool) error {
	if s.readOnly {
		return errReadOnlySpool
	}
	if s.storage == nil {
		return os.MkdirAll(path, 0700)
	}
	return s.storage.MkdirAll(path, lifecycle)
}
func (s *Spool) renameOwned(from, to string) error {
	if s.readOnly {
		return errReadOnlySpool
	}
	if spoolRecordPath(s.root, from) && spoolRecordPath(s.root, to) {
		return s.fsys.Rename(from, to)
	}
	if s.storage == nil {
		return os.Rename(from, to)
	}
	return s.storage.Rename(from, to)
}
func (s *Spool) removeOwned(path string) error {
	if s.readOnly {
		return errReadOnlySpool
	}
	if spoolRecordPath(s.root, path) {
		return s.fsys.Remove(path)
	}
	if s.storage == nil {
		return storagebudget.ErrUnavailable
	}
	return s.storage.Remove(path)
}

// A reservation is released only after the bounded operation closes its files.
func (s *Spool) reserveStorage(bytes int64, files int, lifecycle bool) (func(), error) {
	if s.readOnly {
		return nil, errReadOnlySpool
	}
	if s.storage == nil {
		return func() {}, nil
	}
	return s.storage.Reserve(bytes, files, lifecycle)
}
func (s *Spool) updateStorage(path string, growth int64, update func() error) error {
	if s.readOnly {
		return errReadOnlySpool
	}
	if s.storage == nil {
		return update()
	}
	return s.storage.Update(path, growth, true, update)
}
