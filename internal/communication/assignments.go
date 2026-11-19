package communication

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"net/http"
	"strconv"
	"strings"

	"github.com/letya999/hermes-hub/internal/commodel"
	"github.com/letya999/hermes-hub/internal/identity"
	"github.com/letya999/hermes-hub/internal/routines"
	"github.com/letya999/hermes-hub/shared/locale"
)

// handleAssignments serves POST (create, optional apply) and GET (owner
// list) on /v1/assignments (SPEC-0080 R2.1). Owner scoping comes from the
// verified caller envelope вЂ” the engine refuses foreign ids outright.
func (g *Gateway) handleAssignments(w http.ResponseWriter, r *http.Request) {
	caller, policy, ok := g.authorizeAgentControl(r)
	if !ok || !agentOperationAllowed(policy, "tasks", "delegate", r.Method != http.MethodGet) {
		denyAgentOperation(w, r)
		return
	}
	if r.Method == http.MethodGet {
		var items []commodel.Assignment
		err := g.automationCall(r.Context(), caller, "assignment.list", nil, &items)
		if err != nil {
			http.Error(w, "list failed", automationStatus(err))
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
	var created commodel.Assignment
	err := g.automationCall(r.Context(), caller, "assignment.create", map[string]any{"assignment": item}, &created)
	if err != nil {
		http.Error(w, "assignment create failed", automationStatus(err))
		return
	}
	if req.Apply {
		err = g.automationCall(r.Context(), caller, "assignment.apply", map[string]any{"id": created.AssignmentID}, &created)
		if err != nil {
			http.Error(w, "assignment apply failed", automationStatus(err))
			return
		}
	}
	writeJSON(w, created)
}

// handleAssignmentItem serves GET card on /v1/assignments/<id>
// (SPEC-0080 R2.1). Foreign ids collapse to 404 — no existence leak.
func (g *Gateway) handleAssignmentItem(w http.ResponseWriter, r *http.Request) {
	caller, policy, ok := g.authorizeAgentControl(r)
	dryRun := r.Method == http.MethodPost && r.URL.Path == "/v1/assignments/plan"
	if !ok || !agentOperationAllowed(policy, "tasks", "delegate", r.Method != http.MethodGet && !dryRun) {
		denyAgentOperation(w, r)
		return
	}
	id, action, _ := strings.Cut(strings.TrimPrefix(r.URL.Path, "/v1/assignments/"), "/")
	if id == "plan" && action == "" && r.Method == http.MethodPost {
		g.assignmentPlanDryRun(w, r, caller)
		return
	}
	var item commodel.Assignment
	err := g.automationCall(r.Context(), caller, "assignment.load", map[string]any{"id": id}, &item)
	if err != nil {
		http.NotFound(w, r)
		return
	}
	if r.Method == http.MethodGet && action == "" {
		writeJSON(w, item)
		return
	}
	// DELETE retires the record terminally (ADR-0094 assignment_remove):
	// CAS-guarded like every mutation; applied artifacts retire with it.
	if r.Method == http.MethodDelete && action == "" {
		var control struct {
			ExpectedRevision *uint64 `json:"expected_revision"`
		}
		if decodeJSON(r, &control) != nil || control.ExpectedRevision == nil || *control.ExpectedRevision != item.Revision {
			http.Error(w, "revision conflict", http.StatusConflict)
			return
		}
		if err := g.automationCall(r.Context(), caller, "assignment.delete", map[string]any{"id": id, "expected_revision": *control.ExpectedRevision}, nil); err != nil {
			http.Error(w, "assignment delete failed", automationStatus(err))
			return
		}
		writeJSON(w, map[string]string{"deleted": id})
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
		err = g.automationCall(r.Context(), caller, "assignment.apply", map[string]any{"id": id, "expected_revision": *control.ExpectedRevision}, &item)
	case "pause":
		err = g.automationCall(r.Context(), caller, "assignment.pause", map[string]any{"id": id, "expected_revision": *control.ExpectedRevision}, nil)
	case "resume":
		err = g.automationCall(r.Context(), caller, "assignment.resume", map[string]any{"id": id, "expected_revision": *control.ExpectedRevision}, nil)
	case "cancel":
		err = g.automationCall(r.Context(), caller, "assignment.cancel", map[string]any{"id": id, "expected_revision": *control.ExpectedRevision}, nil)
	case "complete":
		err = g.automationCall(r.Context(), caller, "assignment.complete", map[string]any{"id": id, "expected_revision": *control.ExpectedRevision}, &item)
	}
	if err != nil {
		http.Error(w, "assignment control failed", automationStatus(err))
		return
	}
	if action != "apply" {
		if err := g.automationCall(r.Context(), caller, "assignment.load", map[string]any{"id": id}, &item); err != nil {
			http.Error(w, "assignment state unavailable", automationStatus(err))
			return
		}
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
	var item commodel.Assignment
	err := g.automationCall(r.Context(), caller, "assignment.progress", map[string]any{"id": id, "expected_revision": req.ExpectedRevision, "job_id": req.JobID, "summary": req.Summary, "finding": req.Finding, "next_step": req.NextStep, "complete": req.Complete}, &item)
	if err != nil {
		http.Error(w, "assignment progress failed", automationStatus(err))
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
	var updated commodel.Assignment
	err := g.automationCall(r.Context(), caller, "assignment.plan", map[string]any{"id": id, "plan": req.Plan, "grants": grants, "expected_revision": *req.ExpectedRevision}, &updated)
	if err != nil {
		http.Error(w, "assignment plan failed", automationStatus(err))
		return
	}
	writeJSON(w, updated)
}

// assignmentsCommand answers /assignments (SPEC-0080 R2.5): the owner's
// standing assignments with state and blocking reasons.
func (g *Gateway) assignmentsCommand(user User, sender int64, lang locale.Lang) string {
	var items []commodel.Assignment
	err := g.automationCall(context.Background(), user.Envelope(sender), "assignment.list", nil, &items)
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
	var item commodel.Assignment
	err := g.automationCall(context.Background(), caller, "assignment.load", map[string]any{"id": id}, &item)
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
		err = g.automationCall(context.Background(), caller, "assignment.pause", map[string]any{"id": id}, nil)
	case "resume":
		err = g.automationCall(context.Background(), caller, "assignment.resume", map[string]any{"id": id}, nil)
	case "cancel":
		err = g.automationCall(context.Background(), caller, "assignment.cancel", map[string]any{"id": id}, nil)
	case "complete":
		err = g.automationCall(context.Background(), caller, "assignment.complete", map[string]any{"id": id}, nil)
	case "authorize":
		if len(fields) != 5 || item.Plan == nil {
			return locale.T(lang, "assignment.authorize-usage")
		}
		wakes, e1 := strconv.Atoi(fields[3])
		sources, e2 := strconv.Atoi(fields[4])
		if e1 != nil || e2 != nil {
			return locale.T(lang, "assignment.bad-limits")
		}
		err = g.automationCall(context.Background(), caller, "assignment.grants", map[string]any{"id": id, "plan_revision": item.Plan.Revision + 1, "grants": commodel.AssignmentGrants{MaxWakesPerDay: wakes, MaxSources: sources}}, nil)
	default:
		return locale.T(lang, "assignment.unknown-verb", verb)
	}
	if err != nil {
		return locale.T(lang, "assignment.rejected", verb)
	}
	return locale.T(lang, "assignment.accepted", id, verb)
}
