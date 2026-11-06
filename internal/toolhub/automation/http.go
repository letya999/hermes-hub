package automation

import (
	"crypto/subtle"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/letya999/hermes-hub/internal/commodel"
	"github.com/letya999/hermes-hub/internal/identity"
	"github.com/letya999/hermes-hub/internal/routines"
)

// ControlHandler is the service-only automation boundary. Communication
// supplies its verified channel envelope; the control bearer pins the
// asserted principal, while Host uses ToolHub's enrolled identity for PDP.
func (h *Host) ControlHandler(token string) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/automation/operations" {
			http.NotFound(w, r)
			return
		}
		if r.Method != http.MethodPost {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		if r.Header.Get("Origin") != "" {
			http.Error(w, "browser origin forbidden", http.StatusForbidden)
			return
		}
		value := strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer ")
		principal := r.Header.Get("X-Hub-Principal")
		if token == "" || subtle.ConstantTimeCompare([]byte(value), []byte(token)) != 1 || !identity.ValidID(principal) {
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}
		var body struct {
			Operation string            `json:"operation"`
			Caller    identity.Envelope `json:"caller"`
			Payload   json.RawMessage   `json:"payload"`
		}
		decoder := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<20))
		decoder.DisallowUnknownFields()
		if decoder.Decode(&body) != nil || decoder.Decode(new(any)) != io.EOF || body.Caller.PrincipalID != principal || body.Caller.Validate(principal, body.Caller.ContextID, body.Caller.RuntimeID, body.Caller.PolicyVersion) != nil {
			http.Error(w, "invalid automation request", http.StatusBadRequest)
			return
		}
		var result any
		var err error
		switch body.Operation {
		case "schedule.replay":
			var args struct {
				Schedule commodel.Schedule `json:"schedule"`
			}
			if json.Unmarshal(body.Payload, &args) != nil {
				http.Error(w, "invalid automation request", http.StatusBadRequest)
				return
			}
			var item commodel.Schedule
			var found bool
			item, found, err = h.CheckCreationReplay(r.Context(), body.Caller, args.Schedule)
			result = struct {
				Schedule commodel.Schedule `json:"schedule"`
				Found    bool              `json:"found"`
			}{item, found}
		case "schedule.create":
			var args struct {
				Schedule   commodel.Schedule `json:"schedule"`
				NativeCron string            `json:"native_cron"`
				Baseline   *string           `json:"baseline"`
			}
			if json.Unmarshal(body.Payload, &args) != nil {
				http.Error(w, "invalid automation request", http.StatusBadRequest)
				return
			}
			result, err = h.CreateScheduleWithBaseline(r.Context(), body.Caller, args.Schedule, args.NativeCron, args.Baseline)
		case "schedule.list":
			result, err = h.ListSchedules(r.Context(), body.Caller)
		case "schedule.load":
			var args struct {
				ID string `json:"id"`
			}
			if json.Unmarshal(body.Payload, &args) != nil || args.ID == "" {
				http.Error(w, "invalid automation request", http.StatusBadRequest)
				return
			}
			result, err = h.LoadSchedule(r.Context(), body.Caller, args.ID)
		case "schedule.card":
			var args struct {
				ID string `json:"id"`
			}
			if json.Unmarshal(body.Payload, &args) != nil || args.ID == "" {
				http.Error(w, "invalid automation request", http.StatusBadRequest)
				return
			}
			result, err = h.RoutineCard(r.Context(), body.Caller, args.ID)
		case "schedule.preview":
			var args struct {
				ID    string `json:"id"`
				Count int    `json:"count"`
			}
			if json.Unmarshal(body.Payload, &args) != nil || args.ID == "" || args.Count < 1 || args.Count > 20 {
				http.Error(w, "invalid automation request", http.StatusBadRequest)
				return
			}
			result, err = h.PreviewRoutine(r.Context(), body.Caller, args.ID, args.Count)
		case "schedule.history":
			var args struct {
				ID string `json:"id"`
			}
			if json.Unmarshal(body.Payload, &args) != nil || args.ID == "" {
				http.Error(w, "invalid automation request", http.StatusBadRequest)
				return
			}
			result, err = h.RoutineHistory(r.Context(), body.Caller, args.ID)
		case "schedule.sample":
			var args struct {
				ID     string `json:"id"`
				Filter string `json:"filter"`
			}
			if json.Unmarshal(body.Payload, &args) != nil || args.ID == "" {
				http.Error(w, "invalid automation request", http.StatusBadRequest)
				return
			}
			result, err = h.SampleWatch(r.Context(), body.Caller, args.ID, args.Filter)
		case "schedule.edit":
			var args struct {
				ID       string                     `json:"id"`
				Patch    map[string]json.RawMessage `json:"patch"`
				Resume   bool                       `json:"resume"`
				Baseline *string                    `json:"baseline"`
			}
			if json.Unmarshal(body.Payload, &args) != nil || args.ID == "" || args.Patch == nil {
				http.Error(w, "invalid automation request", http.StatusBadRequest)
				return
			}
			result, err = h.EditRoutine(r.Context(), body.Caller, args.ID, args.Patch, args.Resume, args.Baseline)
		case "schedule.destinations":
			var args struct {
				After string `json:"after"`
				Limit int    `json:"limit"`
				Query string `json:"query"`
			}
			if json.Unmarshal(body.Payload, &args) != nil || args.Limit < 1 || args.Limit > 100 {
				http.Error(w, "invalid automation request", http.StatusBadRequest)
				return
			}
			result, err = h.ListRoutineDestinations(r.Context(), body.Caller, args.After, args.Limit, args.Query)
		case "schedule.pause", "schedule.delete", "schedule.snooze":
			var args struct {
				ID               string    `json:"id"`
				Until            time.Time `json:"until"`
				ExpectedRevision *uint64   `json:"expected_revision"`
			}
			if json.Unmarshal(body.Payload, &args) != nil || args.ID == "" {
				http.Error(w, "invalid automation request", http.StatusBadRequest)
				return
			}
			var expected []uint64
			if args.ExpectedRevision != nil {
				expected = append(expected, *args.ExpectedRevision)
			}
			switch body.Operation {
			case "schedule.pause":
				err = h.PauseSchedule(r.Context(), body.Caller, args.ID, expected...)
			case "schedule.delete":
				err = h.DeleteSchedule(r.Context(), body.Caller, args.ID, expected...)
			case "schedule.snooze":
				err = h.SnoozeSchedule(r.Context(), body.Caller, args.ID, args.Until, expected...)
			}
		case "assignment.list":
			result, err = h.ListAssignments(r.Context(), body.Caller)
		case "assignment.load":
			var args struct {
				ID string `json:"id"`
			}
			if json.Unmarshal(body.Payload, &args) != nil || args.ID == "" {
				http.Error(w, "invalid automation request", http.StatusBadRequest)
				return
			}
			result, err = h.LoadAssignment(r.Context(), body.Caller, args.ID)
		case "assignment.create":
			var args struct {
				Assignment commodel.Assignment `json:"assignment"`
			}
			if json.Unmarshal(body.Payload, &args) != nil {
				http.Error(w, "invalid automation request", http.StatusBadRequest)
				return
			}
			result, _, err = h.CreateAssignment(r.Context(), body.Caller, args.Assignment)
		case "assignment.apply", "assignment.pause", "assignment.resume", "assignment.cancel", "assignment.complete", "assignment.delete":
			var args struct {
				ID               string  `json:"id"`
				ExpectedRevision *uint64 `json:"expected_revision"`
			}
			if json.Unmarshal(body.Payload, &args) != nil || args.ID == "" {
				http.Error(w, "invalid automation request", http.StatusBadRequest)
				return
			}
			var expected []uint64
			if args.ExpectedRevision != nil {
				expected = append(expected, *args.ExpectedRevision)
			}
			switch body.Operation {
			case "assignment.apply":
				result, err = h.ApplyAssignment(r.Context(), body.Caller, args.ID, expected...)
			case "assignment.pause":
				err = h.PauseAssignment(r.Context(), body.Caller, args.ID, expected...)
			case "assignment.resume":
				err = h.ResumeAssignment(r.Context(), body.Caller, args.ID, expected...)
			case "assignment.cancel":
				err = h.CancelAssignment(r.Context(), body.Caller, args.ID, expected...)
			case "assignment.complete":
				result, err = h.CompleteAssignment(r.Context(), body.Caller, args.ID, expected...)
			case "assignment.delete":
				err = h.DeleteAssignment(r.Context(), body.Caller, args.ID, expected...)
			}
		case "assignment.plan":
			var args struct {
				ID               string                    `json:"id"`
				Plan             commodel.AssignmentPlan   `json:"plan"`
				Grants           commodel.AssignmentGrants `json:"grants"`
				ExpectedRevision *uint64                   `json:"expected_revision"`
			}
			if json.Unmarshal(body.Payload, &args) != nil || args.ID == "" || args.ExpectedRevision == nil {
				http.Error(w, "invalid automation request", http.StatusBadRequest)
				return
			}
			result, err = h.UpdateAssignmentPlan(r.Context(), body.Caller, args.ID, args.Plan, args.Grants, *args.ExpectedRevision)
		case "assignment.grants":
			var args struct {
				ID      string                    `json:"id"`
				PlanRev uint64                    `json:"plan_revision"`
				Grants  commodel.AssignmentGrants `json:"grants"`
			}
			if json.Unmarshal(body.Payload, &args) != nil || args.ID == "" || args.PlanRev == 0 {
				http.Error(w, "invalid automation request", http.StatusBadRequest)
				return
			}
			err = h.AuthorizeAssignmentGrants(r.Context(), body.Caller, args.ID, args.PlanRev, args.Grants)
		case "assignment.progress":
			var args struct {
				ID               string `json:"id"`
				ExpectedRevision uint64 `json:"expected_revision"`
				JobID            string `json:"job_id"`
				Summary          string `json:"summary"`
				Finding          string `json:"finding"`
				NextStep         string `json:"next_step"`
				Complete         bool   `json:"complete"`
			}
			if json.Unmarshal(body.Payload, &args) != nil || args.ID == "" || args.ExpectedRevision == 0 || args.JobID == "" {
				http.Error(w, "invalid automation request", http.StatusBadRequest)
				return
			}
			result, err = h.RecordAssignmentProgress(r.Context(), body.Caller, args.ID, args.ExpectedRevision, args.JobID, args.Summary, args.Finding, args.NextStep, args.Complete)
		case "result.report":
			var args struct {
				JobID  string `json:"job_id"`
				Status string `json:"status"`
				Text   string `json:"text"`
			}
			if json.Unmarshal(body.Payload, &args) != nil || args.JobID == "" || !commodel.TerminalStatus(args.Status) {
				http.Error(w, "invalid automation request", http.StatusBadRequest)
				return
			}
			err = h.ReportResult(r.Context(), body.Caller.PrincipalID, args.JobID, args.Status, args.Text)
		case "hook.intake":
			var args struct {
				ID   string `json:"id"`
				Body []byte `json:"body"`
			}
			if json.Unmarshal(body.Payload, &args) != nil || args.ID == "" || len(args.Body) == 0 || len(args.Body) > 64*1024 {
				http.Error(w, "invalid automation request", http.StatusBadRequest)
				return
			}
			err = h.IntakeHook(r.Context(), body.Caller, args.ID, args.Body)
		default:
			http.NotFound(w, r)
			return
		}
		if err != nil {
			status := http.StatusBadRequest
			if errors.Is(err, errUnauthorized) || errors.Is(err, routines.ErrAssignmentGrantWidening) {
				status = http.StatusForbidden
			} else if errors.Is(err, routines.ErrRoutineRevision) || errors.Is(err, routines.ErrScheduleConflict) {
				status = http.StatusConflict
			} else if errors.Is(err, routines.ErrAssignmentNotFound) {
				status = http.StatusNotFound
			} else if errors.Is(err, routines.ErrRoutineNotOwned) {
				status = http.StatusNotFound
			}
			http.Error(w, "automation operation failed", status)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{"result": result})
	})
}
