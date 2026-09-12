package supervisor

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"os"
	"path/filepath"
	"time"

	hubruntime "github.com/letya999/hermes-hub/internal/runtime"
	"github.com/letya999/hermes-hub/internal/stack"
)

type controlRecord struct {
	Choice   string                     `json:"choice"`
	State    string                     `json:"state"`
	Response hubruntime.ExecuteResponse `json:"response"`
}

func (m *Manager) control(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeJSON(w, 405, map[string]string{"error": "method not allowed"})
		return
	}
	r.Body = http.MaxBytesReader(w, r.Body, 256*1024)
	defer r.Body.Close()
	var request hubruntime.RunControl
	if json.NewDecoder(r.Body).Decode(&request) != nil {
		writeJSON(w, 400, map[string]string{"error": "invalid control"})
		return
	}
	jobKey := executeJobKey(request.ExecuteRequest)
	lock := m.lockFor("control:" + jobKey)
	lock.Lock()
	defer lock.Unlock()
	m.mu.Lock()
	record, ok := m.jobs[jobKey]
	m.mu.Unlock()
	if !ok || record.Request.Envelope != request.Envelope || record.Request.ScopeID != request.ScopeID || record.Request.UserID != request.UserID || record.Request.ActorID != request.ActorID || record.Request.OrganizationID != request.OrganizationID || record.Request.IdempotencyKey != request.IdempotencyKey {
		writeJSON(w, 409, map[string]string{"error": "job ownership mismatch"})
		return
	}
	binding, err := m.bindingFor(request.ExecuteRequest)
	if err != nil {
		writeJSON(w, 409, map[string]string{"error": "current binding rejected"})
		return
	}
	if request.Action == "approve" && !request.Reconcile {
		if err := m.authorizeApproval(request); err != nil {
			writeJSON(w, 409, map[string]string{"error": "current policy rejected approval"})
			return
		}
	}
	if request.Action == "cancel" && request.RunID == "" && request.SessionID == "" && request.RuntimeGeneration == "" {
		m.mu.Lock()
		latest := m.jobs[jobKey]
		previous := latest
		latest.CancelRequested = true
		if !latest.Dispatching && latest.Response.RunID == "" && !terminalRunStatus(latest.Status) {
			latest.Status = "cancelled"
			latest.Response = hubruntime.ExecuteResponse{JobID: request.JobID, RuntimeGeneration: latest.Generation, Status: "cancelled", LastEvent: "run.cancelled"}
		}
		m.jobs[jobKey] = latest
		err = m.persistLocked()
		if err != nil {
			m.jobs[jobKey] = previous
		}
		m.mu.Unlock()
		if err != nil {
			writeJSON(w, 500, map[string]string{"error": "cancel state unavailable"})
			return
		}
		writeJSON(w, 202, map[string]string{"status": "cancel_requested"})
		return
	}
	if request.Action == "cancel" && terminalRunStatus(record.Status) {
		if request.RuntimeGeneration != record.Generation || request.RunID != record.Response.RunID || request.SessionID != record.Response.SessionID {
			writeJSON(w, 409, map[string]string{"error": "run binding mismatch"})
			return
		}
		writeJSON(w, 200, record.Response)
		return
	}
	m.mu.Lock()
	entry := m.items[runtimeKey(binding)]
	var runtime Runtime
	if entry != nil {
		runtime = entry.Runtime
	}
	m.mu.Unlock()
	if entry == nil || runtime.Generation != record.Generation || request.RuntimeGeneration != record.Generation || request.RunID != record.Response.RunID || request.SessionID != record.Response.SessionID || request.RunID == "" {
		writeJSON(w, 409, map[string]string{"error": "run binding mismatch"})
		return
	}
	controlKey := request.Action + ":" + request.RequestID
	if request.Action != "cancel" && request.Action != "approve" {
		writeJSON(w, 400, map[string]string{"error": "invalid action"})
		return
	}
	if saved, exists := record.Controls[controlKey]; exists {
		if saved.Choice != request.Choice {
			writeJSON(w, 409, map[string]string{"error": "conflicting decision"})
			return
		}
		if saved.State == "uncertain" && request.Reconcile {
			m.reconcileControl(w, r, request, binding, runtime, controlKey)
			return
		}
		if saved.State == "applied" || saved.State == "closed" {
			writeJSON(w, 200, saved.Response)
		} else {
			writeJSON(w, 409, map[string]string{"error": "control outcome uncertain"})
		}
		return
	}
	if request.Reconcile {
		writeJSON(w, 409, map[string]string{"error": "no uncertain control to reconcile"})
		return
	}
	if request.Action == "approve" {
		allowed := false
		for _, choice := range record.Response.ApprovalChoices {
			if choice == request.Choice {
				allowed = true
			}
		}
		if record.Status != "waiting_for_approval" || request.RequestID == "" || request.RequestID != record.Response.ApprovalID || !allowed || !time.Now().Before(record.ApprovalDeadline) {
			writeJSON(w, 409, map[string]string{"error": "approval is stale or invalid"})
			return
		}
		request.Deadline = record.ApprovalDeadline
	}
	// Persist before dispatch: a lost response must never cause a blind second decision.
	m.mu.Lock()
	latest := m.jobs[jobKey]
	if latest.Generation != request.RuntimeGeneration || (request.Action == "approve" && (latest.Status != "waiting_for_approval" || latest.Response.ApprovalID != request.RequestID || latest.ApprovalDeadline.IsZero() || !time.Now().Before(latest.ApprovalDeadline))) {
		m.mu.Unlock()
		writeJSON(w, 409, map[string]string{"error": "control binding changed before dispatch"})
		return
	}
	if request.Action == "cancel" && terminalRunStatus(latest.Status) {
		m.mu.Unlock()
		writeJSON(w, 200, latest.Response)
		return
	}
	controls := make(map[string]controlRecord, len(latest.Controls)+1)
	for key, value := range latest.Controls {
		controls[key] = value
	}
	controls[controlKey] = controlRecord{Choice: request.Choice, State: "uncertain"}
	previous := latest
	latest.Controls = controls
	m.jobs[jobKey] = latest
	err = m.persistLocked()
	if err != nil {
		m.jobs[jobKey] = previous
	}
	m.mu.Unlock()
	if err != nil {
		writeJSON(w, 500, map[string]string{"error": "control state unavailable"})
		return
	}
	if request.Action == "approve" && !request.Reconcile {
		if err := m.authorizeApproval(request); err != nil {
			writeJSON(w, 409, map[string]string{"error": "current policy changed before dispatch"})
			return
		}
	}
	body, _ := json.Marshal(request)
	upstream, err := http.NewRequestWithContext(r.Context(), http.MethodPost, runtime.Address+"/v1/control", bytes.NewReader(body))
	if err != nil {
		writeJSON(w, 502, map[string]string{"error": "control unavailable"})
		return
	}
	upstream.Header.Set("Authorization", "Bearer "+binding.runtimeAuth)
	upstream.Header.Set("Content-Type", "application/json")
	response, err := m.cfg.HTTP.Do(upstream)
	if err != nil {
		writeJSON(w, 502, map[string]string{"error": "control outcome uncertain"})
		return
	}
	defer response.Body.Close()
	var outcome hubruntime.ExecuteResponse
	if response.StatusCode != 200 || json.NewDecoder(http.MaxBytesReader(w, response.Body, 2*1024*1024+64*1024)).Decode(&outcome) != nil || outcome.JobID != request.JobID || outcome.RunID != request.RunID || outcome.SessionID != request.SessionID || outcome.RuntimeGeneration != request.RuntimeGeneration || outcome.Status == "uncertain" {
		writeJSON(w, 502, map[string]string{"error": "control outcome uncertain"})
		return
	}
	m.mu.Lock()
	latest = m.jobs[jobKey]
	if latest.Generation != request.RuntimeGeneration {
		m.mu.Unlock()
		writeJSON(w, 409, map[string]string{"error": "stale control generation"})
		return
	}
	if terminalRunStatus(latest.Status) {
		outcome = latest.Response
	}
	previous = latest
	controls = make(map[string]controlRecord, len(latest.Controls))
	for key, value := range latest.Controls {
		controls[key] = value
	}
	controls[controlKey] = controlRecord{Choice: request.Choice, State: "applied", Response: outcome}
	latest.Controls = controls
	if request.Action == "approve" {
		latest.ApprovalDeadline = time.Time{}
	}
	m.jobs[jobKey] = latest
	err = m.finishJobGenerationLocked(request.ExecuteRequest, outcome, outcome.Status, request.RuntimeGeneration)
	if err != nil {
		m.jobs[jobKey] = previous
	}
	m.mu.Unlock()
	if err != nil {
		writeJSON(w, 500, map[string]string{"error": "control state unavailable"})
		return
	}
	if terminalRunStatus(outcome.Status) {
		m.settleJobLeases(request.ExecuteRequest, Lease{Generation: request.RuntimeGeneration})
	}
	writeJSON(w, 200, outcome)
}

