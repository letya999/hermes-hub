package supervisor

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	hubruntime "github.com/letya999/hermes-hub/internal/runtime"
	"io"
	"log"
	"net/http"
	"time"
)

var errJobInProgress = errors.New("job is already in progress")

func executeJobKey(request hubruntime.ExecuteRequest) string {
	if request.JobID != "" {
		return request.JobID
	}
	return request.IdempotencyKey
}

func executeFingerprint(request hubruntime.ExecuteRequest) string {
	b, _ := json.Marshal(request)
	h := sha256.Sum256(b)
	return hex.EncodeToString(h[:])
}

func (m *Manager) beginJob(request hubruntime.ExecuteRequest) (hubruntime.ExecuteResponse, bool, error) {
	key := executeJobKey(request)
	if key == "" || request.IdempotencyKey == "" {
		return hubruntime.ExecuteResponse{}, false, errors.New("job identity is required")
	}
	fingerprint := executeFingerprint(request)
	m.mu.Lock()
	defer m.mu.Unlock()
	hold := m.holds[holdKey(request.PrincipalID, request.ContextID)]
	if hold.Active || request.OperationalEpoch != hold.Epoch {
		return hubruntime.ExecuteResponse{}, false, errors.New("administrative hold")
	}
	if previous, ok := m.jobs[key]; ok {
		if previous.Fingerprint != fingerprint {
			return hubruntime.ExecuteResponse{}, false, errors.New("job identity already used with different payload")
		}
		if terminalRunStatus(previous.Status) {
			return previous.Response, true, nil
		}
		if previous.Status == "uncertain" {
			return previous.Response, true, nil
		}
		return hubruntime.ExecuteResponse{}, false, errJobInProgress
	}
	metadata := request
	metadata.Text = ""
	m.jobs[key] = jobRecord{OperationalEpoch: hold.Epoch, Request: metadata, Fingerprint: fingerprint, Status: "running", Phase: phaseAccepted, UpdatedAt: time.Now().UTC()}
	if err := m.persistLocked(); err != nil {
		delete(m.jobs, key)
		return hubruntime.ExecuteResponse{}, false, err
	}
	return hubruntime.ExecuteResponse{}, false, nil
}

func (m *Manager) finishJob(request hubruntime.ExecuteRequest, response hubruntime.ExecuteResponse, status string) {
	m.finishJobGeneration(request, response, status, "") // #nosec G104 -- terminal bookkeeping is best-effort; the caller continues regardless.
}

