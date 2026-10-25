package compaction

import (
	"context"
	"time"
)

// Gate is the resolved hub_sweep policy for one session boundary.
type Gate struct {
	Enabled         bool
	Precompute      bool
	Threshold       int64
	CooldownSeconds int64
}

// Bounded refusal vocabulary — these strings cross into the journal, so the
// set is closed.
const (
	RefusalDisabled       = "disabled"
	RefusalUnderThreshold = "under_threshold"
	RefusalCooldown       = "cooldown"
	RefusalLeaseHeld      = "lease_held"
	RefusalEstimateFailed = "estimate_failed"
	RefusalUnsupported    = "unsupported"
)

// SweepResult reports what a job-boundary evaluation did.
type SweepResult struct {
	Acted   bool
	Refusal string
	Event   *Event
}

type SweepOptions struct {
	StateDir      string
	SessionID     string
	ResolvedModel string
	Gate          Gate
	Now           func() time.Time
	Summarize     func(ctx context.Context, model string, messages []map[string]any) (string, error)
	// Spawn schedules the background precompute. Defaults to a goroutine; a
	// synchronous Spawn exists only so tests can await the parked result.
	Spawn     func(func())
	TailCount int
}

// Sweep evaluates the between-jobs compaction policy for a bound session.
func Sweep(ctx context.Context, o SweepOptions) (SweepResult, error) {
	if !o.Gate.Enabled {
		return SweepResult{Refusal: RefusalDisabled}, nil
	}
	estimate, _, err := EstimateTokens(ctx, o.SessionID)
	if err != nil {
		return SweepResult{}, err
	}
	state := loadSessionSweep(o.StateDir, o.SessionID)
	if estimate < o.Gate.Threshold {
		if o.Gate.Precompute && estimate >= precomputeFloor(o.Gate.Threshold) {
			o.maybePrecompute(state)
		}
		return SweepResult{Refusal: RefusalUnderThreshold}, nil
	}
	now := o.Now()
	if o.Gate.CooldownSeconds > 0 && now.Sub(state.LastAttemptAt) < time.Duration(o.Gate.CooldownSeconds)*time.Second {
		return o.refuseJournal(now, estimate, RefusalCooldown)
	}
	acquired, err := sidecarAcquireLease(ctx, o.SessionID, sweepHolder, sweepLeaseTTL)
	if err != nil {
		return SweepResult{}, err
	}
	if !acquired {
		return o.refuseJournal(now, estimate, RefusalLeaseHeld)
	}
	defer func() { _ = sidecarReleaseLease(context.Background(), o.SessionID, sweepHolder) }()
	return o.execute(ctx, now, state, estimate)
}

// refuseJournal records a post-trigger refusal as a journal row with the
// bounded class — an attempt that refused leaves evidence, not silence.
func (o SweepOptions) refuseJournal(at time.Time, estimate int64, class string) (SweepResult, error) {
	if journal, err := Open(o.StateDir); err == nil {
		_ = journal.Append(Event{At: at, SessionID: o.SessionID, Trigger: TriggerAutoSweep,
			Engine: EngineHub, Method: MethodArchiveCompct,
			TokensBefore: &estimate, FailureClass: class})
	}
	return SweepResult{Refusal: class}, nil
}

