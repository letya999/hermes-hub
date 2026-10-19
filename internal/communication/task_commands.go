package communication

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/letya999/hermes-hub/internal/identity"
	hubruntime "github.com/letya999/hermes-hub/internal/runtime"
)

// newTaskCommand implements /new [имя]: create a session task and switch to
// it. In a group the bot first tries to mint a dedicated forum topic for the
// session; when the group is not a forum or the bot lacks manage_topics the
// command falls back to binding the topic it was typed in, as before. The
// reply lands in the topic the session ended up bound to.
// Tasks are owner-scoped by construction: the resolved user is the only
// authority.
func (g *Gateway) newTaskCommand(ctx context.Context, user User, chatID, topic int64, text string) (string, int64) {
	fields := strings.Fields(text)
	name := strings.TrimSpace(strings.TrimPrefix(text, fields[0]))
	bind := topic
	if chatID < 0 {
		bind = 0
	}
	created, isNew, err := g.spool.CreateTask(user, chatID, bind, name, true)
	if err != nil {
		return "Задача отклонена: " + err.Error(), topic
	}
	if !isNew {
		return "Задача уже есть — переключился на неё: " + taskLabel(created) + ".", topic
	}
	replyTopic, freshTopic := topic, false
	if chatID < 0 {
		if forumTopic, err := g.api.CreateForumTopic(ctx, chatID, created.Name); err == nil {
			if err := g.spool.RebindTaskTopic(user, chatID, created.TaskID, forumTopic); err == nil {
				created.TopicID, replyTopic, freshTopic = forumTopic, forumTopic, true
			}
		}
		if !freshTopic && topic != 0 {
			if err := g.spool.RebindTaskTopic(user, chatID, created.TaskID, topic); err != nil {
				return "Задача создана и выбрана: " + taskLabel(created) + ". Топик не привязан: " + err.Error() + ".", topic
			}
			created.TopicID = topic
		}
	}
	answer := "Задача создана и выбрана: " + taskLabel(created) + "."
	if created.NameAuto {
		answer += " Название появится автоматически после первых сообщений."
	}
	if freshTopic {
		answer += " Для неё создан отдельный топик."
	} else if created.TopicID != 0 {
		answer += " Этот топик теперь привязан к ней."
	}
	return answer, replyTopic
}

// useCommand implements /use <id|имя|default>: switch the chat's current
// session. Posting afterwards follows the selected task's conversation.
func (g *Gateway) useCommand(ctx context.Context, user User, chatID int64, text string) string {
	fields := strings.Fields(text)
	if len(fields) < 2 {
		return "Используйте /use <id|имя|default>."
	}
	selector := strings.Join(fields[1:], " ")
	selected, err := g.spool.UseTask(user, chatID, selector)
	if err == nil {
		return "Текущая задача: " + taskLabel(selected) + "."
	}
	restored, rebound, uerr := g.spool.UnarchiveTask(user, chatID, selector)
	if uerr != nil {
		return "Задача не найдена: " + err.Error()
	}
	if rebound && chatID < 0 {
		if err := g.api.ReopenForumTopic(ctx, chatID, restored.TopicID); err != nil {
			log.Printf("gateway forum topic reopen failed chat=%d topic=%d: %v", chatID, restored.TopicID, err)
		}
	}
	return "Сессия " + taskLabel(restored) + " восстановлена из архива и выбрана текущей."
}

// deleteCommand implements /delete [id|имя]: bare removes the current task,
// a selector removes the named one. In a forum group the session's topic is
// deleted with it; a confirmation typed inside that deleted topic is
// delivered to the group root instead. The upstream Hermes session is kept
// for audit; only the registry record and routing are removed.
func (g *Gateway) deleteCommand(ctx context.Context, user User, chatID, topic int64, task Task, text string) (string, int64) {
	fields := strings.Fields(text)
	selector := task.TaskID
	if len(fields) >= 2 {
		selector = strings.Join(fields[1:], " ")
	}
	deleted, err := g.spool.DeleteTaskBySelector(user, chatID, selector)
	if err != nil {
		return "Не удалось удалить: " + err.Error(), topic
	}
	replyTopic := topic
	if deleted.TopicID != 0 && chatID < 0 {
		if err := g.api.DeleteForumTopic(ctx, chatID, deleted.TopicID); err != nil {
			log.Printf("gateway forum topic delete failed chat=%d topic=%d: %v", chatID, deleted.TopicID, err)
		}
		if replyTopic == deleted.TopicID {
			replyTopic = 0
		}
	}
	return "Задача " + taskLabel(deleted) + " удалена, текущая — default. Её Hermes-сессия сохранена наверху для аудита.", replyTopic
}

