package communication

import (
	"bufio"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"time"

	hubruntime "github.com/letya999/hermes-hub/internal/runtime"
)

// usageEntry is one append-only metering record.
type usageEntry struct {
	ActorID         string    `json:"actor_id"`
	InitialSnapshot bool      `json:"initial_snapshot,omitempty"`
	DurationMS      *int64    `json:"duration_ms"`
	Version         int       `json:"version,omitempty"`
	SessionID       string    `json:"session_id,omitempty"`
	ContextID       string    `json:"context_id,omitempty"`
	Model           string    `json:"model,omitempty"`
	MeasuredAt      time.Time `json:"measured_at"`
	InputCounter    *int64    `json:"input_counter,omitempty"`
	OutputCounter   *int64    `json:"output_counter,omitempty"`
	ScopeID         string    `json:"scope_id"`
	JobID           string    `json:"job_id"`
	RunID           string    `json:"run_id"`
	InputTokens     *int64    `json:"input_tokens"`
	OutputTokens    *int64    `json:"output_tokens"`
}

// UsageTotals is the read surface of the metering journal for one scope.
type UsageTotals struct {
	InitialSnapshots int   `json:"initial_snapshots"`
	UnknownJobs      int   `json:"unknown_jobs"`
	LegacyJobs       int   `json:"legacy_jobs"`
	Jobs             int   `json:"jobs"`
	InputTokens      int64 `json:"input_tokens"`
	OutputTokens     int64 `json:"output_tokens"`
}

func usagePath(root string) string { return filepath.Join(root, "usage.jsonl") }

// RecordUsage appends one metering record unless the (job, run) pair is
// already journaled. It is idempotent across retries and restarts.
func (s *Spool) RecordUsage(job Job, runID string, report hubruntime.SessionUsage) error {
	entry := usageEntry{Version: 2, ScopeID: job.ScopeID, ContextID: job.ContextID, JobID: job.ID, RunID: runID, SessionID: report.SessionID, Model: report.Model, MeasuredAt: report.FetchedAt, InputCounter: report.InputTokens, OutputCounter: report.OutputTokens}
	entry.ActorID = job.ActorID
	if entry.MeasuredAt.IsZero() {
		entry.MeasuredAt = time.Now().UTC()
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := s.repairUsageTailLocked(); err != nil {
		return err
	}
	entries, err := s.usageEntriesLocked()
	if err != nil {
		return err
	}
	for _, existing := range entries {
		if existing.JobID == entry.JobID && existing.RunID == entry.RunID {
			return nil
		}
	}
	var previous *usageEntry
	for i := len(entries) - 1; i >= 0; i-- {
		e := &entries[i]
		if e.Version == 2 && e.ScopeID == entry.ScopeID && e.ContextID == entry.ContextID && e.SessionID == entry.SessionID {
			previous = e
			break
		}
	}
	delta := func(value, last *int64) *int64 {
		if value == nil || *value < 0 || !report.SessionFound || report.SessionID == "" {
			return nil
		}
		v := *value
		if previous != nil {
			if last == nil || v < *last {
				return nil // A reset is an unknown epoch, not negative usage.
			}
			v -= *last
		}
		return &v
	}
	var lastInput, lastOutput *int64
	if previous != nil {
		lastInput, lastOutput = previous.InputCounter, previous.OutputCounter
	}
	entry.InputTokens = delta(report.InputTokens, lastInput)
	entry.OutputTokens = delta(report.OutputTokens, lastOutput)
	entry.InitialSnapshot = previous == nil && report.SessionFound && report.SessionID != ""
	body, err := json.Marshal(entry)
	if err != nil {
		return err
	}
	f, err := os.OpenFile(usagePath(s.root), os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0600)
	if err != nil {
		return err
	}
	defer f.Close()
	if _, err := f.Write(append(body, '\n')); err != nil {
		return err
	}
	return f.Sync()
}

// usageEntriesLocked reads the whole journal. Metering volume is one line per
// job attempt, so a full scan stays cheap for a personal hub.
func (s *Spool) usageEntriesLocked() ([]usageEntry, error) {
	f, err := os.Open(usagePath(s.root)) // #nosec G304 -- spool-owned path.
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	defer f.Close()
	var entries []usageEntry
	scanner := bufio.NewScanner(f)
	for scanner.Scan() {
		line := scanner.Bytes()
		if len(line) == 0 {
			continue
		}
		var entry usageEntry
		if err := json.Unmarshal(line, &entry); err != nil {
			return nil, err
		}
		entries = append(entries, entry)
	}
	return entries, scanner.Err()
}

// UsageTotals sums journaled tokens for one scope.
func (s *Spool) UsageTotals(scopeID string) (UsageTotals, error) {
	return s.UsageTotalsSince(scopeID, time.Time{}, "")
}

// UsageTotalsSince filters measured records; legacy and unknown values remain
// explicitly counted instead of being presented as zero measured consumption.
func (s *Spool) UsageTotalsSince(scopeID string, since time.Time, model string) (UsageTotals, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	entries, err := s.usageEntriesLocked()
	if err != nil {
		return UsageTotals{}, err
	}
	var totals UsageTotals
	for _, entry := range entries {
		if entry.ScopeID != scopeID {
			continue
		}
		if (!since.IsZero() && !entry.MeasuredAt.IsZero() && entry.MeasuredAt.Before(since)) || (model != "" && entry.Model != model) {
			continue
		}
		totals.Jobs++
		if entry.Version != 2 {
			totals.LegacyJobs++
			continue
		}
		if entry.InputTokens == nil || entry.OutputTokens == nil {
			totals.UnknownJobs++
		}
		if entry.InitialSnapshot {
			totals.InitialSnapshots++
		}
		if entry.InputTokens != nil {
			totals.InputTokens += *entry.InputTokens
		}
		if entry.OutputTokens != nil {
			totals.OutputTokens += *entry.OutputTokens
		}
	}
	return totals, nil
}

// An interrupted append can only invalidate the final unterminated record.
// Preserve it separately before truncating; corruption of a complete line stops writes.
func (s *Spool) repairUsageTailLocked() error {
	f, err := os.OpenFile(usagePath(s.root), os.O_RDWR, 0600)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	defer f.Close()
	reader := bufio.NewReader(f)
	var offset int64
	for {
		line, readErr := reader.ReadBytes('\n')
		if len(line) > 64*1024 {
			return errors.New("usage record exceeds limit")
		}
		if readErr != nil && readErr != io.EOF {
			return readErr
		}
		if len(line) == 0 && readErr == io.EOF {
			return nil
		}
		if readErr == io.EOF {
			if json.Valid(line) {
				if _, err := f.Seek(0, io.SeekEnd); err != nil {
					return err
				}
				if _, err := f.Write([]byte{'\n'}); err != nil {
					return err
				}
			} else {
				quarantine := filepath.Join(s.root, fmt.Sprintf("usage-tail-%d.partial", time.Now().UnixNano()))
				if err := atomicJSON(quarantine, map[string][]byte{"bytes": line}); err != nil {
					return err
				}
				if err := f.Truncate(offset); err != nil {
					return err
				}
			}
			return f.Sync()
		}
		if len(line) > 1 && !json.Valid(line) {
			return errors.New("usage journal contains a corrupt completed record")
		}
		offset += int64(len(line))
	}
}
