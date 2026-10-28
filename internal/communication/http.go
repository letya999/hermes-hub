package communication

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/letya999/hermes-hub/internal/identity"
	"github.com/letya999/hermes-hub/internal/serviceauth"
)

func (g *Gateway) Handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("/v1/slack/events", g.HandleSlackEvents)
	mux.HandleFunc("/v1/routines", g.handleRoutines)
	mux.HandleFunc("/v1/goals", g.handleGoals)
	mux.HandleFunc("/v1/routines/", g.handleRoutineItem)
	mux.HandleFunc("/v1/tasks", g.handleTasks)
	mux.HandleFunc("/v1/tasks/", g.handleTasks)
	mux.HandleFunc("/v1/inbound/", g.handleInbound)
	mux.HandleFunc("/v1/hooks/", g.handleHooks)
	mux.HandleFunc("/v1/assignments", g.handleAssignments)
	mux.HandleFunc("/v1/assignments/", g.handleAssignmentItem)
	mux.HandleFunc("/v1/mock/message", g.handleMockMessage)
	mux.HandleFunc("/v1/mock/outbox", g.handleMockOutbox)
	mux.HandleFunc("/v1/credential-forms", g.handleCredentialFormRequest)
	mux.HandleFunc("/v1/prepare-outcome", g.handlePrepareOutcome)
	mux.HandleFunc("/credentials/", g.serveCredentialForm)
	g.registerTelegramAuth(mux)
	return mux
}

func (g *Gateway) authorizeControl(r *http.Request) (identity.Envelope, bool) {
	token := strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer ")
	principal := r.Header.Get("X-Hub-Principal")
	if principal == "" {
		principal = r.Header.Get("X-Hub-User")
	}
	if !g.controlTokenAccepted(token, &principal) {
		return identity.Envelope{}, false
	}
	user := g.user(principal)
	if user.ID == "" || !user.Enabled {
		return identity.Envelope{}, false
	}
	if len(user.TelegramIDs) > 0 {
		return user.Envelope(user.TelegramIDs[0]), true
	}
	if len(user.SlackIDs) > 0 {
		return user.SlackEnvelope(user.SlackIDs[0].TeamID, user.SlackIDs[0].UserID), true
	}
	return identity.Envelope{}, false
}

// controlTokenAccepted checks the primary control bearer and, when a tokens
// file is configured, the enrolled sibling-space runtime tokens. The file is
// re-read on every call: enrollment rewrites it in place and control-plane
// calls are rare, so freshness beats caching. A matched sibling token pins
// the caller principal to the enrolled envelope — a borrowed token can never
// claim a different user's identity through the header.
func (g *Gateway) controlTokenAccepted(token string, principal *string) bool {
	if token == "" {
		return false
	}
	if g.config.ControlAuth != "" && token == g.config.ControlAuth {
		return true
	}
	if slices.Contains(g.config.controlAuthExtra, token) {
		return true
	}
	path := strings.TrimSpace(g.config.ControlTokensFile)
	if path == "" {
		return false
	}
	entries, err := serviceauth.ReadTokenEnvelopes(path)
	if err != nil {
		return false
	}
	env, ok := entries[token]
	if !ok {
		return false
	}
	user := g.user(env.PrincipalID)
	if (user.PolicyVersion != "" && user.PolicyVersion != env.PolicyVersion) || (user.RuntimeID != "" && user.RuntimeID != env.RuntimeID) {
		return false
	}
	*principal = env.PrincipalID
	return true
}

