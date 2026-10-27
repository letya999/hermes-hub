package channel

import (
	"context"
	"errors"
	"slices"
	"strconv"
	"strings"

	"github.com/letya999/hermes-hub/internal/commodel"
	"github.com/letya999/hermes-hub/internal/identity"
	hubruntime "github.com/letya999/hermes-hub/internal/runtime"
)

// LimitTelegramText bounds one Telegram message to the provider's 4000-rune
// limit; longer payloads split upstream in the render pipeline.
func LimitTelegramText(text string) string {
	runes := []rune(text)
	if len(runes) <= 4000 {
		return text
	}
	return string(runes[:4000])
}

// TelegramSender is the subset of the Bot API the adapter's send paths use.
// The gateway's full client satisfies it structurally.
type TelegramSender interface {
	EditMessageText(ctx context.Context, chatID int64, messageID int, text string) error
	SendMessage(ctx context.Context, chatID, topicID int64, text, parseMode string) error
	SendKeyboard(ctx context.Context, chatID, topicID int64, text string, buttons []commodel.DeliveryButton) error
	SendChatAction(ctx context.Context, chatID, topicID int64, action string) error
	SendDocument(ctx context.Context, chatID, topicID int64, name, caption string, data []byte) error
	SendPhoto(ctx context.Context, chatID, topicID int64, name, caption string, data []byte) error
	SendVideo(ctx context.Context, chatID, topicID int64, name, caption string, data []byte) error
}

// Optional receipt-capturing senders: the gateway's real client implements
// these; the adapter type-asserts so test doubles stay small.
type telegramKeyboardReceipt interface {
	SendKeyboardReceipt(context.Context, int64, int64, string, []commodel.DeliveryButton) (int, error)
}

type telegramMessageReceipt interface {
	SendMessageReceipt(context.Context, int64, int64, string, string) (int, error)
}

// TelegramDeps declares every gateway capability the telegram adapter
// borrows. Values evaluate at call time: the registry may construct an
// adapter against a nil gateway for the pure validation methods.
type TelegramDeps struct {
	API func() TelegramSender
	// SendEdit runs the throttled status-edit path (gateway semantics: the
	// throttle and requeue live there); ErrDeliveryRetried means requeued.
	SendEdit            func(ctx context.Context, d *commodel.Delivery) error
	RetryDelivery       func(id string) error
	UpdateSending       func(d commodel.Delivery) error
	TrackApproval       func(d commodel.Delivery) error
	ReadDeliveryBlob    func(deliveryID, blob string) ([]byte, error)
	VoicePlan           func(d commodel.Delivery) (speak string, voiceOnly bool)
	SendVoice           func(ctx context.Context, d commodel.Delivery, text string) error
	SendMarkdown        func(ctx context.Context, d *commodel.Delivery) error
	UserByChat          func(chatID int64) (commodel.User, bool)
	UserByPrincipal     func(id string) commodel.User
	OwnsRoutineEnvelope func(job commodel.Job, canonical identity.Envelope) bool
}

// telegram is the registered adapter for channel "telegram_bot".
type telegram struct {
	d TelegramDeps
}

func NewTelegram(deps TelegramDeps) Channel { return telegram{d: deps} }

func (c telegram) ID() string { return "telegram_bot" }

func (c telegram) NormalizeDelivery(d *commodel.Delivery) error {
	transportID := "telegram-" + strconv.FormatInt(d.ChatID, 10)
	if d.ConversationID == "" {
		d.ConversationID = transportID
	}
	if d.DeliveryTargetID == "" {
		d.DeliveryTargetID = transportID
	}
	// Group forum chats carry negative ids; zero stays invalid.
	if d.ChatID == 0 || !identity.ValidID(d.ConversationID) || d.DeliveryTargetID != transportID {
		return errors.New("invalid delivery identity")
	}
	return nil
}

func (c telegram) Deliver(ctx context.Context, delivery *commodel.Delivery) error {
	deps := c.d
	if delivery.EditMessageID > 0 {
		return deps.SendEdit(ctx, delivery)
	}
	speak, voiceOnly := deps.VoicePlan(*delivery)
	if strings.TrimSpace(delivery.Text) == "" {
		// A VOICE:-only reply keeps its spoken payload as the text
		// fallback so an unavailable synthesizer never swallows it.
		delivery.Text = speak
	}
	voiceSent := false
	var sendErr error
	if voiceOnly && speak != "" {
		voiceErr := deps.SendVoice(ctx, *delivery, speak)
		voiceSent = voiceErr == nil
		delivery.VoiceSent = voiceSent
		if errors.Is(voiceErr, ErrUncertain) {
			sendErr = voiceErr
		}
	}
	if !voiceSent && sendErr == nil {
		if delivery.Format == FormatMarkdown {
			sendErr = deps.SendMarkdown(ctx, delivery)
		} else if len(delivery.Buttons) > 0 {
			if receiptAPI, ok := deps.API().(telegramKeyboardReceipt); ok {
				delivery.TelegramMessageID, sendErr = receiptAPI.SendKeyboardReceipt(ctx, delivery.ChatID, delivery.TopicID, LimitTelegramText(delivery.Text), delivery.Buttons)
				if sendErr == nil && delivery.ApprovalJobID != "" {
					sendErr = deps.TrackApproval(*delivery)
				}
			} else {
				sendErr = deps.API().SendKeyboard(ctx, delivery.ChatID, delivery.TopicID, LimitTelegramText(delivery.Text), delivery.Buttons)
			}
		} else {
			// Capturing the send receipt is what lets later status events
			// edit this message in place instead of stacking new ones.
			if receiptAPI, ok := deps.API().(telegramMessageReceipt); ok {
				delivery.TelegramMessageID, sendErr = receiptAPI.SendMessageReceipt(ctx, delivery.ChatID, delivery.TopicID, LimitTelegramText(delivery.Text), "")
				if sendErr == nil && delivery.TelegramMessageID > 0 {
					sendErr = deps.UpdateSending(*delivery)
				}
			} else {
				sendErr = deps.API().SendMessage(ctx, delivery.ChatID, delivery.TopicID, LimitTelegramText(delivery.Text), "")
			}
		}
		if sendErr == nil && speak != "" && !voiceOnly {
			voiceErr := deps.SendVoice(ctx, *delivery, speak)
			delivery.VoiceSent = voiceErr == nil
			if errors.Is(voiceErr, ErrUncertain) {
				sendErr = voiceErr
			}
		}
	}
	if speak != "" {
		if err := deps.UpdateSending(*delivery); err != nil {
			sendErr = err
		}
	}
	return sendErr
}

