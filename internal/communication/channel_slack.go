package communication

import (
	"context"
	"errors"
	"strings"

	"github.com/letya999/hermes-hub/internal/identity"
	hubruntime "github.com/letya999/hermes-hub/internal/runtime"
)

// slackChannel is the registered adapter for channel "slack_app".
type slackChannel struct {
	g *Gateway
}

func (c slackChannel) ID() string { return "slack_app" }

func (c slackChannel) NormalizeDelivery(d *Delivery) error {
	if !identity.ValidID(d.ConversationID) || !identity.ValidID(d.DeliveryTargetID) || d.DeliveryTargetID != d.ConversationID || !validSlackIMChannel(d.SlackChannel) || !validSlackThread(d.SlackThread) {
		return errors.New("invalid delivery identity")
	}
	return nil
}

func (c slackChannel) Deliver(ctx context.Context, delivery *Delivery) error {
	g := c.g
	if g.slack == nil {
		return errors.New("slack delivery is not configured")
	}
	if delivery.Format == formatMarkdown {
		return g.sendParts(delivery, renderSlack(delivery.Text), func(part string) error {
			return g.slack.PostMessage(ctx, delivery.SlackChannel, delivery.SlackThread, part)
		})
	}
	return g.slack.PostMessage(ctx, delivery.SlackChannel, delivery.SlackThread, delivery.Text)
}

func (c slackChannel) Caps() ChannelCaps {
	// Slack replies with an artifact-ready note instead of a binary upload.
	return ChannelCaps{Artifacts: true}
}

func (c slackChannel) SendPresence(ctx context.Context, chatID, topicID int64) error {
	return nil
}

func (c slackChannel) DeliverArtifact(ctx context.Context, delivery *Delivery, ref hubruntime.ArtifactRef) error {
	g := c.g
	if g.slack == nil {
		return errors.New("slack delivery is not configured")
	}
	note := "Файл готов: " + ref.Name + " — сохранён в workspace, доставка файлов в Slack пока не поддерживается"
	if ref.Error != "" {
		note = "Файл не удалось доставить: " + ref.Name
	}
	return g.slack.PostMessage(ctx, delivery.SlackChannel, delivery.SlackThread, note)
}

func (c slackChannel) ValidateSchedule(in Schedule) error {
	if in.SlackChannel != "" && (!validSlackIMChannel(in.SlackChannel) || !validSlackThread(in.SlackThread)) {
		return errors.New("invalid schedule channel")
	}
	return nil
}

func (c slackChannel) RouteBlockedNotice(item Schedule, d *Delivery) {}

// Slack builds its envelope inside the events handler (team/user ids map to
// slack- identities); this path is the telegram-style fallback.
func (c slackChannel) JobEnvelope(user User, chatID, sender int64) identity.Envelope {
	return user.envelope(sender)
}

func (c slackChannel) PrepareRoutine(in *Schedule, caller identity.Envelope) (identity.Envelope, error) {
	return caller, nil
}

func (c slackChannel) AuthorizeOccurrence(job Job, user User, configured bool) error {
	if job.Envelope.PrincipalID != user.ID || !strings.HasPrefix(job.Envelope.ExternalIdentityID, "slack-") {
		return errors.New("routine policy changed")
	}
	return nil
}

func (c slackChannel) ValidateOccurrence(o RoutineOccurrence, caller identity.Envelope) error {
	if !strings.HasPrefix(caller.ExternalIdentityID, "slack-") {
		return errors.New("invalid occurrence ownership")
	}
	return nil
}
