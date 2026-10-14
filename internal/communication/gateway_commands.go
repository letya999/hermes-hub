package communication

import (
	"context"
	hubruntime "github.com/letya999/hermes-hub/internal/runtime"
	"strings"
)

// botCommandInventory is the single source of truth for supported slash
// commands: it feeds both the Telegram menu (registered on every startup, so
// stale scope registrations left by other adapters cannot linger) and /help.
var botCommandInventory = []struct {
	Command     string
	Description string
}{
	{"start", "Подключиться к своему Hermes-пространству"},
	{"new", "Новая сессия (отдельный контекст): /new [имя]"},
	{"sessions", "Список сессий с датами"},
	{"use", "Переключиться на сессию: /use <id|имя|default>"},
	{"delete", "Удалить сессию: /delete [id|имя]"},
	{"session", "Текущая сессия: имя, дата, session id"},
	{"status", "Текущая задача без вызова модели"},
	{"routines", "Напоминания и фоновые проверки"},
	{"usage", "Журнал расхода: /usage [day|week|month|session]"},
	{"connectors", "Подключения и коннекторы ToolHub"},
	{"connections", "Подключения и коннекторы ToolHub"},
	{"credentials", "Код брокера: /credentials <request-id> <code>"},
	{"approve", "Подтверждение: /approve <job-id> <request-id> <choice>"},
}

// commandHelp is the authoritative inventory of supported slash commands;
// unknown commands are answered with it instead of reaching the model, so a
// typo can never produce a fabricated confirmation.
func commandHelp() string {
	var b strings.Builder
	b.WriteString("Команды:")
	for _, c := range botCommandInventory {
		b.WriteString("\n/" + c.Command + " — " + c.Description)
	}
	return b.String()
}

// autoTitleMinMessages bounds title adoption: a session title is trusted once
// the conversation has at least one real exchange behind it.
const autoTitleMinMessages = 2

// maybeAutoTitle adopts the upstream Hermes session title as the task name.
// Only tasks still carrying their auto-generated placeholder qualify — an
// explicit operator name is never overwritten — and failures are silent: a
// title is cosmetic, never worth failing or retrying a delivery for.
func (g *Gateway) maybeAutoTitle(ctx context.Context, job Job) {
	if !g.spool.TaskNameAuto(job.PrincipalID, job.TaskID) {
		return
	}
	runner, ok := g.runner.(interface {
		SessionUsage(context.Context, hubruntime.ExecuteRequest) (hubruntime.SessionUsage, error)
	})
	if !ok {
		return
	}
	report, err := runner.SessionUsage(ctx, hubruntime.ExecuteRequest{
		Envelope:       job.Envelope,
		OrganizationID: g.config.OrganizationID,
		UserID:         job.UserID,
		ActorID:        job.ActorID,
		ScopeID:        job.ScopeID,
		Channel:        job.Channel,
		Trigger:        "auto-title",
		IdempotencyKey: "auto-title-" + job.ID,
	})
	if err != nil || !report.SessionFound || report.MessageCount == nil || *report.MessageCount < autoTitleMinMessages || strings.TrimSpace(report.Title) == "" {
		return
	}
	if g.spool.AutoTitleTask(job.PrincipalID, job.TaskID, report.Title) {
		_ = g.spool.EnqueueDelivery(Delivery{ID: "task-" + job.TaskID + "-title", IdempotencyKey: "task-" + job.TaskID + "-title", Channel: job.Channel, ChatID: job.ChatID, TaskID: job.TaskID, TopicID: job.TopicID, ConversationID: job.ConversationID, DeliveryTargetID: job.DeliveryTargetID, SlackChannel: job.SlackChannel, SlackThread: job.SlackThread, Text: "Сессия автоматически названа: «" + strings.TrimSpace(report.Title) + "».", CreatedAt: g.now().UTC()})
	}
}
