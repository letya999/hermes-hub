package supervisor

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"

	hubruntime "github.com/letya999/hermes-hub/internal/runtime"
)

func (m *Manager) sessionRead(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeJSON(w, http.StatusMethodNotAllowed, map[string]string{"error": "method not allowed"})
		return
	}
	r.Body = http.MaxBytesReader(w, r.Body, 64<<10)
	defer r.Body.Close()
	var request hubruntime.SessionReadRequest
	if json.NewDecoder(r.Body).Decode(&request) != nil || !hubruntime.ValidSessionReadRequest(request) {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid request"})
		return
	}
	binding, err := m.bindingFor(r.Context(), request.ExecuteRequest)
	if err != nil {
		writeJSON(w, http.StatusConflict, map[string]string{"error": "binding rejected"})
		return
	}
	runtime, err := m.Ensure(r.Context(), binding)
	if err != nil {
		writeJSON(w, http.StatusServiceUnavailable, map[string]string{"error": "runtime unavailable"})
		return
	}
	defer m.ReleaseBinding(binding)
	body, _ := json.Marshal(request)
	upstream, err := http.NewRequestWithContext(r.Context(), http.MethodPost, runtime.Address+"/v1/session-read", bytes.NewReader(body))
	if err != nil {
		writeJSON(w, http.StatusBadGateway, map[string]string{"error": "runtime unavailable"})
		return
	}
	upstream.Header.Set("Authorization", "Bearer "+binding.runtimeAuth)
	upstream.Header.Set("Content-Type", "application/json")
	client := *m.cfg.HTTP
	client.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	response, err := client.Do(upstream) // #nosec G704 -- supervisor-owned runtime address
	if err != nil {
		writeJSON(w, http.StatusBadGateway, map[string]string{"error": "runtime unavailable"})
		return
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		writeJSON(w, http.StatusBadGateway, map[string]string{"error": "session unavailable"})
		return
	}
	result, err := io.ReadAll(io.LimitReader(response.Body, 1<<20+1))
	if err != nil || len(result) > 1<<20 || !json.Valid(result) {
		writeJSON(w, http.StatusBadGateway, map[string]string{"error": "invalid session response"})
		return
	}
	w.Header().Set("Content-Type", "application/json")
	_, _ = w.Write(result)
}
