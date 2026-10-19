package mediasvc

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// The remote OpenAI-compatible engines forward multipart/JSON bodies with
// the bearer key, parse transcript segments (or fall back to flat text) and
// surface upstream errors.
func TestRemoteEngines(t *testing.T) {
	var sawAuth, sawModel, sawDiarize string
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		sawAuth = r.Header.Get("Authorization")
		switch {
		case strings.HasSuffix(r.URL.Path, "/audio/transcriptions"):
			_ = r.ParseMultipartForm(1 << 20)
			sawModel = r.MultipartForm.Value["model"][0]
			sawDiarize = r.MultipartForm.Value["diarize"][0]
			w.Write([]byte(`{"text":"","segments":[{"start":0.1,"end":1.5,"text":" hi "}]}`))
		case strings.HasSuffix(r.URL.Path, "/audio/speech"):
			w.Write([]byte("WAVBYTES"))
		case strings.HasSuffix(r.URL.Path, "/models"):
			w.Write([]byte(`{"data":[]}`))
		default:
			http.NotFound(w, r)
		}
	}))
	defer up.Close()

	wav := filepath.Join(t.TempDir(), "a.wav")
	if err := os.WriteFile(wav, []byte("RIFF....WAVE"), 0600); err != nil {
		t.Fatal(err)
	}
	stt := remoteSTT{base: up.URL, key: "k", client: up.Client()}
	segs, err := stt.Transcribe(context.Background(), wav, TranscribeOpts{Model: "whisper-1", Diarize: true})
	if err != nil || len(segs) != 1 || segs[0].Text != "hi" || segs[0].End != 1.5 {
		t.Fatalf("transcribe: %+v %v", segs, err)
	}
	if sawAuth != "Bearer k" || sawModel != "whisper-1" || sawDiarize != "true" {
		t.Fatalf("request fields: %q %q %q", sawAuth, sawModel, sawDiarize)
	}
	if !stt.Ready(context.Background()) {
		t.Fatal("ready probe failed")
	}
	if _, err := stt.Transcribe(context.Background(), filepath.Join(t.TempDir(), "missing.wav"), TranscribeOpts{}); err == nil {
		t.Fatal("missing audio accepted")
	}

	// Text-only responses fall back to a single whole-text segment.
	textOnly := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(`{"text":"  just words  "}`))
	}))
	defer textOnly.Close()
	segs, err = (remoteSTT{base: textOnly.URL, client: textOnly.Client()}).Transcribe(context.Background(), wav, TranscribeOpts{})
	if err != nil || len(segs) != 1 || segs[0].Text != "just words" {
		t.Fatalf("text fallback: %+v %v", segs, err)
	}
	// Upstream failures surface; empty transcripts are rejected.
	bad := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "no", http.StatusBadGateway)
	}))
	defer bad.Close()
	if _, err := (remoteSTT{base: bad.URL, client: bad.Client()}).Transcribe(context.Background(), wav, TranscribeOpts{}); err == nil {
		t.Fatal("upstream error swallowed")
	}
	empty := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(`{"text":"","segments":[]}`))
	}))
	defer empty.Close()
	if _, err := (remoteSTT{base: empty.URL, client: empty.Client()}).Transcribe(context.Background(), wav, TranscribeOpts{}); err == nil {
		t.Fatal("empty transcript accepted")
	}

	tts := remoteTTS{base: up.URL, key: "k", voice: "v1", client: up.Client()}
	out, err := tts.Synthesize(context.Background(), "say hi", "")
	if err != nil || string(out) != "WAVBYTES" {
		t.Fatalf("synthesize: %q %v", out, err)
	}
	if _, err := (remoteTTS{base: bad.URL, client: bad.Client()}).Synthesize(context.Background(), "x", ""); err == nil {
		t.Fatal("tts upstream error swallowed")
	}
}

// fetchResult only downloads allowlisted https hosts; everything else is
// refused before a dial is attempted.
func TestFetchResultAllowlist(t *testing.T) {
	ctx := context.Background()
	if _, _, err := fetchResult(ctx, "https://cdn.fal.ai/x.png", nil, 1024); err == nil {
		t.Fatal("fetch without allowlist accepted")
	}
	for _, raw := range []string{
		"::bad", "http://cdn.fal.ai/x.png", "https://evil.example/x.png", "https://fal.ai.evil.example/x.png",
	} {
		if _, _, err := fetchResult(ctx, raw, []string{"fal.ai"}, 1024); err == nil {
			t.Fatalf("accepted %q", raw)
		}
	}
	// An allowlisted loopback literal is refused by the address check itself.
	if _, _, err := fetchResult(ctx, "https://127.0.0.1:1/x", []string{"127.0.0.1"}, 1024); err == nil {
		t.Fatal("loopback fetch accepted")
	}
}
