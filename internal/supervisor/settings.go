package supervisor

import (
	"encoding/json"
	"io"
	"net/http"
	"path/filepath"

	"github.com/letya999/hermes-hub/internal/identity"
	"github.com/letya999/hermes-hub/shared/locale"
)

type settingsEditRequest struct {
	PrincipalID  string  `json:"principal_id"`
	Environment  string  `json:"environment"`
	Key          string  `json:"key"`
	Value        *string `json:"value,omitempty"`
	BaseRevision string  `json:"base_revision"`
}

func (m *Manager) settingsEditHTTP(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeJSON(w, http.StatusMethodNotAllowed, map[string]string{"error": locale.T(locale.LangEN, "supervisor.settings-invalid")})
		return
	}
	r.Body = http.MaxBytesReader(w, r.Body, 4096)
	defer r.Body.Close()
	var request settingsEditRequest
	dec := json.NewDecoder(r.Body)
	dec.DisallowUnknownFields()
	if dec.Decode(&request) != nil || dec.Decode(new(any)) != io.EOF ||
		!identity.ValidID(request.PrincipalID) || request.Environment != m.environment() ||
		request.Key == "" || request.BaseRevision == "" {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": locale.T(locale.LangEN, "supervisor.settings-invalid")})
		return
	}
	lang := locale.LangOf(filepath.Join(m.cfg.SpacesRoot, request.PrincipalID, "workspace"))
	if m.cfg.SettingsEdit == nil {
		writeJSON(w, http.StatusForbidden, map[string]string{"error": locale.T(lang, "supervisor.settings-denied")})
		return
	}
	result, err := m.cfg.SettingsEdit(r.Context(), request.PrincipalID, request.Environment, request.Key, request.Value, request.BaseRevision)
	if err != nil {
		writeJSON(w, http.StatusConflict, map[string]string{"error": locale.T(lang, "supervisor.settings-denied")})
		return
	}
	writeJSON(w, http.StatusOK, result)
}
