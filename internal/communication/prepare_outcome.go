package communication

import (
	"crypto/sha256"
	"encoding/hex"
	"net/http"
	"net/url"
	"strconv"
	"strings"

	"github.com/letya999/hermes-hub/internal/identity"
)

type prepareOutcomeRequest struct {
	OnboardingID string `json:"onboarding_id"`
	Phase        string `json:"phase"`
	DefinitionID string `json:"definition_id"`
	Repository   string `json:"repository"`
	Detail       string `json:"detail"`
	FormURL      string `json:"form_url"`
	Event        string `json:"event"`
	Tools        int    `json:"tools"`
}

// handlePrepareOutcome turns a finished ToolHub prepare into a channel reply
// and a continuation job. The MCP logging nudge never reaches Telegram.
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
	if !loopbackCredentialURL(request.FormURL) {
		http.Error(w, "invalid prepare outcome", http.StatusBadRequest)
		return
	}
	user := g.user(caller.PrincipalID)
	if user.ID == "" {
		http.Error(w, "unknown principal", http.StatusForbidden)
		return
	}
	notice := prepareOutcomeNotice(request)
	key := prepareOutcomeKey(request)
	if err := g.deliverPrepareNotice(user, caller, key, notice, request); err != nil {
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

func prepareOutcomeNotice(request prepareOutcomeRequest) string {
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
func prepareOutcomeKey(request prepareOutcomeRequest) string {
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
	return key
}

func loopbackCredentialURL(raw string) bool {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return true
	}
	parsed, err := url.Parse(raw)
	if err != nil || parsed.User != nil || parsed.Scheme != "http" || parsed.RawQuery == "" {
		return false
	}
	switch parsed.Hostname() {
	case "127.0.0.1", "localhost", "::1":
	default:
		return false
	}
	return strings.HasPrefix(parsed.Path, "/credentials/")
}

func (g *Gateway) deliverPrepareNotice(user User, caller identity.Envelope, key, notice string, request prepareOutcomeRequest) error {
	if len(user.TelegramIDs) == 0 {
		return errNoPrepareChannel
	}
	now := g.now().UTC()
	bound := g.spool.TaskByConversation(user, user.TelegramIDs[0], caller.ConversationID)
	delivery := Delivery{ID: key + "-notice", IdempotencyKey: key + "-notice", Channel: "telegram_bot", ChatID: user.TelegramIDs[0], TaskID: bound.TaskID, TopicID: bound.TopicID, Text: notice, CreatedAt: now}
	if err := g.spool.EnqueueDelivery(delivery); err != nil {
		return err
	}
	if request.Event != "" {
		// Interim progress is a channel notice only; a continuation job would
		// push the agent to act on a phase that has not settled yet.
		return nil
	}
	task := g.spool.TaskByConversation(user, user.TelegramIDs[0], caller.ConversationID)
	job := Job{
		Envelope: caller, ID: key, OrganizationID: g.config.OrganizationID, UserID: user.ID, ActorID: user.ID,
		ScopeID: "user:" + user.ID, Channel: "telegram_bot", Trigger: "prepare", IdempotencyKey: key,
		ChatID: user.TelegramIDs[0], TaskID: task.TaskID, TopicID: task.TopicID,
		Text: prepareContinuation(request), CreatedAt: now,
	}
	_, err := g.spool.Enqueue(job)
	return err
}

var errNoPrepareChannel = errString("user has no channel")

type errString string

func (e errString) Error() string { return string(e) }
