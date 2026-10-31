package communication

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"time"
)

// budgetLedger is the atomic reservation/settlement book for one spool: every
// admitted job holds exactly one reservation against its scope, and terminal
// outcomes settle it exactly once. The ledger is written before the job file
// lands so a crash can never admit unpriced work, and reconcile re-closes
// reservations whose job vanished mid-flight.
type budgetLedger struct {
	Reservations map[string]reservation  `json:"reservations,omitempty"`
	Scopes       map[string]*scopeBudget `json:"scopes,omitempty"`
}

type reservation struct {
	JobID     string    `json:"job_id"`
	ScopeID   string    `json:"scope_id"`
	Principal string    `json:"principal_id"`
	Estimate  int64     `json:"estimate_tokens"`
	State     string    `json:"state"` // open | settled | released
	Charged   int64     `json:"charged_tokens,omitempty"`
	CreatedAt time.Time `json:"created_at"`
}

type scopeBudget struct {
	PendingJobs    int    `json:"pending_jobs"`
	ReservedTokens int64  `json:"reserved_tokens"`
	SettledIn      int64  `json:"settled_in"`
	SettledOut     int64  `json:"settled_out"`
	Unknown        int64  `json:"unknown"`
	Day            string `json:"day,omitempty"`
}

// defaultTokenEstimate prices a job that names none; HUB_JOB_TOKEN_ESTIMATE
// overrides it. The estimate is the upper bound a reservation holds against
// the daily scope ceiling until settlement replaces it with measured tokens.
const defaultTokenEstimate int64 = 8192

func budgetPath(root string) string { return filepath.Join(root, "budget.json") }

// loadBudget reads the persisted ledger; a corrupt file marks the spool's
// admission broken (fail closed) rather than silently dropping demand.
func loadBudget(root string) (*budgetLedger, error) {
	ledger := &budgetLedger{Reservations: map[string]reservation{}, Scopes: map[string]*scopeBudget{}}
	body, err := os.ReadFile(budgetPath(root))
	if errors.Is(err, os.ErrNotExist) {
		return ledger, nil
	}
	if err != nil {
		return ledger, err
	}
	if err := json.Unmarshal(body, ledger); err != nil {
		return ledger, fmt.Errorf("budget ledger corrupt: %w", err)
	}
	if ledger.Reservations == nil {
		ledger.Reservations = map[string]reservation{}
	}
	if ledger.Scopes == nil {
		ledger.Scopes = map[string]*scopeBudget{}
	}
	return ledger, nil
}

func (s *Spool) saveBudgetLocked() error {
	return atomicJSON(budgetPath(s.root), s.budget)
}

// bucketLocked returns the scope's counters, rolling settled totals into a
// fresh UTC day so ceilings bound daily rather than cumulative spend.
// s.mu must be held.
func (s *Spool) bucketLocked(scopeID string) *scopeBudget {
	bucket, ok := s.budget.Scopes[scopeID]
	if !ok {
		bucket = &scopeBudget{}
		s.budget.Scopes[scopeID] = bucket
	}
	today := time.Now().UTC().Format("2006-01-02")
	if bucket.Day != today {
		bucket.Day = today
		bucket.SettledIn, bucket.SettledOut, bucket.Unknown = 0, 0, 0
	}
	return bucket
}

// installationScope charges jobs that carry no scope: scope-less admission
// still lands in the ledger so nothing runs unpriced.
const installationScope = "_install"

// spoolLimits are host-only admission ceilings read from the environment at
// open; agents cannot raise them. Zero disables a ceiling.
type spoolLimits struct {
	MaxPendingPerScope int
	MaxPendingTotal    int
	ScopeTokenDaily    int64
	MaxBytes           int64
	// MaxDeliveries bounds ordinary pending outbox entries; control-plane
	// deliveries claim a reserved lane and are exempt.
	MaxDeliveries int
	// MaxParallelPerContext caps conversation lanes held by one context;
	// default 8 stays below the upstream Hermes max_concurrent_runs (10) so
	// a pre-admission 429 is unreachable in steady state (ADR-0090 R3).
	MaxParallelPerContext int
}

func limitsFromEnv() spoolLimits {
	return spoolLimits{
		MaxPendingPerScope:    intFromEnv("HUB_SPOOL_MAX_PENDING_PER_SCOPE", 16),
		MaxPendingTotal:       intFromEnv("HUB_SPOOL_MAX_PENDING_TOTAL", 128),
		ScopeTokenDaily:       int64(intFromEnv("HUB_SCOPE_TOKEN_DAILY", 0)),
		MaxBytes:              int64(intFromEnv("HUB_SPOOL_MAX_BYTES", 256<<20)),
		MaxDeliveries:         intFromEnv("HUB_SPOOL_MAX_DELIVERIES", 1024),
		MaxParallelPerContext: intFromEnv("HUB_CONTEXT_MAX_PARALLEL", 8),
	}
}

