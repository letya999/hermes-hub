// Package communication routes channel messages into isolated Hermes jobs.
package communication

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/letya999/hermes-hub/internal/commops"
	"github.com/letya999/hermes-hub/internal/diagnostics"
	"github.com/letya999/hermes-hub/internal/envstore"
	"github.com/letya999/hermes-hub/internal/identity"
	hubruntime "github.com/letya999/hermes-hub/internal/runtime"
)

type Gateway struct {
	watchHTTP    *http.Client
	config       Config
	users        map[int64]User
	slackUsers   map[string]User
	spool        *Spool
	api          TelegramAPI
	slack        SlackAPI
	runner       Runner
	restart      func(context.Context, hubruntime.ExecuteRequest) error
	busy         atomic.Int32
	usageQueries atomic.Int32
	usageWG      sync.WaitGroup
	commandCtx   context.Context
	now          func() time.Time
	transcriber  Transcriber
	synthesizer  Synthesizer
	forms        formRegistry
	telegramAuth *telegramAuthState
	quota        quotaTracker
	// contentUntil bounds the explicit content-capture window: inside it
	// message logs carry redacted text, outside only lengths and IDs.
	contentUntil time.Time
	// edits throttles status-message edits to one per editInterval per
	// chat (SPEC-0053 R2.2).
	edits editThrottle
	// controlPolls counts control-loop mapping scans; an idle loop scans at
	// most once per second (SPEC-0053 R2.3).
	controlPolls atomic.Int64
	// opsOnce/ops lazily build the commops engine (canary + health) — the
	// ops plane reaches the gateway only through injected deps.
	opsOnce sync.Once
	ops     *commops.Engine
}

func New(config Config) (*Gateway, error) {
	if config.PollTimeout <= 0 {
		config.PollTimeout = 25 * time.Second
	}
	if config.HermesCommand == "" {
		config.HermesCommand = "hermes"
	}
	if err := config.Validate(); err != nil {
		return nil, err
	}
	if config.TelegramToken == "" {
		return nil, errors.New("TELEGRAM_BOT_TOKEN is required")
	}
	for _, user := range config.Users {
		if !user.Enabled {
			continue
		}
		if config.RuntimeURL != "" {
			continue
		}
		for _, root := range []string{user.StateDir, user.WorkspaceDir} {
			info, err := os.Stat(root)
			if err != nil || !info.IsDir() {
				return nil, fmt.Errorf("user %q runtime root is unavailable", user.ID)
			}
		}
	}
	spool, err := NewSpool(config.SpoolDir)
	if err != nil {
		return nil, err
	}
	if config.MockDir != "" {
		for _, dir := range []string{"mock-sent", "mock-links"} {
			if err := os.MkdirAll(filepath.Join(config.MockDir, dir), 0700); err != nil {
				return nil, err
			}
		}
	}
	users := map[int64]User{}
	slackUsers := map[string]User{}
	for _, user := range config.Users {
		if !user.Enabled {
			continue
		}
		for _, id := range user.TelegramIDs {
			users[id] = user
		}
		for _, link := range user.SlackIDs {
			slackUsers[link.Key()] = user
		}
	}
	synthesizer := serviceSynthesizer(config.TTSURL, config.TTSAuth, config.TTSVoice, config.TTSCommand)
	runner := Runner(HermesRunner{Command: config.HermesCommand})
	if config.RuntimeURL != "" {
		runner = HTTPRunner{URL: config.RuntimeURL, Auth: config.RuntimeAuth, Spool: spool, JobsAPI: config.Supervised, Voice: synthesizer != nil}
	}
	restart := runtimeRestart(config.RuntimeURL, config.RuntimeAuth, config.Supervised)
	g := &Gateway{config: config, users: users, slackUsers: slackUsers, spool: spool, api: newTelegramAPI(config.APIBaseURL, config.TelegramToken, config.PollTimeout+10*time.Second), runner: runner, restart: restart, now: time.Now, transcriber: serviceTranscriber(config.STTURL, config.STTAuth, config.STTCommand, config.STTTimeout), synthesizer: synthesizer, forms: formRegistry{m: map[string]credentialForm{}}}
	spool.JobDone = g.toolHubJobDone
	spool.probeScript = g.runProbeScript
	if config.TelegramAuthEnabled {
		// Enabled by default on telegram stacks: missing Broker/form-origin
		// prerequisites degrade to no QR invites instead of refusing to start.
		if state, authErr := newTelegramAuthState(config); authErr == nil {
			g.telegramAuth = state
		} else {
			log.Printf("telegram auth disabled: %v", authErr)
		}
	}
	// HUB_LOG_CONTENT_UNTIL is the explicit content-capture switch: an
	// RFC3339 instant up to one hour ahead after which logs drop message
	// text again. A malformed or far-future value must never silently pin
	// content logging open, so it fails render or clamps to the bound.
	if raw := strings.TrimSpace(os.Getenv("HUB_LOG_CONTENT_UNTIL")); raw != "" {
		until, err := time.Parse(time.RFC3339, raw)
		if err != nil {
			return nil, fmt.Errorf("HUB_LOG_CONTENT_UNTIL: %w", err)
		}
		if max := time.Now().Add(time.Hour); until.After(max) {
			until = max
		}
		g.contentUntil = until
	}
	if config.SlackBotToken != "" {
		g.slack = newSlackAPI(config.SlackBotToken)
	}
	return g, nil
}

