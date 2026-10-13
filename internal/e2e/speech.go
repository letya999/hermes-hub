package e2e

import (
	"context"
	"os"
	"regexp"
	"strings"

	"github.com/letya999/hermes-hub/internal/communication"
)

// Speech checks the real adapters separately from Telegram update reception.
func speechCase(ctx context.Context, tts communication.Synthesizer, stt communication.Transcriber) (string, string) {
	audio, mime, err := tts.Synthesize(ctx, "Тест номер сорок два")
	if err != nil || len(audio) == 0 {
		return "FAIL", "speech synthesis failed"
	}
	f, err := os.CreateTemp("", "hermes-e2e-voice-*.ogg")
	if err != nil {
		return "FAIL", "synthetic audio staging failed"
	}
	defer os.Remove(f.Name())
	_, err = f.Write(audio)
	closeErr := f.Close()
	if err != nil || closeErr != nil {
		return "FAIL", "synthetic audio staging failed"
	}
	text, err := stt.Transcribe(ctx, f.Name(), mime)
	if err != nil {
		return "FAIL", "real speech transcription failed"
	}
	text = strings.ToLower(strings.Join(strings.Fields(text), " "))
	if !regexp.MustCompile(`(?:^|[^\p{L}\p{N}])(?:42|сорок два)(?:$|[^\p{L}\p{N}])`).MatchString(text) {
		return "FAIL", "transcription did not retain the expected number"
	}
	return "PARTIAL_PASS", "real TTS to STT passed; incoming Telegram update/polling not exercised"
}
