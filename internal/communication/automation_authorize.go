package communication

import (
	"crypto/subtle"
	"encoding/json"
	"net/http"
	"strings"

	"github.com/letya999/hermes-hub/internal/identity"
	"github.com/letya999/hermes-hub/internal/locale"
)

func (g *Gateway) serviceControlToken() string {
	if g.config.AutomationAuth != "" {
		return g.config.AutomationAuth
	}
	return g.config.ControlAuth
}

func (g *Gateway) serviceControlTokenAccepted(token string) bool {
	expected := g.serviceControlToken()
	return expected != "" && subtle.ConstantTimeCompare([]byte(token), []byte(expected)) == 1
}

func (g *Gateway) authorizeServiceControl(r *http.Request) (identity.Envelope, bool) {
	if !g.serviceControlTokenAccepted(strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer ")) {
		return identity.Envelope{}, false
	}
	return g.controlCaller(r.Header.Get("X-Hub-Principal"))
}

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
	if !g.serviceControlTokenAccepted(token) || !identity.ValidID(principal) {
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