// contentField renders the text field of a message log line. Bracketed
// placeholders are class markers, not content, so they always print; real
// text prints redacted only inside the capture window, otherwise the log
// carries its length and the surrounding IDs — never the body.
func (g *Gateway) contentField(visible string) string {
	if strings.HasPrefix(visible, "[") && strings.HasSuffix(visible, "]") {
		return "text=" + strconv.Quote(visible)
	}
	if g.now().Before(g.contentUntil) {
		return "text=" + strconv.Quote(diagnostics.Redact(visible))
	}
	return "text_len=" + strconv.Itoa(len(visible))
}

func (g *Gateway) Run(ctx context.Context) error {
	unlock, err := LockSpool(g.spool.root)
	if err != nil {
		return err
	}
	defer unlock()
	if err := g.spool.recover(); err != nil {
		return err
	}
	workerCtx, cancel := context.WithCancel(ctx)
	g.commandCtx = workerCtx
	var workerWG sync.WaitGroup
	for i := 0; i < max(1, g.config.Workers); i++ {
		workerWG.Add(1)
		go func() {
			defer workerWG.Done()
			g.worker(workerCtx)
		}()
	}
	deliveryDone := make(chan struct{})
	controlDone := make(chan struct{})
	proactiveDone := make(chan struct{})
	progressDone := make(chan struct{})
	streamDone := g.startStreamLoops(workerCtx)
	canaryDone := make(chan struct{})
	go func() {
		defer close(canaryDone)
		g.canaryLoop(workerCtx, canaryInterval())
	}()
	go func() {
		defer close(proactiveDone)
		ticker := time.NewTicker(5 * time.Second)
		defer ticker.Stop()
		for {
			g.tickProactiveContext(workerCtx, g.now().UTC())
			select {
			case <-workerCtx.Done():
				return
			case <-ticker.C:
			}
		}
	}()
	go func() {
		defer close(controlDone)
		g.controlLoop(workerCtx)
	}()
	go func() {
		defer close(progressDone)
		ticker := time.NewTicker(5 * time.Second)
		defer ticker.Stop()
		for {
			g.progressTick(workerCtx)
			select {
			case <-workerCtx.Done():
				return
			case <-ticker.C:
			}
		}
	}()
	go func() {
		defer close(deliveryDone)
		g.deliveryLoop(workerCtx)
	}()
	defer func() {
		cancel()
		workerWG.Wait()
		<-deliveryDone
		<-controlDone
		<-proactiveDone
		<-progressDone
		<-streamDone
		<-canaryDone
		g.usageWG.Wait()
	}()
	offset, err := g.spool.LoadOffset()
	if err != nil {
		return err
	}
	if err := g.api.SetMyCommands(ctx); err != nil {
		log.Printf("gateway command menu registration failed: %v", err)
	}
	for {
		updates, err := g.api.GetUpdates(ctx, offset, int(g.config.PollTimeout/time.Second))
		if err != nil {
			if ctx.Err() != nil {
				return ctx.Err()
			}
			var status telegramStatusError
			if errors.As(err, &status) && status.code == http.StatusConflict {
				// A second process holds this bot token: log once per wake,
				// tell the owner once ever, then retry quietly after a minute.
				log.Printf("telegram-conflict: getUpdates returned HTTP 409")
				g.notifyTelegramConflict()
				select {
				case <-ctx.Done():
					return ctx.Err()
				case <-time.After(telegramConflictBackoff):
				}
				continue
			}
			time.Sleep(time.Second)
			continue
		}
		offset, err = g.processBatch(ctx, updates, offset)
		if err != nil {
			return err
		}
	}
}