// abandonJob removes a job that failed before dispatch. Acquire errors mean no
// runtime ever saw the job, so a retry must re-admit it rather than replay a
// synthetic terminal result. Records bound to a generation or already past
// "running" are left untouched.
func (m *Manager) abandonJob(request hubruntime.ExecuteRequest) {
	key := executeJobKey(request)
	if key == "" {
		return
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	record, ok := m.jobs[key]
	if !ok || record.Status != "running" || record.Generation != "" || record.Response.RunID != "" {
		return
	}
	delete(m.jobs, key)
	_ = m.persistLocked()
}

func (m *Manager) bindJobGeneration(request hubruntime.ExecuteRequest, generation string) error {
	key := executeJobKey(request)
	m.mu.Lock()
	defer m.mu.Unlock()
	record, ok := m.jobs[key]
	if !ok {
		return errors.New("job is not registered")
	}
	if record.Status != "running" && !(record.Status == "cancelled" && !record.Dispatching && record.Response.RunID == "") {
		return errors.New("job cannot bind generation")
	}
	previous := record
	transitionJobPhase(key, &record, phaseAdmitted)
	record.Generation, record.UpdatedAt = generation, time.Now().UTC()
	if record.Status == "cancelled" {
		record.Response.RuntimeGeneration = generation
	}
	m.jobs[key] = record
	if err := m.persistLocked(); err != nil {
		m.jobs[key] = previous
		return err
	}
	return nil
}

func (m *Manager) finishJobGeneration(request hubruntime.ExecuteRequest, response hubruntime.ExecuteResponse, status, generation string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.finishJobGenerationLocked(request, response, status, generation)
}

func (m *Manager) finishJobGenerationLocked(request hubruntime.ExecuteRequest, response hubruntime.ExecuteResponse, status, generation string) error {
	key := executeJobKey(request)
	if key == "" {
		return errors.New("job identity is required")
	}
	if record, ok := m.jobs[key]; ok {
		hold := m.holds[holdKey(record.Request.PrincipalID, record.Request.ContextID)]
		if hold.Active || record.OperationalEpoch != hold.Epoch {
			return errors.New("administrative effect fence")
		}
		if terminalRunStatus(record.Status) && status != record.Status {
			return errors.New("job already terminal")
		}
		if generation != "" && record.Generation != generation {
			return errors.New("stale job generation")
		}
		current := m.items[runtimeKey(Binding{PrincipalID: request.PrincipalID, ContextID: request.ContextID, RuntimeMode: "gateway"})]
		if current != nil && current.Generation != record.Generation && (record.Response.RunID != "" || response.RunID != "") {
			return errors.New("obsolete runtime producer")
		}
		if (response.JobID != "" && response.JobID != request.JobID) || (response.RuntimeGeneration != "" && response.RuntimeGeneration != record.Generation) || (record.Response.RunID != "" && response.RunID != "" && record.Response.RunID != response.RunID) || (record.Response.SessionID != "" && response.SessionID != "" && record.Response.SessionID != response.SessionID) {
			return errors.New("job response binding mismatch")
		}
		if terminalRunStatus(record.Status) {
			if response.Text != "" && response.Text != record.Response.Text {
				return errors.New("terminal result cannot change")
			}
			return nil
		}
		if response.RunID == "" {
			response.RunID = record.Response.RunID
		}
		if response.SessionID == "" {
			response.SessionID = record.Response.SessionID
		}
		previous := record
		durable := record.Status != status || (record.Response.RunID == "" && response.RunID != "") || (record.Response.SessionID == "" && response.SessionID != "")
		if terminalRunStatus(status) {
			controls := make(map[string]controlRecord, len(record.Controls))
			for id, control := range record.Controls {
				if control.State == "uncertain" {
					control.State = "closed"
					control.Response = response
				}
				controls[id] = control
			}
			record.Controls = controls
		}
		var approvalEntry *runtimeEntry
		var previousRuntime Runtime
		var previousLeases map[string]Lease
		for _, entry := range m.items {
			if entry.Generation != record.Generation {
				continue
			}
			approvalEntry = entry
			previousRuntime = entry.Runtime
			previousLeases = entry.leases
			entry.leases = make(map[string]Lease, len(previousLeases)+1)
			for id, lease := range previousLeases {
				entry.leases[id] = lease
			}
			if response.ApprovalID != "" {
				id := "approval:" + key + ":" + response.ApprovalID
				if _, exists := entry.leases[id]; !exists {
					durable = true
					deadline := record.ApprovalDeadline
					if response.ApprovalID != record.Response.ApprovalID {
						deadline = time.Now().UTC().Add(2 * time.Minute)
					}
					entry.leases[id] = Lease{ID: id, Kind: LeaseApproval, PrincipalID: entry.PrincipalID, ContextID: entry.ContextID, RuntimeMode: entry.RuntimeMode, Generation: entry.Generation, Owner: "job:" + key, ExpiresAt: deadline}
					entry.Leases++
					entry.State, entry.IdleDeadline = Busy, time.Time{}
				}
			}
			{
				for id, lease := range entry.leases {
					obsolete := response.ApprovalID != "" && id != "approval:"+key+":"+response.ApprovalID
					release := terminalRunStatus(status) || (status == "running" && record.ApprovalDeadline.IsZero()) || obsolete
					if release && lease.Kind == LeaseApproval && lease.Owner == "job:"+key {
						delete(entry.leases, id)
						entry.Leases--
					}
				}
				if entry.Leases == 0 {
					entry.State, entry.IdleDeadline = Idle, m.cfg.Now().Add(m.cfg.WarmTTL)
				}
			}
			if status == "uncertain" {
				entry.consecutiveUncertain++
			} else if terminalRunStatus(status) {
				entry.consecutiveUncertain = 0
			}
			break
		}
		if response.ApprovalID != "" && response.ApprovalID != record.Response.ApprovalID {
			durable = true
			record.ApprovalDeadline = time.Now().UTC().Add(2 * time.Minute)
		}
		if status == "uncertain" {
			if !record.Unconfirmed {
				record.UnconfirmedSince = time.Now().UTC()
			}
			record.Unconfirmed = true
		} else {
			transitionJobPhase(key, &record, statusPhase(status))
			record.Unconfirmed, record.UnconfirmedSince = false, time.Time{}
		}
		record.Response, record.Status, record.UpdatedAt = response, status, time.Now().UTC()
		m.jobs[key] = record
		if !durable {
			return nil
		}
		if err := m.persistLocked(); err != nil {
			m.jobs[key] = previous
			if approvalEntry != nil {
				approvalEntry.Runtime = previousRuntime
				approvalEntry.leases = previousLeases
			}
			return err
		}
		return nil
	}
	return errors.New("job is not registered")
}

func (m *Manager) execute(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeJSON(w, http.StatusMethodNotAllowed, map[string]string{"error": "method not allowed"})
		return
	}
	r.Body = http.MaxBytesReader(w, r.Body, 2*1024*1024+64*1024)
	defer r.Body.Close()
	body, err := io.ReadAll(r.Body)
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "request body too large"})
		return
	}
	var request hubruntime.ExecuteRequest
	if json.Unmarshal(body, &request) != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid request"})
		return
	}
	binding, err := m.bindingFor(request)
	if err != nil {
		writeJSON(w, http.StatusConflict, map[string]string{"error": err.Error()})
		return
	}
	if request.Trigger == "cron" {
		if err := m.authorizeCurrentPolicy(request); err != nil {
			writeJSON(w, 409, map[string]string{"error": "scheduled job current policy rejected"})
			return
		}
	}
	known, replay, err := m.beginJob(request)
	if err != nil {
		status := http.StatusConflict
		if errors.Is(err, errJobInProgress) {
			status = http.StatusTooEarly
		}
		writeJSON(w, status, map[string]string{"error": err.Error()})
		return
	}
	if replay {
		writeJSON(w, http.StatusOK, known)
		return
	}
	kind := LeaseJob
	if r.Header.Get("Accept") == hubruntime.RunStreamContentType {
		kind = LeaseStream
	}
	m.setJobPhase(request, phaseWaking)
	lease, runtime, err := m.Acquire(r.Context(), binding, kind)
	if err != nil {
		// The job never reached a runtime: drop the record so a retry re-admits
		// it instead of replaying a synthetic "uncertain" result. The cause is
		// logged host-side; the caller only gets that the runtime is unavailable.
		log.Printf("acquire failed for %s/%s: %v", binding.PrincipalID, binding.ContextID, err)
		m.abandonJob(request)
		writeJSON(w, http.StatusServiceUnavailable, map[string]string{"error": "runtime unavailable"})
		return
	}
	if err := m.bindJobGeneration(request, runtime.Generation); err != nil {
		_ = m.ReleaseLease(lease.ID)
		writeJSON(w, 500, map[string]string{"error": "job generation state unavailable"})
		return
	}
	if err := m.attachJobLease(lease.ID, executeJobKey(request)); err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "lease state unavailable"})
		return
	}
	defer m.settleJobLeases(request, lease)
	m.mu.Lock()
	latest := m.jobs[executeJobKey(request)]
	if terminalRunStatus(latest.Status) {
		m.mu.Unlock()
		writeJSON(w, 200, latest.Response)
		return
	}
	previous := latest
	latest.Dispatching = true
	m.jobs[executeJobKey(request)] = latest
	err = m.persistLocked()
	if err != nil {
		m.jobs[executeJobKey(request)] = previous
	}
	m.mu.Unlock()
	if err != nil {
		writeJSON(w, 500, map[string]string{"error": "dispatch state unavailable"})
		return
	}
	callCtx := r.Context()
	client := m.cfg.HTTP
	streaming := r.Header.Get("Accept") == hubruntime.RunStreamContentType
	var release context.CancelFunc = func() {}
	if streaming {
		// No total timeout on the stream hop: the run budget bounds the whole
		// job and an idle deadline abandons silent hops.
		callCtx, release = context.WithTimeout(r.Context(), hubruntime.RunBudget())
		client = m.cfg.StreamHTTP
	}
	defer release()
	req, err := http.NewRequestWithContext(callCtx, http.MethodPost, runtime.Address+"/v1/execute", bytes.NewReader(body))
	if err != nil {
		uncertain := hubruntime.ExecuteResponse{JobID: request.JobID, RuntimeGeneration: runtime.Generation, Status: "uncertain", LastEvent: "run.unknown"}
		m.finishJobGeneration(request, uncertain, "uncertain", runtime.Generation) // #nosec G104 -- terminal bookkeeping is best-effort; the response is committed either way.
		writeJSON(w, http.StatusBadGateway, uncertain)
		return
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+binding.runtimeAuth)
	req.Header.Set("Accept", r.Header.Get("Accept"))
	if request.JobID != "" {
		req.Header.Set("X-Hub-Job-ID", request.JobID)
	}
	response, err := client.Do(req)
	if err != nil {
		uncertain := hubruntime.ExecuteResponse{JobID: request.JobID, RuntimeGeneration: runtime.Generation, Status: "uncertain", LastEvent: "run.unknown"}
		m.finishJobGeneration(request, uncertain, "uncertain", runtime.Generation) // #nosec G104 -- terminal bookkeeping is best-effort; the response is committed either way.
		writeJSON(w, http.StatusBadGateway, uncertain)
		return
	}
	defer response.Body.Close()
	if response.StatusCode == http.StatusOK && response.Header.Get("Content-Type") == hubruntime.RunStreamContentType {
		m.relayRunStream(w, request, runtime, hubruntime.IdleReader(response.Body, hubruntime.StreamIdleDeadline(), release))
		return
	}
	body, readErr := io.ReadAll(io.LimitReader(response.Body, 2*1024*1024+1))
	if readErr != nil || len(body) > 2*1024*1024 {
		uncertain := hubruntime.ExecuteResponse{JobID: request.JobID, RuntimeGeneration: runtime.Generation, Status: "uncertain", LastEvent: "run.unknown"}
		m.finishJobGeneration(request, uncertain, "uncertain", runtime.Generation) // #nosec G104 -- terminal bookkeeping is best-effort; the response is committed either way.
		writeJSON(w, http.StatusBadGateway, uncertain)
		return
	}
	var result hubruntime.ExecuteResponse
	if json.Unmarshal(body, &result) == nil {
		status := result.Status
		if status == "" && response.StatusCode/100 != 2 {
			status = "uncertain"
			event := "run.unknown"
			if response.StatusCode == http.StatusBadRequest || response.StatusCode == http.StatusUnauthorized || response.StatusCode == http.StatusForbidden || response.StatusCode == http.StatusUnprocessableEntity {
				status = "failed"
				event = "run.failed"
			}
			result = hubruntime.ExecuteResponse{JobID: request.JobID, RuntimeGeneration: runtime.Generation, Status: status, LastEvent: event}
			m.finishJobGeneration(request, result, status, runtime.Generation) // #nosec G104 -- terminal bookkeeping is best-effort; the response is committed either way.
			writeJSON(w, response.StatusCode, result)
			return
		}
		if status == "" && response.StatusCode/100 == 2 {
			status = "completed"
		}
		m.finishJobGeneration(request, result, status, runtime.Generation) // #nosec G104 -- terminal bookkeeping is best-effort; the response is committed either way.
	} else {
		uncertain := hubruntime.ExecuteResponse{JobID: request.JobID, RuntimeGeneration: runtime.Generation, Status: "uncertain", LastEvent: "run.unknown"}
		m.finishJobGeneration(request, uncertain, "uncertain", runtime.Generation) // #nosec G104 -- terminal bookkeeping is best-effort; the response is committed either way.
		writeJSON(w, http.StatusBadGateway, uncertain)
		return
	}
	w.Header().Set("Content-Type", response.Header.Get("Content-Type"))
	w.WriteHeader(response.StatusCode)
	_, _ = w.Write(body)
}

