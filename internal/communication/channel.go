package communication

import (
	"context"
	"time"

	"github.com/letya999/hermes-hub/internal/channel"
	"github.com/letya999/hermes-hub/internal/identity"
)

// The provider seam lives in internal/channel: the contract, capability
// flags, contract errors and the adapters. These aliases keep the package's
// existing call sites and its exported contract stable.
type (
	Channel                = channel.Channel
	ChannelCaps            = channel.Caps
	deliveryTargetVerifier = channel.TargetVerifier
)

var (
	ErrUncertain       = channel.ErrUncertain
	errDeliveryRetried = channel.ErrDeliveryRetried
	errRoutineDenied   = channel.ErrRoutineDenied
)

// channelPlugins is the registry: provider id → adapter constructor. Deps
// closures evaluate gateway fields at call time, so struct-literal Gateway
// fixtures in tests — and nil gateways for the pure validation methods —
// resolve channels without init.
var channelPlugins = map[string]func(*Gateway) Channel{
	"telegram_bot": func(g *Gateway) Channel { return channel.NewTelegram(g.telegramDeps()) },
	"slack_app":    func(g *Gateway) Channel { return channel.NewSlack(g.slackDeps()) },
	"mock":         func(g *Gateway) Channel { return channel.NewMock(g.mockDeps()) },
}

// channelFor resolves an adapter; g may be nil for the pure methods
// (ValidateOccurrence, Caps) which never touch gateway state.
func channelFor(g *Gateway, id string) Channel {
	if makeChannel, ok := channelPlugins[id]; ok {
		return makeChannel(g)
	}
	return nil
}

func (g *Gateway) channel(id string) Channel {
	return channelFor(g, id)
}

// telegramDeps adapts gateway capabilities into the telegram adapter's
// contract. Every field is a closure so a nil gateway still constructs.
func (g *Gateway) telegramDeps() channel.TelegramDeps {
	return channel.TelegramDeps{
		API:              func() channel.TelegramSender { return g.api },
		SendEdit:         func(ctx context.Context, d *Delivery) error { return g.sendEditReply(ctx, d) },
		RetryDelivery:    func(id string) error { return g.spool.RetryDelivery(id) },
		UpdateSending:    func(d Delivery) error { return g.spool.UpdateSending(d) },
		TrackApproval:    func(d Delivery) error { return g.spool.trackApprovalMessage(d) },
		ReadDeliveryBlob: func(deliveryID, blob string) ([]byte, error) { return g.spool.ReadDeliveryBlob(deliveryID, blob) },
		VoicePlan:        func(d Delivery) (string, bool) { return g.voicePlan(d) },
		SendVoice:        func(ctx context.Context, d Delivery, text string) error { return g.sendVoiceReply(ctx, d, text) },
		SendMarkdown:     func(ctx context.Context, d *Delivery) error { return g.sendTelegramMarkdown(ctx, d) },
		UserByChat:       func(chatID int64) (User, bool) { u, ok := g.users[chatID]; return u, ok },
		UserByPrincipal:  func(id string) User { return g.user(id) },
		OwnsRoutineEnvelope: func(job Job, canonical identity.Envelope) bool {
			return g.ownsRoutineEnvelope(job, canonical)
		},
	}
}

func (g *Gateway) slackDeps() channel.SlackDeps {
	return channel.SlackDeps{
		Slack:     func() channel.SlackPoster { return g.slack },
		SendParts: func(d *Delivery, parts []string, send func(string) error) error { return g.sendParts(d, parts, send) },
		Render:    renderSlack,
	}
}

func (g *Gateway) mockDeps() channel.MockDeps {
	return channel.MockDeps{
		Capture:             func(d *Delivery) error { return g.captureMock(d) },
		LinkOwner:           func(chatID int64) (string, error) { return g.mockLinkOwner(chatID) },
		UserByPrincipal:     func(id string) User { return g.user(id) },
		OwnsRoutineEnvelope: func(job Job, canonical identity.Envelope) bool { return g.ownsRoutineEnvelope(job, canonical) },
	}
}

// sendEditReply is the throttled status-edit path (SPEC-0053 R2.2): one edit
// per interval per chat; a throttled edit requeues the delivery instead of
// sending. The throttle is gateway semantics, so it stays here — the
// telegram adapter only dispatches on EditMessageID.
func (g *Gateway) sendEditReply(ctx context.Context, delivery *Delivery) error {
	g.edits.mu.Lock()
	if now, last := g.now(), g.edits.last[delivery.ChatID]; !last.IsZero() && now.Sub(last) < 2*time.Second {
		g.edits.mu.Unlock()
		_ = g.spool.RetryDelivery(delivery.ID)
		return errDeliveryRetried
	}
	sendErr := g.api.EditMessageText(ctx, delivery.ChatID, delivery.EditMessageID, limitTelegramText(delivery.Text))
	if sendErr == nil {
		if g.edits.last == nil {
			g.edits.last = map[int64]time.Time{}
		}
		g.edits.last[delivery.ChatID] = g.now()
	}
	g.edits.mu.Unlock()
	return sendErr
}
