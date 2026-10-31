package supervisor

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"strings"
)

// skillRefLease routes a ToolHub lease to the exact admitted runtime generation.
func (m *Manager) skillRefLease(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeJSON(w, http.StatusMethodNotAllowed, map[string]string{"error": "method not allowed"})
		return
	}
	defer r.Body.Close()
	var request struct {
		PrincipalID   string   `json:"principal_id"`
		ContextID     string   `json:"context_id"`
		RuntimeID     string   `json:"runtime_id"`
		PolicyVersion string   `json:"policy_version"`
		Refs          []string `json:"refs"`
	}
	if json.NewDecoder(http.MaxBytesReader(w, r.Body, 4096)).Decode(&request) != nil || !validID(request.PrincipalID) || !validID(request.ContextID) || !validID(request.RuntimeID) || request.PolicyVersion == "" || len(request.Refs) == 0 || len(request.Refs) > 16 {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid lease request"})
		return
	}
	m.mu.Lock()
	entry := m.items[runtimeKey(Binding{PrincipalID: request.PrincipalID, ContextID: request.ContextID, RuntimeMode: "gateway"})]
	if entry == nil || entry.RuntimeID != request.RuntimeID || entry.PolicyVersion != request.PolicyVersion || entry.State != Ready && entry.State != Busy && entry.State != Idle {
		m.mu.Unlock()
		writeJSON(w, http.StatusServiceUnavailable, map[string]string{"error": "runtime unavailable"})
		return
	}
	address, auth := entry.Address, entry.auth
	m.mu.Unlock()
	body, _ := json.Marshal(map[string]any{"refs": request.Refs})
	upstream, err := http.NewRequestWithContext(r.Context(), http.MethodPost, strings.TrimRight(address, "/")+"/v1/skill-ref-lease", bytes.NewReader(body))
	if err != nil {
		writeRuntimeFailure(w, http.StatusBadGateway, err)
		return
	}
	upstream.Header.Set("Authorization", "Bearer "+auth)
	upstream.Header.Set("Content-Type", "application/json")
	response, err := m.cfg.HTTP.Do(upstream)
	if err != nil {
		writeRuntimeFailure(w, http.StatusBadGateway, err)
		return
	}
	defer response.Body.Close()
	result, err := io.ReadAll(io.LimitReader(response.Body, 128*1024+1))
	if err != nil || len(result) > 128*1024 || response.StatusCode != http.StatusOK {
		writeRuntimeFailure(w, http.StatusBadGateway, err)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	_, _ = w.Write(result)
}
