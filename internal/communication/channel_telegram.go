package communication

import (
	"context"
	"errors"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/letya999/hermes-hub/internal/identity"
	hubruntime "github.com/letya999/hermes-hub/internal/runtime"
)

// telegramChannel is the registered adapter for channel "telegram_bot".
type telegramChannel struct {
	g *Gateway
}

func (c telegramChannel) ID() string { return "telegram_bot" }

func (c telegramChannel) NormalizeDelivery(d *Delivery) error {
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

func (c telegramChannel) Deliver(ctx context.Context, delivery *Delivery) error {
	g := c.g
	if delivery.EditMessageID > 0 {
		// Status update: edit the delivered status message in place
		// (SPEC-0053 R2.2), no more than once per editInterval per chat.
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
	speak, voiceOnly := g.voicePlan(*delivery)
	if strings.TrimSpace(delivery.Text) == "" {
		// A VOICE:-only reply keeps its spoken payload as the text
		// fallback so an unavailable synthesizer never swallows it.
		delivery.Text = speak
	}
	voiceSent := false
	var sendErr error
	if voiceOnly && speak != "" {
		voiceErr := g.sendVoiceReply(ctx, *delivery, speak)
		voiceSent = voiceErr == nil
		delivery.VoiceSent = voiceSent
		if errors.Is(voiceErr, ErrUncertain) {
			sendErr = voiceErr
		}
	}
	if !voiceSent && sendErr == nil {
		if delivery.Format == formatMarkdown {
			sendErr = g.sendTelegramMarkdown(ctx, delivery)
		} else if len(delivery.Buttons) > 0 {
			if receiptAPI, ok := g.api.(interface {
				SendKeyboardReceipt(context.Context, int64, int64, string, []DeliveryButton) (int, error)
			}); ok {
				delivery.TelegramMessageID, sendErr = receiptAPI.SendKeyboardReceipt(ctx, delivery.ChatID, delivery.TopicID, limitTelegramText(delivery.Text), delivery.Buttons)
				if sendErr == nil && delivery.ApprovalJobID != "" {
					sendErr = g.spool.trackApprovalMessage(*delivery)
				}
			} else {
				sendErr = g.api.SendKeyboard(ctx, delivery.ChatID, delivery.TopicID, limitTelegramText(delivery.Text), delivery.Buttons)
			}
		} else {
			// Capturing the send receipt is what lets later status events
			// edit this message in place instead of stacking new ones.
			if receiptAPI, ok := g.api.(interface {
				SendMessageReceipt(context.Context, int64, int64, string, string) (int, error)
			}); ok {
				delivery.TelegramMessageID, sendErr = receiptAPI.SendMessageReceipt(ctx, delivery.ChatID, delivery.TopicID, limitTelegramText(delivery.Text), "")
				if sendErr == nil && delivery.TelegramMessageID > 0 {
					sendErr = g.spool.UpdateSending(*delivery)
				}
			} else {
				sendErr = g.api.SendMessage(ctx, delivery.ChatID, delivery.TopicID, limitTelegramText(delivery.Text), "")
			}
		}
		if sendErr == nil && speak != "" && !voiceOnly {
			voiceErr := g.sendVoiceReply(ctx, *delivery, speak)
			delivery.VoiceSent = voiceErr == nil
			if errors.Is(voiceErr, ErrUncertain) {
				sendErr = voiceErr
			}
		}
	}
	if speak != "" {
		if err := g.spool.UpdateSending(*delivery); err != nil {
			sendErr = err
		}
	}
	return sendErr
}

func (c telegramChannel) AuthorizeOccurrence(job Job, user User, configured bool) error {
	g := c.g
	mapped, ok := g.users[job.ChatID]
	if !ok || mapped.ID != user.ID || (configured && !slices.Contains(user.TelegramIDs, job.ChatID)) || !g.ownsRoutineEnvelope(job, user.envelope(job.ChatID)) {
		return errors.New("routine policy changed")
	}
	return nil
}

func (c telegramChannel) ValidateSchedule(in Schedule) error { return nil }

func (c telegramChannel) JobEnvelope(user User, chatID, sender int64) identity.Envelope {
	envelope := user.envelope(sender)
	if chatID < 0 {
		// Group forum jobs deliver to the group chat, not the sender's DM.
		envelope.DeliveryTargetID = "telegram-" + strconv.FormatInt(chatID, 10)
	}
	return envelope
}

func (c telegramChannel) RouteBlockedNotice(item Schedule, d *Delivery) {
	target := item.Envelope.DeliveryTargetID
	d.ConversationID, d.DeliveryTargetID = target, target
	d.TaskID, d.TopicID = "", 0
	d.ChatID, _ = strconv.ParseInt(strings.TrimPrefix(target, "telegram-"), 10, 64)
}

func (c telegramChannel) PrepareRoutine(in *Schedule, caller identity.Envelope) (identity.Envelope, error) {
	if in.ChatID == 0 {
		if ids := c.g.user(caller.PrincipalID).TelegramIDs; len(ids) > 0 {
			in.ChatID = ids[0]
		}
	}
	return caller, nil
}

func (c telegramChannel) Caps() ChannelCaps {
	return ChannelCaps{Typing: true, EditableStatus: true, Artifacts: true, Destinations: true, RoutineOrigin: true}
}

func (c telegramChannel) SendPresence(ctx context.Context, chatID, topicID int64) error {
	return c.g.api.SendChatAction(ctx, chatID, topicID, "typing")
}

func (c telegramChannel) DeliverArtifact(ctx context.Context, delivery *Delivery, ref hubruntime.ArtifactRef) error {
	g := c.g
	if ref.Error != "" {
		return g.api.SendMessage(ctx, delivery.ChatID, delivery.TopicID, limitTelegramText("Файл не удалось доставить: "+ref.Name), "")
	}
	data, err := g.spool.ReadDeliveryBlob(delivery.ID, ref.Blob)
	if err != nil {
		return err
	}
	if ref.Mime == "image/png" || ref.Mime == "image/jpeg" {
		if err := g.api.SendPhoto(ctx, delivery.ChatID, delivery.TopicID, ref.Name, "", data); err == nil {
			return nil
		}
	}
	if strings.HasPrefix(ref.Mime, "video/") {
		if err := g.api.SendVideo(ctx, delivery.ChatID, delivery.TopicID, ref.Name, "", data); err == nil {
			return nil
		}
	}
	return g.api.SendDocument(ctx, delivery.ChatID, delivery.TopicID, ref.Name, "", data)
}

func (c telegramChannel) ValidateOccurrence(o RoutineOccurrence, caller identity.Envelope) error {
	if o.Job.ChatID <= 0 || caller != identity.TelegramEnvelope(o.Job.UserID, o.Job.ChatID, o.Job.RuntimeID, o.Job.PolicyVersion) {
		return errors.New("invalid occurrence ownership")
	}
	return nil
}