// processBatch applies one polled batch. A poison update is quarantined and
// skipped — one bad update must never take the whole gateway down or wedge
// the offset behind it (SPEC-0053 R3.3).
func (g *Gateway) processBatch(ctx context.Context, updates []Update, offset int64) (int64, error) {
	for _, update := range updates {
		if err := g.handleUpdate(ctx, update); err != nil {
			log.Printf("gateway quarantined update_id=%d class=%s", update.UpdateID, quarantineClass(err))
			if qerr := g.spool.Quarantine(update, err, g.now()); qerr != nil {
				return offset, qerr
			}
		}
		if int64(update.UpdateID) >= offset {
			offset = int64(update.UpdateID) + 1
			if err := g.spool.SaveOffset(offset); err != nil {
				return offset, err
			}
		}
	}
	return offset, nil
}

// handleCallback applies an inline-keyboard tap: the opaque data resolves to a
// durable (job, request, choice) record, then the same ownership-checked
// approval path as /approve runs. Every tap is answered so Telegram stops
// showing the loading spinner; rejected taps change nothing.
func (g *Gateway) handleCallback(ctx context.Context, query *CallbackQuery) error {
	answer := "Запрос отклонен: подтверждение недоступно."
	if query.From != nil {
		if user, ok := g.users[query.From.ID]; ok {
			if jobID, requestID, choice, err := g.spool.ApprovalCallback(query.Data); err == nil {
				caller := user.Envelope(query.From.ID)
				if mapping, found, err := g.spool.Mapping(jobID); err == nil && found {
					caller.ConversationID = mapping.ConversationID
					caller.DeliveryTargetID = mapping.DeliveryTargetID
					// Legacy keyboards had no send receipt. A verified tap supplies
					// the exact old message, without guessing IDs or deleting chat history.
					if ownsMapping(caller, mapping) && terminalStatus(mapping.Status) && query.Message != nil && query.Message.MessageID > 0 && mapping.DeliveryTargetID == "telegram-"+strconv.FormatInt(query.Message.Chat.ID, 10) {
						_ = g.api.DeleteMessage(ctx, query.Message.Chat.ID, query.Message.MessageID)
					}
				}
				if err := g.spool.RequestApproval(jobID, caller, requestID, choice); err == nil {
					answer = "Ответ на подтверждение сохранен."
				}
			}
		}
	}
	return g.api.AnswerCallbackQuery(ctx, query.ID, answer)
}