func (g *Gateway) handleRoutines(w http.ResponseWriter, r *http.Request) {
	caller, ok := g.authorizeControl(r)
	if !ok {
		http.Error(w, "unauthorized", http.StatusUnauthorized)
		return
	}
	switch r.Method {
	case http.MethodGet:
		limit := 20
		if value, ok := r.URL.Query()["limit"]; ok {
			var err error
			limit, err = strconv.Atoi(value[0])
			if err != nil || limit < 1 || limit > 100 {
				http.Error(w, "limit must be 1..100", http.StatusBadRequest)
				return
			}
		}
		if r.URL.Query().Get("view") == "destinations" {
			page, err := g.spool.ListRoutineDestinations(g.user(caller.PrincipalID), caller, r.URL.Query().Get("after"), limit, r.URL.Query().Get("query"))
			if err != nil {
				http.Error(w, "destination list failed", http.StatusInternalServerError)
				return
			}
			writeJSON(w, page)
			return
		}
		items, err := g.spool.ListSchedules(caller)
		if err != nil {
			http.Error(w, "list failed", http.StatusInternalServerError)
			return
		}
		if r.URL.RawQuery != "" {
			writeJSON(w, routinePage(items, r.URL.Query().Get("after"), limit, r.URL.Query().Get("kind"), r.URL.Query().Get("state"), r.URL.Query().Get("query"), g.spool.watchLastError))
		} else {
			writeJSON(w, items)
		}
	case http.MethodPost:
		var draft RoutineDraft
		if decodeJSON(r, &draft) != nil {
			http.Error(w, "invalid schedule", http.StatusBadRequest)
			return
		}
		in := draft.Schedule()
		in.Envelope = caller
		in.UserID = caller.PrincipalID
		in.ActorID = caller.PrincipalID
		in.ScopeID = "user:" + caller.PrincipalID
		in.OrganizationID = g.config.OrganizationID
		if in.Channel == "" {
			in.Channel = "telegram_bot"
		}
		ch := g.channel(in.Channel)
		if ch == nil {
			http.Error(w, "invalid schedule channel", http.StatusBadRequest)
			return
		}
		if prepared, err := ch.PrepareRoutine(&in, caller); err != nil {
			if errors.Is(err, errRoutineDenied) {
				http.Error(w, "mock chat not owned", http.StatusForbidden)
			} else {
				http.Error(w, err.Error(), http.StatusBadRequest)
			}
			return
		} else {
			caller = prepared
		}
		in = normalizeScheduleDraft(in)
		if err := validateScheduleDraft(in, caller); err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		g.spool.mu.Lock()
		if in.OriginJobID != "" || in.DestinationTaskID != "" {
			var err error
			in, err = g.spool.bindRoutineDestinationLocked(in, caller, g.user(caller.PrincipalID))
			if err != nil {
				g.spool.mu.Unlock()
				http.Error(w, err.Error(), http.StatusBadRequest)
				return
			}
			caller = in.Envelope
		}
		replayed, found, existingErr := g.spool.creationReplayLocked(in, caller)
		g.spool.mu.Unlock()
		if existingErr != nil {
			if errors.Is(existingErr, ErrScheduleConflict) {
				http.Error(w, existingErr.Error(), http.StatusConflict)
			} else {
				http.Error(w, existingErr.Error(), http.StatusBadRequest)
			}
			return
		}
		if found {
			writeJSON(w, replayed)
			return
		}
		now := g.now().UTC()
		if err := ValidateSchedule(in, caller, now); err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		var initialValue *string
		if in.JobKind == "check" {
			// The follow-up predicate is anchored at creation: probe once,
			// seed the baseline, then create — a dead source must not
			// become a silently-skipping schedule.
			value, err := g.checkBaseline(r.Context(), caller.PrincipalID, in.Probe)
			if err != nil {
				http.Error(w, err.Error(), http.StatusBadGateway)
				return
			}
			initialValue = &value
		}
		created, err := g.spool.createScheduleAt(in, caller, g.config.NativeCron, initialValue, now, g.user(caller.PrincipalID))
		if err != nil {
			status := http.StatusBadRequest
			if errors.Is(err, ErrScheduleConflict) {
				status = http.StatusConflict
			}
			http.Error(w, err.Error(), status)
			return
		}
		writeJSON(w, created)
	default:
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
	}
}

