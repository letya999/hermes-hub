package supervisor

import (
	"crypto/sha256"
	"encoding/hex"
	"net/http"
	"strings"
	"time"

	"github.com/letya999/hermes-hub/internal/identity"
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

// jobStatus is an operator/gateway endpoint protected by the private host token.
// The stored audience is authoritative; a status lookup cannot redirect delivery.
func (m *Manager) jobStatus(w http.ResponseWriter, r *http.Request, id string) {
	if r.Method != http.MethodGet {
		writeJSON(w, 405, map[string]string{"error": "method not allowed"})
		return
	}
	if id == "" || len(id) > 256 || strings.ContainsAny(id, "/\r\n") {
		writeJSON(w, 400, map[string]string{"error": "invalid job ID"})
		return
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
	if ok {
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
		writeJSON(w, 404, map[string]string{"error": "unknown job"})
		return
	}
	capture := record.CaptureContext
	if capture.JobID == "" {
		capture = captureRunContext(id, record, m.environment())
	}
	writeJSON(w, 200, map[string]any{
		"job_id": id, "identity": capture.Envelope, "status": record.Status, "outcome": record.Response,
		"admission_id": capture.AdmissionID,
		"run_id":       capture.RunID, "session_id": capture.SessionID,
		"scope_id": capture.ScopeID, "trigger": capture.Trigger, "channel": capture.Channel,
		"user_id": capture.UserID, "text_sha256": capture.TextSHA256,
		"response_sha256": capture.ResponseSHA256,
		"sensitive":       capture.Sensitive, "untrusted": capture.Untrusted,
		"assignment": capture.Assignment, "probe": capture.Probe,
		"environment": capture.Environment, "runtime_generation": capture.RuntimeGeneration, "current_generation": currentGeneration,
	})
}

func captureRunContext(id string, record jobRecord, environment string) api.RunContext {
	responseHash := sha256.Sum256([]byte(record.Response.Text))
	return api.RunContext{JobID: id, RunID: record.Response.RunID, SessionID: record.Response.SessionID,
		AdmissionID:       record.AdmissionID,
		RuntimeGeneration: record.Generation, Environment: environment, Status: record.Status,
		ScopeID: record.Request.ScopeID, Trigger: record.Request.Trigger, Channel: record.Request.Channel,
		UserID: record.Request.UserID, TextSHA256: record.RequestTextSHA256, ResponseSHA256: hex.EncodeToString(responseHash[:]),
		Sensitive: record.Request.Sensitive, Untrusted: record.Request.Untrusted, Assignment: record.Request.Assignment,
		Probe: record.Request.Probe, Envelope: record.Request.Envelope}
}
