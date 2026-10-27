package communication

import (
	"context"
	"strings"

	"github.com/letya999/hermes-hub/internal/locale"
	hubruntime "github.com/letya999/hermes-hub/internal/runtime"
)

// botCommandInventory is the single source of truth for supported slash
// commands: it feeds both the Telegram menu (registered on every startup, so
// stale scope registrations left by other adapters cannot linger) and /help.
// DescKey is the catalog entry rendered per the recipient's language.
var botCommandInventory = []struct {
	Command string
	DescKey string
}{
	{"start", "menu.desc.start"},
	{"new", "menu.desc.new"},
	{"sessions", "menu.desc.sessions"},
	{"use", "menu.desc.use"},
	{"delete", "menu.desc.delete"},
	{"session", "menu.desc.session"},
	{"status", "menu.desc.status"},
	{"routines", "menu.desc.routines"},
	{"usage", "menu.desc.usage"},
	{"compact", "menu.desc.compact"},
	{"goal", "menu.desc.goal"},
	{"model", "menu.desc.model"},
	{"connectors", "menu.desc.connectors"},
	{"connections", "menu.desc.connections"},
	{"watches", "menu.desc.watches"},
	{"credentials", "menu.desc.credentials"},
	{"approve", "menu.desc.approve"},
	{"stop", "menu.desc.stop"},
	{"cancel", "menu.desc.cancel"},
	{"archive", "menu.desc.archive"},
	{"archived", "menu.desc.archived"},
}

// commandHelp is the authoritative inventory of supported slash commands;
// unknown commands are answered with it instead of reaching the model, so a
// typo can never produce a fabricated confirmation.
func commandHelp(lang locale.Lang) string {
	var b strings.Builder
	b.WriteString(locale.T(lang, "help.title"))
	for _, c := range botCommandInventory {
		b.WriteString("\n/" + c.Command + " — " + locale.T(lang, c.DescKey))
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
		if job.ChatID < 0 && job.TopicID != 0 {
			_ = g.api.EditForumTopic(ctx, job.ChatID, job.TopicID, strings.TrimSpace(report.Title))
		}
		_ = g.spool.EnqueueDelivery(Delivery{ID: "task-" + job.TaskID + "-title", IdempotencyKey: "task-" + job.TaskID + "-title", Channel: job.Channel, ChatID: job.ChatID, TaskID: job.TaskID, TopicID: job.TopicID, ConversationID: job.ConversationID, DeliveryTargetID: job.DeliveryTargetID, SlackChannel: job.SlackChannel, SlackThread: job.SlackThread, Text: locale.T(locale.LangOf(g.user(job.UserID).WorkspaceDir), "chat.auto-titled", strings.TrimSpace(report.Title)), CreatedAt: g.now().UTC()})
	}
}