func (g *Gateway) handleUpdate(ctx context.Context, update Update) error {
	if query := update.CallbackQuery; query != nil {
		return g.handleCallback(ctx, query)
	}
	edited := update.EditedMessage != nil
	message := update.Message
	if message == nil {
		message = update.EditedMessage
	}
	if message == nil || message.From == nil {
		return nil
	}
	text := strings.TrimSpace(message.Text)
	if text == "" {
		text = strings.TrimSpace(message.Caption)
	}
	if message.Chat.Type != "private" {
		// Groups only serve verified users; everyone else is ignored silently.
		if _, ok := g.users[message.From.ID]; !ok {
			return nil
		}
		if envstore.LooksLikeEnv(text) {
			_ = g.api.DeleteMessage(ctx, message.Chat.ID, message.MessageID)
			return g.queueDelivery(ctx, "telegram-"+strconv.Itoa(update.UpdateID)+"-group-secret", message.Chat.ID, 0, "Группы не принимают секреты.")
		}
	}
	user, ok := g.users[message.From.ID]
	if !ok {
		if text == "" && message.Voice == nil && message.Audio == nil && message.Document == nil && len(message.Photo) == 0 {
			return nil
		}
		return g.queueDelivery(ctx, "telegram-"+strconv.Itoa(update.UpdateID)+"-reply", message.Chat.ID, messageTopicID(message), "Доступ к этому боту не настроен.")
	}
	if os.Getenv("HUB_DIAGNOSTICS_ENABLED") != "false" {
		visible := text
		if envstore.LooksLikeEnv(text) || strings.HasPrefix(strings.ToLower(text), "/credentials") || strings.HasPrefix(strings.ToLower(text), "/broker-approve") {
			visible = "[credential input redacted]"
		}
		if visible == "" && (message.Voice != nil || message.Audio != nil) {
			visible = "[voice message]"
		}
		if visible == "" && (message.Document != nil || len(message.Photo) > 0) {
			visible = "[file message]"
		}
		log.Printf("gateway telegram-received user=%q update_id=%d chat_id=%d message_id=%d %s", user.ID, update.UpdateID, message.Chat.ID, message.MessageID, g.contentField(visible))
	}
	if message.Chat.ID < 0 && message.ForumTopicCreated != nil {
		return g.forumTopicCreated(user, message.Chat.ID, messageTopicID(message), message.ForumTopicCreated.Name)
	}
	if message.Chat.ID < 0 && message.ForumTopicEdited != nil {
		if err := g.spool.RenameTopicTask(user, message.Chat.ID, messageTopicID(message), message.ForumTopicEdited.Name); err != nil {
			log.Printf("gateway forum topic rename failed chat=%d topic=%d: %v", message.Chat.ID, messageTopicID(message), err)
		}
		return nil
	}
	if edited {
		return g.queueDelivery(ctx, "telegram-"+strconv.Itoa(update.UpdateID)+"-edit", message.Chat.ID, messageTopicID(message), "Изменение сообщения не перезапускает задачу. Используйте новое сообщение или /cancel.")
	}
	if strings.HasPrefix(text, "/") {
		topic := messageTopicID(message)
		// Commands resolve the addressed task without adopting an unbound
		// topic: /task new is the explicit binding action.
		task, taskErr := g.spool.ResolveTask(user, message.Chat.ID, topic, false)
		if taskErr != nil {
			return g.queueDelivery(ctx, "telegram-"+strconv.Itoa(update.UpdateID)+"-reply", message.Chat.ID, topic, "Не удалось открыть задачу.")
		}
		return g.dispatchChannelCommand(ctx, user, task, "telegram_bot", message.Chat.ID, topic, message.From.ID, "telegram-"+strconv.Itoa(update.UpdateID), text)
	}
	if message.Voice != nil || message.Audio != nil {
		return g.handleVoice(ctx, update, user, message)
	}
	if message.Document != nil || len(message.Photo) > 0 {
		return g.handleFile(ctx, update, user, message)
	}
	if text == "" {
		return nil
	}
	if envstore.LooksLikeEnv(text) {
		return g.interceptSecret(ctx, user, update.UpdateID, message.Chat.ID, messageTopicID(message), message.MessageID, text)
	}
	// A job's task binding is resolved at acceptance: posting inside a bound
	// topic adopts it, root-DM text follows the chat's current task.
	task, err := g.spool.ResolveTask(user, message.Chat.ID, messageTopicID(message), true)
	if err != nil {
		return g.queueDelivery(ctx, "telegram-"+strconv.Itoa(update.UpdateID)+"-reply", message.Chat.ID, messageTopicID(message), "Не удалось открыть задачу.")
	}
	return g.enqueueChannelJob(ctx, user, task, "telegram_bot", "telegram-"+strconv.Itoa(update.UpdateID), "telegram:"+strconv.Itoa(update.UpdateID), message.Chat.ID, message.MessageID, text, message.From.ID, "")
}

