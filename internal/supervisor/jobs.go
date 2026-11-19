package supervisor

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"net/http"
	"os"
	"path/filepath"
	"time"

	task "github.com/letya999/hermes-hub/contracts/task/v1"
	"github.com/letya999/hermes-hub/internal/commodel"
	"github.com/letya999/hermes-hub/internal/identity"
	"github.com/letya999/hermes-hub/internal/passportview"
	hubruntime "github.com/letya999/hermes-hub/internal/runtime"
	"github.com/letya999/hermes-hub/internal/toolhub/api"
)

// CompactJobs shrinks terminal job records older than 24h to their durable
// minimum so supervisor state does not accumulate stale run detail. The
// fingerprint stays so a same-payload retry still replays instead of 409ing;
// live and uncertain records are never touched.
func (m *Manager) CompactJobs(now time.Time) {
	m.mu.Lock()
	defer m.mu.Unlock()
	dirty := false
	for key, record := range m.jobs {
		if !terminalRunStatus(record.Status) || now.Sub(record.UpdatedAt) < 24*time.Hour || record.Request.Envelope.Equal(identity.Envelope{}) {
			continue
		}
		m.jobs[key] = jobRecord{Fingerprint: record.Fingerprint, Status: record.Status, UpdatedAt: record.UpdatedAt,
			CaptureContext: captureRunContext(key, record, m.environment()),
			Response:       hubruntime.ExecuteResponse{JobID: record.Response.JobID, RunID: record.Response.RunID, Status: record.Response.Status}}
		dirty = true
	}
	if dirty {
		_ = m.persistLocked()
	}
}

// Run implements task/v1 from the same snapshot as the legacy status endpoint.
func (m *Manager) Run(ctx context.Context, id string) (task.RunRecord, error) {
	record, _, err := m.readRunRecord(ctx, id)
	return record, err
}

func (m *Manager) readRunRecord(ctx context.Context, id string) (task.RunRecord, hubruntime.ExecuteResponse, error) {
	if !task.ValidJobID(id) {
		return task.RunRecord{}, hubruntime.ExecuteResponse{}, task.ErrInvalid
	}
	m.mu.Lock()
	record, ok := m.jobs[id]
	// The binding's live generation is read under the same lock so a run
	// context consumer can compare it against the job's dispatch generation;
	// the key derives from the stored request fields bindingFor already bound
	// (ContextID is enforced equal to the scope-derived context at admission),
	// never re-derived from caller input. A compacted or unbindable record has
	// no live generation to report.
	currentGeneration := ""
	revoked := false
	if ok {
		revoked = !m.operationallyCurrentLocked(record) || record.CancelRequested ||
			(!record.RunDeadline.IsZero() && !time.Now().Before(record.RunDeadline))
		mode := record.RuntimeMode
		if mode == "" {
			mode = "gateway"
		}
		key := runtimeKey(Binding{PrincipalID: record.Request.PrincipalID, ContextID: record.Request.ContextID, RuntimeMode: mode})
		if entry := m.items[key]; entry != nil {
			currentGeneration = entry.Runtime.Generation
		}
	}
	m.mu.Unlock()

	if !ok {
		return task.RunRecord{}, hubruntime.ExecuteResponse{}, task.ErrNotFound
	}
	if record.Status == "running" && !revoked && record.ActingIdentity.Subject != "" {
		_, revision, err := m.identityTemplate(ctx, Binding{
			SubjectID: record.ActingIdentity.Subject, AgentID: record.ActingIdentity.Actor,
			OrganizationID: record.ActingIdentity.Organization, ScopeID: record.Request.ScopeID,
			PrincipalID: record.Request.PrincipalID, ContextID: record.Request.ContextID,
		})
		revoked = err != nil || revision != record.ActingIdentity.TemplateRevision
	}
	if record.Status == "running" && !revoked && record.PolicyRevision != "" {
		registry, ok := m.identityClient().(interface {
			ActivePassports(context.Context, string, string) (string, error)
		})
		if !ok || record.PassportDigest == "" {
			revoked = true
		} else {
			active, err := registry.ActivePassports(ctx, record.Request.ContextID, m.environment())
			revoked = err != nil || active != record.PassportDigest
		}
		if !revoked && m.cfg.SpacesRoot != "" && identity.ValidID(record.Request.ContextID) {
			space := filepath.Join(m.cfg.SpacesRoot, record.Request.ContextID)
			journal := filepath.Join(space, "generated", "passports", passportview.ApplyJournalFile)
			_, err := os.Stat(journal)
			revoked = !errors.Is(err, os.ErrNotExist) || !passportview.MatchesPolicyPointer(space, record.PassportDigest)
		}
	}
	capture := record.CaptureContext
	if capture.JobID == "" {
		capture = captureRunContext(id, record, m.environment())
	}

	capture.Status = record.Status
	capture.CurrentGeneration = currentGeneration
	capture.Revoked = revoked
	return capture, record.Response, nil
}