// checkCeilingsLocked enforces the pending-queue, daily token and spool
// storage ceilings before a reservation lands. Demand is the open-
// reservation count — the ledger is the book; a settled-but-unclaimed job
// file is not demand. s.mu is held.
func (s *Spool) checkCeilingsLocked(scopeID string, estimate int64) error {
	if s.limits.MaxPendingPerScope > 0 && s.bucketLocked(scopeID).PendingJobs >= s.limits.MaxPendingPerScope {
		return fmt.Errorf("scope %s queue full (%d pending)", scopeID, s.limits.MaxPendingPerScope)
	}
	if s.limits.MaxPendingTotal > 0 {
		total := 0
		for _, bucket := range s.budget.Scopes {
			total += bucket.PendingJobs
		}
		if total >= s.limits.MaxPendingTotal {
			return fmt.Errorf("spool queue full (%d pending)", s.limits.MaxPendingTotal)
		}
	}
	if s.limits.ScopeTokenDaily > 0 {
		bucket := s.bucketLocked(scopeID)
		if bucket.SettledIn+bucket.SettledOut+bucket.ReservedTokens+estimate > s.limits.ScopeTokenDaily {
			return fmt.Errorf("scope %s daily token budget exhausted", scopeID)
		}
	}
	if s.limits.MaxBytes > 0 {
		var bytes int64
		entries, err := os.ReadDir(filepath.Join(s.root, "pending"))
		if err != nil {
			return err
		}
		for _, entry := range entries {
			if info, err := entry.Info(); err == nil {
				bytes += info.Size()
			}
		}
		if bytes >= s.limits.MaxBytes {
			return fmt.Errorf("spool storage ceiling reached (%d bytes)", s.limits.MaxBytes)
		}
	}
	return nil
}

// reserveLocked prices admission: one open reservation per job, charged to
// the job's scope (scope-less jobs charge the installation bucket). A
// negative estimate is unpriceable and rejected before the job file is
// written. Re-reserving an existing job is a no-op so idempotent retries
// never double-count.
func (s *Spool) reserveLocked(job Job) error {
	if s.budgetBroken != nil {
		return s.budgetBroken
	}
	if job.TokenEstimate < 0 {
		return errors.New("invalid token estimate")
	}
	if _, ok := s.budget.Reservations[job.ID]; ok {
		return nil
	}
	estimate := job.TokenEstimate
	if estimate == 0 {
		estimate = defaultTokenEstimate
	}
	scopeID := job.ScopeID
	if scopeID == "" {
		scopeID = installationScope
	}
	if err := s.checkCeilingsLocked(scopeID, estimate); err != nil {
		return err
	}
	s.budget.Reservations[job.ID] = reservation{JobID: job.ID, ScopeID: scopeID, Principal: job.PrincipalID, Estimate: estimate, State: "open", CreatedAt: time.Now().UTC()}
	bucket := s.bucketLocked(scopeID)
	bucket.PendingJobs++
	bucket.ReservedTokens += estimate
	return s.saveBudgetLocked()
}

// settleReservationLocked closes an open reservation exactly once: a job that
// produced usage entries settles at measured tokens (nil token counters mean
// the provider never reported them — priced at the estimate and counted as
// unknown), and a job that never metered releases without charge. Replay of
// a settled or released reservation is a no-op.
func (s *Spool) settleReservationLocked(jobID string) error {
	res, ok := s.budget.Reservations[jobID]
	if !ok || res.State != "open" {
		return nil
	}
	entries, err := s.usageEntriesLocked()
	if err != nil {
		return err
	}
	bucket := s.bucketLocked(res.ScopeID)
	bucket.PendingJobs--
	bucket.ReservedTokens -= res.Estimate
	var in, out int64
	metered, measured := false, false
	for _, entry := range entries {
		if entry.JobID != jobID {
			continue
		}
		metered = true
		if entry.InputTokens != nil {
			in += *entry.InputTokens
			measured = true
		}
		if entry.OutputTokens != nil {
			out += *entry.OutputTokens
			measured = true
		}
	}
	switch {
	case !metered:
		res.State, res.Charged = "released", 0
	case !measured:
		res.State, res.Charged = "settled", res.Estimate
		bucket.Unknown++
	default:
		res.State, res.Charged = "settled", in+out
		bucket.SettledIn += in
		bucket.SettledOut += out
	}
	s.budget.Reservations[jobID] = res
	return s.saveBudgetLocked()
}

// reconcileBudgetLocked closes reservations whose job is gone: a terminal
// mapping settles, anything else releases. Runs at startup and at each
// admission so mapping-less completions cannot leak open demand forever.
func (s *Spool) reconcileBudgetLocked() error {
	changed := false
	for jobID, res := range s.budget.Reservations {
		if res.State != "open" {
			continue
		}
		live := false
		for _, dir := range []string{"pending", "running"} {
			if _, err := os.Stat(filepath.Join(s.root, dir, spoolFileID(jobID)+".json")); err == nil {
				live = true
			}
		}
		if live {
			continue
		}
		mapping, err := s.loadMappingLocked(jobID)
		if err == nil && terminalStatus(mapping.Status) {
			if err := s.settleReservationLocked(jobID); err != nil {
				return err
			}
			changed = true
			continue
		}
		bucket := s.bucketLocked(res.ScopeID)
		bucket.PendingJobs--
		bucket.ReservedTokens -= res.Estimate
		res.State = "released"
		s.budget.Reservations[jobID] = res
		changed = true
	}
	if changed {
		return s.saveBudgetLocked()
	}
	return nil
}

// budgetScope returns the scope bucket for tests and inspection surfaces.
func (s *Spool) budgetScope(scopeID string) scopeBudget {
	s.mu.Lock()
	defer s.mu.Unlock()
	if bucket, ok := s.budget.Scopes[scopeID]; ok {
		return *bucket
	}
	return scopeBudget{}
}

func (s *Spool) reservationState(jobID string) string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.budget.Reservations[jobID].State
}
