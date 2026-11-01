package toolhub

import (
	"os"
	"path/filepath"
	"strconv"
	"time"

	"github.com/letya999/hermes-hub/internal/storagebudget"
	"github.com/letya999/hermes-hub/internal/toolhub/api"
)

// ToolHub-owned persistent state under the snapshot directory: store.json,
// its lock/initialized marker, the refresh-lease and viewer-link ledgers, the
// governance document and the bounded audit segments. The artifact image
// store keeps its own build-time cap (MaxArtifactBytes) and is not summed
// into this budget.
var toolhubStateDefaults = storagebudget.Limits{
	Bytes:          256 << 20,
	Files:          8192,
	NextWrite:      32 << 20,
	LifecycleBytes: 16 << 20,
	LifecycleFiles: 128,
}

// toolhubStorageLimits resolves the state-dir budget from the environment;
// malformed values make the whole configuration invalid — fail closed, same
// posture as the communication spool limits.
func toolhubStorageLimits() (storagebudget.Limits, error) {
	limits := toolhubStateDefaults
	bytes := map[string]*int64{
		"HUB_TOOLHUB_STATE_MAX_BYTES":               &limits.Bytes,
		"HUB_TOOLHUB_STATE_MAX_NEXT_WRITE_BYTES":    &limits.NextWrite,
		"HUB_TOOLHUB_STATE_LIFECYCLE_RESERVE_BYTES": &limits.LifecycleBytes,
	}
	files := map[string]*int{
		"HUB_TOOLHUB_STATE_MAX_OWNED_FILES":         &limits.Files,
		"HUB_TOOLHUB_STATE_LIFECYCLE_RESERVE_FILES": &limits.LifecycleFiles,
	}
	for name, target := range bytes {
		if raw, ok := os.LookupEnv(name); ok {
			value, err := strconv.ParseInt(raw, 10, 64)
			if err != nil {
				return storagebudget.Limits{}, storagebudget.ErrLimits
			}
			*target = value
		}
	}
	for name, target := range files {
		if raw, ok := os.LookupEnv(name); ok {
			value, err := strconv.ParseInt(raw, 10, 64)
			if err != nil {
				return storagebudget.Limits{}, storagebudget.ErrLimits
			}
			*target = int(value)
		}
	}
	return limits, limits.Validate()
}

// storageCounter binds the shared owner counter to the directory the
// persisted path resolves to. In-memory stores never reach this. The lazy
// binding is guarded by storageMu: the snapshot path holds s.mu while the
// lease/viewer sidecar paths hold leaseMu, so s.mu alone cannot serialize
// the first bind.
func (s *Store) storageCounter(dir string) (*storagebudget.Counter, error) {
	s.storageMu.Lock()
	defer s.storageMu.Unlock()
	if s.storage != nil {
		return s.storage, nil
	}
	limits, err := toolhubStorageLimits()
	if err != nil {
		return nil, err
	}
	counter, err := storagebudget.New(dir, limits)
	if err != nil {
		return nil, err
	}
	s.storage, s.storageDir = counter, dir
	return counter, nil
}

// StorageStatus reports the owned-byte aggregates for the directory the
// registry path lives in. It never opens the store and never reports paths
// or object names — read-only accounting for operator surfaces.
func StorageStatus(storePath string) (storagebudget.Snapshot, storagebudget.Limits, error) {
	if err := api.SafeStorePath(storePath); err != nil {
		return storagebudget.Snapshot{}, storagebudget.Limits{}, err
	}
	limits, err := toolhubStorageLimits()
	if err != nil {
		return storagebudget.Snapshot{}, storagebudget.Limits{}, err
	}
	counter, err := storagebudget.New(filepath.Dir(storePath), limits)
	if err != nil {
		return storagebudget.Snapshot{}, limits, err
	}
	return counter.Snapshot(), limits, nil
}

type storageMaintenanceStatus struct {
	LastAttempt time.Time
	LastSuccess time.Time
	LastError   string
}

// maintainStorageLocked reconciles the owned directory back into the counter
// at most once an hour; a failed reconcile is recorded as a safe allowlisted
// code instead of the raw filesystem error. Callers hold s.mu and only run
// after the counter is bound.
func (s *Store) maintainStorageLocked(now time.Time) {
	if now.Sub(s.maintenance.LastAttempt) < time.Hour {
		return
	}
	s.maintenance.LastAttempt = now
	if err := s.storage.Reconcile(); err != nil {
		s.maintenance.LastError = "cleanup_failed"
		return
	}
	s.maintenance.LastSuccess = now
	s.maintenance.LastError = ""
}
