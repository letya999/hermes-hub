package mediasvc

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"mime/multipart"
	"net/http"
	"os"
	"os/exec"
	"strings"
	"time"
)

// Segment is one recognized span. Speaker is set only when diarization ran.
type Segment struct {
	Start   float64 `json:"start"`
	End     float64 `json:"end"`
	Text    string  `json:"text"`
	Speaker string  `json:"speaker,omitempty"`
}

// TranscribeOpts carries the request knobs a backend may honor.
type TranscribeOpts struct {
	Language string
	Model    string
	Diarize  bool
}

// STTEngine transcribes a 16 kHz mono wav file into segments.
type STTEngine interface {
	Transcribe(ctx context.Context, wav string, opts TranscribeOpts) ([]Segment, error)
	Ready(ctx context.Context) bool
}

// TTSEngine synthesizes text into wav bytes.
type TTSEngine interface {
	Synthesize(ctx context.Context, text, voice string) ([]byte, error)
	Ready(ctx context.Context) bool
}

func sttEngine(c Config) (STTEngine, error) {
	switch c.Engine {
	case "remote":
		return remoteSTT{base: c.Upstream, key: c.UpstreamKey, client: &http.Client{Timeout: 10 * time.Minute}}, nil
	case "command":
		return commandSTT{command: c.Command}, nil
	case "sherpa":
		return newSherpaSTT(c)
	}
	return nil, fmt.Errorf("unknown engine %q", c.Engine)
}

func ttsEngine(c Config) (TTSEngine, error) {
	switch c.Engine {
	case "remote":
		return remoteTTS{base: c.Upstream, key: c.UpstreamKey, voice: c.Voice, client: &http.Client{Timeout: 2 * time.Minute}}, nil
	case "elevenlabs":
		base := c.Upstream
		if base == "" {
			base = "https://api.elevenlabs.io"
		}
		return elevenlabsTTS{base: base, key: c.UpstreamKey, voice: c.Voice, model: c.TTSModel, client: &http.Client{Timeout: 2 * time.Minute}}, nil
	case "command":
		return commandTTS{command: c.Command}, nil
	case "sherpa":
		return newSherpaTTS(c)
	}
	return nil, fmt.Errorf("unknown engine %q", c.Engine)
}

// ---- command engine: keeps the CommandTranscriber/CommandSynthesizer
// contract so an existing hub-stt/hub-tts binary stays a valid backend.

type commandSTT struct{ command string }

func (e commandSTT) Transcribe(ctx context.Context, wav string, _ TranscribeOpts) ([]Segment, error) {
	out, err := exec.CommandContext(ctx, e.command, wav, "audio/wav").Output()
	if err != nil {
		return nil, errors.New("stt engine failed")
	}
	text := strings.TrimSpace(string(out))
	if text == "" {
		return nil, errors.New("empty transcript")
	}
	return []Segment{{Text: text}}, nil
}

func (e commandSTT) Ready(context.Context) bool { return true }

type commandTTS struct{ command string }

func (e commandTTS) Synthesize(ctx context.Context, text, voice string) ([]byte, error) {
	cmd := exec.CommandContext(ctx, e.command)
	// The requested voice rides in env, not argv: keeps the worker argv
	// contract untouched and lets multi-voice workers (tts-worker) map it
	// onto backends (silero/piper) or speakers.
	if strings.TrimSpace(voice) != "" {
		cmd.Env = append(os.Environ(), "HUB_TTS_REQUEST_VOICE="+voice)
	}
	cmd.Stdin = strings.NewReader(text)
	out, err := cmd.Output()
	if err != nil || len(out) == 0 {
		return nil, errors.New("tts engine failed")
	}
	return out, nil
}

func (e commandTTS) Ready(context.Context) bool { return true }

// ---- remote engine: forwards to any OpenAI-compatible audio server
// (speaches, Groq, a bespoke model service). This is the "bring your own
// model" adapter — swap upstreams without touching this service's queue,
// auth, fetch or diarization-merge code.

type remoteSTT struct {
	base   string
	key    string
	client *http.Client
}