// ExpireApprovals closes work fail-closed through the same durable cancellation
// path. A timeout alone never releases an execution or uncertain lease.
func (m *Manager) ExpireApprovals(ctx context.Context, now time.Time) {
	m.mu.Lock()
	var expired []jobRecord
	for _, record := range m.jobs {
		if !terminalRunStatus(record.Status) && !record.ApprovalDeadline.IsZero() && !now.Before(record.ApprovalDeadline) && record.Response.RunID != "" {
			expired = append(expired, record)
		}
	}
	m.mu.Unlock()
	for _, record := range expired {
		request := hubruntime.RunControl{RunReference: hubruntime.RunReference{ExecuteRequest: record.Request, RunID: record.Response.RunID, SessionID: record.Response.SessionID, RuntimeGeneration: record.Generation}, Action: "cancel"}
		if saved, ok := record.Controls["cancel:"]; ok && saved.State == "uncertain" {
			request.Reconcile = true
		}
		body, _ := json.Marshal(request)
		callCtx, cancel := context.WithTimeout(ctx, 15*time.Second)
		r, err := http.NewRequestWithContext(callCtx, http.MethodPost, "http://supervisor/v1/control", bytes.NewReader(body))
		if err == nil {
			m.control(&controlSink{header: make(http.Header)}, r)
		}
		cancel()
	}
}

