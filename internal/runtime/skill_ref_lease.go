package runtime

import (
	"encoding/json"
	"net/http"
	"os"
	"path/filepath"

	"github.com/letya999/hermes-hub/internal/envstore"
	"github.com/letya999/hermes-hub/internal/skillstore"
)

// skillRefLease serves only configured names to the owning runtime's control peer.
func (s *runtimeHTTP) skillRefLease(w http.ResponseWriter, r *http.Request) {
	if !s.authorized(r) {
		writeRuntimeError(w, http.StatusUnauthorized, "unauthorized")
		return
	}
	if r.Method != http.MethodPost {
		writeRuntimeError(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}
	var request struct {
		Refs []string `json:"refs"`
	}
	if json.NewDecoder(http.MaxBytesReader(w, r.Body, 4096)).Decode(&request) != nil || len(request.Refs) == 0 || len(request.Refs) > 16 {
		writeRuntimeError(w, http.StatusBadRequest, "invalid refs")
		return
	}
	for _, ref := range request.Refs {
		if !skillstore.ValidCredentialName(ref) {
			writeRuntimeError(w, http.StatusBadRequest, "invalid refs")
			return
		}
	}
	values, err := envstore.Load(filepath.Join(os.Getenv("HUB_STATE"), envstore.FileName), os.Getenv("HUB_SELF_ENV_KEYS"), os.Getenv("HUB_PROTECTED_ENV_KEYS"))
	if err != nil {
		writeRuntimeError(w, http.StatusServiceUnavailable, "self-env unavailable")
		return
	}
	result := map[string]string{}
	for _, ref := range request.Refs {
		if values[ref] != "" {
			result[ref] = values[ref]
		}
	}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(result)
}
