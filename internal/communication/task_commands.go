package communication

import (
	"context"
	"strconv"
	"strings"
	"time"

	hubruntime "github.com/letya999/hermes-hub/internal/runtime"
)

// taskCommand implements /task, /task new|use|rename|archive. Tasks are
// owner-scoped by construction: the resolved user is the only authority.
func (g *Gateway) taskCommand(user User, chatID, topic int64, task Task, text string) string {
	fields := strings.Fields(text)
	if len(fields) == 1 {
		return g.taskStatus(user, task)
	}
	switch strings.ToLower(fields[1]) {
	case "new":
		name := strings.TrimSpace(strings.TrimPrefix(text, fields[0]+" "+fields[1]))
		created, isNew, err := g.spool.CreateTask(user, chatID, topic, name, true)
		if err != nil {
			return "Задача отклонена: " + err.Error()
		}
		if !isNew {
			return "Задача уже есть — переключился на неё: " + taskLabel(created) + "."
		}
		answer := "Задача создана и выбрана: " + taskLabel(created) + "."
		if created.NameAuto {
			answer += " Название появится автоматически после первых сообщений; задать своё: /task rename <имя>."
		}
		if topic != 0 {
			answer += " Этот топик теперь привязан к ней."
		}
		return answer
	case "use", "switch", "resume":
		if len(fields) < 3 {
			return "Используйте /task use <id|имя|default>."
		}
		selected, err := g.spool.UseTask(user, chatID, strings.Join(fields[2:], " "))
		if err != nil {
			return "Задача не найдена: " + err.Error()
		}
		return "Текущая задача: " + taskLabel(selected) + "."
	case "rename":
		name := strings.TrimSpace(strings.TrimPrefix(text, fields[0]+" "+fields[1]))
		if name == "" {
			return "Используйте /task rename <новое имя>."
		}
		renamed, err := g.spool.RenameTask(user, chatID, task.TaskID, name)
		if err != nil {
			return "Не удалось переименовать: " + err.Error()
		}
		return "Задача переименована: " + taskLabel(renamed) + ". История и сессия сохранены."
	case "archive":
		archived, err := g.spool.ArchiveTask(user, chatID, task.TaskID)
		if err != nil {
			return "Не удалось архивировать: " + err.Error()
		}
		return "Задача " + taskLabel(archived) + " в архиве. Текущая — default."
	case "archived", "archive-list":
		return g.archivedTasksCommand(user, chatID)
	case "delete":
		selector := task.TaskID
		if len(fields) >= 3 {
			selector = strings.Join(fields[2:], " ")
		}
		deleted, err := g.spool.DeleteTaskBySelector(user, chatID, selector)
		if err != nil {
			return "Не удалось удалить: " + err.Error()
		}
		return "Задача " + taskLabel(deleted) + " удалена, текущая — default. Её Hermes-сессия сохранена наверху для аудита."
	default:
		return "Используйте /task, /task new [имя], /task use <id|имя|default>, /task rename <имя>, /task archive, /task archived, /task delete."
	}
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
	if task.Style != "" {
		answer += " Стиль: " + task.Style
	}
	return answer
}

// tasksCommand lists the owner's live tasks in this chat with the current one
// marked. Archived tasks stay in the spool for audit but are not listed.
func (g *Gateway) tasksCommand(user User, chatID int64) string {
	tasks, err := g.spool.ListTasks(user, chatID)
	if err != nil {
		return "Не удалось получить список задач."
	}
	current, resolveErr := g.spool.ResolveTask(user, chatID, 0, false)
	if resolveErr != nil {
		return "Не удалось получить список задач."
	}
	lines := []string{"Задачи:"}
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
	lines = append(lines, "Переключение: /task use <id|имя|default>. Новая: /new [имя]. Архив: /task archived.")
	return strings.Join(lines, "\n")
}

// archivedTasksCommand lists tasks retired by /task archive; they keep their
// Hermes session for audit but no longer accept messages.
func (g *Gateway) archivedTasksCommand(user User, chatID int64) string {
	tasks, err := g.spool.ListArchivedTasks(user, chatID)
	if err != nil {
		return "Не удалось получить архив задач."
	}
	if len(tasks) == 0 {
		return "Архив пуст."
	}
	lines := []string{"Архив задач:"}
	for _, task := range tasks {
		line := "- " + taskLabel(task)
		if !task.CreatedAt.IsZero() {
			line += " — " + task.CreatedAt.UTC().Format("02.01 15:04")
		}
		lines = append(lines, line)
	}
	lines = append(lines, "Вернуть нельзя; новая сессия: /new [имя]. Удалить: /task delete внутри активной задачи.")
	return strings.Join(lines, "\n")
}

