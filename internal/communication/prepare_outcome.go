package communication

import (
	"crypto/sha256"
	"encoding/hex"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/letya999/hermes-hub/internal/identity"
	"github.com/letya999/hermes-hub/internal/toolhub/api"
)

type prepareOutcomeRequest struct {
	OnboardingID    string `json:"onboarding_id"`
	Phase           string `json:"phase"`
	DefinitionID    string `json:"definition_id"`
	ContractID      string `json:"contract_id"`
	BrokerRequestID string `json:"broker_request_id"`
	Repository      string `json:"repository"`
	Detail          string `json:"detail"`
	FormURL         string `json:"form_url"`
	Event           string `json:"event"`
	Tools           int    `json:"tools"`
	// CreatedAt pins the onboarding creation instant so the gateway can find
	// the message job that was running when the install was requested.
	CreatedAt string `json:"created_at,omitempty"`
}

// handlePrepareOutcome turns a finished ToolHub prepare into a channel reply
// and, when no user login is pending, a continuation job.
func (g *Gateway) handlePrepareOutcome(w http.ResponseWriter, r *http.Request) {
	caller, ok := g.authorizeControl(r)
	if !ok {
		http.Error(w, "unauthorized", http.StatusUnauthorized)
		return
	}
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	var request prepareOutcomeRequest
	if decodeJSON(r, &request) != nil {
		http.Error(w, "invalid prepare outcome", http.StatusBadRequest)
		return
	}
	if !identity.ValidID(request.OnboardingID) || len(request.Repository) > 300 || len(request.Detail) > 400 {
		http.Error(w, "invalid prepare outcome", http.StatusBadRequest)
		return
	}
	if request.Event != "" {
		if !knownPrepareEvent(request.Event) || request.Phase == "" || len(request.Phase) > 40 {
			http.Error(w, "invalid prepare outcome", http.StatusBadRequest)
			return
		}
	} else if !knownPreparePhase(request.Phase) {
		http.Error(w, "invalid prepare outcome", http.StatusBadRequest)
		return
	}
	if request.Tools < 0 || request.Tools > 500 {
		http.Error(w, "invalid prepare outcome", http.StatusBadRequest)
		return
	}
	if request.DefinitionID != "" && !identity.ValidID(request.DefinitionID) {
		http.Error(w, "invalid prepare outcome", http.StatusBadRequest)
		return
	}
	if request.ContractID != "" && !identity.ValidID(request.ContractID) {
		http.Error(w, "invalid prepare outcome", http.StatusBadRequest)
		return
	}
	if request.BrokerRequestID != "" && !identity.ValidID(request.BrokerRequestID) {
		http.Error(w, "invalid prepare outcome", http.StatusBadRequest)
		return
	}
	if !loopbackCredentialURL(request.FormURL) {
		http.Error(w, "invalid prepare outcome", http.StatusBadRequest)
		return
	}
	user := g.user(caller.PrincipalID)
	if user.ID == "" {
		http.Error(w, "unknown principal", http.StatusForbidden)
		return
	}
	loginURL := g.telegramAuthInvite(caller, request)
	notice := prepareOutcomeNotice(request, loginURL)
	key := prepareOutcomeKey(request, loginURL)
	if err := g.deliverPrepareNotice(user, caller, key, notice, request, loginURL != ""); err != nil {
		http.Error(w, "prepare outcome rejected", http.StatusBadRequest)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func knownPreparePhase(phase string) bool {
	switch phase {
	case "awaiting-credentials", "awaiting-confirm", "awaiting-oauth", "confirmed", "enabled", "failed":
		return true
	default:
		return false
	}
}

// prepareProgressEvent marks an interim step, not a settled phase: the
// onboarding's phase alone would either fail validation or collapse distinct
// moments (a rejected credential submit returns to awaiting-credentials, the
// phase the form link already used).
func knownPrepareEvent(event string) bool {
	switch event {
	case "prepare-started", "credentials-check", "credentials-rejected", "binding-failed":
		return true
	default:
		return false
	}
}

func prepareOutcomeNotice(request prepareOutcomeRequest, loginURL string) string {
	repo := strings.TrimSpace(request.Repository)
	if repo == "" {
		repo = "репозиторий"
	}
	switch request.Event {
	case "prepare-started":
		return "Принял установку MCP для " + repo + ": скачиваю, собираю образ и проверяю. Это занимает несколько минут — напишу, когда понадобится форма или будет результат."
	case "credentials-check":
		return "Данные из формы приняты. Проверяю их у сервера и запускаю MCP — обычно до минуты."
	case "credentials-rejected":
		text := "Проверка данных для " + repo + " не прошла."
		if detail := strings.TrimSpace(request.Detail); detail != "" {
			text += " Причина: " + detail
		}
		return text + " Форма на этой странице жива — можно исправить и отправить ещё раз."
	case "binding-failed":
		text := "Данные для " + repo + " сохранены, но запуск не завершился."
		if detail := strings.TrimSpace(request.Detail); detail != "" {
			text += " Причина: " + detail
		}
		return text + " Напишите в чат «продолжи» — повторное подтверждение пройдёт без формы."
	}
	switch request.Phase {
	case "awaiting-credentials":
		if request.ContractID == "telegram-session" && loginURL != "" {
			text := "Для подключения Telegram откройте на этом компьютере: " + loginURL + " Введите API ID и API hash, затем создайте QR и подтвердите вход в приложении Telegram. После входа напишите боту «продолжи подключение Telegram». Сессию в чат отправлять не нужно."
			if request.FormURL != "" {
				text += " Запасная форма: " + request.FormURL
			}
			return text
		}
		text := "Подготовка MCP для " + repo + " собрала образ. Сервер ждёт данные в защищённой форме."
		if formURL := strings.TrimSpace(request.FormURL); formURL != "" {
			text += " Откройте её на этом компьютере: " + formURL
		} else {
			text += " Ссылка на защищённую форму придёт следующим сообщением."
		}
		return text + " Токен в чат отправлять не нужно."
	case "failed":
		text := "Подготовка MCP для " + repo + " не завершилась."
		if detail := strings.TrimSpace(request.Detail); detail != "" {
			text += " Причина: " + detail
		}
		return text
	case "awaiting-confirm":
		return "Подготовка MCP для " + repo + " готова к подтверждению. Продолжаю подключение."
	case "enabled":
		if request.Tools > 0 {
			return "MCP для " + repo + " подключён: " + strconv.Itoa(request.Tools) + " инструментов в рантайме."
		}
		return "MCP для " + repo + " подключён."
	default:
		return "Подготовка MCP для " + repo + " завершилась с фазой " + request.Phase + "."
	}
}

func prepareContinuation(request prepareOutcomeRequest) string {
	repo := strings.TrimSpace(request.Repository)
	if repo == "" {
		repo = "репозиторий"
	}
	text := "Служебное продолжение установки MCP. prepare_source для " + repo + " закончился, фаза " + request.Phase + ", onboarding_id " + request.OnboardingID + ". Вызови mcp__toolhub__status с этим onboarding_id. Если фаза awaiting-credentials, вызови mcp__toolhub__required_credentials и отправь пользователю form_url. Не проси токен в чат. Не используй терминал и не меняй config.yaml."
	if request.Phase == "awaiting-credentials" && strings.TrimSpace(request.FormURL) != "" {
		text = "Служебное продолжение установки MCP. Пользователю уже отправлена ссылка на форму для " + repo + ", onboarding_id " + request.OnboardingID + ". Не отправляй другую ссылку и не выдумывай ошибку сети. Если пользователь пишет, что форма не приняла токен, вызови mcp__toolhub__status и перескажи error. Не проси токен в чат. Не используй терминал и не меняй config.yaml."
	}
	return text
}

// prepareOutcomeKey changes when a new form link is minted. Reusing the
// onboarding id with the previous link's payload made the notice return 400
// and the channel never received the fresh URL.
func prepareOutcomeKey(request prepareOutcomeRequest, loginURL string) string {
	key := "prepare-" + request.OnboardingID + "-" + request.Phase
	if request.Event != "" {
		key += "-" + request.Event
		// Two identical rejections carry the same key and dedupe away; a
		// different rejection reason must still reach the channel.
		if request.Event == "credentials-rejected" {
			sum := sha256.Sum256([]byte(request.Detail))
			key += "-" + hex.EncodeToString(sum[:4])
		}
	}
	if form := strings.TrimSpace(request.FormURL); form != "" {
		sum := sha256.Sum256([]byte(form))
		key += "-" + hex.EncodeToString(sum[:4])
	}
	if loginURL != "" {
		sum := sha256.Sum256([]byte(loginURL))
		key += "-" + hex.EncodeToString(sum[:4])
	}
	return key
}

func loopbackCredentialURL(raw string) bool {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return true
	}
	parsed, err := url.Parse(raw)
	if err != nil || parsed.User != nil || parsed.RawQuery == "" {
		return false
	}
	if parsed.Scheme == "https" {
		// Public links exist only when the operator configured one; the
		// notice host must be exactly that origin — no look-alike domains.
		origin, err := url.Parse(api.PublicOrigin())
		if err != nil || origin.Host == "" || parsed.Host != origin.Host {
			return false
		}
	} else if parsed.Scheme == "http" {
		switch parsed.Hostname() {
		case "127.0.0.1", "localhost", "::1":
		default:
			return false
		}
	} else {
		return false
	}
	return strings.HasPrefix(parsed.Path, "/credentials/") || strings.HasPrefix(parsed.Path, "/confirm/") || strings.HasPrefix(parsed.Path, "/remove/") || strings.HasPrefix(parsed.Path, "/connect/") || strings.HasPrefix(parsed.Path, "/device/")
}

func (g *Gateway) deliverPrepareNotice(user User, caller identity.Envelope, key, notice string, request prepareOutcomeRequest, waitingForLogin bool) error {
	now := g.now().UTC()
	// The callback authenticates by principal only, so caller.ConversationID is
	// the synthesized DM envelope: route by the still-active message job that
	// initiated this onboarding, else the notice leaks to the DM root.
	conversation, deliveryTarget := caller.ConversationID, caller.DeliveryTargetID
	var bound Task
	if len(user.TelegramIDs) > 0 {
		bound = g.spool.TaskByConversation(user, user.TelegramIDs[0], conversation)
	}
	// The initiating tool call runs inside its message job, so the job that
	// existed — and most recently started — at the onboarding's creation
	// instant is the initiator. It outranks a persisted route, which older
	// builds may have bound through the unsafe newest-active heuristic.
	origin, found := prepareOrigin{}, false
	if created, err := time.Parse(time.RFC3339Nano, request.CreatedAt); err == nil && !created.IsZero() {
		if mapping, ok := g.spool.initiatingMessageMapping(user.ID, created, request.Repository); ok {
			origin = prepareOrigin{ConversationID: mapping.ConversationID, DeliveryTargetID: mapping.DeliveryTargetID, Channel: mapping.Channel, ChatID: mapping.ChatID, TopicID: mapping.TopicID}
			found = true
		}
	}
	if !found {
		if persisted, ok := g.spool.loadPrepareOrigin(request.OnboardingID); ok {
			origin, found = persisted, true
		}
	}
	if !found {
		if mapping, ok := g.spool.newestActiveMessageMapping(user.ID); ok {
			origin = prepareOrigin{ConversationID: mapping.ConversationID, DeliveryTargetID: mapping.DeliveryTargetID, Channel: mapping.Channel, ChatID: mapping.ChatID, TopicID: mapping.TopicID}
			found = true
		}
	}
	if found {
		// The origin job ends long before the build settles; persist the route
		// so later outcomes still reach the same session.
		_ = g.spool.putPrepareOrigin(request.OnboardingID, origin)
	}
	if found && origin.ConversationID != "" {
		bound = g.spool.TaskByConversation(user, origin.ChatID, origin.ConversationID)
		conversation, deliveryTarget = origin.ConversationID, origin.DeliveryTargetID
	}
	// The notice returns on the channel the human asked from. Channels without
	// Caps().Actions get the same text — the link is plain and clickable.
	channelName := origin.Channel
	if channelName == "" {
		channelName = bound.Channel
	}
	if channelName == "" {
		channelName = "telegram_bot"
	}
	chatID, topicID := bound.ChatID, bound.TopicID
	if channelName == "telegram_bot" {
		if len(user.TelegramIDs) == 0 {
			return errNoPrepareChannel
		}
		if chatID == 0 {
			chatID = user.TelegramIDs[0]
		}
	} else if chatID == 0 {
		chatID, topicID = origin.ChatID, origin.TopicID
	}
	delivery := Delivery{ID: key + "-notice", IdempotencyKey: key + "-notice", Channel: channelName, ChatID: chatID, TaskID: bound.TaskID, TopicID: topicID, Text: notice, CreatedAt: now}
	// One evolving status message per onboarding: once the first notice was
	// delivered, later updates edit it instead of stacking new messages.
	delivery.EditMessageID = g.spool.prepareMessageID(request.OnboardingID)
	if err := g.spool.EnqueueDelivery(delivery); err != nil {
		return err
	}
	if request.Event != "" || waitingForLogin {
		// Wait for user action before resuming the agent.
		return nil
	}
	envelope := caller
	envelope.ConversationID = conversation
	envelope.DeliveryTargetID = deliveryTarget
	if bound.ChatID < 0 && deliveryTarget == "" {
		envelope.DeliveryTargetID = "telegram-" + strconv.FormatInt(bound.ChatID, 10)
	}
	job := Job{
		Envelope: envelope, ID: key, OrganizationID: g.config.OrganizationID, UserID: user.ID, ActorID: user.ID,
		ScopeID: "user:" + user.ID, Channel: channelName, Trigger: "prepare", IdempotencyKey: key,
		ChatID: chatID, TaskID: bound.TaskID, TopicID: topicID,
		Text: prepareContinuation(request), CreatedAt: now,
	}
	_, err := g.spool.Enqueue(job)
	return err
}

var errNoPrepareChannel = errString("user has no channel")

type errString string

func (e errString) Error() string { return string(e) }
