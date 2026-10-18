package communication

import (
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"os"
	"slices"
	"strings"
	"time"

	"github.com/letya999/hermes-hub/internal/identity"
)

func (g *Gateway) Handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("/v1/slack/events", g.HandleSlackEvents)
	mux.HandleFunc("/v1/routines", g.handleRoutines)
	mux.HandleFunc("/v1/routines/", g.handleRoutineItem)
	mux.HandleFunc("/v1/tasks", g.handleTasks)
	mux.HandleFunc("/v1/tasks/", g.handleTasks)
	mux.HandleFunc("/v1/inbound/", g.handleInbound)
	mux.HandleFunc("/v1/knowledge", g.handleKnowledge)
	mux.HandleFunc("/v1/knowledge/", g.handleKnowledge)
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
		return user.envelope(user.TelegramIDs[0]), true
	}
	if len(user.SlackIDs) > 0 {
		return user.slackEnvelope(user.SlackIDs[0].TeamID, user.SlackIDs[0].UserID), true
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
	body, err := os.ReadFile(path)
	if err != nil || len(body) > 1<<20 {
		return false
	}
	var entries map[string]identity.Envelope
	if err := json.Unmarshal(body, &entries); err != nil {
		return false
	}
	env, ok := entries[token]
	if !ok {
		return false
	}
	if env.Validate(env.PrincipalID, env.ContextID, env.RuntimeID, env.PolicyVersion) != nil {
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
		items, err := g.spool.ListSchedules(caller)
		if err != nil {
			http.Error(w, "list failed", http.StatusInternalServerError)
			return
		}
		writeJSON(w, items)
	case http.MethodPost:
		var in Schedule
		if decodeJSON(r, &in) != nil {
			http.Error(w, "invalid schedule", http.StatusBadRequest)
			return
		}
		in.Envelope = caller
		in.UserID = caller.PrincipalID
		in.ActorID = caller.PrincipalID
		in.ScopeID = "user:" + caller.PrincipalID
		in.OrganizationID = g.config.OrganizationID
		if in.Channel == "" {
			in.Channel = "telegram_bot"
		}
		if in.Channel == "telegram_bot" && in.ChatID == 0 {
			user := g.user(caller.PrincipalID)
			if len(user.TelegramIDs) > 0 {
				in.ChatID = user.TelegramIDs[0]
			}
		}
		if in.Channel == "mock" {
			owner, err := g.mockLinkOwner(in.ChatID)
			if err != nil || owner != caller.PrincipalID {
				http.Error(w, "mock chat not owned", http.StatusForbidden)
				return
			}
			// Mock-channel routines carry the mock envelope so occurrence
			// authorization and delivery targeting stay coherent end to end.
			in.Envelope = g.user(caller.PrincipalID).mockEnvelope(in.ChatID)
			caller = in.Envelope
		}
		in = normalizeScheduleDraft(in)
		if err := validateScheduleDraft(in, caller); err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		g.spool.mu.Lock()
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
			var body []byte
			if in.Probe.Kind == "mcp_tool" {
				var err error
				body, err = g.toolCallProbe(r.Context(), caller.PrincipalID, in.Probe)
				if err != nil {
					http.Error(w, "check source unreachable", http.StatusBadGateway)
					return
				}
			} else if in.Probe.Kind == "render" || in.Probe.Kind == "service" {
				var err error
				body, err = g.serviceProbe(r.Context(), caller.PrincipalID, in.Probe)
				if err != nil {
					http.Error(w, "check source unreachable", http.StatusBadGateway)
					return
				}
			} else if strings.HasPrefix(in.Probe.URL, "task:") {
				g.spool.mu.Lock()
				state, taskErr := g.spool.taskProbeValueLocked(caller.PrincipalID, strings.TrimPrefix(in.Probe.URL, "task:"))
				g.spool.mu.Unlock()
				if taskErr != nil {
					http.Error(w, "check task unreadable", http.StatusBadGateway)
					return
				}
				body = []byte(state)
			} else {
				var err error
				body, err = g.fetchWatch(r.Context(), in.Probe.URL)
				if err != nil {
					http.Error(w, "check source unreachable", http.StatusBadGateway)
					return
				}
			}
			value, err := probeValue(in.Probe, body)
			if err != nil {
				http.Error(w, "invalid check probe result", http.StatusBadGateway)
				return
			}
			initialValue = &value
		}
		created, err := g.spool.createScheduleAt(in, caller, g.config.NativeCron, initialValue, now)
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
	case r.Method == http.MethodGet && action == "":
		items, err := g.spool.ListSchedules(caller)
		if err != nil {
			http.Error(w, "list failed", http.StatusInternalServerError)
			return
		}
		for _, item := range items {
			if item.ScheduleID == id {
				g.spool.mu.Lock()
				state, err := g.spool.loadWatchStateLocked(id)
				g.spool.mu.Unlock()
				if err != nil {
					http.Error(w, "state unavailable", http.StatusInternalServerError)
					return
				}
				writeJSON(w, map[string]any{"schedule": item, "watch_state": state})
				return
			}
		}
		http.NotFound(w, r)
	case r.Method == http.MethodPost && action == "snooze":
		var patch struct {
			Until time.Time `json:"until"`
		}
		if decodeJSON(r, &patch) != nil || !patch.Until.After(g.now().UTC()) {
			http.Error(w, "future until required", http.StatusBadRequest)
			return
		}
		if err := g.spool.SnoozeSchedule(id, caller, patch.Until); err != nil {
			http.Error(w, "schedule not owned", http.StatusForbidden)
			return
		}
		w.WriteHeader(http.StatusOK)
	case r.Method == http.MethodDelete && action == "":
		if err := g.spool.DeleteSchedule(id, caller); err != nil {
			http.Error(w, err.Error(), http.StatusForbidden)
			return
		}
		w.WriteHeader(http.StatusNoContent)
	case r.Method == http.MethodPost && action == "pause":
		if err := g.spool.PauseSchedule(id, caller); err != nil {
			http.Error(w, err.Error(), http.StatusForbidden)
			return
		}
		w.WriteHeader(http.StatusOK)
	case r.Method == http.MethodPost && action == "":
		var patch struct {
			Expression string `json:"expression"`
			Input      string `json:"input"`
		}
		if decodeJSON(r, &patch) != nil {
			http.Error(w, "invalid patch", http.StatusBadRequest)
			return
		}
		item, err := g.spool.UpdateSchedule(id, caller, patch.Expression, patch.Input, nil)
		if err != nil {
			http.Error(w, err.Error(), http.StatusForbidden)
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

func decodeJSON(r *http.Request, v any) error {
	defer r.Body.Close()
	return json.NewDecoder(io.LimitReader(r.Body, 64*1024)).Decode(v)
}

func writeJSON(w http.ResponseWriter, v any) {
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(v)
}