// jobStatus is an operator/gateway endpoint protected by the private host token.
// The stored audience is authoritative; a status lookup cannot redirect delivery.
func (m *Manager) jobStatus(w http.ResponseWriter, r *http.Request, id string) {
	if r.Method != http.MethodGet {
		writeJSON(w, 405, map[string]string{"error": "method not allowed"})
		return
	}
	capture, outcome, err := m.readRunRecord(r.Context(), id)
	if errors.Is(err, task.ErrInvalid) {
		writeJSON(w, 400, map[string]string{"error": "invalid job ID"})
		return
	}
	if err != nil {
		writeJSON(w, 404, map[string]string{"error": "unknown job"})
		return
	}
	writeJSON(w, 200, map[string]any{
		"job_id": id, "identity": capture.Envelope, "status": capture.Status, "outcome": outcome,
		"admission_id": capture.AdmissionID,
		"run_id":       capture.RunID, "session_id": capture.SessionID,
		"scope_id": capture.ScopeID, "trigger": capture.Trigger, "channel": capture.Channel,
		"user_id": capture.UserID, "text_sha256": capture.TextSHA256,
		"response_sha256": capture.ResponseSHA256,
		"sensitive":       capture.Sensitive, "untrusted": capture.Untrusted,
		"assignment": capture.Assignment, "probe": capture.Probe,
		"task_id": capture.TaskID, "assignment_id": capture.AssignmentID, "assignment_plan_revision": capture.AssignmentPlanRevision,
		"deadline": capture.Deadline, "revoked": capture.Revoked, "acting_identity": capture.ActingIdentity,
		"policy_revision": capture.PolicyRevision, "passport_digest": capture.PassportDigest, "initiator": capture.Initiator,
		"environment": capture.Environment, "runtime_generation": capture.RuntimeGeneration, "current_generation": capture.CurrentGeneration,
	})
}

func captureRunContext(id string, record jobRecord, environment string) api.RunContext {
	responseHash := sha256.Sum256([]byte(record.Response.Text))
	taskID := record.Request.TaskID
	if taskID == "" && record.PolicyRevision != "" {
		taskID = commodel.DefaultTaskID
	}
	return api.RunContext{JobID: id, RunID: record.Response.RunID, SessionID: record.Response.SessionID,
		AdmissionID:       record.AdmissionID,
		RuntimeGeneration: record.Generation, Environment: environment, Status: record.Status,
		ScopeID: record.Request.ScopeID, Trigger: record.Request.Trigger, Channel: record.Request.Channel,
		UserID: record.Request.UserID, TextSHA256: record.RequestTextSHA256, ResponseSHA256: hex.EncodeToString(responseHash[:]),
		Sensitive: record.Request.Sensitive, Untrusted: record.Request.Untrusted, Assignment: record.Request.Assignment,
		TaskID: taskID, AssignmentID: record.Request.AssignmentID, AssignmentPlanRevision: record.Request.AssignmentPlanRevision,
		Deadline: record.RunDeadline, ActingIdentity: record.ActingIdentity,
		PolicyRevision: record.PolicyRevision, PassportDigest: record.PassportDigest, Initiator: record.Initiator,
		Probe: record.Request.Probe, Envelope: record.Request.Envelope}
}