func (c telegram) AuthorizeOccurrence(job commodel.Job, user commodel.User, configured bool) error {
	mapped, ok := c.d.UserByChat(job.ChatID)
	if !ok || mapped.ID != user.ID || (configured && !slices.Contains(user.TelegramIDs, job.ChatID)) || !c.d.OwnsRoutineEnvelope(job, user.Envelope(job.ChatID)) {
		return errors.New("routine policy changed")
	}
	return nil
}

func (c telegram) ValidateSchedule(in commodel.Schedule) error { return nil }

func (c telegram) JobEnvelope(user commodel.User, chatID, sender int64) identity.Envelope {
	envelope := user.Envelope(sender)
	if chatID < 0 {
		// Group forum jobs deliver to the group chat, not the sender's DM.
		envelope.DeliveryTargetID = "telegram-" + strconv.FormatInt(chatID, 10)
	}
	return envelope
}

func (c telegram) RouteBlockedNotice(item commodel.Schedule, d *commodel.Delivery) {
	// The notice goes to the owner's CURRENT verified binding — the durable
	// envelope target may be stale (the chat remapped to another principal
	// since enrollment), and a stale binding is exactly what may have caused
	// the block.
	owner := c.d.UserByPrincipal(item.Envelope.PrincipalID)
	if len(owner.TelegramIDs) == 0 {
		// No verified home: clear the address so normalize refuses the
		// notice instead of rerouting it to the stale chat.
		d.ConversationID, d.DeliveryTargetID, d.ChatID = "", "", 0
		return
	}
	target := "telegram-" + strconv.FormatInt(owner.TelegramIDs[0], 10)
	d.ConversationID, d.DeliveryTargetID = target, target
	d.TaskID, d.TopicID = "", 0
	d.ChatID = owner.TelegramIDs[0]
}

func (c telegram) PrepareRoutine(in *commodel.Schedule, caller identity.Envelope) (identity.Envelope, error) {
	if in.ChatID == 0 {
		if ids := c.d.UserByPrincipal(caller.PrincipalID).TelegramIDs; len(ids) > 0 {
			in.ChatID = ids[0]
		}
	}
	return caller, nil
}

func (c telegram) Caps() Caps {
	return Caps{Typing: true, EditableStatus: true, Artifacts: true, Destinations: true, RoutineOrigin: true, Actions: true}
}

func (c telegram) SendPresence(ctx context.Context, chatID, topicID int64) error {
	return c.d.API().SendChatAction(ctx, chatID, topicID, "typing")
}

func (c telegram) DeliverArtifact(ctx context.Context, delivery *commodel.Delivery, ref hubruntime.ArtifactRef) error {
	deps := c.d
	if ref.Error != "" {
		return deps.API().SendMessage(ctx, delivery.ChatID, delivery.TopicID, LimitTelegramText("Файл не удалось доставить: "+ref.Name), "")
	}
	data, err := deps.ReadDeliveryBlob(delivery.ID, ref.Blob)
	if err != nil {
		return err
	}
	if ref.Mime == "image/png" || ref.Mime == "image/jpeg" {
		if err := deps.API().SendPhoto(ctx, delivery.ChatID, delivery.TopicID, ref.Name, "", data); err == nil {
			return nil
		}
	}
	if strings.HasPrefix(ref.Mime, "video/") {
		if err := deps.API().SendVideo(ctx, delivery.ChatID, delivery.TopicID, ref.Name, "", data); err == nil {
			return nil
		}
	}
	return deps.API().SendDocument(ctx, delivery.ChatID, delivery.TopicID, ref.Name, "", data)
}

func (c telegram) ValidateOccurrence(o commodel.RoutineOccurrence, caller identity.Envelope) error {
	if o.Job.ChatID <= 0 || !caller.Equal(identity.TelegramEnvelope(o.Job.UserID, o.Job.ChatID, o.Job.RuntimeID, o.Job.PolicyVersion)) {
		return errors.New("invalid occurrence ownership")
	}
	return nil
}