type controlSink struct{ header http.Header }

func (s *controlSink) Header() http.Header       { return s.header }
func (*controlSink) WriteHeader(int)             {}
func (*controlSink) Write(b []byte) (int, error) { return len(b), nil }

// reconcileControl observes the original run and can confirm an idempotent stop.
// Approval mutations are never repeated. Leaving an approval wait
// proves that the request is inactive, not that the submitted choice was applied.
func (m *Manager) reconcileControl(w http.ResponseWriter, r *http.Request, request hubruntime.RunControl, binding Binding, runtime Runtime, key string) {
	outcome, err := m.readControlOutcome(r, request, binding, runtime)
	if err != nil {
		writeJSON(w, 502, map[string]string{"error": "run state unavailable"})
		return
	}
	// Native stop is idempotent. Retry it only after observing the exact original
	// run still active; approval mutations are never retried here.
	if request.Action == "cancel" && (outcome.Status == "running" || outcome.Status == "waiting_for_approval" || outcome.Status == "queued") {
		m.mu.Lock()
		latest := m.jobs[executeJobKey(request.ExecuteRequest)]
		current := m.items[runtimeKey(binding)]
		valid := latest.Generation == request.RuntimeGeneration && current != nil && current.Generation == request.RuntimeGeneration
		m.mu.Unlock()
		if !valid {
			writeJSON(w, 409, map[string]string{"error": "stale control generation"})
			return
		}
		request.Reconcile = false
		outcome, err = m.readControlOutcome(r, request, binding, runtime)
		if err != nil {
			writeJSON(w, 502, map[string]string{"error": "cancel outcome uncertain"})
			return
		}
	}
	terminal := terminalRunStatus(outcome.Status)
	inactive := request.Action == "approve" && (outcome.Status == "running" || (outcome.Status == "waiting_for_approval" && outcome.ApprovalID != "" && outcome.ApprovalID != request.RequestID))
	if !terminal && !inactive {
		writeJSON(w, 202, map[string]string{"status": "control_uncertain"})
		return
	}
	m.mu.Lock()
	latest := m.jobs[executeJobKey(request.ExecuteRequest)]
	if latest.Generation != request.RuntimeGeneration {
		m.mu.Unlock()
		writeJSON(w, 409, map[string]string{"error": "stale control generation"})
		return
	}
	if terminalRunStatus(latest.Status) {
		outcome = latest.Response
	}
	previous := latest
	controls := make(map[string]controlRecord, len(latest.Controls))
	for id, value := range latest.Controls {
		controls[id] = value
	}
	controls[key] = controlRecord{Choice: request.Choice, State: "closed", Response: outcome}
	latest.Controls = controls
	if request.Action == "approve" {
		latest.ApprovalDeadline = time.Time{}
	}
	m.jobs[executeJobKey(request.ExecuteRequest)] = latest
	err = m.finishJobGenerationLocked(request.ExecuteRequest, outcome, outcome.Status, request.RuntimeGeneration)
	if err != nil {
		m.jobs[executeJobKey(request.ExecuteRequest)] = previous
	}
	m.mu.Unlock()
	if err != nil {
		writeJSON(w, 500, map[string]string{"error": "control state unavailable"})
		return
	}
	if terminalRunStatus(outcome.Status) {
		m.settleJobLeases(request.ExecuteRequest, Lease{Generation: request.RuntimeGeneration})
	}
	writeJSON(w, 200, outcome)
}

