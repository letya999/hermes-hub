package e2e

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/letya999/hermes-hub/internal/communication"
)

func TestMockServerRequiresRealRunAndCorrectAudience(t *testing.T) {
	for _, variant := range []string{"text", "voice-out", "voice-in", "foreign", "foreign-task", "failed", "invalid"} {
		root := t.TempDir()
		for _, dir := range []string{"mock-sent", "mappings"} {
			if err := os.MkdirAll(filepath.Join(root, dir), 0700); err != nil {
				t.Fatal(err)
			}
		}
		cfg := communication.Config{SpoolDir: root, ControlAuth: "synthetic"}
		tts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Content-Type", "audio/ogg")
			_, _ = w.Write([]byte("OggS synthetic"))
		}))
		cfg.TTSURL, cfg.STTURL = tts.URL, "http://configured-stt"
		id := "mock-" + strings.Repeat("a", 64)
		client := &http.Client{Transport: transportFunc(func(r *http.Request) (*http.Response, error) {
			if r.URL.Path != "/v1/mock/message" || r.Header.Get("X-Hub-Principal") != "alice" {
				t.Fatal("wrong admission route")
			}
			var input struct {
				ChatID int64  `json:"chat_id"`
				Text   string `json:"text"`
				Voice  *struct {
					Data []byte `json:"data"`
					MIME string `json:"mime"`
				} `json:"voice"`
			}
			if err := json.NewDecoder(r.Body).Decode(&input); err != nil {
				t.Fatal(err)
			}
			marker := input.Text[strings.LastIndex(input.Text, "E2E "):]
			d := communication.Delivery{ID: "job-" + id + "-response", JobID: id, ChatID: input.ChatID, TaskID: "task", Channel: "mock", Text: marker}
			if variant == "voice-in" {
				if input.Voice == nil || string(input.Voice.Data) != "OggS synthetic" || input.Voice.MIME != "audio/ogg" || strings.Contains(strings.TrimSuffix(input.Text, marker), "42") {
					t.Fatal("voice input missing or answer disclosed in caption")
				}
				d.Text += " 42"
			}
			if variant == "voice-out" {
				d.Voice = marker
			}
			if variant == "foreign" {
				d.ChatID++
			}
			m := communication.JobMapping{JobID: id, PrincipalID: "alice", TaskID: "task", SessionID: "session", RunID: "run", Status: "completed"}
			if variant == "foreign-task" {
				m.TaskID = "another-task"
			}
			if variant == "failed" {
				m.Status = "failed"
			}
			data, _ := json.Marshal(m)
			if err := os.WriteFile(filepath.Join(root, "mappings", id+".json"), data, 0600); err != nil {
				t.Fatal(err)
			}
			if variant != "failed" {
				data, _ = json.Marshal(map[string]any{"principal": "alice", "delivery": d})
				if err := os.WriteFile(filepath.Join(root, "mock-sent", d.ID+".json"), data, 0600); err != nil {
					t.Fatal(err)
				}
			}
			if variant == "invalid" {
				id = "../invalid"
			}
			body, _ := json.Marshal(map[string]string{"job_id": id, "task_id": "task"})
			return &http.Response{StatusCode: 200, Body: io.NopCloser(strings.NewReader(string(body)))}, nil
		})}
		name := "text"
		if variant == "voice-out" || variant == "voice-in" {
			name = variant
		}
		status, reason := mockServerCase(context.Background(), client, cfg, communication.User{ID: "alice"}, name)
		want := "FAIL"
		if variant == "text" || variant == "voice-out" || variant == "voice-in" {
			want = "PASS"
		}
		if status != want {
			t.Fatalf("%s: %s %s", variant, status, reason)
		}
		tts.Close()
	}
}
