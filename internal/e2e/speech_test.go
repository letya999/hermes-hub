package e2e

import (
	"context"
	"errors"
	"github.com/letya999/hermes-hub/internal/locale"
	"os"
	"testing"
)

type syntheticSpeech struct{ fail bool }

func (s syntheticSpeech) Synthesize(context.Context, locale.Lang, string) ([]byte, string, error) {
	if s.fail {
		return nil, "", errors.New("synthetic failure")
	}
	return []byte("synthetic ogg"), "audio/ogg", nil
}

type syntheticTranscript struct {
	text string
	fail bool
	path string
}

func (s *syntheticTranscript) Transcribe(_ context.Context, path, mime string) (string, error) {
	s.path = path
	if _, err := os.ReadFile(path); err != nil {
		return "", err
	}
	if mime != "audio/ogg" {
		return "", errors.New("wrong mime")
	}
	if s.fail {
		return "", errors.New("synthetic failure")
	}
	return s.text, nil
}
func TestSpeechSuccessRemainsPartialAndAudioIsRemoved(t *testing.T) {
	for _, text := range []string{"Тест номер сорок два.", "Тест номер 42.", "unrelated", "Тест номер 142."} {
		stt := &syntheticTranscript{text: text}
		status, _ := speechCase(context.Background(), syntheticSpeech{}, stt)
		want := "PARTIAL_PASS"
		if text == "unrelated" || text == "Тест номер 142." {
			want = "FAIL"
		}
		if status != want {
			t.Fatal(status, want)
		}
		if _, err := os.Stat(stt.path); !os.IsNotExist(err) {
			t.Fatal("synthetic audio retained", err)
		}
		if Complete(Report{Results: []Result{{Status: status}}}) {
			t.Fatal("partial boundary promoted to full acceptance")
		}
	}
	if status, _ := speechCase(context.Background(), syntheticSpeech{fail: true}, &syntheticTranscript{}); status != "FAIL" {
		t.Fatal("TTS failure passed")
	}
	if status, _ := speechCase(context.Background(), syntheticSpeech{}, &syntheticTranscript{fail: true}); status != "FAIL" {
		t.Fatal("STT failure passed")
	}
}
