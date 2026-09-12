package supervisor

import (
	"net/http"
	"strings"
)

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
	m.mu.Unlock()
	if !ok {
		writeJSON(w, 404, map[string]string{"error": "unknown job"})
		return
	}
	writeJSON(w, 200, map[string]any{"job_id": id, "identity": record.Request.Envelope, "status": record.Status, "outcome": record.Response})
}
