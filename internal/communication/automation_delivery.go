package communication

import (
	"encoding/json"
	"io"
	"net/http"
	"slices"
	"strconv"
	"strings"

	"github.com/letya999/hermes-hub/internal/identity"
)

// Only the service control bearer may hand ToolHub-owned results to the
// communication outbox; user runtime control tokens cannot send arbitrary text.
func (g *Gateway) handleAutomationDelivery(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		w.WriteHeader(http.StatusMethodNotAllowed)
		return
	}
	token := strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer ")
	if !g.serviceControlTokenAccepted(token) {
		w.WriteHeader(http.StatusUnauthorized)
		return
	}
	principal := r.Header.Get("X-Hub-Principal")
	contextID := r.Header.Get("X-Hub-Context")
	r.Body = http.MaxBytesReader(w, r.Body, 2*1024*1024)
	defer r.Body.Close()
	body, err := io.ReadAll(r.Body)
	if err != nil {
		w.WriteHeader(http.StatusBadRequest)
		return
	}
	var d Delivery
	if json.Unmarshal(body, &d) != nil || d.OwnerPrincipalID != "" || d.OwnerContextID != "" || d.Automation || d.ID == "" || d.Channel == "" || len(d.ID) > 256 || len(d.Buttons) != 0 || len(d.Artifacts) != 0 || d.Voice != "" || d.ApprovalJobID != "" || !identity.ValidID(principal) || !identity.ValidID(contextID) {
		w.WriteHeader(http.StatusBadRequest)
		return
	}
	d, err = normalizeDelivery(d)
	if err != nil || !g.automationDeliveryTarget(principal, d) {
		w.WriteHeader(http.StatusForbidden)
		return
	}
	d.OwnerPrincipalID = principal
	d.OwnerContextID = contextID
	d.Automation = true
	d.IdempotencyKey = d.ID
	if err := g.spool.EnqueueDelivery(d); err != nil {
		w.WriteHeader(http.StatusServiceUnavailable)
		return
	}
	w.WriteHeader(http.StatusCreated)
}

func (g *Gateway) automationDeliveryTarget(principal string, d Delivery) bool {
	user := g.user(principal)
	if principal == "" || !user.Enabled || user.ID != principal {
		return false
	}
	switch d.Channel {
	case "telegram_bot":
		if d.DeliveryTargetID != "telegram-"+strconv.FormatInt(d.ChatID, 10) {
			return false
		}
		if d.ChatID > 0 {
			return slices.Contains(user.TelegramIDs, d.ChatID)
		}
		group := g.groupFor(d.ChatID)
		if group == nil {
			return false
		}
		if group.Mode != "topics" {
			return group.Space == principal
		}
		return len(user.TelegramIDs) > 0
	case "mock":
		owner, err := g.mockLinkOwner(d.ChatID)
		return err == nil && owner == principal && d.DeliveryTargetID == "mock-"+strconv.FormatInt(d.ChatID, 10)
	case "slack_app":
		for _, link := range user.SlackIDs {
			if d.DeliveryTargetID == identity.SlackIdentity(link.TeamID, link.UserID) && d.SlackChannel != "" {
				return true
			}
		}
		return false
	default:
		return false
	}
}