// dispatchChannelCommand runs one slash command for an already authenticated
// caller, whatever channel carried it (Telegram update, mock inject). The
// caller identity is the verified envelope principal — never the command
// text. deliveryKey namespaces the reply's outbox idempotency ids; sender is
// the verified channel sender used for job envelopes. channel is the
// registered delivery channel the reply must leave through.
func (g *Gateway) dispatchChannelCommand(ctx context.Context, user User, task Task, channel string, chatID, topic, sender int64, deliveryKey, text string) error {
	{
		command := strings.ToLower(strings.TrimPrefix(strings.Fields(text)[0], "/"))
		if at := strings.IndexByte(command, '@'); at >= 0 {
			command = command[:at]
		}
		// callerForJob authorizes /cancel and /approve in the job's own
		// conversation so an owner can act on another task's job; the verified
		// sender identity still carries the proof.
		callerForJob := func(jobID string) identity.Envelope {
			env := user.Envelope(sender)
			if mapping, found, err := g.spool.Mapping(jobID); err == nil && found {
				env.ConversationID = mapping.ConversationID
				env.DeliveryTargetID = mapping.DeliveryTargetID
			}
			return env
		}
		reply := func(answer string) error {
			return g.queueDeliveryChannel(ctx, deliveryKey+"-reply", channel, chatID, topic, answer)
		}
		switch command {
		case "approve":
			fields := strings.Fields(text)
			answer := "Используйте /approve <job-id> <request-id> <choice>."
			var err error
			if len(fields) == 4 {
				err = g.spool.RequestApproval(fields[1], callerForJob(fields[1]), fields[2], fields[3])
				answer = "Ответ на подтверждение сохранен."
			}
			if err != nil {
				answer = "Запрос отклонен: задача или подтверждение недоступны в этом разговоре."
			}
			return reply(answer)
		case "credentials", "broker-approve":
			fields := strings.Fields(text)
			answer := "Используйте /credentials <request-id> <код из формы Credential Broker>."
			if len(fields) == 3 && g.config.BrokerApprove.Enabled() {
				caller := user.Envelope(sender)
				client, err := g.config.BrokerApprove.New(caller, "broker:approve")
				if err == nil {
					err = client.Approve(ctx, fields[1], fields[2])
				}
				if err == nil {
					answer = "Подтверждение Credential Broker сохранено. Вернитесь в Hermes и запросите статус подключения."
				} else {
					answer = "Подтверждение отклонено или истекло. Запросите новую ссылку подключения."
				}
			}
			return reply(answer)
		case "start":
			return reply("Готово. Вы подключены к своему Hermes-пространству.")
		case "new":
			answer, replyTopic := g.newTaskCommand(ctx, user, chatID, topic, text)
			return g.queueDeliveryChannel(ctx, deliveryKey+"-reply", channel, chatID, replyTopic, answer)
		case "sessions":
			return reply(g.sessionsCommand(user, chatID))
		case "use":
			return reply(g.useCommand(ctx, user, chatID, text))
		case "delete":
			answer, replyTopic := g.deleteCommand(ctx, user, chatID, topic, task, text)
			return g.queueDeliveryChannel(ctx, deliveryKey+"-reply", channel, chatID, replyTopic, answer)
		case "archive":
			return reply(g.archiveCommand(ctx, user, chatID, task, text))
		case "archived":
			return reply(g.archivedCommand(user, chatID))
		case "session", "status":
			return reply(g.taskStatus(user, task))
		case "stop":
			return reply(g.stopCommand(user, task, callerForJob))
		case "cancel":
			fields := strings.Fields(text)
			answer := "Используйте /cancel <job-id>."
			if len(fields) >= 2 {
				mapping, err := g.spool.RequestCancel(fields[1], callerForJob(fields[1]))
				if err == nil {
					answer = "Отмена " + mapping.JobID + ": " + mapping.Status + "."
				} else {
					answer = "Не удалось отменить: задача недоступна в этом разговоре."
				}
			}
			return reply(answer)
		case "routines":
			items, err := g.spool.ListSchedules(user.Envelope(sender))
			if err != nil {
				return reply("Список напоминаний недоступен.")
			}
			lines := []string{"Напоминания и проверки:"}
			for _, item := range items {
				state := "активно"
				switch routineState(item) {
				case "paused":
					state = "пауза"
				case "completed":
					state = "расписание завершено"
				}
				if item.LastOccurrenceState == "missed" {
					state += ", последнее срабатывание пропущено"
				}
				if item.LastDeliveryState == "uncertain" {
					state += ", доставка не подтверждена"
				} else if item.LastDeliveryState == "delivered" {
					state += ", доставлено"
				}
				due := "следующих запусков нет"
				if !item.NextDue.IsZero() {
					due = item.NextDue.UTC().Format(time.RFC3339)
				}
				lines = append(lines, item.ScheduleID+" — "+item.JobKind+", "+state+", "+due)
			}
			if len(items) == 0 {
				lines = append(lines, "Пока нет.")
			}
			return reply(strings.Join(lines, "\n"))
		case "model":
			return reply(modelCommand(ctx, user.ID, text))
		case "usage":
			overview := true
			if fields := strings.Fields(text); len(fields) >= 2 && fields[1] == "session" {
				overview = false
			}
			journal := ""
			if overview {
				journal = g.journalUsage(user, text)
				if strings.HasPrefix(journal, "Используйте /usage") {
					return reply(journal)
				}
			}
			// Measuring usage means up to minutes of sequential HTTP work
			// (supervisor ensure, session row, lineage, quota calls); run it
			// beside the poll or one /usage would stall every later update.
			if g.usageQueries.Add(1) > 2 {
				g.usageQueries.Add(-1)
				return reply("Измерение уже выполняется. Повторите позже.")
			}
			queryCtx := ctx
			if g.commandCtx != nil {
				queryCtx = g.commandCtx
			}
			g.usageWG.Add(1)
			go func() { // #nosec G118 -- detached usage job intentionally outlives the request context
				defer func() { g.usageQueries.Add(-1); g.usageWG.Done() }()
				usageCtx, cancel := context.WithTimeout(queryCtx, 2*time.Minute)
				defer cancel()
				answer := journal
				if overview {
					answer += "\n" + strings.Join(g.quotaLines(usageCtx), "\n")
				} else {
					answer = g.usageCommand(usageCtx, user, sender, task)
				}
				_ = g.queueDeliveryChannel(context.Background(), deliveryKey+"-usage", channel, chatID, topic, answer)
			}()
			return reply("Измеряю расход и квоту…")
		case "connections", "connection", "connectors":
			return reply(g.connectionsCommand(user, sender))
		case "watches":
			return reply(g.watchesCommand(user, sender))
		case "help":
			return reply(commandHelp())
		default:
			return reply("Неизвестная команда /" + command + ".\n\n" + commandHelp())
		}
	}
}

