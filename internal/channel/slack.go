package channel

import (
	"context"
	"errors"
	"regexp"
	"strings"

	"github.com/letya999/hermes-hub/internal/commodel"
	"github.com/letya999/hermes-hub/internal/identity"
	hubruntime "github.com/letya999/hermes-hub/internal/runtime"
)

// Slack IM/channel ids (D…/C…/G…) are not identity.ValidID; they are only a
// chat.postMessage target and never fill Envelope.DeliveryTargetID.
var slackIMChannel = regexp.MustCompile(`^[CDG][A-Za-z0-9]{1,32}$`)
var slackThreadTS = regexp.MustCompile(`^[0-9]{10,16}\.[0-9]{1,6}$`)

func ValidSlackIMChannel(id string) bool { return slackIMChannel.MatchString(id) }
func ValidSlackThread(ts string) bool    { return ts == "" || slackThreadTS.MatchString(ts) }

// SlackPoster is the subset of the Slack web API the adapter uses.
type SlackPoster interface {
	PostMessage(ctx context.Context, channel, thread, text string) error
}

// SlackDeps declares the gateway capabilities the slack adapter borrows.
type SlackDeps struct {
	// Slack yields the configured client; nil means the channel is not
	// configured — deliveries fail with a clear error instead of panicking.
	Slack     func() SlackPoster
	SendParts func(d *commodel.Delivery, parts []string, send func(part string) error) error
	Render    func(src string) []string
}

// slack is the registered adapter for channel "slack_app".
type slack struct {
	d SlackDeps
}

func NewSlack(deps SlackDeps) Channel { return slack{d: deps} }

func (c slack) ID() string { return "slack_app" }

func (c slack) NormalizeDelivery(d *commodel.Delivery) error {
	if !identity.ValidID(d.ConversationID) || !identity.ValidID(d.DeliveryTargetID) || d.DeliveryTargetID != d.ConversationID || !ValidSlackIMChannel(d.SlackChannel) || !ValidSlackThread(d.SlackThread) {
		return errors.New("invalid delivery identity")
	}
	return nil
}

func (c slack) Deliver(ctx context.Context, delivery *commodel.Delivery) error {
	api := c.d.Slack()
	if api == nil {
		return errors.New("slack delivery is not configured")
	}
	if delivery.Format == FormatMarkdown {
		return c.d.SendParts(delivery, c.d.Render(delivery.Text), func(part string) error {
			return api.PostMessage(ctx, delivery.SlackChannel, delivery.SlackThread, part)
		})
	}
	return api.PostMessage(ctx, delivery.SlackChannel, delivery.SlackThread, delivery.Text)
}

func (c slack) Caps() Caps {
	// Slack replies with an artifact-ready note instead of a binary upload.
	return Caps{Artifacts: true}
}

func (c slack) SendPresence(ctx context.Context, chatID, topicID int64) error {
	return nil
}

func (c slack) DeliverArtifact(ctx context.Context, delivery *commodel.Delivery, ref hubruntime.ArtifactRef) error {
	api := c.d.Slack()
	if api == nil {
		return errors.New("slack delivery is not configured")
	}
	note := "Файл готов: " + ref.Name + " — сохранён в workspace, доставка файлов в Slack пока не поддерживается"
	if ref.Error != "" {
		note = "Файл не удалось доставить: " + ref.Name
	}
	return api.PostMessage(ctx, delivery.SlackChannel, delivery.SlackThread, note)
}

func (c slack) ValidateSchedule(in commodel.Schedule) error {
	if in.SlackChannel != "" && (!ValidSlackIMChannel(in.SlackChannel) || !ValidSlackThread(in.SlackThread)) {
		return errors.New("invalid schedule channel")
	}
	return nil
}

func (c slack) RouteBlockedNotice(item commodel.Schedule, d *commodel.Delivery) {}

// Slack builds its envelope inside the events handler (team/user ids map to
// slack- identities); this path is the telegram-style fallback.
func (c slack) JobEnvelope(user commodel.User, chatID, sender int64) identity.Envelope {
	return user.Envelope(sender)
}

func (c slack) PrepareRoutine(in *commodel.Schedule, caller identity.Envelope) (identity.Envelope, error) {
	return caller, nil
}

func (c slack) AuthorizeOccurrence(job commodel.Job, user commodel.User, configured bool) error {
	if job.Envelope.PrincipalID != user.ID || !strings.HasPrefix(job.Envelope.ExternalIdentityID, "slack-") {
		return errors.New("routine policy changed")
	}
	return nil
}

func (c slack) ValidateOccurrence(o commodel.RoutineOccurrence, caller identity.Envelope) error {
	if !strings.HasPrefix(caller.ExternalIdentityID, "slack-") {
		return errors.New("invalid occurrence ownership")
	}
	return nil
}
