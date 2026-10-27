package routines

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"math"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"time"

	"github.com/letya999/hermes-hub/internal/commodel"
)

// AcquireState is the durable shared-acquisition record (SPEC-0081): one
// boundary-keyed entry per (principal, source) carrying the last fetch
// timestamp, backoff deadline and gap visibility. The principal is inside
// the key — pooling can never cross owners.
type AcquireState struct {
	Key         string           `json:"key"`
	PrincipalID string           `json:"principal_id"`
	URL         string           `json:"url"`
	Subscribers map[string]int64 `json:"subscribers,omitempty"` // schedule id -> interval seconds
	ETag        string           `json:"etag,omitempty"`
	LastMod     string           `json:"last_modified,omitempty"`
	FetchedAt   time.Time        `json:"fetched_at,omitempty"`
	NextPollAt  time.Time        `json:"next_poll_at,omitempty"`
	Failures    int              `json:"failures,omitempty"`
	GapCount    int              `json:"gap_count,omitempty"`
	LastGapAt   time.Time        `json:"last_gap_at,omitempty"`
}

// acquireKey hashes principal+url: the boundary is part of the key so
// identical URLs under different owners stay strictly separate pools.
func acquireKey(principal, url string) string {
	sum := sha256.Sum256([]byte(principal + "\x00" + url))
	return hex.EncodeToString(sum[:16])
}

func (e *Engine) acquirePath(key string) string {
	return filepath.Join(e.root, "acquire", key+".json")
}

func (e *Engine) loadAcquireLocked(principal, url string) (AcquireState, error) {
	var st AcquireState
	b, err := os.ReadFile(e.acquirePath(acquireKey(principal, url)))
	if err != nil {
		return st, err
	}
	if json.Unmarshal(b, &st) != nil || st.PrincipalID != principal || st.URL != url {
		return st, errors.New("invalid acquire record")
	}
	return st, nil
}

func (e *Engine) writeAcquireLocked(st AcquireState) error {
	return e.atomicJSON(e.acquirePath(st.Key), st)
}

// AcquireSubscribeLocked registers a schedule's interest in the shared
// source; the pool's freshness window is the shortest subscriber interval.
func (e *Engine) AcquireSubscribeLocked(principal, url, scheduleID string, interval time.Duration, now time.Time) {
	key := acquireKey(principal, url)
	st, err := e.loadAcquireLocked(principal, url)
	if err != nil {
		st = AcquireState{Key: key, PrincipalID: principal, URL: url}
	}
	if st.Subscribers == nil {
		st.Subscribers = map[string]int64{}
	}
	st.Subscribers[scheduleID] = int64(math.Ceil(interval.Seconds()))
	_ = e.writeAcquireLocked(st)
}

// AcquirePeekLocked returns the body fetched earlier in this probe sweep —
// coalescing never serves a body fetched by a previous sweep (SPEC-0081
// R2.1). Durable retry-after backoff still fences peeks across sweeps.
func (e *Engine) AcquirePeekLocked(principal, url string, now time.Time) ([]byte, bool) {
	if st, err := e.loadAcquireLocked(principal, url); err == nil && st.NextPollAt.After(now) {
		return nil, false
	}
	body, ok := e.acquireBurst[acquireKey(principal, url)]
	return body, ok
}

// AcquireRecordLocked caches the body for the rest of this sweep and stamps
// the durable fetch timestamp.
func (e *Engine) AcquireRecordLocked(principal, url string, body []byte, now time.Time) error {
	key := acquireKey(principal, url)
	if e.acquireBurst == nil {
		e.acquireBurst = map[string][]byte{}
	}
	e.acquireBurst[key] = body
	st, err := e.loadAcquireLocked(principal, url)
	if err != nil {
		st = AcquireState{Key: key, PrincipalID: principal, URL: url}
	}
	st.FetchedAt = now.UTC()
	return e.writeAcquireLocked(st)
}

// resetAcquireBurstLocked drops sweep bodies before a fresh probe pass.
func (e *Engine) resetAcquireBurstLocked() {
	e.acquireBurst = map[string][]byte{}
}

// IntakeHook durably stages one inbound hook payload in the schedule's
// inbox directory (SPEC-0081 R5.1). Files survive restart; probeOne drains
// the oldest payload per tick.
func (e *Engine) IntakeHook(scheduleID string, body []byte, now time.Time) error {
	e.mu.Lock()
	defer e.mu.Unlock()
	dir := filepath.Join(e.root, "hooks", commodel.FileID(scheduleID))
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return err
	}
	sum := sha256.Sum256(append([]byte(scheduleID+"\x00"), body...))
	name := hex.EncodeToString(sum[:16]) + ".json"
	return os.WriteFile(filepath.Join(dir, name), body, 0o600)
}

// HookPendingCount reports staged payloads awaiting the next probe tick.
func (e *Engine) HookPendingCount(scheduleID string) int {
	e.mu.Lock()
	defer e.mu.Unlock()
	entries, err := os.ReadDir(filepath.Join(e.root, "hooks", commodel.FileID(scheduleID)))
	if err != nil {
		return 0
	}
	return len(entries)
}

// popHookLocked removes and returns the oldest staged payload, or nil.
func (e *Engine) popHookLocked(scheduleID string) []byte {
	dir := filepath.Join(e.root, "hooks", commodel.FileID(scheduleID))
	entries, err := os.ReadDir(dir)
	if err != nil || len(entries) == 0 {
		return nil
	}
	slices.SortFunc(entries, func(a, b os.DirEntry) int { return strings.Compare(a.Name(), b.Name()) })
	path := filepath.Join(dir, entries[0].Name())
	body, err := os.ReadFile(path)
	if err != nil {
		return nil
	}
	_ = os.Remove(path)
	return body
}

// AcquireBackoffLocked records a Retry-After/backoff deadline; peeks return
// miss until it elapses (SPEC-0081 R2.2).
func (e *Engine) AcquireBackoffLocked(principal, url string, until, now time.Time) {
	key := acquireKey(principal, url)
	st, err := e.loadAcquireLocked(principal, url)
	if err != nil {
		st = AcquireState{Key: key, PrincipalID: principal, URL: url}
	}
	st.NextPollAt = until.UTC()
	_ = e.writeAcquireLocked(st)
}

// RetryAfterError wraps a transient fetch failure with its source-declared
// retry delay (HTTP Retry-After, rate-limit hints); the engine stores the
// deadline in the pool instead of re-hammering the source.
type RetryAfterError struct {
	Cause error
	Delay time.Duration
}

func (e *RetryAfterError) Error() string { return e.Cause.Error() }
func (e *RetryAfterError) Unwrap() error { return e.Cause }
