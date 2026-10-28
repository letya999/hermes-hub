package runtime

import (
	"encoding/json"
	"net/http"
	"os"

	"github.com/letya999/hermes-hub/internal/skillstore"
)

// /v1/skill-apply is the sole mutator for the mounted skill stores (R5.3):
// the communication confirmation card relays here after the owner's click.
// Drafts never self-publish — an approver name is mandatory.
type skillApplyRequest struct {
	Action       string `json:"action"`
	DraftID      string `json:"draft_id"`
	SkillName    string `json:"skill_name"`
	Scope        string `json:"scope"`
	BaseRevision string `json:"base_revision"`
	Approver     string `json:"approver"`
}

func (s *runtimeHTTP) skillApply(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost || !s.authorized(r) {
		writeRuntimeError(w, http.StatusUnauthorized, "unauthorized")
		return
	}
	var request skillApplyRequest
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 64*1024)).Decode(&request); err != nil {
		writeRuntimeError(w, http.StatusBadRequest, "invalid apply request")
		return
	}
	scope := skillstore.ScopeUser
	storePath := os.Getenv("HUB_SKILLS_STORE")
	if request.Scope == "org" {
		switch role := os.Getenv("HUB_ORGANIZATION_ROLE"); role {
		case "owner", "admin":
		default:
			writeRuntimeError(w, http.StatusForbidden, "org publish requires an owner/admin role")
			return
		}
		scope = skillstore.ScopeOrg
		storePath = os.Getenv("HUB_SKILLS_ORG")
	}
	st, err := skillstore.Open(storePath, scope)
	if err != nil {
		writeRuntimeError(w, http.StatusInternalServerError, "skill store unavailable")
		return
	}
	var rev string
	switch request.Action {
	case "publish":
		if rev, err = st.PublishDraft(request.DraftID, request.Approver); err != nil {
			writeRuntimeError(w, http.StatusConflict, err.Error())
			return
		}
	case "rollback":
		if rev, err = st.Rollback(request.SkillName, request.BaseRevision); err != nil {
			writeRuntimeError(w, http.StatusConflict, err.Error())
			return
		}
	default:
		writeRuntimeError(w, http.StatusBadRequest, "unknown action")
		return
	}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]string{"state": "applied", "revision": rev})
}
