package migration

import (
	"encoding/json"
	"errors"
	"github.com/letya999/hermes-hub/internal/communication"
	"github.com/letya999/hermes-hub/internal/stack"
	"os"
	"path/filepath"
	"strings"
)

type ExecutionReport struct {
	NativeCron          *NativeCronSnapshot       `json:"native_cron_snapshot,omitempty"`
	User                string                    `json:"user"`
	Pending             int                       `json:"pending"`
	Terminal            int                       `json:"terminal"`
	Uncertain           int                       `json:"uncertain"`
	PendingDeliveries   int                       `json:"pending_deliveries"`
	UncertainDeliveries int                       `json:"uncertain_deliveries"`
	Mode                string                    `json:"mode"`
	Selection           stack.ExecutionSelection  `json:"selection"`
	Previous            *stack.ExecutionSelection `json:"previous,omitempty"`
	Rollback            string                    `json:"rollback"`
}

// AuditExecutionSpool is read-only. Unknown claimed work never becomes retryable.
// The operator stops the gateway before this snapshot and keeps it stopped until render/up.
func AuditExecutionSpool(spool, user string) (ExecutionReport, error) {
	report := ExecutionReport{User: user}
	if err := validateID(user); err != nil {
		return report, err
	}
	root, err := os.OpenRoot(spool)
	if err != nil {
		return report, err
	}
	defer root.Close()
	for _, dir := range []string{"pending", "running", "done", "failed"} {
		folder, err := root.Open(dir)
		if err != nil {
			return report, err
		}
		entries, err := folder.ReadDir(-1)
		folder.Close()
		if err != nil {
			return report, err
		}
		for _, entry := range entries {
			if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".json") {
				continue
			}
			info, err := entry.Info()
			if err != nil || !info.Mode().IsRegular() || info.Size() > 3<<20 {
				return report, errors.New("invalid spool job file")
			}
			body, err := root.ReadFile(filepath.Join(dir, entry.Name()))
			var job communication.Job
			if err != nil || json.Unmarshal(body, &job) != nil || job.ID == "" || job.IdempotencyKey == "" {
				return report, errors.New("ambiguous legacy job requires manual reconciliation")
			}
			if job.UserID != user {
				continue
			}
			if dir == "running" {
				return report, errors.New("claimed job must settle before changing executor")
			}
			if dir == "pending" {
				if job.Envelope.Validate(job.UserID, job.ContextID, job.RuntimeID, job.PolicyVersion) != nil {
					return report, errors.New("queued job lacks verified migration authority")
				}
				report.Pending++
			} else {
				report.Terminal++
			}
			mappingBody, err := root.ReadFile(filepath.Join("mappings", entry.Name()))
			var mapping communication.JobMapping
			if err != nil || json.Unmarshal(mappingBody, &mapping) != nil || !mapping.MatchesJob(job) {
				return report, errors.New("job mapping unavailable or ambiguous")
			}
			if mapping.RunID != "" && mapping.Status != "completed" && mapping.Status != "failed" && mapping.Status != "cancelled" && mapping.Status != "interrupted" {
				return report, errors.New("admitted run must settle before changing executor")
			}
			if mapping.Status == "uncertain" {
				report.Uncertain++
			}
		}
	}
	for _, dir := range []string{"outbox/pending", "outbox/sending", "outbox/failed"} {
		folder, err := root.Open(dir)
		if err != nil {
			return report, err
		}
		entries, err := folder.ReadDir(-1)
		folder.Close()
		if err != nil {
			return report, err
		}
		for _, entry := range entries {
			if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".json") {
				continue
			}
			if dir == "outbox/pending" {
				report.PendingDeliveries++
			} else {
				report.UncertainDeliveries++
			}
		}
	}
	return report, nil
}

// SelectExecution writes only host routing state; no queued job, delivery or home moves.
func SelectExecution(dir string, selection stack.ExecutionSelection, audit ExecutionReport, apply bool) (ExecutionReport, error) {
	audit.Mode, audit.Selection = "dry-run", selection
	audit.Rollback = "Select the previous execution mode, render and start only this context; retain the same home and spool."
	if err := selection.Validate(); err != nil {
		return audit, err
	}
	settings, err := stack.ReadEnvironment(dir, selection.Environment)
	if err != nil {
		return audit, err
	}
	if settings.User != selection.User || audit.User != selection.User {
		return audit, errors.New("execution selection/audit belongs to another user")
	}
	previous, found, err := stack.ReadExecution(dir, selection.Environment, selection.User)
	if err != nil {
		return audit, err
	}
	if found {
		audit.Previous = &previous
	}
	if !apply {
		return audit, nil
	}
	if audit.Uncertain != 0 {
		return audit, errors.New("uncertain jobs must be reconciled before executor selection")
	}
	if selection.Mode == "supervisor" && (audit.NativeCron == nil || audit.NativeCron.HermesPin != HermesMigrationPin || audit.NativeCron.ActiveJobs != 0) {
		return audit, errors.New("scale-to-zero requires verified zero active native schedules")
	}
	root, err := os.OpenRoot(dir)
	if err != nil {
		return audit, err
	}
	defer root.Close()
	// Exclusive create serializes concurrent host selectors. A crashed selector
	// leaves an explicit lock requiring operator inspection, not automatic replay.
	lock, err := root.OpenFile(".execution-lock", os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0600)
	if err != nil {
		return audit, errors.New("another execution selection is active")
	}
	lock.Close()
	defer root.Remove(".execution-lock")
	body, err := json.MarshalIndent(selection, "", "  ")
	if err != nil {
		return audit, err
	}
	if err = atomicWriteRoot(root, stack.ExecutionPath(selection.Environment), body); err != nil {
		return audit, err
	}
	audit.Mode = "applied"
	return audit, nil
}
