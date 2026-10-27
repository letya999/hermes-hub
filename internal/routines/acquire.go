package routines

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"math"
	"os"
	"path/filepath"
	"time"
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

func (e *Engine) acquireBodyPath(key string) string {
	return filepath.Join(e.root, "acquire", key+".body")
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

// AcquirePeekLocked returns the shared body while it is fresh for the
// fastest subscriber and no retry-after backoff is in effect.
func (e *Engine) AcquirePeekLocked(principal, url string, now time.Time) ([]byte, bool) {
	st, err := e.loadAcquireLocked(principal, url)
	if err != nil || st.FetchedAt.IsZero() || st.NextPollAt.After(now) {
		return nil, false
	}
	window := time.Duration(math.MaxInt64)
	for _, iv := range st.Subscribers {
		if d := time.Duration(iv) * time.Second; d < window {
			window = d
		}
	}
	if window == time.Duration(math.MaxInt64) || now.Sub(st.FetchedAt) > window {
		return nil, false
	}
	body, err := os.ReadFile(e.acquireBodyPath(st.Key))
	if err != nil {
		return nil, false
	}
	return body, true
}

// AcquireRecordLocked stores the fetched body and advances the cursor.
func (e *Engine) AcquireRecordLocked(principal, url string, body []byte, now time.Time) error {
	key := acquireKey(principal, url)
	st, err := e.loadAcquireLocked(principal, url)
	if err != nil {
		st = AcquireState{Key: key, PrincipalID: principal, URL: url}
	}
	if err := os.MkdirAll(filepath.Dir(e.acquireBodyPath(key)), 0700); err != nil {
		return err
	}
	if err := os.WriteFile(e.acquireBodyPath(key)+".tmp", body, 0600); err != nil {
		return err
	}
	if err := os.Rename(e.acquireBodyPath(key)+".tmp", e.acquireBodyPath(key)); err != nil {
		return err
	}
	st.FetchedAt = now.UTC()
	return e.writeAcquireLocked(st)
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
