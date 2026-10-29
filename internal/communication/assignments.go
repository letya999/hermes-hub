package communication

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/letya999/hermes-hub/internal/commodel"
	"github.com/letya999/hermes-hub/internal/identity"
	"github.com/letya999/hermes-hub/internal/locale"
	"github.com/letya999/hermes-hub/internal/routines"
)

// handleAssignments serves POST (create, optional apply) and GET (owner
// list) on /v1/assignments (SPEC-0080 R2.1). Owner scoping comes from the
// verified caller envelope вЂ” the engine refuses foreign ids outright.
func (g *Gateway) handleAssignments(w http.ResponseWriter, r *http.Request) {
	caller, ok := g.authorizeControl(r)
	if !ok {
		http.Error(w, "unauthorized", http.StatusUnauthorized)
		return
	}
	if r.Method == http.MethodGet {
		items, err := g.spool.rt.ListAssignments(caller)
		if err != nil {
			http.Error(w, "list failed", http.StatusInternalServerError)
			return
		}
		writeJSON(w, items)
		return
	}
	var req struct {
		Title  string                    `json:"title"`
		Intent string                    `json:"intent"`
		Apply  bool                      `json:"apply"`
		Plan   *commodel.AssignmentPlan  `json:"plan"`
		Grants commodel.AssignmentGrants `json:"grants"`
	}
	if decodeJSON(r, &req) != nil {
		http.Error(w, "invalid assignment", http.StatusBadRequest)
		return
	}
	if req.Apply && (req.Plan == nil || len(req.Plan.Sources) == 0) {
		http.Error(w, "assignment plan needs a source", http.StatusBadRequest)
		return
	}
	now := g.now().UTC()
	sum := sha256.Sum256([]byte(caller.PrincipalID + "\x00" + req.Intent))
	item := commodel.Assignment{
		AssignmentID:   "asg-" + newID(),
		Revision:       1,
		Envelope:       caller,
		OrganizationID: g.config.OrganizationID,
		Intent:         req.Intent,
		IntentHash:     hex.EncodeToString(sum[:]),
		Title:          req.Title,
		State:          "draft",
		Plan:           req.Plan,
		Grants:         req.Grants,
		CreatedAt:      now,
		UpdatedAt:      now,
	}
	created, _, err := g.spool.rt.CreateAssignment(item, caller)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	if req.Apply {
		created, err = g.spool.rt.ApplyAssignment(created.AssignmentID, caller)
		if err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
	}
	writeJSON(w, created)
}