func (g *Gateway) enqueueChannelJob(ctx context.Context, user User, task Task, channel, id, idempotency string, chatID int64, messageID int, text string, sender int64, inboundID string) error {
	ch := g.channel(channel)
	if ch == nil {
		return errors.New("channel is not registered")
	}
	envelope := ch.JobEnvelope(user, chatID, sender)
	envelope.ConversationID = task.ConversationID
	job := Job{Envelope: envelope, ID: id, OrganizationID: g.config.OrganizationID, UserID: user.ID, ActorID: user.ID, ScopeID: "user:" + user.ID, Channel: channel, Trigger: "message", IdempotencyKey: idempotency, ChatID: chatID, MessageID: messageID, TaskID: task.TaskID, TopicID: task.TopicID, Text: text, InboundID: inboundID, Sensitive: false, CreatedAt: g.now().UTC()}
	if _, err := g.spool.Enqueue(job); err != nil {
		return err
	}
	if g.config.Supervised {
		// The runtime spawn overlaps the gateway's own work (SPEC-0053 R2.1);
		// a failed wake is harmless — /v1/jobs ensures again on dispatch.
		go g.wakeRuntime(job) // #nosec G118 -- fire-and-forget wake intentionally outlives the request context
	}
	if ch.Caps().Typing {
		_ = ch.SendPresence(ctx, chatID, task.TopicID)
	}
	return nil
}