// archiveCommand implements /archive [id|имя]: retire the resolved or
// addressed session. In a forum group its topic is closed so the UI mirrors
// the archive state; the Hermes session itself is kept for audit and /use
// can restore it.
func (g *Gateway) archiveCommand(ctx context.Context, user User, chatID int64, task Task, text string) string {
	if fields := strings.Fields(text); len(fields) >= 2 {
		addressed, err := g.spool.TaskBySelector(user, chatID, strings.Join(fields[1:], " "))
		if err != nil {
			return "Задача не найдена: " + err.Error()
		}
		task = addressed
	}
	archived, err := g.spool.ArchiveTask(user, chatID, task.TaskID)
	if err != nil {
		return "Не удалось архивировать: " + err.Error()
	}
	if archived.TopicID != 0 && chatID < 0 {
		if err := g.api.CloseForumTopic(ctx, chatID, archived.TopicID); err != nil {
			log.Printf("gateway forum topic close failed chat=%d topic=%d: %v", chatID, archived.TopicID, err)
		}
	}
	return "Сессия " + taskLabel(archived) + " заархивирована (" + archived.UpdatedAt.UTC().Format("02.01.2006 15:04") + " UTC). Восстановить: /use " + archived.TaskID + "."
}

// archivedCommand implements /archived: list archived sessions with dates.
func (g *Gateway) archivedCommand(user User, chatID int64) string {
	tasks, err := g.spool.ListArchivedTasks(user, chatID)
	if err != nil {
		return "Список архива недоступен."
	}
	if len(tasks) == 0 {
		return "Архив пуст."
	}
	lines := []string{"Архив сессий:"}
	for _, task := range tasks {
		line := "— " + taskLabel(task)
		if !task.UpdatedAt.IsZero() {
			line += " — архив " + task.UpdatedAt.UTC().Format("02.01.2006 15:04") + " UTC"
		}
		lines = append(lines, line)
	}
	return strings.Join(lines, "\n")
}

// stopCommand implements /stop: cancel the non-terminal job bound to the
// resolved session. A queued job is cancelled locally; an admitted run still
// goes through the runtime control loop for confirmation.
func (g *Gateway) stopCommand(user User, task Task, callerForJob func(string) identity.Envelope) string {
	jobs, err := g.spool.ActiveJobsForTask(user.ID, task.TaskID)
	if err != nil {
		return "Не удалось проверить активные задачи."
	}
	if len(jobs) == 0 {
		return "В этой сессии нет активной работы."
	}
	lines := []string{}
	for _, job := range jobs {
		updated, err := g.spool.RequestCancel(job.JobID, callerForJob(job.JobID))
		if err != nil {
			lines = append(lines, job.JobID+": отмена недоступна.")
			continue
		}
		lines = append(lines, "Останавливаю "+job.JobID+" (состояние: "+updated.Status+").")
	}
	return strings.Join(lines, "\n")
}

// forumTopicCreated mirrors a user-created forum topic into a bound session
// named exactly like it: the first post lands in that session instead of
// minting a "topic-<id>" placeholder. The chat's current pointer stays
// untouched until the user actually posts. A topic bound before the service
// message arrived gets its placeholder replaced by the real name.
func (g *Gateway) forumTopicCreated(user User, chatID, topicID int64, name string) error {
	if topicID == 0 {
		return nil
	}
	name = strings.TrimSpace(name)
	if _, isNew, err := g.spool.CreateTask(user, chatID, topicID, name, false); err == nil && isNew {
		return nil
	}
	if err := g.spool.FixTopicPlaceholder(user, chatID, topicID, name); err != nil {
		log.Printf("gateway forum topic bind failed chat=%d topic=%d: %v", chatID, topicID, err)
	}
	return nil
}

