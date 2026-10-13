package communication

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"testing"
)

func TestVoiceReceiptDistinguishesRealSendFromFallback(t *testing.T) {
	for _, fail := range []bool{false, true} {
		c := testConfig(t)
		g, err := New(c)
		if err != nil {
			t.Fatal(err)
		}
		api := &fakeAPI{}
		g.api = api
		tts := stubTTS{audio: []byte("synthetic ogg")}
		if fail {
			tts.err = errors.New("synthesis failed")
		}
		g.synthesizer = tts
		d := Delivery{ID: "e2e-voice", Channel: "telegram_bot", ChatID: 11, Voice: "synthetic spoken marker"}
		if err := g.spool.EnqueueDelivery(d); err != nil {
			t.Fatal(err)
		}
		g.deliverOne(context.Background())
		data, err := os.ReadFile(filepath.Join(g.spool.root, "outbox", "done", spoolFileID(d.ID)+".json"))
		if err != nil {
			t.Fatal(err)
		}
		var saved Delivery
		if err := json.Unmarshal(data, &saved); err != nil {
			t.Fatal(err)
		}
		if saved.VoiceSent == fail {
			t.Fatalf("fallback=%t receipt voice_sent=%t", fail, saved.VoiceSent)
		}
		if fail && (len(api.voices) != 0 || len(api.sent) != 1) {
			t.Fatal("failed TTS has wrong fallback", api)
		}
		if !fail && (len(api.voices) != 1 || len(api.sent) != 0) {
			t.Fatal("voice-only sent text", api)
		}
	}
}

type uncertainVoiceAPI struct{ *fakeAPI }

func (a uncertainVoiceAPI) SendVoice(ctx context.Context, chatID, topicID int64, audio []byte, caption string) error {
	_ = a.fakeAPI.SendVoice(ctx, chatID, topicID, audio, caption)
	return errors.New("synthetic timeout after acceptance")
}

func TestUncertainVoiceSendDoesNotFallbackOrRepeat(t *testing.T) {
	for _, text := range []string{"", "intro"} {
		g, err := New(testConfig(t))
		if err != nil {
			t.Fatal(err)
		}
		api := &fakeAPI{}
		g.api = uncertainVoiceAPI{api}
		g.synthesizer = stubTTS{audio: []byte("synthetic ogg")}
		d := Delivery{ID: "uncertain-voice", Channel: "telegram_bot", ChatID: 11, Text: text, Format: formatMarkdown, Voice: "synthetic speech"}
		if err := g.spool.EnqueueDelivery(d); err != nil {
			t.Fatal(err)
		}
		g.deliverOne(context.Background())
		g.deliverOne(context.Background())
		wantTexts := 0
		if text != "" {
			wantTexts = 1
		}
		if len(api.voices) != 1 || len(api.sent) != wantTexts {
			t.Fatal("unknown voice send caused fallback or repeated action", api)
		}
		data, err := os.ReadFile(filepath.Join(g.spool.root, "outbox", "failed", d.ID+".json"))
		if err != nil {
			t.Fatal(err)
		}
		var receipt Delivery
		if err := json.Unmarshal(data, &receipt); err != nil {
			t.Fatal(err)
		}
		if receipt.VoiceSent || receipt.SentParts != wantTexts {
			t.Fatal("uncertain receipt falsely proves delivery", receipt)
		}
		if _, err := os.Stat(filepath.Join(g.spool.root, "outbox", "done", d.ID+".json")); !os.IsNotExist(err) {
			t.Fatal("unknown outcome marked complete", err)
		}
	}
}
