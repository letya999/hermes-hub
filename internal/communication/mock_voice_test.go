package communication

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
)

type recordingMockSTT struct {
	calls int
	path  string
	err   error
}

func (s *recordingMockSTT) Transcribe(_ context.Context, path, mime string) (string, error) {
	s.calls++
	s.path = path
	data, err := os.ReadFile(path)
	if err != nil || string(data) != "OggS synthetic" || mime != "audio/ogg" {
		return "", errors.New("wrong staged audio")
	}
	return "Тест номер сорок два", s.err
}

func TestMockVoiceAdmissionIsBoundedOwnedAndTranscribed(t *testing.T) {
	for _, variant := range []string{"ok", "foreign", "bad-mime", "too-long", "too-large", "stt-failure", "trailing", "method"} {
		t.Run(variant, func(t *testing.T) {
			cfg := testConfig(t)
			cfg.ControlAuth = "synthetic-control"
			g, err := New(cfg)
			if err != nil {
				t.Fatal(err)
			}
			stt := &recordingMockSTT{}
			g.transcriber = stt
			v := mockVoice{Data: []byte("OggS synthetic"), MIME: "audio/ogg", Duration: 5}
			if variant == "bad-mime" {
				v.MIME = "text/plain"
			}
			if variant == "too-long" {
				v.Duration = defaultMediaDurationLimit + 1
			}
			if variant == "too-large" {
				v.Data = make([]byte, mediaSizeLimit+1)
			}
			if variant == "stt-failure" {
				stt.err = errors.New("synthetic failure")
			}
			if variant == "foreign" {
				if err := g.mockLinkClaim(42, "bob"); err != nil {
					t.Fatal(err)
				}
			}
			body, err := json.Marshal(map[string]any{"chat_id": 42, "client_id": "voice-one", "text": "Answer the number", "voice": v})
			if err != nil {
				t.Fatal(err)
			}
			if variant == "trailing" {
				body = append(body, []byte(" {}")...)
			}
			method := http.MethodPost
			if variant == "method" {
				method = http.MethodGet
			}
			r := httptest.NewRequest(method, "/v1/mock/message", bytes.NewReader(body))
			r.Header.Set("Authorization", "Bearer synthetic-control")
			r.Header.Set("X-Hub-Principal", "alice")
			w := httptest.NewRecorder()
			g.Handler().ServeHTTP(w, r)
			job, err := g.spool.ClaimJob()
			if err != nil {
				t.Fatal(err)
			}
			if variant == "ok" {
				if w.Code != 200 || job == nil || job.Channel != "mock" || job.UserID != "alice" || !strings.Contains(job.Text, "transcript=ok]\nТест номер сорок два") {
					t.Fatal("voice not admitted with real transcript", w.Code, job)
				}
				if strings.Contains(job.Text, "OggS") {
					t.Fatal("audio leaked into canonical text")
				}
			} else if w.Code < 400 || job != nil {
				t.Fatal("invalid voice admitted", variant, w.Code, job)
			}
			wantCalls := 0
			if variant == "ok" || variant == "stt-failure" {
				wantCalls = 1
			}
			if stt.calls != wantCalls {
				t.Fatal("invalid voice reached STT", stt.calls)
			}
			if stt.path != "" {
				if _, err := os.Stat(stt.path); !os.IsNotExist(err) {
					t.Fatal("staged voice retained", err)
				}
			}
		})
	}
}