func (g *Gateway) taskStatus(user User, task Task) string {
	answer := "Текущая задача: " + taskLabel(task) + "."
	if !task.CreatedAt.IsZero() {
		answer += " Создана " + task.CreatedAt.UTC().Format("02.01.2006 15:04") + " UTC."
	}
	if task.NameAuto {
		answer += " Название будет присвоено автоматически."
	}
	if task.TopicID != 0 {
		answer += " Топик #" + itoa64(task.TopicID) + "."
	}
	mapping, found, err := g.spool.SessionFor(user.ID, user.ID, task.ConversationID)
	if err == nil && found && mapping.SessionID != "" {
		answer += " Сессия: " + mapping.SessionID + "."
	}
	return answer + g.surfaceLines(user) + g.healthLines(user)
}

// runtimeSurface is the additive health observation the runtime reports and
// the supervisor persists.
type runtimeSurface struct {
	PrincipalID  string            `json:"principal_id"`
	ContextID    string            `json:"context_id"`
	ToolSurface  string            `json:"tool_surface"`
	MissingTools []string          `json:"missing_tools"`
	ToolReasons  map[string]string `json:"tool_reasons"`
	ModelStatus  string            `json:"model_status"`
	Restarts     int               `json:"restarts"`
}

// surfaceLines renders the caller's own tool-surface and model observations in
// plain language (SPEC-0053 R1.4-R1.6). The supervised gateway reads the
// persisted supervisor records and keeps strictly to the caller's
// principal/context — a sibling's missing tools never leak. An unreachable
// supervisor adds nothing: status stays available when supervision is not.
func (g *Gateway) surfaceLines(user User) string {
	if strings.TrimSpace(g.config.RuntimeURL) == "" || strings.TrimSpace(g.config.RuntimeAuth) == "" {
		return ""
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	var surface *runtimeSurface
	if g.config.Supervised {
		surface = g.supervisedSurface(ctx, user)
	} else {
		surface = g.residentSurface(ctx)
	}
	if surface == nil {
		return ""
	}
	var lines []string
	if len(surface.MissingTools) > 0 {
		lines = append(lines, "", "Недоступно:")
		for _, name := range surface.MissingTools {
			lines = append(lines, "- "+name+" — инструмент не раздаётся.")
		}
	}
	keys := make([]string, 0, len(surface.ToolReasons))
	for key := range surface.ToolReasons {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	for _, key := range keys {
		lines = append(lines, "- "+key+": "+surface.ToolReasons[key]+".")
	}
	if surface.ModelStatus == "model_not_found" {
		lines = append(lines, "Модель не найдена в каталоге провайдера (model_not_found).")
	}
	if len(lines) == 0 {
		return ""
	}
	return "\n" + strings.Join(lines, "\n")
}

func (g *Gateway) supervisedSurface(ctx context.Context, user User) *runtimeSurface {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, strings.TrimRight(g.config.RuntimeURL, "/")+"/v1/runtimes", nil)
	if err != nil {
		return nil
	}
	req.Header.Set("Authorization", "Bearer "+g.config.RuntimeAuth)
	response, err := http.DefaultClient.Do(req)
	if err != nil {
		return nil
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		return nil
	}
	var runtimes []runtimeSurface
	if json.NewDecoder(io.LimitReader(response.Body, 1<<20)).Decode(&runtimes) != nil {
		return nil
	}
	for i := range runtimes {
		if runtimes[i].PrincipalID == user.ID && runtimes[i].ContextID == user.ID {
			return &runtimes[i]
		}
	}
	return nil
}

// residentSurface reads the resident runtime's own health when there is no
// supervisor — the same additive fields, and the resident runtime is the
// caller's by construction.
func (g *Gateway) residentSurface(ctx context.Context) *runtimeSurface {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, strings.TrimRight(g.config.RuntimeURL, "/")+"/v1/health", nil)
	if err != nil {
		return nil
	}
	req.Header.Set("Authorization", "Bearer "+g.config.RuntimeAuth)
	response, err := http.DefaultClient.Do(req)
	if err != nil {
		return nil
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		return nil
	}
	var surface runtimeSurface
	if json.NewDecoder(io.LimitReader(response.Body, 64*1024)).Decode(&surface) != nil {
		return nil
	}
	return &surface
}

