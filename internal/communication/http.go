package communication

import (
	"encoding/json"
	"io"
	"net/http"
	"os"
	"slices"
	"strings"

	"github.com/letya999/hermes-hub/internal/identity"
)

func (g *Gateway) Handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("/v1/slack/events", g.HandleSlackEvents)
	mux.HandleFunc("/v1/routines", g.handleRoutines)
	mux.HandleFunc("/v1/routines/", g.handleRoutineItem)
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
	if user.ID == "" {
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
		created, err := g.spool.CreateSchedule(in, caller, g.config.NativeCron)
		if err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
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

func decodeJSON(r *http.Request, v any) error {
	defer r.Body.Close()
	return json.NewDecoder(io.LimitReader(r.Body, 64*1024)).Decode(v)
}

func writeJSON(w http.ResponseWriter, v any) {
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(v)
}
