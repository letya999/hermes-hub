package toolhub

import (
	"encoding/json"
	"maps"
	"os"
	"path/filepath"
	"sync"
	"time"
)

// cursorFlushInterval bounds the broker-replay window after a crash: events
// since the last durable cursor come back through reconcileBrokerReady, which
// is already at-least-once (ErrStale, NoticeVersion dedup).
const cursorFlushInterval = 60 * time.Second

// CursorStore is the durable home for hot, at-least-once bookkeeping that must
// not ride the registry commit channel. Broker cursors advance roughly once a
// second; paying a whole-snapshot registry commit — double full-file hash plus
// FULL-sync transaction on the Docker Desktop bind mount — per step starved
// /v1/rights/decide readers behind Store.mu writers (CHG-0308, .work/done/
// CHG-0305 records the same starvation one incident earlier). The sidecar is a
// few hundred bytes, so temp-write + rename stays cheap even on 9p and flushes
// on a cadence instead of per event.
//
// ponytail: one flat JSON file, no schema machinery — the Store's digest/fence
// contract buys nothing for counters whose correctness window is a replay
// interval. If more hot ledgers arrive, lift them here rather than reopening
// the registry for them; the ceiling is per-domain sidecars, not a second
// Store.
type CursorStore struct {
	mu      sync.Mutex
	path    string
	cursors map[string]uint64
	dirty   bool
	flushed time.Time
}

// LoadCursorStore opens the cursor sidecar at path. The seed carries the
// registry's persisted brokerCursors so pre-cutover stores migrate without
// replaying the whole event stream; the sidecar file wins whenever it exists.
// An empty path keeps the store in memory (tests, fileless stores).
func LoadCursorStore(path string, seed map[string]uint64) (*CursorStore, error) {
	c := &CursorStore{path: path, cursors: map[string]uint64{}}
	maps.Copy(c.cursors, seed)
	if path == "" {
		return c, nil
	}
	body, err := os.ReadFile(path) // #nosec G304 -- path is derived from the validated store path.
	if err != nil {
		if os.IsNotExist(err) {
			return c, nil
		}
		return nil, err
	}
	onDisk := map[string]uint64{}
	if err := json.Unmarshal(body, &onDisk); err != nil {
		return nil, err
	}
	maps.Copy(c.cursors, onDisk)
	return c, nil
}

// Get returns the durable-plus-pending cursor for key.
func (c *CursorStore) Get(key string) uint64 {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.cursors[key]
}

// CompareAndAdvance moves key to next only when the current value still equals
// expected — the same CAS the registry map provided under Store.mu, now on the
// sidecar's own lock so concurrent reconcilers lose deterministically.
func (c *CursorStore) CompareAndAdvance(key string, expected, next uint64) bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.cursors[key] != expected {
		return false
	}
	c.cursors[key] = next
	c.dirty = true
	return true
}

// Flush persists pending cursors at most once per cursorFlushInterval; a
// failure leaves the dirty flag set so the next pass retries.
func (c *CursorStore) Flush(now time.Time) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	if !c.dirty || c.path == "" || now.Sub(c.flushed) < cursorFlushInterval {
		return nil
	}
	body, err := json.Marshal(c.cursors)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(c.path), 0700); err != nil {
		return err
	}
	tmp := c.path + ".tmp"
	f, err := os.OpenFile(tmp, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, 0600) // #nosec G304 -- path is derived from the validated store path.
	if err != nil {
		return err
	}
	if _, err := f.Write(body); err != nil {
		_ = f.Close()
		return err
	}
	if err := f.Sync(); err != nil {
		_ = f.Close()
		return err
	}
	if err := f.Close(); err != nil {
		return err
	}
	if err := os.Rename(tmp, c.path); err != nil {
		return err
	}
	c.dirty = false
	c.flushed = now
	return nil
}
