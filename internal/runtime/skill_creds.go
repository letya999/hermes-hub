package runtime

import (
	"encoding/json"
	"net/http"
	"os"

	"github.com/letya999/hermes-hub/internal/skillstore"
)

// /v1/skill-creds is the managed credential sink for skill-scoped protected
// forms (SPEC-0084 R8.2): the communication gateway relays here so the owning
// runtime writes its own store's .state — secrets never cross container env
// or argv. Cells read them at exec time.
func (s *runtimeHTTP) skillCreds(w http.ResponseWriter, r *http.Request) {
	if !s.authorized(r) {
		writeRuntimeError(w, http.StatusUnauthorized, "unauthorized")
		return
	}
	var request SkillCredsRequest
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 64*1024)).Decode(&request); err != nil {
		writeRuntimeError(w, http.StatusBadRequest, "invalid creds request")
		return
	}
	if err := skillstore.WriteCredentials(os.Getenv("HUB_SKILLS_STORE"), request.Skill, request.Values); err != nil {
		writeRuntimeError(w, http.StatusBadRequest, err.Error())
		return
	}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]string{"state": "applied"})
}