func (e remoteSTT) Transcribe(ctx context.Context, wav string, opts TranscribeOpts) ([]Segment, error) {
	var body bytes.Buffer
	form := multipart.NewWriter(&body)
	part, err := form.CreateFormFile("file", "audio.wav")
	if err != nil {
		return nil, err
	}
	in, err := os.Open(wav)
	if err != nil {
		return nil, err
	}
	defer in.Close()
	if _, err := io.Copy(part, in); err != nil {
		return nil, err
	}
	_ = form.WriteField("response_format", "verbose_json")
	_ = form.WriteField("timestamp_granularities[]", "segment")
	if opts.Model != "" {
		_ = form.WriteField("model", opts.Model)
	}
	if opts.Language != "" {
		_ = form.WriteField("language", opts.Language)
	}
	if opts.Diarize {
		// whisperx-style upstreams honor this; OpenAI/Groq/Speaches ignore it.
		_ = form.WriteField("diarize", "true")
	}
	if err := form.Close(); err != nil {
		return nil, err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, apiURL(e.base, "/audio/transcriptions"), &body)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", form.FormDataContentType())
	if e.key != "" {
		req.Header.Set("Authorization", "Bearer "+e.key)
	}
	resp, err := e.client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	payload, err := io.ReadAll(io.LimitReader(resp.Body, 64<<20))
	if err != nil {
		return nil, err
	}
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("upstream stt %d", resp.StatusCode)
	}
	var parsed struct {
		Text     string `json:"text"`
		Segments []struct {
			Start float64 `json:"start"`
			End   float64 `json:"end"`
			Text  string  `json:"text"`
		} `json:"segments"`
	}
	if err := json.Unmarshal(payload, &parsed); err != nil {
		return nil, err
	}
	if len(parsed.Segments) == 0 && strings.TrimSpace(parsed.Text) == "" {
		return nil, errors.New("empty transcript")
	}
	segments := make([]Segment, 0, len(parsed.Segments))
	for _, s := range parsed.Segments {
		segments = append(segments, Segment{Start: s.Start, End: s.End, Text: strings.TrimSpace(s.Text)})
	}
	if len(segments) == 0 {
		segments = append(segments, Segment{Text: strings.TrimSpace(parsed.Text)})
	}
	return segments, nil
}

func (e remoteSTT) Ready(ctx context.Context) bool {
	return probe(ctx, e.client, apiURL(e.base, "/models"), e.key)
}

type remoteTTS struct {
	base   string
	key    string
	voice  string
	client *http.Client
}

func (e remoteTTS) Synthesize(ctx context.Context, text, voice string) ([]byte, error) {
	if voice == "" {
		voice = e.voice
	}
	payload, err := json.Marshal(map[string]any{"model": "tts-1", "voice": voice, "input": text, "response_format": "wav"})
	if err != nil {
		return nil, err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, apiURL(e.base, "/audio/speech"), bytes.NewReader(payload))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	if e.key != "" {
		req.Header.Set("Authorization", "Bearer "+e.key)
	}
	resp, err := e.client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	out, err := io.ReadAll(io.LimitReader(resp.Body, 64<<20))
	if err != nil {
		return nil, err
	}
	if resp.StatusCode != http.StatusOK || len(out) == 0 {
		return nil, fmt.Errorf("upstream tts %d", resp.StatusCode)
	}
	return out, nil
}

func (e remoteTTS) Ready(ctx context.Context) bool {
	return probe(ctx, e.client, apiURL(e.base, "/models"), e.key)
}

// ---- ElevenLabs engine: native REST API, not OpenAI-compatible.
// HUB_MEDIA_UPSTREAM_KEY carries xi-api-key, HUB_TTS_VOICE the voice_id,
// HUB_TTS_MODEL the model_id (eleven_multilingual_v2 handles Russian).

type elevenlabsTTS struct {
	base   string
	key    string
	voice  string
	model  string
	client *http.Client
}

func (e elevenlabsTTS) Synthesize(ctx context.Context, text, voice string) ([]byte, error) {
	if voice == "" {
		voice = e.voice
	}
	if voice == "" {
		return nil, errors.New("elevenlabs voice id is not configured")
	}
	model := e.model
	if model == "" {
		model = "eleven_multilingual_v2"
	}
	payload, err := json.Marshal(map[string]string{"text": text, "model_id": model})
	if err != nil {
		return nil, err
	}
	url := strings.TrimRight(e.base, "/") + "/v1/text-to-speech/" + voice + "?output_format=mp3_44100_64"
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(payload))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("xi-api-key", e.key)
	resp, err := e.client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	out, err := io.ReadAll(io.LimitReader(resp.Body, 64<<20))
	if err != nil {
		return nil, err
	}
	if resp.StatusCode != http.StatusOK || len(out) == 0 {
		return nil, fmt.Errorf("elevenlabs tts %d", resp.StatusCode)
	}
	return out, nil
}

func (e elevenlabsTTS) Ready(ctx context.Context) bool {
	// /v1/user is the authenticated self-check; no audio is generated.
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, strings.TrimRight(e.base, "/")+"/v1/user", nil)
	if err != nil {
		return false
	}
	req.Header.Set("xi-api-key", e.key)
	client := &http.Client{Timeout: 3 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		return false
	}
	defer resp.Body.Close()
	return resp.StatusCode == http.StatusOK
}

// apiURL joins a configured upstream (bare host or a base already ending in
// /v1, e.g. https://api.groq.com/openai/v1) with an OpenAI-style path.
func apiURL(base, tail string) string {
	if strings.HasSuffix(base, "/v1") {
		return base + tail
	}
	return base + "/v1" + tail
}

func probe(ctx context.Context, client *http.Client, url, key string) bool {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return false
	}
	if key != "" {
		req.Header.Set("Authorization", "Bearer "+key)
	}
	probeClient := &http.Client{Timeout: 3 * time.Second}
	if client != nil {
		probeClient.Transport = client.Transport
	}
	resp, err := probeClient.Do(req)
	if err != nil {
		return false
	}
	defer resp.Body.Close()
	return resp.StatusCode == http.StatusOK
}
