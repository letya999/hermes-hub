package communication

import (
	"encoding/json"
	"io"
	"net/http"
	"strings"

	"github.com/letya999/hermes-hub/internal/identity"
)

func (g *Gateway) Handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("/v1/slack/events", g.HandleSlackEvents)
	mux.HandleFunc("/v1/routines", g.handleRoutines)
	mux.HandleFunc("/v1/routines/", g.handleRoutineItem)
	return mux
}

func (g *Gateway) authorizeControl(r *http.Request) (identity.Envelope, bool) {
	if g.config.ControlAuth == "" || r.Header.Get("Authorization") != "Bearer "+g.config.ControlAuth {
		return identity.Envelope{}, false
	}
	principal := r.Header.Get("X-Hub-Principal")
	if principal == "" {
		principal = r.Header.Get("X-Hub-User")
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

func (g *Gateway) routineCommand(user User, sender, chatID int64, text string) string {
	fields := strings.Fields(text)
	caller := user.envelope(sender)
	if len(fields) < 2 {
		return "Используйте /routine list|create|pause|delete."
	}
	switch strings.ToLower(fields[1]) {
	case "list":
		items, err := g.spool.ListSchedules(caller)
		if err != nil {
			return "Не удалось получить расписания."
		}
		if len(items) == 0 {
			return "Нет расписаний."
		}
		lines := []string{"Расписания:"}
		for _, item := range items {
			state := "on"
			if item.Paused || !item.Enabled {
				state = "paused"
			}
			lines = append(lines, item.ScheduleID+" "+state+" "+item.Expression)
		}
		return strings.Join(lines, "\n")
	case "create":
		if len(fields) < 6 {
			return "Используйте /routine create <id> <tz> <once:RFC3339|m h dom mon dow> <задание>."
		}
		input := strings.Join(fields[5:], " ")
		_, err := g.spool.CreateSchedule(Schedule{ScheduleID: fields[2], Envelope: caller, OrganizationID: g.config.OrganizationID, UserID: user.ID, ActorID: user.ID, ScopeID: "user:" + user.ID, Channel: "telegram_bot", ChatID: chatID, Timezone: fields[3], Expression: fields[4], Input: input, CreatedAt: g.now().UTC()}, caller, g.config.NativeCron)
		if err != nil {
			return "Расписание отклонено."
		}
		return "Расписание сохранено."
	case "pause":
		if len(fields) != 3 || g.spool.PauseSchedule(fields[2], caller) != nil {
			return "Не удалось приостановить расписание."
		}
		return "Расписание приостановлено."
	case "delete":
		if len(fields) != 3 || g.spool.DeleteSchedule(fields[2], caller) != nil {
			return "Не удалось удалить расписание."
		}
		return "Расписание удалено."
	default:
		return "Используйте /routine list|create|pause|delete."
	}
}
