package communication

import (
	"crypto/subtle"
	"encoding/json"
	"net/http"
	"strings"

	"github.com/letya999/hermes-hub/internal/identity"
	"github.com/letya999/hermes-hub/internal/locale"
)

// OwnerHome exposes only the current safe notice address and language.
// ToolHub never receives the gateway's user config, paths or channel secrets.
func (g *Gateway) handleAutomationOwnerHome(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		w.WriteHeader(http.StatusMethodNotAllowed)
		return
	}
	if r.Header.Get("Origin") != "" {
		w.WriteHeader(http.StatusForbidden)
		return
	}
	token := strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer ")
	principal := r.Header.Get("X-Hub-Principal")
	if g.config.ControlAuth == "" || subtle.ConstantTimeCompare([]byte(token), []byte(g.config.ControlAuth)) != 1 || !identity.ValidID(principal) {
		w.WriteHeader(http.StatusUnauthorized)
		return
	}
	user := g.user(principal)
	if !user.Enabled || user.ID != principal {
		w.WriteHeader(http.StatusNotFound)
		return
	}
	var chatID int64
	for _, candidate := range user.TelegramIDs {
		if mapped, ok := g.users[candidate]; ok && mapped.ID == principal {
			chatID = candidate
			break
		}
	}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(struct {
		ChatID int64       `json:"chat_id"`
		Lang   locale.Lang `json:"lang"`
	}{chatID, locale.LangOf(user.WorkspaceDir)})
}