func terminalRunStatus(status string) bool {
	return status == "completed" || status == "failed" || status == "cancelled" || status == "interrupted"
}

func (m *Manager) relayRunStream(w http.ResponseWriter, request hubruntime.ExecuteRequest, runtime Runtime, reader io.Reader) {
	w.Header().Set("Content-Type", hubruntime.RunStreamContentType)
	m.consumeJobStream(request, runtime, reader, func(event hubruntime.ExecuteResponse) error {
		if err := json.NewEncoder(w).Encode(event); err != nil {
			return err
		}
		return http.NewResponseController(w).Flush()
	})
}

// consumeJobStream records runtime stream events into the durable job record
// and optionally forwards each event downstream. Identity and generation
// fences apply identically to the live relay and the reconciler's re-observe.
func (m *Manager) consumeJobStream(request hubruntime.ExecuteRequest, runtime Runtime, reader io.Reader, forward func(hubruntime.ExecuteResponse) error) {
	known := hubruntime.ExecuteResponse{JobID: request.JobID, RuntimeGeneration: runtime.Generation, Status: "uncertain", LastEvent: "run.unknown"}
	err := hubruntime.ReadRunStream(reader, func(event hubruntime.ExecuteResponse) error {
		if event.JobID != request.JobID || event.RuntimeGeneration != runtime.Generation {
			return errors.New("stream identity mismatch")
		}
		if event.LastEvent == "run.heartbeat" {
			// Transport keepalive: forward it so the next hop's idle deadline
			// resets, but never persist it into the job record.
			if forward == nil {
				return nil
			}
			return forward(event)
		}
		if (known.RunID != "" && event.RunID != known.RunID) || (known.SessionID != "" && event.SessionID != known.SessionID) {
			return errors.New("stream identity mismatch")
		}
		m.mu.Lock()
		current := m.items[runtimeKey(Binding{PrincipalID: runtime.PrincipalID, ContextID: runtime.ContextID, RuntimeMode: runtime.RuntimeMode})]
		valid := current != nil && current.Generation == runtime.Generation
		m.mu.Unlock()
		if !valid {
			return errors.New("stale stream generation")
		}
		if err := m.finishJobGeneration(request, event, event.Status, runtime.Generation); err != nil {
			return err
		}
		known = event
		if forward == nil {
			return nil
		}
		return forward(event)
	})
	if !terminalRunStatus(known.Status) {
		known.Status, known.LastEvent, known.Text = "uncertain", "run.unknown", ""
		m.finishJobGeneration(request, known, "uncertain", runtime.Generation) // #nosec G104 -- terminal bookkeeping is best-effort after a terminal event was already recorded.
	}
	_ = err // A durably recorded terminal event remains authoritative after a later transport error.
}
