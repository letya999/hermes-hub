package media

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestTranscribeScopedUploadAndSegments(t *testing.T) {
	s, dir := openSession(t)
	writeWorkspace(t, dir, "voice.ogg", []byte("OggS-fixture"))
	calls := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		if r.Method != "POST" || r.URL.Path != "/v1/audio/transcriptions" || r.Header.Get("Authorization") != "Bearer test-media" {
			t.Errorf("unexpected request %s %s", r.Method, r.URL.Path)
		}
		file, header, err := r.FormFile("file")
		if err != nil {
			t.Error(err)
			w.WriteHeader(400)
			return
		}
		defer file.Close()
		raw, _ := io.ReadAll(file)
		if string(raw) != "OggS-fixture" || header.Filename != "voice.ogg" || r.FormValue("language") != "ru" || r.FormValue("response_format") != "verbose_json" {
			t.Error("upload lost bytes or transcription options")
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"text":"Проверка речи","segments":[{"start":0.2,"end":1.3,"text":"Проверка речи"}]}`)
	}))
	defer server.Close()
	t.Setenv("HUB_STT_URL", server.URL)
	t.Setenv("HUB_MEDIA_AUTH", "test-media")
	result, err := s.Transcribe(t.Context(), "voice.ogg", "ru", true)
	segments, _ := json.Marshal(result["segments"])
	if err != nil || result["text"] != "Проверка речи" || !strings.Contains(string(segments), `"start":0.2`) {
		t.Fatalf("transcript/segments: %+v %v", result, err)
	}
	for _, path := range []string{"../voice.ogg", "/voice.ogg", "missing.ogg"} {
		if _, err := s.Transcribe(t.Context(), path, "ru", false); err == nil {
			t.Errorf("invalid path accepted: %s", path)
		}
	}
	if calls != 1 {
		t.Fatalf("invalid paths reached service: %d calls", calls)
	}
}