func (g *Gateway) handleRoutineItem(w http.ResponseWriter, r *http.Request) {
	caller, ok := g.authorizeControl(r)
	if !ok {
		http.Error(w, "unauthorized", http.StatusUnauthorized)
		return
	}
	path := strings.TrimPrefix(r.URL.Path, "/v1/routines/")
	id, action, _ := strings.Cut(path, "/")
	if id == "" {
		http.Error(w, "missing id", http.StatusBadRequest)
		return
	}
	switch {
	case r.Method == http.MethodGet && action == "history":
		limit := 20
		if value := r.URL.Query().Get("limit"); value != "" {
			var err error
			limit, err = strconv.Atoi(value)
			if err != nil || limit < 1 || limit > 100 {
				http.Error(w, "limit must be 1..100", http.StatusBadRequest)
				return
			}
		}
		items, err := g.spool.RoutineHistory(id, caller)
		if err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		writeJSON(w, routineHistoryPage(items, r.URL.Query().Get("after"), limit))
	case r.Method == http.MethodGet && action == "preview":
		limit := 20
		if value := r.URL.Query().Get("limit"); value != "" {
			var err error
			limit, err = strconv.Atoi(value)
			if err != nil || limit < 1 || limit > 20 {
				http.Error(w, "limit must be 1..20", http.StatusBadRequest)
				return
			}
		}
		preview, err := g.spool.PreviewRoutine(id, caller, limit, g.now().UTC())
		if err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		writeJSON(w, preview)
	case r.Method == http.MethodGet && action == "":
		card, err := g.getRoutineCard(id, caller)
		if err != nil {
			if errors.Is(err, ErrRoutineNotOwned) {
				http.NotFound(w, r)
			} else {
				http.Error(w, "state unavailable", http.StatusInternalServerError)
			}
			return
		}
		writeJSON(w, card)
	case r.Method == http.MethodPost && action == "snooze":
		var patch struct {
			Until            time.Time `json:"until"`
			ExpectedRevision *uint64   `json:"expected_revision"`
		}
		if decodeJSON(r, &patch) != nil || !patch.Until.After(g.now().UTC()) {
			http.Error(w, "future until required", http.StatusBadRequest)
			return
		}
		var expected []uint64
		if patch.ExpectedRevision != nil {
			expected = append(expected, *patch.ExpectedRevision)
		}
		if err := g.spool.SnoozeSchedule(id, caller, patch.Until, expected...); err != nil {
			if errors.Is(err, ErrRoutineRevision) {
				http.Error(w, err.Error(), http.StatusConflict)
				return
			}
			if errors.Is(err, ErrRoutineSnoozeNotLater) {
				http.Error(w, err.Error(), http.StatusBadRequest)
				return
			}
			http.Error(w, "schedule not owned", http.StatusForbidden)
			return
		}
		if len(expected) > 0 {
			g.writeRoutineCard(w, id, caller)
			return
		}
		w.WriteHeader(http.StatusOK)
	case r.Method == http.MethodDelete && action == "":
		expected, err := routineRevisionRequest(r)
		if err != nil {
			http.Error(w, "invalid request", http.StatusBadRequest)
			return
		}
		if err := g.spool.DeleteSchedule(id, caller, expected...); err != nil {
			if errors.Is(err, ErrRoutineRevision) {
				http.Error(w, err.Error(), http.StatusConflict)
				return
			}
			http.Error(w, err.Error(), http.StatusForbidden)
			return
		}
		if len(expected) > 0 {
			g.writeRoutineCard(w, id, caller)
			return
		}
		w.WriteHeader(http.StatusNoContent)
	case r.Method == http.MethodPost && action == "pause":
		expected, err := routineRevisionRequest(r)
		if err != nil {
			http.Error(w, "invalid request", http.StatusBadRequest)
			return
		}
		if err := g.spool.PauseSchedule(id, caller, expected...); err != nil {
			if errors.Is(err, ErrRoutineRevision) {
				http.Error(w, err.Error(), http.StatusConflict)
				return
			}
			http.Error(w, err.Error(), http.StatusForbidden)
			return
		}
		if len(expected) > 0 {
			g.writeRoutineCard(w, id, caller)
			return
		}
		w.WriteHeader(http.StatusOK)
	case r.Method == http.MethodPost && action == "sample":
		var req struct {
			Filter string `json:"filter"`
		}
		if r.ContentLength > 0 && decodeJSON(r, &req) != nil {
			http.Error(w, "invalid sample request", http.StatusBadRequest)
			return
		}
		toolCall := func(ctx context.Context, principal string, probe *WatchProbe) ([]byte, error) {
			return g.toolCallProbeWithin(ctx, principal, probe, sampleCallBudget)
		}
		sample, err := g.sampleRoutine(r.Context(), id, caller, req.Filter, toolCall)
		if err != nil {
			status := http.StatusBadRequest
			if errors.Is(err, ErrRoutineNotOwned) {
				status = http.StatusForbidden
			}
			http.Error(w, err.Error(), status)
			return
		}
		writeJSON(w, sample)
	case r.Method == http.MethodPost && (action == "" || action == "resume"):
		var patch map[string]json.RawMessage
		if decodeJSON(r, &patch) != nil {
			http.Error(w, "invalid patch", http.StatusBadRequest)
			return
		}
		baseline, err := g.routineEditBaseline(r.Context(), id, caller, patch)
		var item Schedule
		if err == nil {
			item, err = g.spool.editRoutine(id, caller, patch, action == "resume", g.now().UTC(), routineEditContext{User: g.user(caller.PrincipalID), Baseline: baseline})
		}
		if err != nil {
			status := http.StatusBadRequest
			if errors.Is(err, ErrRoutineNotOwned) {
				status = http.StatusForbidden
			}
			if errors.Is(err, ErrRoutineRevision) {
				status = http.StatusConflict
			}
			http.Error(w, err.Error(), status)
			return
		}
		writeJSON(w, item)
	default:
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
	}
}

