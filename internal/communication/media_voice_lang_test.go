package communication

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/letya999/hermes-hub/internal/locale"
)

// The voice used for synthesis follows the delivery language: a per-language
// map overrides the configured default, unmapped languages fall back to it.
func TestHTTPSynthesizerVoiceForLang(t *testing.T) {
	var gotVoice string
	tts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req map[string]string
		_ = json.NewDecoder(r.Body).Decode(&req)
		gotVoice = req["voice"]
		w.Write([]byte("AUDIO"))
	}))
	defer tts.Close()
	syn := HTTPSynthesizer{Base: tts.URL, Voice: "xenia",
		Voices: map[locale.Lang]string{locale.LangKK: "aidar", locale.LangES: "piper/es"}, Timeout: 5 * time.Second}
	if _, _, err := syn.Synthesize(context.Background(), locale.LangKK, "сәлем"); err != nil || gotVoice != "aidar" {
		t.Fatalf("kk voice: %v %q", err, gotVoice)
	}
	if _, _, err := syn.Synthesize(context.Background(), locale.LangES, "hola"); err != nil || gotVoice != "piper/es" {
		t.Fatalf("es voice: %v %q", err, gotVoice)
	}
	if _, _, err := syn.Synthesize(context.Background(), locale.LangRU, "привет"); err != nil || gotVoice != "xenia" {
		t.Fatalf("ru fallback to default voice: %v %q", err, gotVoice)
	}
	if _, _, err := syn.Synthesize(context.Background(), locale.LangEN, "hi"); err != nil || gotVoice != "xenia" {
		t.Fatalf("en fallback to default voice: %v %q", err, gotVoice)
	}
}

// HUB_TTS_VOICE_{EN,RU,KK,ES} populate the per-language voice map; the plain
// HUB_TTS_VOICE stays the default for unmapped languages.
func TestApplyEnvMediaLangVoices(t *testing.T) {
	t.Setenv("HUB_TTS_VOICE", "xenia")
	t.Setenv("HUB_TTS_VOICE_RU", "eugene")
	t.Setenv("HUB_TTS_VOICE_KK", "aidar")
	var config Config
	fillChannelSecrets(&config)
	if config.TTSVoices[locale.LangRU] != "eugene" || config.TTSVoices[locale.LangKK] != "aidar" {
		t.Fatalf("lang voices: %v", config.TTSVoices)
	}
	if _, ok := config.TTSVoices[locale.LangES]; ok {
		t.Fatalf("unset env produced a voice: %v", config.TTSVoices)
	}
}

// The gateway factory hands the per-language map to the HTTP synthesizer so a
// delivery language selects the voice.
func TestServiceSynthesizerCarriesLangVoices(t *testing.T) {
	voices := map[locale.Lang]string{locale.LangKK: "aidar"}
	syn, ok := serviceSynthesizer("http://tts", "tok", "xenia", voices).(HTTPSynthesizer)
	if !ok {
		t.Fatalf("url did not select http synthesizer: %T", serviceSynthesizer("http://tts", "tok", "xenia", voices))
	}
	if syn.Voices[locale.LangKK] != "aidar" {
		t.Fatalf("lang voices not propagated: %v", syn.Voices)
	}
}
