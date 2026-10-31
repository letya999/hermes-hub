package communication

import (
	"bytes"
	"context"
	"errors"
	"os"
	"path/filepath"
	"strconv"
	"strings"
)

// Mock voice accepts bytes, never a caller-supplied filesystem path or URL.
type mockVoice struct {
	Data     []byte `json:"data"`
	MIME     string `json:"mime"`
	Duration int    `json:"duration"`
}

func voiceInputText(transcript, caption, mime string, duration int) string {
	text := "[voice mime=" + mime + " duration=" + strconv.Itoa(duration) + " transcript=ok]\n" + transcript
	if caption = strings.TrimSpace(caption); caption != "" {
		text = caption + "\n" + text
	}
	return text
}

func (g *Gateway) mockVoiceText(ctx context.Context, voice *mockVoice, caption string) (string, error) {
	media := MediaEnvelope{FileID: "synthetic-mock", MIME: voice.MIME, Size: int64(len(voice.Data)), Duration: voice.Duration}
	if err := rejectMedia(media, g.config.MediaMaxDuration); err != nil {
		return "", err
	}
	if voice.MIME != "audio/ogg" || !bytes.HasPrefix(voice.Data, []byte("OggS")) || voice.Duration < 0 || g.transcriber == nil {
		return "", errors.New("invalid or unavailable mock voice")
	}
	release, err := g.spool.reserveStorage(int64(len(voice.Data)), 1, false)
	if err != nil {
		return "", err
	}
	defer release()
	f, err := os.CreateTemp(filepath.Join(g.spool.root, "tmp"), "mock-voice-*.ogg")
	if err != nil {
		return "", err
	}
	defer g.spool.removeOwned(f.Name())
	_, writeErr := f.Write(voice.Data)
	closeErr := f.Close()
	if writeErr != nil || closeErr != nil {
		return "", errors.New("mock voice staging failed")
	}
	transcript, err := g.transcribe(ctx, f.Name(), voice.MIME)
	if err != nil || strings.TrimSpace(transcript) == "" {
		return "", errors.New("mock voice transcription failed")
	}
	return voiceInputText(transcript, caption, voice.MIME, voice.Duration), nil
}