func (m *Manager) readControlOutcome(r *http.Request, request hubruntime.RunControl, binding Binding, runtime Runtime) (hubruntime.ExecuteResponse, error) {
	var outcome hubruntime.ExecuteResponse
	body, _ := json.Marshal(request)
	upstream, err := http.NewRequestWithContext(r.Context(), http.MethodPost, runtime.Address+"/v1/control", bytes.NewReader(body))
	if err != nil {
		return outcome, err
	}
	upstream.Header.Set("Authorization", "Bearer "+binding.runtimeAuth)
	upstream.Header.Set("Content-Type", "application/json")
	response, err := m.cfg.HTTP.Do(upstream)
	if err != nil {
		return outcome, err
	}
	defer response.Body.Close()
	if response.StatusCode != 200 || json.NewDecoder(http.MaxBytesReader(nil, response.Body, 2*1024*1024+64*1024)).Decode(&outcome) != nil || outcome.JobID != request.JobID || outcome.RunID != request.RunID || outcome.SessionID != request.SessionID || outcome.RuntimeGeneration != request.RuntimeGeneration {
		return outcome, errors.New("run state unavailable")
	}
	return outcome, nil
}

// Approval cannot expand the current effective user/organization policy.
func (m *Manager) authorizeApproval(request hubruntime.RunControl) error {
	return m.authorizeCurrentPolicy(request.ExecuteRequest)
}

func (m *Manager) authorizeCurrentPolicy(request hubruntime.ExecuteRequest) error {
	if request.ActorID != request.PrincipalID || request.ScopeID != "user:"+request.UserID {
		return errors.New("approval actor/scope mismatch")
	}
	root := filepath.Join(m.cfg.SpacesRoot, request.UserID)
	path := filepath.Join(root, "settings.yaml")
	info, err := os.Lstat(path)
	if err != nil || !info.Mode().IsRegular() || info.Mode()&os.ModeSymlink != 0 || info.Size() > 1024*1024 {
		return errors.New("current settings unavailable")
	}
	// Check organization selection before loading its policy.
	selected, err := stack.Read(path)
	if err != nil {
		return err
	}
	selectedOrg := selected.Organization
	if selectedOrg == "" {
		selectedOrg = "personal"
	}
	if selected.User != request.UserID || selectedOrg != request.OrganizationID {
		return errors.New("approval organization changed")
	}
	if selected.Organization != "" {
		orgRoot := filepath.Join(m.cfg.SpacesRoot, selected.Organization)
		for _, name := range []string{"scope.yaml", "settings.yaml"} {
			info, e := os.Lstat(filepath.Join(orgRoot, name))
			if e != nil && !os.IsNotExist(e) {
				return e
			}
			if e == nil && (!info.Mode().IsRegular() || info.Mode()&os.ModeSymlink != 0 || info.Size() > 1024*1024) {
				return errors.New("organization policy unavailable")
			}
		}
	}
	settings := selected
	if selected.Organization != "" {
		orgRoot := filepath.Join(m.cfg.SpacesRoot, selected.Organization)
		org, e := stack.ReadOrganization(orgRoot)
		if e != nil {
			return e
		}
		settings, e = stack.ApplyOrganization(org, settings, orgRoot)
		if e != nil {
			return e
		}
	}
	settings.Environment = envOr("HUB_ENV", "prod")
	if settings.Environment == "dev" {
		settings.BrowserPort++
		settings.OAuthPort++
	}
	if err := settings.Validate(); err != nil {
		return err
	}
	organization := settings.Organization
	if organization == "" {
		organization = "personal"
	}
	if settings.User != request.UserID || organization != request.OrganizationID || stack.PolicyVersion(settings) != request.PolicyVersion {
		return errors.New("approval policy changed")
	}
	return nil
}
