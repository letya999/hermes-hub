package communication

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/letya999/hermes-hub/internal/identity"
	"github.com/letya999/hermes-hub/internal/toolhub/api"
)

type prepareOutcomeRequest struct {
	OnboardingID     string `json:"onboarding_id"`
	Phase            string `json:"phase"`
	DefinitionID     string `json:"definition_id"`
	ContractID       string `json:"contract_id"`
	CredentialIntake string `json:"credential_intake"`
	BrokerRequestID  string `json:"broker_request_id"`
	Repository       string `json:"repository"`
	Detail           string `json:"detail"`
	FormURL          string `json:"form_url"`
	Event            string `json:"event"`
	Tools            int    `json:"tools"`
	// CreatedAt pins the onboarding creation instant so the gateway can find
	// the message job that was running when the install was requested.
	CreatedAt string `json:"created_at,omitempty"`
	// Browser viewer links carry no onboarding: the connection is the target.
	ConnectionID string `json:"connection_id,omitempty"`
	ViewerURL    string `json:"viewer_url,omitempty"`
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
	if request.ConnectionID != "" || request.ViewerURL != "" {
		g.handleViewerNotice(w, caller, request)
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
	if len(request.CredentialIntake) > 40 {
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
	// The channel-side link (QR invite, etc.) goes back to the caller so the
	// agent can print it in its own reply — the notice alone is easy to miss.
	if loginURL == "" {
		loginURL = request.FormURL
	}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]string{"login_url": loginURL})
}

// handleViewerNotice relays the one-time browser viewer link minted by
// connector_status. The connection plays the routing role the onboarding id
// plays for prepare outcomes: origin fallback and message edit key use it.
func (g *Gateway) handleViewerNotice(w http.ResponseWriter, caller identity.Envelope, request prepareOutcomeRequest) {
	viewer := actionLink(request.ViewerURL, "/viewer/")
	if !identity.ValidID(request.ConnectionID) || !loopbackCredentialURL(request.ViewerURL) || viewer == "" {
		http.Error(w, "invalid viewer outcome", http.StatusBadRequest)
		return
	}
	user := g.user(caller.PrincipalID)
	if user.ID == "" {
		http.Error(w, "unknown principal", http.StatusForbidden)
		return
	}
	sum := sha256.Sum256([]byte(viewer))
	key := "viewer-" + request.ConnectionID + "-" + hex.EncodeToString(sum[:4])
	notice := "Откройте интерактивную сессию браузера: " + viewer + " Вход на сайты выполняется только на этой странице — пароли в чат не отправляйте."
	route := prepareOutcomeRequest{OnboardingID: request.ConnectionID, Phase: "viewer", FormURL: viewer}
	if err := g.deliverPrepareNotice(user, caller, key, notice, route, true); err != nil {
		http.Error(w, "viewer notice rejected", http.StatusBadRequest)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]string{})
}

