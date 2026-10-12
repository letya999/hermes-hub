package communication

import (
	"net/http"
	"strings"
)

// Task fixtures use the same gateway writer as ordinary channel commands.
func (g *Gateway) handleTasks(w http.ResponseWriter, r *http.Request) {
	caller, ok := g.authorizeControl(r)
	if !ok {
		http.Error(w, "unauthorized", http.StatusUnauthorized)
		return
	}
	user := g.user(caller.PrincipalID)
	if len(user.TelegramIDs) == 0 {
		http.Error(w, "Telegram binding required", http.StatusConflict)
		return
	}
	if r.URL.Path == "/v1/tasks" && r.Method == http.MethodPost {
		var in struct {
			Name string `json:"name"`
		}
		if decodeJSON(r, &in) != nil || strings.TrimSpace(in.Name) == "" {
			http.Error(w, "task name required", http.StatusBadRequest)
			return
		}
		task, _, err := g.spool.CreateTask(user, user.TelegramIDs[0], 0, in.Name, false)
		if err != nil {
			http.Error(w, "task creation failed", http.StatusBadRequest)
			return
		}
		writeJSON(w, task)
		return
	}
	if r.Method == http.MethodDelete && strings.HasPrefix(r.URL.Path, "/v1/tasks/") {
		task, err := g.spool.DeleteTask(user, user.TelegramIDs[0], strings.TrimPrefix(r.URL.Path, "/v1/tasks/"))
		if err != nil {
			http.Error(w, "task deletion refused", http.StatusConflict)
			return
		}
		writeJSON(w, task)
		return
	}
	http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
}
