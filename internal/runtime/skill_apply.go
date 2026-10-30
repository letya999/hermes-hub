package runtime

import (
	"encoding/json"
	"net/http"
	"os"
	"sync"

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
	// The store changed — refresh the agent-visible view in place so the
	// next turn sees the new catalog without a respawn (R6.5). A rebuild
	// failure must not mask the applied mutation: the draft is consumed
	// and the next spawn rebuilds anyway, so report the stale view.
	out := map[string]string{"state": "applied", "revision": rev}
	if err := rebuildEffectiveView(); err != nil {
		out["view"] = "stale: " + err.Error()
	}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(out)
}

// viewRebuild serializes applies so two publishes cannot interleave their
// in-place view writes (a loser's temp files would hit the winner's sweep).
var viewRebuild sync.Mutex

// rebuildEffectiveView recomposes the agent-visible view in place after a
// store mutation, from the mounted sources and the render-time capability
// snapshot. No-op when the view contract is not mounted — the next spawn
// still rebuilds it.
func rebuildEffectiveView() error {
	dest := os.Getenv("HUB_SKILLS_EFFECTIVE_RW")
	if dest == "" {
		return nil
	}
	sources := []skillstore.Source{
		{Name: "platform", Dir: os.Getenv("HUB_SKILLS_PLATFORM")},
	}
	if org := os.Getenv("HUB_SKILLS_ORG"); org != "" {
		sources = append(sources, skillstore.Source{Name: "org", Dir: org})
	}
	sources = append(sources, skillstore.Source{Name: "user", Dir: os.Getenv("HUB_SKILLS_STORE")})
	viewRebuild.Lock()
	defer viewRebuild.Unlock()
	_, err := skillstore.RebuildEffective(dest, sources, os.Getenv("HUB_SKILLS_META"))
	return err
}
