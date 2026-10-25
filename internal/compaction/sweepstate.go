package compaction

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"time"
)

// SessionSweep is the per-session between-jobs record: last attempt (the
// cooldown clock), consecutive background-summary failures (the precompute
// breaker), a held speculative summary and the one-shot next-turn notice.
type SessionSweep struct {
	LastAttemptAt  time.Time    `json:"last_attempt_at"`
	ConsecFailures int          `json:"consec_failures,omitempty"`
	Held           *HeldSummary `json:"held,omitempty"`
	// NoticeConsumedAt is the newest committed journal event the next-turn
	// notice already fired for — a commit newer than this arms one.
	NoticeConsumedAt time.Time       `json:"notice_consumed_at,omitempty"`
	Pending          *PendingCompact `json:"pending,omitempty"`
}

// PendingCompact is the bounded queued request parked on a busy session:
// the parsed args and resolved engine, nothing else.
type PendingCompact struct {
	Args   CompactArgs `json:"args"`
	Engine string      `json:"engine"`
	At     time.Time   `json:"at"`
}

// QueueCompact parks a manual request for the next turn boundary. One
// pending slot: a second queue overwrites the first — the newer ask
// supersedes, never accumulates.
func QueueCompact(stateDir, sessionID string, pending PendingCompact) error {
	state := loadSessionSweep(stateDir, sessionID)
	state.Pending = &pending
	return saveSessionSweep(stateDir, sessionID, state)
}

// TakePending consumes the queued compact — once. The caller runs it after
// the turn boundary; a lost take can never double-apply.
func TakePending(stateDir, sessionID string) *PendingCompact {
	state := loadSessionSweep(stateDir, sessionID)
	if state.Pending == nil {
		return nil
	}
	pending := state.Pending
	state.Pending = nil
	_ = saveSessionSweep(stateDir, sessionID, state)
	return pending
}

// HeldSummary is a precomputed summary parked between jobs. It commits only
// when the transcript watermark and the resolved model still match — a stale
// hold is discarded, never applied.
type HeldSummary struct {
	Model     string    `json:"model"`
	Watermark int       `json:"watermark"`
	Summary   string    `json:"summary"`
	At        time.Time `json:"at"`
}

func sweepStatePath(stateDir string) string {
	return filepath.Join(stateDir, "hub", "sweep-state.json")
}

func loadSweepState(stateDir string) (map[string]SessionSweep, error) {
	body, err := os.ReadFile(sweepStatePath(stateDir))
	if errors.Is(err, os.ErrNotExist) {
		return map[string]SessionSweep{}, nil
	}
	if err != nil {
		return nil, err
	}
	var sessions map[string]SessionSweep
	if err := json.Unmarshal(body, &sessions); err != nil {
		return nil, err
	}
	return sessions, nil
}

func loadSessionSweep(stateDir, sessionID string) SessionSweep {
	sessions, err := loadSweepState(stateDir)
	if err != nil {
		return SessionSweep{}
	}
	return sessions[sessionID]
}

func saveSessionSweep(stateDir, sessionID string, state SessionSweep) error {
	sessions, err := loadSweepState(stateDir)
	if err != nil {
		sessions = map[string]SessionSweep{}
	}
	sessions[sessionID] = state
	return writeSweepState(stateDir, sessions)
}

// Notice is the bounded one-shot next-turn marker a committed compaction
// arms: trigger vocabulary + timestamp, nothing from the transcript.
type Notice struct {
	Trigger string    `json:"trigger"`
	At      time.Time `json:"at"`
}

// TakeNotice consumes the pending notice for a session — exactly once. It
// fires on the newest committed journal event (sweep, manual compact, or an
// observed in-loop compaction) that no earlier take already covered.
func TakeNotice(stateDir, sessionID string) *Notice {
	j, err := Open(stateDir)
	if err != nil {
		return nil
	}
	events, err := j.Events(sessionID)
	if err != nil {
		return nil
	}
	var last *Event
	for i := range events {
		if events[i].FailureClass == "" {
			last = &events[i]
		}
	}
	if last == nil {
		return nil
	}
	state := loadSessionSweep(stateDir, sessionID)
	if !last.At.After(state.NoticeConsumedAt) {
		return nil
	}
	state.NoticeConsumedAt = last.At
	_ = saveSessionSweep(stateDir, sessionID, state)
	return &Notice{Trigger: last.Trigger, At: last.At}
}

func writeSweepState(stateDir string, sessions map[string]SessionSweep) error {
	path := sweepStatePath(stateDir)
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	body, err := json.MarshalIndent(sessions, "", "  ")
	if err != nil {
		return err
	}
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, body, 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}
