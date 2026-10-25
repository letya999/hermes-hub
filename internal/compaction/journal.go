// Package compaction owns the cross-turn compaction journal and the bounded
// context-status projection shared by the runtime (writer) and the in-runtime
// tool surface (reader). Journal rows carry metadata only — never transcript
// or summary text — so a status read can never leak message content.
package compaction

import (
	"bufio"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"time"
)

// Bounded vocabularies. Anything outside the set is a write-time refusal —
// provenance strings are a contract consumers pattern-match on.
const (
	TriggerAutoInLoop = "auto_inloop"
	TriggerAutoSweep  = "auto_sweep"
	TriggerManual     = "manual"
	TriggerOverflow   = "overflow"

	EngineHub      = "hub"
	EngineNative   = "native"
	EngineProvider = "provider"

	MethodInLoop        = "in_loop"
	MethodArchiveCompct = "archive_and_compact"

	// Hub cascade stage names — the journal records the stage that
	// committed so a status read can tell a free prune from a paid summary.
	MethodPruneOnly      = "prune_only"
	MethodPrecomputed    = "precomputed_swap"
	MethodSummary        = "summary"
	MethodShrinkRetry    = "shrink_retry"
	MethodDropCheckpoint = "drop_checkpoint"

	ProvenanceDeclared  = "declared"
	ProvenanceDefault   = "default"
	ProvenanceEstimated = "estimated"
	ProvenanceMeasured  = "measured"
)

// DefaultSweepThreshold is the between-jobs sweep bound when the space
// declares none — below upstream's 200k in-loop trigger so the sweep stays
// the quality knob and in-loop stays the survival defense (SPEC-0072 R4).
const DefaultSweepThreshold = 160_000

var (
	triggers = map[string]bool{
		TriggerAutoInLoop: true, TriggerAutoSweep: true,
		TriggerManual: true, TriggerOverflow: true,
	}
	engines = map[string]bool{
		EngineHub: true, EngineNative: true, EngineProvider: true,
	}
	methods = map[string]bool{
		MethodInLoop: true, MethodArchiveCompct: true,
		MethodPruneOnly: true, MethodPrecomputed: true, MethodSummary: true,
		MethodShrinkRetry: true, MethodDropCheckpoint: true,
	}
)

// Event is one journaled compaction row. Token fields stay nil — serialized
// as null — when no honest estimate exists; consumers render null as unknown
// and never fabricate.
type Event struct {
	At           time.Time `json:"at"`
	SessionID    string    `json:"session_id"`
	Trigger      string    `json:"trigger"`
	Engine       string    `json:"engine"`
	Method       string    `json:"method"`
	TokensBefore *int64    `json:"tokens_before_est"`
	TokensAfter  *int64    `json:"tokens_after_est"`
	FailureClass string    `json:"failure_class,omitempty"`
}

// valid re-checks the bounded vocabulary plus the failure-class length cap.
// Failure classes are short machine tokens (locked, unavailable, refused_*),
// not messages — the cap keeps prose and transcript text off the wire.
func (e Event) valid() error {
	switch {
	case e.SessionID == "":
		return errors.New("session_id required")
	case !triggers[e.Trigger]:
		return fmt.Errorf("trigger %q out of vocabulary", e.Trigger)
	case !engines[e.Engine]:
		return fmt.Errorf("engine %q out of vocabulary", e.Engine)
	case !methods[e.Method]:
		return fmt.Errorf("method %q out of vocabulary", e.Method)
	case len(e.FailureClass) > 48:
		return errors.New("failure_class exceeds bound")
	}
	return nil
}

// Journal is the append-only per-runtime compaction log at
// <state>/hub/compaction-journal.jsonl. The runtime container owns the file;
// every process that opens it sees the same rows.
type Journal struct {
	path string
	mu   sync.Mutex
}

// Open binds the journal under stateDir, creating the directory lazily.
func Open(stateDir string) (*Journal, error) {
	if err := os.MkdirAll(filepath.Join(stateDir, "hub"), 0o700); err != nil {
		return nil, err
	}
	return &Journal{path: filepath.Join(stateDir, "hub", "compaction-journal.jsonl")}, nil
}

// Append validates and writes one row. A bad line can never land —
// vocabulary refusal happens before the file opens.
func (j *Journal) Append(e Event) error {
	if err := e.valid(); err != nil {
		return err
	}
	j.mu.Lock()
	defer j.mu.Unlock()
	body, err := json.Marshal(e)
	if err != nil {
		return err
	}
	f, err := os.OpenFile(j.path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o600)
	if err != nil {
		return err
	}
	defer f.Close()
	if _, err := f.Write(append(body, '\n')); err != nil {
		return err
	}
	return f.Sync()
}

// LatestSession returns the session of the newest journal row — the
// "current session" an agent-facing status surface defaults to when the
// caller cannot name its upstream session id.
func (j *Journal) LatestSession() (string, error) {
	j.mu.Lock()
	defer j.mu.Unlock()
	f, err := os.Open(j.path) // #nosec G304 -- journal-owned path.
	if errors.Is(err, os.ErrNotExist) {
		return "", nil
	}
	if err != nil {
		return "", err
	}
	defer f.Close()
	latest := ""
	scanner := bufio.NewScanner(f)
	for scanner.Scan() {
		line := scanner.Bytes()
		if len(line) == 0 {
			continue
		}
		var e Event
		if err := json.Unmarshal(line, &e); err != nil {
			return "", err
		}
		latest = e.SessionID
	}
	return latest, scanner.Err()
}

// Events returns journal rows for one session, oldest first. A corrupt line
// fails the whole read — a status surface that silently drops rows invents
// history.
func (j *Journal) Events(sessionID string) ([]Event, error) {
	j.mu.Lock()
	defer j.mu.Unlock()
	f, err := os.Open(j.path) // #nosec G304 -- journal-owned path.
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	defer f.Close()
	var out []Event
	scanner := bufio.NewScanner(f)
	for scanner.Scan() {
		line := scanner.Bytes()
		if len(line) == 0 {
			continue
		}
		var e Event
		if err := json.Unmarshal(line, &e); err != nil {
			return nil, err
		}
		if e.SessionID == sessionID {
			out = append(out, e)
		}
	}
	return out, scanner.Err()
}
