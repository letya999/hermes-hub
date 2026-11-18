package communication

import (
	"net/http"
	"strings"

	"github.com/letya999/hermes-hub/internal/commodel"
	"github.com/letya999/hermes-hub/internal/locale"
)

// ToolHub owns admission; communication binds the destination to its durable job.
func (g *Gateway) handleSpeak(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		w.WriteHeader(http.StatusMethodNotAllowed)
		return
	}
	if !g.serviceControlTokenAccepted(strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer ")) {
		w.WriteHeader(http.StatusUnauthorized)
		return
	}
	var in struct {
		JobID          string `json:"job_id"`
		ConversationID string `json:"conversation_id"`
		DeliveryID     string `json:"delivery_id"`
		Text           string `json:"text"`
		Language       string `json:"language"`
	}
	if decodeJSON(r, &in) != nil || len(in.DeliveryID) > 128 || !commodel.IDPattern.MatchString(in.DeliveryID) || strings.TrimSpace(in.Text) == "" || len(in.Text) > 16000 {
		w.WriteHeader(http.StatusBadRequest)
		return
	}
	principal := r.Header.Get("X-Hub-Principal")
	mapping, found, err := g.spool.Mapping(in.JobID)
	if err != nil || !found || mapping.PrincipalID != principal || mapping.ConversationID != in.ConversationID || !g.user(principal).Enabled || (mapping.Channel != "telegram_bot" && mapping.Channel != "mock") || g.authorizeGroupMapping(mapping) != nil {
		w.WriteHeader(http.StatusForbidden)
		return
	}
	d := Delivery{ID: in.DeliveryID, IdempotencyKey: in.DeliveryID, OwnerPrincipalID: principal, OwnerContextID: mapping.ContextID, Automation: true,
		ConversationID: mapping.ConversationID, DeliveryTargetID: mapping.DeliveryTargetID,
		Channel: mapping.Channel, ChatID: mapping.ChatID, TopicID: mapping.TopicID,
		Voice: strings.TrimSpace(in.Text), CreatedAt: g.now().UTC()}
	if mapping.Channel == "mock" && !g.automationDeliveryTarget(principal, d) {
		w.WriteHeader(http.StatusForbidden)
		return
	}
	if !g.voiceRight(d) {
		w.WriteHeader(http.StatusForbidden)
		return
	}
	if g.synthesizer == nil {
		w.WriteHeader(http.StatusServiceUnavailable)
		return
	}
	d.SpeechLang = locale.LangOf(g.user(principal).WorkspaceDir)
	if in.Language != "" {
		lang, ok := locale.Parse(in.Language)
		if !ok {
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		d.SpeechLang = lang
	}
	if err := g.spool.EnqueueDelivery(d); err != nil {
		w.WriteHeader(http.StatusInternalServerError)
		return
	}
	writeJSON(w, map[string]any{"delivery_id": d.ID, "status": "queued", "channel": d.Channel})
}
