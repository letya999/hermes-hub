package communication

import (
	"context"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/letya999/hermes-hub/internal/compaction"
	hubruntime "github.com/letya999/hermes-hub/internal/runtime"
)

const compactUsage = "Использование: /compact [here N] [focus <текст>] [prune] [preview] [queue] [hub|native] — сжать контекст текущей задачи."

// parseCompactArgs translates the channel grammar into the runtime request's
// argument subset. `here` without a number defaults to 2 retained exchanges,
// matching upstream /compress.
func parseCompactArgs(text string) (hubruntime.CompactRequest, bool) {
	var args hubruntime.CompactRequest
	fields := strings.Fields(text)
	seen := map[string]bool{}
	for i := 1; i < len(fields); i++ {
		token := fields[i]
		switch {
		case token == "here" && !seen["here"]:
			seen["here"] = true
			args.Here = 2
			if i+1 < len(fields) {
				if n, err := strconv.Atoi(fields[i+1]); err == nil {
					if n <= 0 || n > 100 {
						return hubruntime.CompactRequest{}, false
					}
					args.Here = n
					i++
				}
			}
		case token == "focus" && !seen["focus"]:
			seen["focus"] = true
			args.Focus = strings.Join(fields[i+1:], " ")
			return args, args.Focus != ""
		case token == "prune" && !seen["prune"]:
			seen["prune"] = true
			args.Prune = true
		case (token == "preview" || token == "dry-run") && !seen["preview"]:
			seen["preview"] = true
			args.Preview = true
		case token == "queue" && !seen["queue"]:
			seen["queue"] = true
			args.BusyPolicy = "queue"
		case (token == "hub" || token == "native") && !seen["engine"]:
			seen["engine"] = true
			args.Engine = token
		case token == "engine" && !seen["engine"] && i+1 < len(fields):
			seen["engine"] = true
			args.Engine = fields[i+1]
			i++
		default:
			return hubruntime.CompactRequest{}, false
		}
	}
	return args, true
}

// compactRunner is the runtime surface the command needs; HTTPRunner
// implements it and tests stub it.
type compactRunner interface {
	Compact(context.Context, hubruntime.CompactRequest) (compaction.CompactResult, error)
}

// compactCommand implements /compact. The request rides the caller's verified
// envelope and the task's stable conversation to POST /v1/compact — the slash
// command itself is never enqueued as a run. Replies carry only the
// allowlisted result projection.
func (g *Gateway) compactCommand(ctx context.Context, user User, task Task, channel string, chatID, topic, sender int64, deliveryKey, text string) error {
	reply := func(answer string) error {
		return g.queueDeliveryChannel(ctx, deliveryKey+"-reply", channel, chatID, topic, answer)
	}
	args, ok := parseCompactArgs(text)
	if !ok {
		return reply(compactUsage)
	}
	runner, ok := g.runner.(compactRunner)
	if !ok {
		return reply("Сжатие контекста недоступно для этого исполнителя.")
	}
	envelope := user.Envelope(sender)
	envelope.ConversationID = task.ConversationID
	args.ExecuteRequest = hubruntime.ExecuteRequest{
		Envelope:       envelope,
		OrganizationID: g.config.OrganizationID,
		UserID:         user.ID,
		ActorID:        user.ID,
		ScopeID:        "user:" + user.ID,
		Channel:        "telegram_bot",
		Trigger:        "compact",
		IdempotencyKey: "compact",
	}
	if g.usageQueries.Add(1) > 2 {
		g.usageQueries.Add(-1)
		return reply("Сжатие уже выполняется. Повторите позже.")
	}
	queryCtx := ctx
	if g.commandCtx != nil {
		queryCtx = g.commandCtx
	}
	g.usageWG.Add(1)
	go func() { // #nosec G118 -- detached compact call outlives the update poll like /usage
		defer func() { g.usageQueries.Add(-1); g.usageWG.Done() }()
		compactCtx, cancel := context.WithTimeout(queryCtx, 5*time.Minute)
		defer cancel()
		result, err := runner.Compact(compactCtx, args)
		answer := compactReply(result)
		if err != nil {
			answer = "Сжатие контекста недоступно: " + err.Error()
		}
		_ = g.queueDeliveryChannel(context.Background(), deliveryKey+"-compact", channel, chatID, topic, answer)
	}()
	return reply("Сжимаю контекст…")
}

// compactReply renders only the allowlisted result projection — summary text
// and transcript content never cross this boundary by construction.
func compactReply(result compaction.CompactResult) string {
	tokens := func(p *int64) int64 {
		if p == nil {
			return 0
		}
		return *p
	}
	switch {
	case result.WouldMethod != "":
		return fmt.Sprintf("Предпросмотр: сработал бы %s; ~%d ток., сообщений %d.",
			result.WouldMethod, tokens(result.TokensBefore), result.MessagesBefore)
	case result.FailureClass == "queued":
		return "Сессия занята — сжатие выполнится на ближайшей границе хода."
	case result.Applied:
		return fmt.Sprintf("Контекст сжат (%s): ~%d → ~%d ток., сообщений %d → %d.",
			result.Method, tokens(result.TokensBefore), tokens(result.TokensAfter),
			result.MessagesBefore, result.MessagesAfter)
	}
	return "Сжатие не применено: " + compactFailureText(result.FailureClass)
}

func compactFailureText(class string) string {
	switch class {
	case "lease_held":
		return "сессия занята другим ходом; повторите позже или добавьте queue"
	case "engine_denied":
		return "запрошенный движок не разрешён политикой (context.compact)"
	case "unsupported":
		return "нативное сжатие недоступно в этой сборке Hermes"
	case "nothing_to_compress", "nothing_to_prune":
		return "сжимать нечего — транскрипт уже компактен"
	case "under_threshold":
		return "контекст ниже порога сжатия"
	case "estimate_failed":
		return "не удалось оценить размер контекста"
	case "disabled":
		return "сжатие отключено политикой"
	default:
		if class == "" {
			return "неизвестная ошибка"
		}
		return class
	}
}
