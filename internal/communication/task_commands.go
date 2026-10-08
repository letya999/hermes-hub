package communication

import (
	"context"
	"strconv"
	"strings"
	"time"

	hubruntime "github.com/letya999/hermes-hub/internal/runtime"
)

// newTaskCommand implements /new [имя]: create a session task and switch to
// it. Tasks are owner-scoped by construction: the resolved user is the only
// authority.
func (g *Gateway) newTaskCommand(user User, chatID, topic int64, text string) string {
	fields := strings.Fields(text)
	name := strings.TrimSpace(strings.TrimPrefix(text, fields[0]))
	created, isNew, err := g.spool.CreateTask(user, chatID, topic, name, true)
	if err != nil {
		return "Задача отклонена: " + err.Error()
	}
	if !isNew {
		return "Задача уже есть — переключился на неё: " + taskLabel(created) + "."
	}
	answer := "Задача создана и выбрана: " + taskLabel(created) + "."
	if created.NameAuto {
		answer += " Название появится автоматически после первых сообщений."
	}
	if topic != 0 {
		answer += " Этот топик теперь привязан к ней."
	}
	return answer
}

// useCommand implements /use <id|имя|default>: switch the chat's current
// session. Posting afterwards follows the selected task's conversation.
func (g *Gateway) useCommand(user User, chatID int64, text string) string {
	fields := strings.Fields(text)
	if len(fields) < 2 {
		return "Используйте /use <id|имя|default>."
	}
	selected, err := g.spool.UseTask(user, chatID, strings.Join(fields[1:], " "))
	if err != nil {
		return "Задача не найдена: " + err.Error()
	}
	return "Текущая задача: " + taskLabel(selected) + "."
}

// deleteCommand implements /delete [id|имя]: bare removes the current task,
// a selector removes the named one. The upstream Hermes session is kept for
// audit; only the registry record and routing are removed.
func (g *Gateway) deleteCommand(user User, chatID int64, task Task, text string) string {
	fields := strings.Fields(text)
	selector := task.TaskID
	if len(fields) >= 2 {
		selector = strings.Join(fields[1:], " ")
	}
	deleted, err := g.spool.DeleteTaskBySelector(user, chatID, selector)
	if err != nil {
		return "Не удалось удалить: " + err.Error()
	}
	return "Задача " + taskLabel(deleted) + " удалена, текущая — default. Её Hermes-сессия сохранена наверху для аудита."
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
	return answer
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
	envelope := user.envelope(sender)
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
	lines = append(lines, g.quotaLines(ctx)...)
	return strings.Join(lines, "\n")
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
