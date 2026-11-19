package automation

import (
	"context"
	"net/http"

	task "github.com/letya999/hermes-hub/contracts/task/v1"
)

type assignmentRunAuthority struct {
	host *Host
	runs task.Client
}

func (h *Host) AssignmentRunHandler(runs task.Client, token string) http.Handler {
	return task.AssignmentAuthorityHandler(assignmentRunAuthority{host: h, runs: runs}, token)
}

func (a assignmentRunAuthority) AuthorizeAssignmentJob(ctx context.Context, jobID string) error {
	if a.runs == nil {
		return task.ErrUnavailable
	}
	record, err := a.runs.Run(ctx, jobID)
	if err != nil || record.JobID != jobID || !record.LiveAt(a.host.now()) || !record.Assignment ||
		record.Environment != a.host.cfg.Environment || record.UserID != record.Envelope.PrincipalID ||
		a.host.AuthorizeAssignmentRun(record.Envelope, record.AssignmentID, record.AssignmentPlanRevision) != nil {
		return task.ErrForbidden
	}
	return nil
}