// sessionsCommand implements /sessions: the owner's live tasks in this chat
// with the current one marked. Archived tasks stay in the spool for audit but
// are not listed.
func (g *Gateway) sessionsCommand(user User, chatID int64) string {
	tasks, err := g.spool.ListTasks(user, chatID)
	if err != nil {
		return "Не удалось получить список задач."
	}
	current, resolveErr := g.spool.ResolveTask(user, chatID, 0, false)
	if resolveErr != nil {
		return "Не удалось получить список задач."
	}
	lines := []string{"Сессии:"}
	for _, task := range tasks {
		line := "- " + taskLabel(task)
		if !task.CreatedAt.IsZero() {
			line += " — " + task.CreatedAt.UTC().Format("02.01 15:04")
		}
		if task.TaskID == current.TaskID {
			line += " — текущая"
		}
		if task.TopicID != 0 {
			line += " (топик #" + itoa64(task.TopicID) + ")"
		}
		lines = append(lines, line)
	}
	lines = append(lines, "Переключение: /use <id|имя|default>. Новая: /new [имя]. Удалить: /delete [id|имя].")
	return strings.Join(lines, "\n")
}

// usageCommand implements /usage: a compact report — tokens burned, context
// window fill, compactions, and subscription limits with reset times. Only
// measured upstream fields are rendered; missing counters print "unknown"
// instead of an estimate.
func (g *Gateway) usageCommand(ctx context.Context, user User, sender int64, task Task) string {
	runner, ok := g.runner.(interface {
		SessionUsage(context.Context, hubruntime.ExecuteRequest) (hubruntime.SessionUsage, error)
	})
	if !ok {
		return "Измерение расхода недоступно для этого исполнителя."
	}
	envelope := user.Envelope(sender)
	envelope.ConversationID = task.ConversationID
	request := hubruntime.ExecuteRequest{
		Envelope:       envelope,
		OrganizationID: g.config.OrganizationID,
		UserID:         user.ID,
		ActorID:        user.ID,
		ScopeID:        "user:" + user.ID,
		Channel:        "telegram_bot",
		Trigger:        "usage",
		IdempotencyKey: "usage",
	}
	report, err := runner.SessionUsage(ctx, request)
	if err != nil && usageTransient(err) {
		// The supervisor ensures the runtime before measuring; one retry
		// covers the window where it dies between acquire and forward.
		timer := time.NewTimer(3 * time.Second)
		select {
		case <-ctx.Done():
		case <-timer.C:
		}
		timer.Stop()
		report, err = runner.SessionUsage(ctx, request)
	}
	if err != nil {
		if usageTransient(err) {
			return "Измерение расхода недоступно: сессионный рантайм не поднялся. Повторите /usage через минуту."
		}
		return "Измерение расхода недоступно: " + err.Error()
	}
	if !report.SessionFound {
		return "Задача " + taskLabel(task) + ": постоянная Hermes-сессия ещё не создана."
	}
	lines := []string{"Расход сессии: вход " + usageNum(report.InputTokens) + ", выход " + usageNum(report.OutputTokens) +
		", кэш " + usageNum(report.CacheReadTokens) + "/" + usageNum(report.CacheWriteTokens) + "."}
	compactions := usageNum(report.Compactions)
	if report.CompactionsTruncated && report.Compactions != nil {
		// Hop-capped lineage is a lower bound, never a silent truncation.
		compactions = "≥" + compactions
	}
	contextLine := "Сжатий сессии: " + compactions + usageSuffix(report.LastCompactionAt) + "."
	if report.ContextTokens != nil && report.ContextWindow != nil && *report.ContextWindow > 0 {
		contextLine = "Контекст: " + strconv.FormatInt(*report.ContextTokens, 10) + "/" + strconv.FormatInt(*report.ContextWindow, 10) +
			" (" + strconv.FormatInt(*report.ContextTokens*100/(*report.ContextWindow), 10) + "%). " + contextLine
	}
	lines = append(lines, contextLine)
	if totals, err := g.spool.UsageTotals("user:" + user.ID); err == nil && totals.Jobs > 0 {
		lines = append(lines, "Всего по журналу: "+strconv.Itoa(totals.Jobs)+" запусков, вход "+strconv.FormatInt(totals.InputTokens, 10)+", выход "+strconv.FormatInt(totals.OutputTokens, 10)+".")
	}
	lines = append(lines, g.quotaLines(ctx)...)
	return strings.Join(lines, "\n")
}

