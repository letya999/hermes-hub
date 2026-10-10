package communication

import (
	"bufio"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"

	hubruntime "github.com/letya999/hermes-hub/internal/runtime"
)

// usageEntry is one append-only metering record.
type usageEntry struct {
	ScopeID      string `json:"scope_id"`
	JobID        string `json:"job_id"`
	RunID        string `json:"run_id"`
	InputTokens  *int64 `json:"input_tokens"`
	OutputTokens *int64 `json:"output_tokens"`
}

// UsageTotals is the read surface of the metering journal for one scope.
type UsageTotals struct {
	Jobs         int   `json:"jobs"`
	InputTokens  int64 `json:"input_tokens"`
	OutputTokens int64 `json:"output_tokens"`
}

func usagePath(root string) string { return filepath.Join(root, "usage.jsonl") }

// RecordUsage appends one metering record unless the (job, run) pair is
// already journaled. It is idempotent across retries and restarts.
func (s *Spool) RecordUsage(job Job, runID string, report hubruntime.SessionUsage) error {
	entry := usageEntry{ScopeID: job.ScopeID, JobID: job.ID, RunID: runID, InputTokens: report.InputTokens, OutputTokens: report.OutputTokens}
	s.mu.Lock()
	defer s.mu.Unlock()
	entries, err := s.usageEntriesLocked()
	if err != nil {
		return err
	}
	for _, existing := range entries {
		if existing.JobID == entry.JobID && existing.RunID == entry.RunID {
			return nil
		}
	}
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
		totals.Jobs++
		if entry.InputTokens != nil {
			totals.InputTokens += *entry.InputTokens
		}
		if entry.OutputTokens != nil {
			totals.OutputTokens += *entry.OutputTokens
		}
	}
	return totals, nil
}