func knownPreparePhase(phase string) bool {
	switch phase {
	case "awaiting-credentials", "awaiting-confirm", "awaiting-oauth", "confirmed", "enabled", "failed",
		// A remove/disable outcome posts the onboarding's current phase, not
		// an install phase — the /remove/ link carries the pending action.
		"disabled", "revoked", "cleanup_incomplete":
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
	case "prepare-started", "credentials-check", "credentials-rejected", "binding-failed", "link-resent":
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
	// A removal preview keeps the onboarding's prior phase, so the link path —
	// not the phase — names the pending human action.
	if remove := actionLink(request.FormURL, "/remove/"); remove != "" {
		return "Удаление MCP для " + repo + " ждёт подтверждения. Подтвердите по ссылке: " + remove
	}
	switch request.Phase {
	case "awaiting-credentials":
		if request.CredentialIntake == "telegram_qr" && loginURL != "" {
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
		text := "Подготовка MCP для " + repo + " готова к подтверждению."
		if confirm := actionLink(request.FormURL, "/confirm/"); confirm != "" {
			return text + " Подтвердите подключение по ссылке: " + confirm
		}
		// The stable wizard link already sits in the edited message — the
		// replacement text must keep it or the user loses the only URL.
		if wizardLink(request.FormURL) {
			return text + " Подтвердите подключение по ссылке: " + strings.TrimSpace(request.FormURL)
		}
		return text + " Продолжаю подключение."
	case "awaiting-oauth":
		text := "MCP для " + repo + " ждёт входа у провайдера."
		if connect := actionLink(request.FormURL, "/connect/"); connect != "" {
			return text + " Откройте страницу авторизации: " + connect
		}
		if device := actionLink(request.FormURL, "/device/"); device != "" {
			return text + " Откройте страницу входа по коду: " + device
		}
		return text
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
	if request.Phase == "awaiting-oauth" && actionLink(request.FormURL, "/connect/") != "" {
		text = "Служебное продолжение установки MCP. Ссылка авторизации для " + repo + " уже отправлена пользователю в статусном сообщении выше — скажи ему открыть её и войти у провайдера; ссылку в чат не дублируй и не конструируй. Если фаза стала enabled, сообщи о подключении. Если пользователь просит ссылку или сообщает, что она не работает, вызови mcp__toolhub__status с onboarding_id " + request.OnboardingID + " и resend=true — в чат придёт свежее сообщение со ссылкой. Не проси токен в чат. Не используй терминал и не меняй config.yaml."
	}
	if request.Phase == "awaiting-confirm" && actionLink(request.FormURL, "/confirm/") != "" {
		text = "Служебное продолжение установки MCP. Ссылка подтверждения для " + repo + " уже отправлена пользователю отдельным сообщением — скажи открыть её. Если он не видит ссылку или она истекла, вызови mcp__toolhub__status с onboarding_id " + request.OnboardingID + " и resend=true: канал отправит новое сообщение. Не конструируй ссылку и не проси данные в чат."
	}
	if request.Phase == "awaiting-credentials" && strings.TrimSpace(request.FormURL) != "" {
		text = "Служебное продолжение установки MCP. Пользователю уже отправлена ссылка на форму для " + repo + ", onboarding_id " + request.OnboardingID + ". Не отправляй другую ссылку и не выдумывай ошибку сети. Если пользователь пишет, что форма не приняла токен, вызови mcp__toolhub__status и перескажи error. Не проси токен в чат. Не используй терминал и не меняй config.yaml."
	}
	if wizardLink(request.FormURL) && (request.Phase == "awaiting-credentials" || request.Phase == "awaiting-confirm" || request.Phase == "awaiting-oauth") {
		text = "Служебное продолжение установки MCP " + repo + ", фаза " + request.Phase + ". Вся установка идёт на одной странице: ссылка пользователю уже отправлена, скажи ему вернуться на ту же ссылку — страница сама покажет текущий шаг (форма, подтверждение или вход Google). Других сообщений, кнопок или ссылок подтверждения нет, не упоминай их. Если ссылка не открывается или истекла, вызови mcp__toolhub__status с onboarding_id " + request.OnboardingID + " и resend=true. Ответь одним коротким сообщением. Не конструируй ссылку и не проси данные в чат."
	}
	return text
}

// actionLink returns the validated human-action URL when its path carries the
// requested prefix; every other input — empty, unparseable, a different action —
// yields "" so callers can fall back to link-free text.
func actionLink(raw, prefix string) string {
	parsed, err := url.Parse(strings.TrimSpace(raw))
	if err != nil || !strings.HasPrefix(parsed.Path, prefix) {
		return ""
	}
	return strings.TrimSpace(raw)
}

// wizardLink reports whether the action URL is the stable single-link wizard
// page (/connect/<id>?k=<nonce>) rather than a one-time hop: those keep their
// own fresh-message rule because each is a different capability token.
func wizardLink(raw string) bool {
	parsed, err := url.Parse(strings.TrimSpace(raw))
	return err == nil && strings.HasPrefix(parsed.Path, "/connect/") && parsed.Query().Get("k") != ""
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
	return strings.HasPrefix(parsed.Path, "/credentials/") || strings.HasPrefix(parsed.Path, "/confirm/") || strings.HasPrefix(parsed.Path, "/remove/") || strings.HasPrefix(parsed.Path, "/connect/") || strings.HasPrefix(parsed.Path, "/device/") || strings.HasPrefix(parsed.Path, "/viewer/")
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
	// Each new action link lands as a fresh message; the key dedupes repeats.
	// Editing the old status can bury the only link the user needs to
	// continue. The stable wizard link is exempt: it stays the same URL for
	// the whole flow, so a phase notice refreshes the message in place.
	if request.Event != "link-resent" && (request.FormURL == "" || wizardLink(request.FormURL)) {
		delivery.EditMessageID = g.spool.prepareMessageID(request.OnboardingID)
	}
	if err := g.spool.EnqueueDelivery(delivery); err != nil {
		return err
	}
	if request.Event != "" || waitingForLogin || actionLink(request.FormURL, "/remove/") != "" {
		// Wait for user action before resuming the agent — an armed remove
		// link is itself the continuation.
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
	// The outcome key tracks the action link, not this job's payload: a
	// resend under a stable wizard link can carry a drifted envelope or
	// reworded text, which must enqueue afresh instead of colliding with
	// the spent job's fingerprint.
	jobKey := key + "-" + jobFingerprint(job)[:8]
	job.ID, job.IdempotencyKey = jobKey, jobKey
	_, err := g.spool.Enqueue(job)
	return err
}

var errNoPrepareChannel = errString("user has no channel")

type errString string

func (e errString) Error() string { return string(e) }