// execute performs the compaction under the held turn lease: compression
// lock, transcript read, summary, atomic apply, journal row, state flip.
func (o SweepOptions) execute(ctx context.Context, now time.Time, state SessionSweep, estimate int64) (SweepResult, error) {
	locked, err := sidecarLock(ctx, o.SessionID, sweepHolder, sweepLockTTL)
	if err != nil || !locked {
		return o.refuseJournal(now, estimate, "locked")
	}
	defer func() { _ = sidecarUnlock(context.Background(), o.SessionID, sweepHolder) }()
	transcript, err := sidecarReadTranscript(ctx, o.SessionID)
	if err != nil {
		return o.refuseJournal(now, estimate, "unavailable")
	}
	summary := ""
	if h := state.Held; h != nil && h.Model == o.ResolvedModel && h.Watermark == transcript.Watermark {
		summary = h.Summary
	} else {
		summary, err = o.Summarize(ctx, o.ResolvedModel, transcript.Messages)
		if err != nil {
			return o.refuseJournal(now, estimate, "summarize_failed")
		}
	}
	carrier := map[string]any{
		"role":                "user",
		"content":             summaryCarrierPrefix + summary + "\n" + summaryEndMarker,
		"_compressed_summary": true,
		"timestamp":           float64(now.Unix()),
	}
	tail := transcript.Messages
	if o.TailCount > 0 && len(tail) > o.TailCount {
		tail = tail[len(tail)-o.TailCount:]
	}
	messages := make([]map[string]any, 0, len(tail)+1)
	messages = append(messages, carrier)
	for _, m := range tail {
		copy := make(map[string]any, len(m))
		for k, v := range m {
			if k != "_row_id" {
				copy[k] = v
			}
		}
		messages = append(messages, copy)
	}
	if _, err := sidecarApplyTranscript(ctx, o.SessionID, sweepHolder, messages, &transcript.Watermark, o.TailCount); err != nil {
		return o.refuseJournal(now, estimate, "apply_failed")
	}
	event := Event{At: now, SessionID: o.SessionID, Trigger: TriggerAutoSweep,
		Engine: EngineHub, Method: MethodArchiveCompct, TokensBefore: &estimate}
	journal, err := Open(o.StateDir)
	if err != nil {
		return SweepResult{}, err
	}
	if err := journal.Append(event); err != nil {
		return SweepResult{}, err
	}
	state.LastAttemptAt = now
	state.ConsecFailures = 0
	state.Held = nil
	if err := saveSessionSweep(o.StateDir, o.SessionID, state); err != nil {
		return SweepResult{}, err
	}
	return SweepResult{Acted: true, Event: &event}, nil
}

// precomputeFloor is the ratio of the sweep threshold where a speculative
// summary starts paying for itself — late enough to matter, early enough to
// land before the next boundary.
func precomputeFloor(threshold int64) int64 {
	return threshold * 85 / 100
}

// maybePrecompute starts a background summary when the estimate crossed the
// ratio. It never runs inside a user turn: Spawn schedules it and Sweep has
// already returned by the time the model answers.
func (o SweepOptions) maybePrecompute(state SessionSweep) {
	if o.Summarize == nil || state.ConsecFailures >= precomputeFailureCap {
		return
	}
	spawn := o.Spawn
	if spawn == nil {
		spawn = func(fn func()) { go fn() }
	}
	spawn(o.runPrecompute)
}

// runPrecompute summarizes the current transcript in the background and
// parks it on the session state for the next boundary to pick up.
func (o SweepOptions) runPrecompute() {
	ctx, cancel := context.WithTimeout(context.Background(), precomputeTimeout)
	defer cancel()
	transcript, err := sidecarReadTranscript(ctx, o.SessionID)
	state := loadSessionSweep(o.StateDir, o.SessionID)
	if err != nil {
		state.ConsecFailures++
		_ = saveSessionSweep(o.StateDir, o.SessionID, state)
		return
	}
	summary, err := o.Summarize(ctx, o.ResolvedModel, transcript.Messages)
	if err != nil {
		state.ConsecFailures++
		_ = saveSessionSweep(o.StateDir, o.SessionID, state)
		return
	}
	state.Held = &HeldSummary{
		Model: o.ResolvedModel, Watermark: transcript.Watermark,
		Summary: summary, At: o.Now(),
	}
	_ = saveSessionSweep(o.StateDir, o.SessionID, state)
}

const (
	precomputeTimeout    = 120 * time.Second
	precomputeFailureCap = 2
)

// sweepHolder owns the lease and compression lock during a between-jobs
// compaction so a newly admitted run cannot interleave the mutation.
const sweepHolder = "hub-sweep"

// sweepLeaseTTL bounds how long a sweep may hold the turn lease; a wedged
// summarizer cannot wedge the session past this.
const sweepLeaseTTL = 180

// sweepLockTTL bounds the compression lock inside the lease window.
const sweepLockTTL = 120

// The summary carrier mirrors upstream's own compaction row so upstream
// projections recognize it as context history, not a live user turn.
const summaryCarrierPrefix = "[CONTEXT SUMMARY]:\n"
const summaryEndMarker = "--- END OF CONTEXT SUMMARY — respond to the message below, not the summary above ---"