// styleCommand implements /style [text|reset]. Style is presentation guidance
// only: it rides every run as a trusted instruction, never as a user message,
// and never touches authorization or tool policy.
func (g *Gateway) styleCommand(user User, chatID int64, task Task, text string) string {
	fields := strings.Fields(text)
	if len(fields) == 1 {
		if task.Style == "" {
			return "Стиль не задан. Используйте /style <текст> или /style reset. Действует только на задачу " + taskLabel(task) + "."
		}
		return "Стиль задачи " + taskLabel(task) + " (v" + itoaU(task.StyleVersion) + "): " + task.Style
	}
	arg := strings.TrimSpace(strings.TrimPrefix(text, fields[0]))
	if strings.EqualFold(arg, "reset") {
		updated, err := g.spool.SetTaskStyle(user, chatID, task.TaskID, "")
		if err != nil {
			return "Не удалось сбросить стиль."
		}
		return "Стиль сброшен для задачи " + taskLabel(updated) + "."
	}
	updated, err := g.spool.SetTaskStyle(user, chatID, task.TaskID, arg)
	if err != nil {
		return "Стиль отклонён: до 1024 символов, без управляющих символов."
	}
	return "Стиль задачи " + taskLabel(updated) + " обновлён (v" + itoaU(updated.StyleVersion) + "). Применится со следующего запуска."
}

// usageCommand implements /usage: only measured upstream fields are rendered;
// anything the pinned API does not carry prints "unknown" instead of an
// estimate. Cumulative billed counters and current-prompt context are kept as
// separate lines on purpose.
func (g *Gateway) usageCommand(ctx context.Context, user User, sender int64, task Task) string {
	runner, ok := g.runner.(interface {
		SessionUsage(context.Context, hubruntime.ExecuteRequest) (hubruntime.SessionUsage, error)
	})
	if !ok {
		return "Измерение расхода недоступно для этого исполнителя."
	}
	envelope := user.envelope(sender)
	envelope.ConversationID = task.ConversationID
	report, err := runner.SessionUsage(ctx, hubruntime.ExecuteRequest{
		Envelope:       envelope,
		OrganizationID: g.config.OrganizationID,
		UserID:         user.ID,
		ActorID:        user.ID,
		ScopeID:        "user:" + user.ID,
		Channel:        "telegram_bot",
		Trigger:        "usage",
		IdempotencyKey: "usage",
	})
	if err != nil {
		return "Измерение расхода недоступно: " + err.Error()
	}
	if !report.SessionFound {
		return "Задача " + taskLabel(task) + ": постоянная Hermes-сессия ещё не создана."
	}
	lines := []string{"Задача " + taskLabel(task) + "."}
	if report.SessionID != report.DeclaredSessionID {
		lines = append(lines, "Сессия (эффективная): "+report.SessionID+" — продолжение "+report.DeclaredSessionID+".")
	} else {
		lines = append(lines, "Сессия: "+report.SessionID+".")
	}
	if report.Model != "" {
		lines = append(lines, "Модель: "+report.Model+".")
	}
	if report.Title != "" || report.StartedAt != "" {
		line := "Название сессии: " + usageText(report.Title) + "."
		if report.StartedAt != "" {
			if when, perr := time.Parse(time.RFC3339, report.StartedAt); perr == nil {
				line += " Создана " + when.UTC().Format("02.01.2006 15:04") + " UTC."
			}
		}
		lines = append(lines, line)
	}
	contextText := "unknown"
	if report.ContextTokens != nil && report.ContextWindow != nil && *report.ContextWindow > 0 {
		contextText = strconv.FormatInt(*report.ContextTokens, 10) + "/" + strconv.FormatInt(*report.ContextWindow, 10) +
			" (" + strconv.FormatInt(*report.ContextTokens*100/(*report.ContextWindow), 10) + "%)"
	}
	lines = append(lines, "Контекст запроса: "+contextText+".")
	lines = append(lines, "Накоплено по сессии: вход "+usageNum(report.InputTokens)+", выход "+usageNum(report.OutputTokens)+
		", кэш "+usageNum(report.CacheReadTokens)+"/"+usageNum(report.CacheWriteTokens)+
		", рассуждения "+usageNum(report.ReasoningTokens)+".")
	lines = append(lines, "Сообщений "+usageNum(report.MessageCount)+", вызовов API "+usageNum(report.APICallCount)+
		", вызовов инструментов "+usageNum(report.ToolCallCount)+".")
	if report.EstimatedCostUSD != nil || report.ActualCostUSD != nil {
		lines = append(lines, "Стоимость: оценка "+usageCost(report.EstimatedCostUSD)+", фактическая "+usageCost(report.ActualCostUSD)+".")
	}
	lines = append(lines, "Сжатий сессии: "+usageNum(report.Compactions)+usageSuffix(report.LastCompactionAt)+".")
	lines = append(lines, g.quotaLines(ctx)...)
	lines = append(lines, "Источник: "+report.Source+".")
	return strings.Join(lines, "\n")
}

func usageNum(v *int64) string {
	if v == nil {
		return "unknown"
	}
	return strconv.FormatInt(*v, 10)
}

func usageText(v string) string {
	if v == "" {
		return "unknown"
	}
	return v
}

func usageCost(v *float64) string {
	if v == nil {
		return "unknown"
	}
	return "$" + strconv.FormatFloat(*v, 'f', 4, 64)
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

func itoaU(v uint64) string {
	return strconv.FormatUint(v, 10)
}
