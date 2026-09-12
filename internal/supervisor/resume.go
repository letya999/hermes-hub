package supervisor

import (
	"bytes"
	"encoding/json"
	"errors"
	"net/http"

	hubruntime "github.com/letya999/hermes-hub/internal/runtime"
)

// Resume observes the already admitted run; it never submits another Hermes run.
func (m *Manager) resume(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeJSON(w, http.StatusMethodNotAllowed, map[string]string{"error": "method not allowed"})
		return
	}
	r.Body = http.MaxBytesReader(w, r.Body, 256*1024)
	defer r.Body.Close()
	var request hubruntime.ExecuteRequest
	if json.NewDecoder(r.Body).Decode(&request) != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid request"})
		return
	}
	m.mu.Lock()
	record, ok := m.jobs[executeJobKey(request)]
	m.mu.Unlock()
	if !ok || record.Request.Envelope != request.Envelope || record.Request.ScopeID != request.ScopeID || record.Request.UserID != request.UserID || record.Request.ActorID != request.ActorID || record.Request.OrganizationID != request.OrganizationID || record.Request.IdempotencyKey != request.IdempotencyKey {
		writeJSON(w, http.StatusConflict, map[string]string{"error": "job ownership mismatch"})
		return
	}
	binding, err := m.bindingFor(request)
	if err != nil {
		writeJSON(w, http.StatusConflict, map[string]string{"error": "current binding rejected"})
		return
	}
	if terminalRunStatus(record.Status) {
		writeJSON(w, http.StatusOK, record.Response)
		return
	}
	if record.Response.RunID == "" || record.Response.SessionID == "" {
		writeJSON(w, http.StatusConflict, map[string]string{"error": "run admission unknown"})
		return
	}
	observationLock := m.lockFor("observation:" + executeJobKey(request))
	if !observationLock.TryLock() {
		writeJSON(w, http.StatusTooEarly, map[string]string{"error": "observation already active"})
		return
	}
	defer observationLock.Unlock()
	lease, runtime, err := m.Acquire(r.Context(), binding, LeaseStream)
	if err != nil {
		writeJSON(w, http.StatusServiceUnavailable, map[string]string{"error": "runtime unavailable"})
		return
	}
	if err := m.attachJobLease(lease.ID, executeJobKey(request)); err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "lease state unavailable"})
		return
	}
	defer m.settleJobLeases(request, lease)
	// Only this trusted recovery path may rebind observation to a replacement generation.
	record, err = m.rebindJobObservation(request, record, runtime)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "recovery state unavailable"})
		return
	}
	if terminalRunStatus(record.Status) {
		writeJSON(w, http.StatusOK, record.Response)
		return
	}
	ref := hubruntime.RunReference{ExecuteRequest: record.Request, RunID: record.Response.RunID, SessionID: record.Response.SessionID, RuntimeGeneration: runtime.Generation}
	body, _ := json.Marshal(ref)
	req, err := http.NewRequestWithContext(r.Context(), http.MethodPost, runtime.Address+"/v1/observe", bytes.NewReader(body))
	if err != nil {
		writeJSON(w, http.StatusBadGateway, map[string]string{"error": "observation unavailable"})
		return
	}
	req.Header.Set("Authorization", "Bearer "+binding.runtimeAuth)
	req.Header.Set("Content-Type", "application/json")
	response, err := m.cfg.HTTP.Do(req)
	if err != nil {
		writeJSON(w, http.StatusBadGateway, map[string]string{"error": "observation unavailable"})
		return
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK || response.Header.Get("Content-Type") != hubruntime.RunStreamContentType {
		writeJSON(w, http.StatusBadGateway, map[string]string{"error": "observation rejected"})
		return
	}
	m.relayRunStream(w, request, runtime, response.Body)
}

func (m *Manager) rebindJobObservation(request hubruntime.ExecuteRequest, expected jobRecord, runtime Runtime) (jobRecord, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	key := executeJobKey(request)
	latest, ok := m.jobs[key]
	entry := m.items[runtimeKey(Binding{PrincipalID: runtime.PrincipalID, ContextID: runtime.ContextID, RuntimeMode: runtime.RuntimeMode})]
	if !ok || entry == nil || entry.Generation != runtime.Generation || latest.Request.Envelope != request.Envelope || latest.Response.RunID != expected.Response.RunID || latest.Response.SessionID != expected.Response.SessionID {
		return jobRecord{}, errors.New("observation binding changed")
	}
	if terminalRunStatus(latest.Status) {
		return latest, nil
	}
	previous := latest
	latest.Generation = runtime.Generation
	m.jobs[key] = latest
	if err := m.persistLocked(); err != nil {
		m.jobs[key] = previous
		return jobRecord{}, err
	}
	return latest, nil
}

func (m *Manager) attachJobLease(id, jobID string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	for _, entry := range m.items {
		if lease, ok := entry.leases[id]; ok {
			previous := lease
			lease.Owner = "job:" + jobID
			entry.leases[id] = lease
			if err := m.persistLocked(); err != nil {
				entry.leases[id] = previous
				return err
			}
			return nil
		}
	}
	return errors.New("lease is not registered")
}

func (m *Manager) settleJobLeases(request hubruntime.ExecuteRequest, hold Lease) {
	m.mu.Lock()
	record := m.jobs[executeJobKey(request)]
	var release []string
	for _, entry := range m.items {
		for id, lease := range entry.leases {
			if lease.Owner != "job:"+executeJobKey(request) || lease.Generation != hold.Generation {
				continue
			}
			if record.Generation == hold.Generation && terminalRunStatus(record.Status) {
				release = append(release, id)
			} else {
				if lease.Kind != LeaseApproval {
					lease.Kind = LeaseUncertain
				}
				entry.leases[id] = lease
			}
		}
	}
	_ = m.persistLocked()
	m.mu.Unlock()
	for _, id := range release {
		_ = m.ReleaseLease(id)
	}
}