func (g *Gateway) journalUsage(user User, command string) string {
	fields := strings.Fields(command)
	var since time.Time
	model := ""
	for _, field := range fields[1:] {
		days := map[string]int{"day": 1, "week": 7, "month": 30}[field]
		if days > 0 && since.IsZero() {
			since = g.now().UTC().Add(-time.Duration(days) * 24 * time.Hour)
			continue
		}
		if model == "" && (strings.HasPrefix(field, "model=") || !since.IsZero()) && days == 0 {
			model = strings.TrimPrefix(field, "model=")
			if model != "" && len(model) <= 128 {
				continue
			}
		}
		return "Используйте /usage [day|week|month] [model=<id>] или /usage session."
	}
	totals, err := g.spool.UsageTotalsSince("user:"+user.ID, since, model)
	if err != nil {
		return "Журнал расхода недоступен."
	}
	return fmt.Sprintf("Журнал: %d попыток; измерено вход %d, выход %d. Неполных измерений: %d; старых записей без точного учёта: %d. Первичных снимков сессии: %d (могут включать более ранний расход).", totals.Jobs, totals.InputTokens, totals.OutputTokens, totals.UnknownJobs, totals.LegacyJobs, totals.InitialSnapshots)
}

// usageTransient reports lookup-path failures worth one retry: the runtime is
// mid-spawn (404/503) or died between acquire and forward (502).
func usageTransient(err error) bool {
	return strings.Contains(err.Error(), "404") || strings.Contains(err.Error(), "502") || strings.Contains(err.Error(), "503")
}

func usageNum(v *int64) string {
	if v == nil {
		return "unknown"
	}
	return strconv.FormatInt(*v, 10)
}

func usageSuffix(at string) string {
	if at == "" {
		return ""
	}
	return ", последнее " + at
}

func itoa64(v int64) string {
	return strconv.FormatInt(v, 10)
}

// modelCommand implements /model: list the owner's granted logical models and
// switch the selection through the space's own model-relay admin endpoint.
// The bearer comes from model-relay.auth (HUB_MODEL_RELAY_ADMIN) — the relay
// re-checks grants, so the command can never widen access.
func modelCommand(ctx context.Context, text string) string {
	base := strings.TrimSpace(os.Getenv("HUB_MODEL_RELAY_URL"))
	if base == "" {
		return "Переключение моделей недоступно: в этом пространстве нет управляемого релея."
	}
	admin := strings.TrimSpace(os.Getenv("HUB_MODEL_RELAY_ADMIN"))
	call := func(ctx context.Context, method, path string, body []byte) (int, []byte) {
		req, err := http.NewRequestWithContext(ctx, method, strings.TrimRight(base, "/")+path, bytes.NewReader(body))
		if err != nil {
			return 0, nil
		}
		if body != nil {
			req.Header.Set("Content-Type", "application/json")
		}
		if admin != "" {
			req.Header.Set("Authorization", "Bearer "+admin)
		}
		client := &http.Client{Timeout: 15 * time.Second, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
		resp, err := client.Do(req)
		if err != nil {
			return 0, nil
		}
		defer resp.Body.Close()
		b, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
		return resp.StatusCode, b
	}
	fields := strings.Fields(text)
	if len(fields) < 2 {
		code, body := call(ctx, http.MethodGet, "/v1/models", nil)
		if code != http.StatusOK {
			return "Список моделей недоступен: релей не отвечает."
		}
		var catalog struct {
			Data []struct {
				ID string `json:"id"`
			} `json:"data"`
		}
		if json.Unmarshal(body, &catalog) != nil || len(catalog.Data) == 0 {
			return "Список моделей пуст."
		}
		var names []string
		for _, m := range catalog.Data {
			names = append(names, m.ID)
		}
		return "Модели: " + strings.Join(names, ", ") + ". Переключить: /model <имя>."
	}
	name := fields[1]
	payload, _ := json.Marshal(map[string]string{"model": name})
	code, _ := call(ctx, http.MethodPut, "/v1/model", payload)
	switch code {
	case http.StatusOK:
		return "Модель переключена: " + name + ". Новые запросы пойдут через её лесенку."
	case http.StatusForbidden:
		return "Нет права на модель " + name + "."
	default:
		return "Переключение недоступно: релей отклонил запрос."
	}
}