// handleInbound serves one staged inbound file to its owning tools plane
// (GET) and removes it once the workspace write is durable (POST .../consume).
func (g *Gateway) handleInbound(w http.ResponseWriter, r *http.Request) {
	caller, ok := g.authorizeControl(r)
	if !ok {
		http.Error(w, "unauthorized", http.StatusUnauthorized)
		return
	}
	id, action, _ := strings.Cut(strings.TrimPrefix(r.URL.Path, "/v1/inbound/"), "/")
	rec, data, err := g.spool.ReadInbound(id)
	if err != nil {
		http.Error(w, "not found", http.StatusNotFound)
		return
	}
	if rec.UserID != caller.PrincipalID {
		http.Error(w, "forbidden", http.StatusForbidden)
		return
	}
	switch {
	case r.Method == http.MethodGet && action == "":
		w.Header().Set("Content-Type", "application/octet-stream")
		w.Header().Set("X-Hub-Name", rec.Name)
		_, _ = w.Write(data)
	case r.Method == http.MethodPost && action == "consume":
		if err := g.spool.ConsumeInbound(id); err != nil {
			http.Error(w, "consume failed", http.StatusInternalServerError)
			return
		}
		w.WriteHeader(http.StatusOK)
	default:
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
	}
}

func (g *Gateway) handleHooks(w http.ResponseWriter, r *http.Request) {
	caller, ok := g.authorizeControl(r)
	if !ok {
		http.Error(w, "unauthorized", http.StatusUnauthorized)
		return
	}
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	id := strings.TrimPrefix(r.URL.Path, "/v1/hooks/")
	body, err := io.ReadAll(io.LimitReader(r.Body, 64*1024))
	if err != nil || len(body) == 0 {
		http.Error(w, "bad payload", http.StatusBadRequest)
		return
	}
	card, err := g.spool.GetRoutine(id, caller)
	if err != nil || card.Schedule.Probe == nil || card.Schedule.Probe.Kind != "hook" {
		http.NotFound(w, r)
		return
	}
	if err := g.spool.rt.IntakeHook(id, body, g.now().UTC()); err != nil {
		http.Error(w, "intake failed", http.StatusInternalServerError)
		return
	}
	w.WriteHeader(http.StatusAccepted)
}

func decodeJSON(r *http.Request, v any) error {
	defer r.Body.Close()
	return json.NewDecoder(io.LimitReader(r.Body, 64*1024)).Decode(v)
}

func writeJSON(w http.ResponseWriter, v any) {
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(v)
}