// wakeRuntime asks the supervisor to warm the job's runtime (SPEC-0053 R2.1).
// It carries the durable binding identity only — never the prompt — and logs
// its failures instead of affecting the accepted update.
func (g *Gateway) wakeRuntime(job Job) {
	request := hubruntime.ExecuteRequest{Envelope: job.Envelope, OrganizationID: job.OrganizationID, UserID: job.UserID, ActorID: job.ActorID, ScopeID: job.ScopeID, Channel: "communication", Trigger: "wake"}
	body, err := json.Marshal(request)
	if err != nil {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, strings.TrimRight(g.config.RuntimeURL, "/")+"/v1/wake", bytes.NewReader(body))
	if err != nil {
		return
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+g.config.RuntimeAuth)
	response, err := http.DefaultClient.Do(req)
	if err != nil {
		log.Printf("gateway wake failed job_id=%q: %v", job.ID, err)
		return
	}
	defer response.Body.Close()
	_, _ = io.Copy(io.Discard, io.LimitReader(response.Body, 64*1024))
	if response.StatusCode >= 300 {
		log.Printf("gateway wake job_id=%q status=%d", job.ID, response.StatusCode)
	}
}

// progressTick refreshes live progress for every non-terminal telegram job:
// typing is re-armed (it expires after ~5s) and the job's single status
// message gets a fresh elapsed-time suffix through the durable upsert path,
// so a real stream event or a restarted gateway never duplicates it.
func (g *Gateway) progressTick(ctx context.Context) {
	ids, err := g.spool.activeMappingIDs()
	if err != nil {
		return
	}
	for _, id := range ids {
		m, found, err := g.spool.Mapping(id)
		if err != nil || !found || m.CancelRequested {
			continue
		}
		ch := g.channel(m.Channel)
		if ch == nil || !ch.Caps().Typing {
			continue
		}
		// Only an admitted run earns live progress: a queued or wedged job
		// would farm typing and edits forever with nothing behind them.
		if m.RunID == "" {
			continue
		}
		_ = ch.SendPresence(ctx, m.ChatID, m.TopicID)
		base := strings.TrimSpace(m.StatusText)
		if base == "" {
			base = "Выполняю запрос."
		}
		_ = g.spool.UpsertStatusDelivery(Delivery{ID: "job-" + m.JobID + "-status", Channel: m.Channel, ConversationID: m.ConversationID, DeliveryTargetID: m.DeliveryTargetID, ChatID: m.ChatID, TaskID: m.TaskID, TopicID: m.TopicID, Text: base + " · " + formatElapsed(g.now().Sub(m.CreatedAt)), CreatedAt: g.now().UTC()})
	}
}

func formatElapsed(d time.Duration) string {
	if d < 0 {
		d = 0
	}
	s := int(d.Round(time.Second).Seconds())
	switch {
	case s < 60:
		return strconv.Itoa(s) + " сек"
	case s < 3600:
		return strconv.Itoa(s/60) + " мин " + strconv.Itoa(s%60) + " сек"
	default:
		return strconv.Itoa(s/3600) + " ч " + strconv.Itoa((s%3600)/60) + " мин"
	}
}

// mediaReady resolves readiness for either an embedded command worker (binary
// must exist) or a URL-configured sidecar (health endpoint must answer).
func mediaReady(impl any, command, url string) bool {
	if impl == nil {
		return false
	}
	if strings.TrimSpace(url) == "" {
		return commandAvailable(command)
	}
	type healthchecker interface {
		Healthy(context.Context) bool
	}
	if h, ok := impl.(healthchecker); ok {
		ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
		defer cancel()
		return h.Healthy(ctx)
	}
	return true
}

func (g *Gateway) interceptSecret(ctx context.Context, user User, updateID int, chatID, topicID int64, messageID int, text string) error {
	_ = g.api.DeleteMessage(ctx, chatID, messageID)
	reply := "Секреты через чат не принимаются."
	values, err := envstore.Parse(text)
	if err == nil && len(values) > 0 && len(user.TelegramIDs) > 0 {
		keys := make([]string, 0, len(values))
		for key := range values {
			keys = append(keys, key)
			values[key] = ""
		}
		slices.Sort(keys)
		if form, formErr := g.createCredentialForm(user.Envelope(user.TelegramIDs[0]), keys); formErr == nil {
			reply = "Введите данные только в защищённой форме: " + form.FormURL
		}
	}
	return g.queueDelivery(ctx, "telegram-"+strconv.Itoa(updateID)+"-secret", chatID, topicID, reply)
}