// handleAssignmentItem serves GET card on /v1/assignments/<id>
// (SPEC-0080 R2.1). Foreign ids collapse to 404 — no existence leak.
func (g *Gateway) handleAssignmentItem(w http.ResponseWriter, r *http.Request) {
	caller, ok := g.authorizeControl(r)
	if !ok {
		http.Error(w, "unauthorized", http.StatusUnauthorized)
		return
	}
	id, action, _ := strings.Cut(strings.TrimPrefix(r.URL.Path, "/v1/assignments/"), "/")
	if id == "plan" && action == "" && r.Method == http.MethodPost {
		g.assignmentPlanDryRun(w, r, caller)
		return
	}
	item, err := g.spool.rt.LoadAssignment(id, caller)
	if err != nil {
		http.NotFound(w, r)
		return
	}
	if r.Method == http.MethodGet && action == "" {
		writeJSON(w, item)
		return
	}
	if action == "plan" && r.Method == http.MethodPost {
		g.assignmentPlanUpdate(w, r, id, caller, item)
		return
	}
	if action == "progress" && r.Method == http.MethodPost {
		g.assignmentProgress(w, r, id, caller)
		return
	}
	if r.Method != http.MethodPost || (action != "apply" && action != "pause" && action != "resume" && action != "cancel" && action != "complete") {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	if item.State == "cancelled" {
		http.Error(w, "cancelled is terminal", http.StatusConflict)
		return
	}
	var control struct {
		ExpectedRevision *uint64 `json:"expected_revision"`
	}
	if decodeJSON(r, &control) != nil {
		http.Error(w, "invalid assignment control", http.StatusBadRequest)
		return
	}
	if control.ExpectedRevision == nil || *control.ExpectedRevision != item.Revision {
		http.Error(w, "revision conflict", http.StatusConflict)
		return
	}
	switch action {
	case "apply":
		item, err = g.spool.rt.ApplyAssignment(id, caller, *control.ExpectedRevision)
	case "pause":
		err = g.spool.rt.PauseAssignment(id, caller, *control.ExpectedRevision)
	case "resume":
		err = g.spool.rt.ResumeAssignment(id, caller, *control.ExpectedRevision)
	case "cancel":
		err = g.spool.rt.CancelAssignment(id, caller, *control.ExpectedRevision)
	case "complete":
		item, err = g.spool.rt.CompleteAssignment(id, caller, *control.ExpectedRevision)
	}
	if err != nil {
		status := http.StatusBadRequest
		if errors.Is(err, routines.ErrRoutineRevision) {
			status = http.StatusConflict
		}
		http.Error(w, err.Error(), status)
		return
	}
	if action != "apply" {
		item, _ = g.spool.rt.LoadAssignment(id, caller)
	}
	writeJSON(w, item)
}

func (g *Gateway) assignmentProgress(w http.ResponseWriter, r *http.Request, id string, caller identity.Envelope) {
	var req struct {
		ExpectedRevision uint64 `json:"expected_revision"`
		JobID            string `json:"job_id"`
		Summary          string `json:"summary"`
		Finding          string `json:"finding"`
		NextStep         string `json:"next_step"`
		Complete         bool   `json:"complete"`
	}
	if decodeJSON(r, &req) != nil || req.ExpectedRevision == 0 || req.JobID == "" {
		http.Error(w, "invalid progress", http.StatusBadRequest)
		return
	}
	// A progress tool may update only the assignment that produced this
	// currently running trigger. The job carries immutable engine-stamped
	// assignment identity; no agent-supplied id can redirect the write.
	var job Job
	jobPath := filepath.Join(g.spool.root, "running", spoolFileID(req.JobID)+".json")
	if raw, err := os.ReadFile(jobPath); err != nil || json.Unmarshal(raw, &job) != nil || job.ID != req.JobID || job.AssignmentID != id || job.PrincipalID != caller.PrincipalID || job.ContextID != caller.ContextID {
		http.Error(w, "trigger job unavailable", http.StatusForbidden)
		return
	}
	if item, err := g.spool.rt.LoadAssignment(id, caller); err != nil || item.AppliedRev != job.AssignmentPlanRevision {
		http.Error(w, "assignment generation changed", http.StatusConflict)
		return
	}
	item, err := g.spool.rt.RecordAssignmentProgress(id, caller, req.ExpectedRevision, req.JobID, req.Summary, req.Finding, req.NextStep, req.Complete)
	if err != nil {
		status := http.StatusBadRequest
		if errors.Is(err, routines.ErrRoutineRevision) {
			status = http.StatusConflict
		}
		http.Error(w, err.Error(), status)
		return
	}
	writeJSON(w, item)
}

// assignmentPlanDryRun validates each plan source draft and returns
// per-source verdicts; it never persists (SPEC-0080 R2.1 dry-run).
func (g *Gateway) assignmentPlanDryRun(w http.ResponseWriter, r *http.Request, caller identity.Envelope) {
	var plan commodel.AssignmentPlan
	if decodeJSON(r, &plan) != nil || len(plan.Sources) == 0 {
		http.Error(w, "invalid plan", http.StatusBadRequest)
		return
	}
	if plan.Revision == 0 {
		plan.Revision = 1
	}
	if err := routines.ValidateAssignmentPlan(&plan, commodel.AssignmentGrants{}); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	verdicts := map[string]string{}
	for _, src := range plan.Sources {
		in := src.Schedule.Schedule()
		if in.ScheduleID == "" {
			in.ScheduleID = "dry-" + src.Name
		}
		in.Envelope = caller
		in.UserID = caller.PrincipalID
		in.ActorID = caller.PrincipalID
		in.ScopeID = "user:" + caller.PrincipalID
		in.OrganizationID = g.config.OrganizationID
		if err := validateScheduleDraft(normalizeScheduleDraft(in), caller); err != nil {
			verdicts[src.Name] = err.Error()
		} else {
			verdicts[src.Name] = "ok"
		}
	}
	writeJSON(w, verdicts)
}

// assignmentPlanUpdate applies a CAS-guarded plan replace; grant widening
// without owner authorization at the plan revision is refused outright
// (SPEC-0080 R2.4) — the chat /assignment authorize verb is the only path.
func (g *Gateway) assignmentPlanUpdate(w http.ResponseWriter, r *http.Request, id string, caller identity.Envelope, item commodel.Assignment) {
	var req struct {
		Plan             commodel.AssignmentPlan    `json:"plan"`
		Grants           *commodel.AssignmentGrants `json:"grants"`
		ExpectedRevision *uint64                    `json:"expected_revision"`
	}
	if decodeJSON(r, &req) != nil {
		http.Error(w, "invalid plan", http.StatusBadRequest)
		return
	}
	if req.ExpectedRevision == nil || *req.ExpectedRevision != item.Revision {
		http.Error(w, "revision conflict", http.StatusConflict)
		return
	}
	grants := item.Grants
	if req.Grants != nil {
		grants = *req.Grants
	}
	updated, err := g.spool.rt.UpdateAssignmentPlan(id, req.Plan, grants, caller, *req.ExpectedRevision)
	if err != nil {
		status := http.StatusBadRequest
		if strings.Contains(err.Error(), "authorization") {
			status = http.StatusForbidden
		} else if errors.Is(err, routines.ErrRoutineRevision) {
			status = http.StatusConflict
		}
		http.Error(w, err.Error(), status)
		return
	}
	writeJSON(w, updated)
}

// assignmentsCommand answers /assignments (SPEC-0080 R2.5): the owner's
// standing assignments with state and blocking reasons.
func (g *Gateway) assignmentsCommand(user User, sender int64, lang locale.Lang) string {
	items, err := g.spool.rt.ListAssignments(user.Envelope(sender))
	if err != nil {
		return locale.T(lang, "assignment.list-unavailable")
	}
	lines := []string{locale.T(lang, "assignment.list-title")}
	for _, item := range items {
		line := item.AssignmentID + " — " + item.Title + ", " + item.State
		if item.Blocked != "" {
			line += " (" + item.Blocked + ")"
		}
		if item.Checkpoint.WakesToday > 0 {
			line += ", " + locale.T(lang, "assignment.wakes-today", item.Checkpoint.WakesToday)
		}
		lines = append(lines, line)
	}
	if len(items) == 0 {
		lines = append(lines, locale.T(lang, "assignment.list-empty"))
	}
	return strings.Join(lines, "\n")
}

// assignmentCommand answers /assignment <id> <verb> (SPEC-0080 R2.5):
// status|pause|resume|cancel|complete steer the record; authorize stamps
// the current plan revision as grant-authorized — the only grant-widening
// path, unreachable over REST or agent tools.
func (g *Gateway) assignmentCommand(user User, sender int64, text string, lang locale.Lang) string {
	fields := strings.Fields(text)
	if len(fields) < 3 {
		return locale.T(lang, "assignment.usage")
	}
	caller := user.Envelope(sender)
	id, verb := fields[1], fields[2]
	item, err := g.spool.rt.LoadAssignment(id, caller)
	if err != nil {
		return locale.T(lang, "assignment.unavailable")
	}
	switch verb {
	case "status":
		out := locale.T(lang, "assignment.status", id, item.State, item.Revision)
		if item.Blocked != "" {
			out += ", " + item.Blocked
		}
		out += ", " + locale.T(lang, "assignment.wakes-today", item.Checkpoint.WakesToday)
		if item.Plan != nil {
			for _, source := range item.Plan.Unavailable {
				out += "\n" + locale.T(lang, "assignment.unavailable-source", source.Reference, source.Reason)
			}
		}
		return out
	case "pause":
		err = g.spool.rt.PauseAssignment(id, caller)
	case "resume":
		err = g.spool.rt.ResumeAssignment(id, caller)
	case "cancel":
		err = g.spool.rt.CancelAssignment(id, caller)
	case "complete":
		_, err = g.spool.rt.CompleteAssignment(id, caller)
	case "authorize":
		if len(fields) != 5 || item.Plan == nil {
			return locale.T(lang, "assignment.authorize-usage")
		}
		wakes, e1 := strconv.Atoi(fields[3])
		sources, e2 := strconv.Atoi(fields[4])
		if e1 != nil || e2 != nil {
			return locale.T(lang, "assignment.bad-limits")
		}
		err = g.spool.rt.AuthorizeAssignmentGrants(id, item.Plan.Revision+1, commodel.AssignmentGrants{MaxWakesPerDay: wakes, MaxSources: sources}, caller)
	default:
		return locale.T(lang, "assignment.unknown-verb", verb)
	}
	if err != nil {
		return locale.T(lang, "assignment.rejected", verb)
	}
	return locale.T(lang, "assignment.accepted", id, verb)
}
