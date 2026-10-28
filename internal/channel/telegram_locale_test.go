package channel

import (
	"context"
	"testing"

	"github.com/letya999/hermes-hub/internal/commodel"
	"github.com/letya999/hermes-hub/internal/locale"
	hubruntime "github.com/letya999/hermes-hub/internal/runtime"
)

type captureTelegramSender struct{ texts []string }

func (s *captureTelegramSender) EditMessageText(ctx context.Context, chatID int64, messageID int, text string) error {
	return nil
}
func (s *captureTelegramSender) SendMessage(ctx context.Context, chatID, topicID int64, text, parseMode string) error {
	s.texts = append(s.texts, text)
	return nil
}
func (s *captureTelegramSender) SendKeyboard(ctx context.Context, chatID, topicID int64, text string, buttons []commodel.DeliveryButton) error {
	return nil
}
func (s *captureTelegramSender) SendChatAction(ctx context.Context, chatID, topicID int64, action string) error {
	return nil
}
func (s *captureTelegramSender) SendDocument(ctx context.Context, chatID, topicID int64, name, caption string, data []byte) error {
	return nil
}
func (s *captureTelegramSender) SendPhoto(ctx context.Context, chatID, topicID int64, name, caption string, data []byte) error {
	return nil
}
func (s *captureTelegramSender) SendVideo(ctx context.Context, chatID, topicID int64, name, caption string, data []byte) error {
	return nil
}

// DeliverArtifact reports a failed artifact in the delivery's stamped
// language — Telegram shares the channel-neutral catalog key with the
// claim-time resolver as source of truth.
func TestTelegramArtifactFailedLocalized(t *testing.T) {
	sender := &captureTelegramSender{}
	c := NewTelegram(TelegramDeps{API: func() TelegramSender { return sender }})
	d := commodel.Delivery{Lang: locale.LangES, ChatID: 11}
	if err := c.DeliverArtifact(context.Background(), &d, hubruntime.ArtifactRef{Name: "report.pdf", Error: "too large"}); err != nil {
		t.Fatalf("DeliverArtifact: %v", err)
	}
	want := locale.T(locale.LangES, "artifact.failed", "report.pdf")
	if len(sender.texts) != 1 || sender.texts[0] != want {
		t.Fatalf("sent %v, want %q", sender.texts, want)
	}
}