// notifyTelegramConflict enqueues one owner notice — the idempotency key
// telegram-conflict makes a flapping second poller notify exactly once.
func (g *Gateway) notifyTelegramConflict() {
	for _, u := range g.config.Users {
		if u.Enabled && len(u.TelegramIDs) > 0 {
			_ = g.queueDelivery(context.Background(), "telegram-conflict", u.TelegramIDs[0], 0, "Обнаружен второй экземпляр этого бота с тем же токеном — опрос приостановлен. Остановите лишний процесс.")
			return
		}
	}
}

func (g *Gateway) user(id string) User {
	for _, user := range g.config.Users {
		if user.ID == id {
			return user
		}
	}
	return User{}
}

func (g *Gateway) userByChatID(chatID int64) User {
	for _, user := range g.config.Users {
		for _, telegramID := range user.TelegramIDs {
			if telegramID == chatID {
				return user
			}
		}
	}
	return User{}
}

// restartRequest builds the restart call for the runtime that actually ran
// the delivered job. Supervised gateways must address the supervisor with the
// job's durable identity so it can route to that user's spawned runtime;
// resident runtimes take an empty POST.
func (g *Gateway) restartRequest(delivery Delivery) (hubruntime.ExecuteRequest, bool) {
	if !g.config.Supervised {
		return hubruntime.ExecuteRequest{}, true
	}
	mapping, ok, err := g.spool.Mapping(delivery.JobID)
	if err != nil || !ok || mapping.PrincipalID == "" || mapping.ContextID == "" || mapping.RuntimeID == "" || mapping.PolicyVersion == "" || mapping.ExternalIdentityID == "" || mapping.ConversationID == "" || mapping.DeliveryTargetID == "" {
		return hubruntime.ExecuteRequest{}, false
	}
	return supervisorRestartRequest(
		identity.Envelope{Schema: identity.Schema, PrincipalID: mapping.PrincipalID, ExternalIdentityID: mapping.ExternalIdentityID, ContextID: mapping.ContextID, RuntimeID: mapping.RuntimeID, ConversationID: mapping.ConversationID, DeliveryTargetID: mapping.DeliveryTargetID, PolicyVersion: mapping.PolicyVersion},
		mapping.OrganizationID, mapping.UserID, mapping.ActorID, mapping.ScopeID, delivery.JobID), true
}

// supervisorRestartRequest addresses the supervisor's restart route with the
// durable identity of the runtime that must be bounced.
func supervisorRestartRequest(env identity.Envelope, organizationID, userID, actorID, scopeID, idempotency string) hubruntime.ExecuteRequest {
	return hubruntime.ExecuteRequest{
		Envelope:       env,
		OrganizationID: organizationID,
		UserID:         userID,
		ActorID:        actorID,
		ScopeID:        scopeID,
		Channel:        "communication",
		Trigger:        "restart",
		JobID:          "restart-" + idempotency,
		IdempotencyKey: "restart-" + idempotency,
	}
}

func signalRestartAfterDelivery(stateDir string) error {
	restartPath := filepath.Join(stateDir, "restart.request")
	if _, err := os.Stat(restartPath); errors.Is(err, os.ErrNotExist) {
		return nil
	} else if err != nil {
		return err
	}
	body, err := os.ReadFile(filepath.Join(stateDir, "runtime.json"))
	if err != nil {
		return err
	}
	var status struct {
		PIDs []int `json:"pids"`
	}
	if err := json.Unmarshal(body, &status); err != nil || len(status.PIDs) == 0 || status.PIDs[0] <= 1 {
		return errors.New("invalid runtime marker")
	}
	process, err := os.FindProcess(status.PIDs[0])
	if err != nil {
		return err
	}
	return process.Signal(os.Interrupt)
}
