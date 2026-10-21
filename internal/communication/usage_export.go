package communication

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"time"
)

// UsageRecord is the operator-facing read view of one journaled entry. It
// mirrors usageEntry without exposing spool internals.
type UsageRecord struct {
	JobID           string    `json:"job_id"`
	RunID           string    `json:"run_id"`
	ScopeID         string    `json:"scope_id"`
	ActorID         string    `json:"actor_id"`
	ContextID       string    `json:"context_id,omitempty"`
	SessionID       string    `json:"session_id,omitempty"`
	Model           string    `json:"model,omitempty"`
	Version         int       `json:"version,omitempty"`
	DurationMS      *int64    `json:"duration_ms,omitempty"`
	InputTokens     *int64    `json:"input_tokens,omitempty"`
	OutputTokens    *int64    `json:"output_tokens,omitempty"`
	InitialSnapshot bool      `json:"initial_snapshot,omitempty"`
	MeasuredAt      time.Time `json:"measured_at"`
}

// UsageReport is the aggregation an operator reads: totals overall, per scope
// and per actor, over the requested window.
type UsageReport struct {
	Totals UsageTotals            `json:"totals"`
	Scopes map[string]UsageTotals `json:"scopes"`
	Actors map[string]UsageTotals `json:"actors"`
}

// ReadUsageJournal parses the append-only journal directly, never opening the
// spool or mutating state.
func ReadUsageJournal(root string) ([]UsageRecord, error) {
	body, err := os.ReadFile(usagePath(root)) // #nosec G304 -- operator-selected spool root.
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var records []UsageRecord
	lines := bytes.Split(body, []byte("\n"))
	for i, line := range lines {
		line = bytes.TrimSpace(line)
		if len(line) == 0 {
			continue
		}
		var entry usageEntry
		if err := json.Unmarshal(line, &entry); err != nil {
			// Only the unterminated final fragment may be a torn write the
			// writer will quarantine on its next append; every completed
			// record is authoritative and fails closed.
			if i == len(lines)-1 {
				continue
			}
			return nil, fmt.Errorf("usage journal line %d: %w", i+1, err)
		}
		records = append(records, usageRecord(entry))
	}
	return records, nil
}

func usageRecord(e usageEntry) UsageRecord {
	return UsageRecord{JobID: e.JobID, RunID: e.RunID, ScopeID: e.ScopeID, ActorID: e.ActorID,
		ContextID: e.ContextID, SessionID: e.SessionID, Model: e.Model, Version: e.Version,
		DurationMS: e.DurationMS, InputTokens: e.InputTokens, OutputTokens: e.OutputTokens,
		InitialSnapshot: e.InitialSnapshot, MeasuredAt: e.MeasuredAt}
}

// AggregateUsage filters records by window and actor/model, then sums per
// scope and per actor.
func AggregateUsage(records []UsageRecord, since time.Time, actor, model string) UsageReport {
	report := UsageReport{Scopes: map[string]UsageTotals{}, Actors: map[string]UsageTotals{}}
	add := func(totals UsageTotals, r UsageRecord) UsageTotals {
		totals.Jobs++
		if r.Version != 2 {
			totals.LegacyJobs++
			return totals
		}
		if r.InputTokens == nil || r.OutputTokens == nil {
			totals.UnknownJobs++
		}
		if r.InitialSnapshot {
			totals.InitialSnapshots++
		}
		if r.InputTokens != nil {
			totals.InputTokens += *r.InputTokens
		}
		if r.OutputTokens != nil {
			totals.OutputTokens += *r.OutputTokens
		}
		if r.DurationMS != nil {
			totals.RuntimeMS += *r.DurationMS
		}
		return totals
	}
	for _, r := range records {
		// Untimed records stay inside every window: a record that cannot be
		// placed in time is still consumption the operator must see.
		if (!since.IsZero() && !r.MeasuredAt.IsZero() && r.MeasuredAt.Before(since)) ||
			(actor != "" && r.ActorID != actor) || (model != "" && r.Model != model) {
			continue
		}
		report.Totals = add(report.Totals, r)
		report.Scopes[r.ScopeID] = add(report.Scopes[r.ScopeID], r)
		if r.ActorID != "" {
			report.Actors[r.ActorID] = add(report.Actors[r.ActorID], r)
		}
	}
	return report
}
