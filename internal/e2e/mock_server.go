package e2e

import (
	"context"
	"crypto/rand"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	"github.com/letya999/hermes-hub/internal/communication"
)

// A synthetic peer gives mock transport its own conversation without changing
// the Telegram current task. The real agent/provider still execute the prompt.
func mockServerCase(ctx context.Context, client *http.Client, config communication.Config, user communication.User, name string) (string, string) {
	return mockPromptCase(ctx, client, config, user, name, "", "")
}

func mockPromptCase(ctx context.Context, client *http.Client, config communication.Config, user communication.User, name, override, expected string) (string, string) {
	return mockPromptJobCase(ctx, client, config, user, name, override, expected, nil)
}

func mockPromptJobCase(ctx context.Context, client *http.Client, config communication.Config, user communication.User, name, override, expected string, admittedJob *string) (string, string) {
	nonce := make([]byte, 16)
	if _, err := rand.Read(nonce); err != nil {
		return "FAIL", "random identity unavailable"
	}
	marker := "E2E " + hex.EncodeToString(nonce[:8])
	chat := int64(binary.BigEndian.Uint64(nonce[8:])&((1<<52)-1)) + 1
	prompt := "Acceptance test. Return exactly this text, no tools or explanation: " + marker
	payload := map[string]any{"chat_id": chat, "client_id": hex.EncodeToString(nonce)}
	if name == "voice-in" {
		if config.TTSURL == "" || config.STTURL == "" {
			return "BLOCKED", "configured TTS and STT services required"
		}
		tts := communication.HTTPSynthesizer{Base: config.TTSURL, Auth: config.TTSAuth, Voice: config.TTSVoice}
		audio, mime, err := tts.Synthesize(ctx, "Тест номер сорок два")
		if err != nil || len(audio) == 0 {
			return "FAIL", "synthetic voice generation failed"
		}
		payload["voice"] = map[string]any{"data": audio, "mime": mime, "duration": 5}
		prompt = "Acceptance test. Return only this marker followed by the number spoken in the attached voice, as digits: " + marker
	}
	if name == "voice-out" {
		prompt = "Acceptance test. Return only VOICE: followed by these spoken words: " + marker
	}
	if override != "" {
		prompt, marker = override, expected
	}
	var admitted struct {
		JobID  string `json:"job_id"`
		TaskID string `json:"task_id"`
	}
	payload["text"] = prompt
	admissionClient := client
	if name == "voice-in" {
		copy := *client
		copy.Timeout = 3 * time.Minute // Cold STT model load shares the production default deadline.
		admissionClient = &copy
	}
	err := routineRequest(ctx, admissionClient, config, user.ID, http.MethodPost, "/mock/message", payload, &admitted)
	if err != nil {
		return "FAIL", err.Error()
	}
	if !regexp.MustCompile(`^mock-[a-f0-9]{64}$`).MatchString(admitted.JobID) || admitted.TaskID == "" {
		return "FAIL", "invalid mock admission identity"
	}
	if admittedJob != nil {
		*admittedJob = admitted.JobID
	}
	deadline, cancel := context.WithTimeout(ctx, 5*time.Minute)
	defer cancel()
	ticker := time.NewTicker(time.Second)
	defer ticker.Stop()
	for {
		data, err := os.ReadFile(filepath.Join(config.SpoolDir, "mock-sent", "job-"+admitted.JobID+"-response.json"))
		if err == nil {
			var receipt struct {
				Principal string                 `json:"principal"`
				Delivery  communication.Delivery `json:"delivery"`
			}
			if json.Unmarshal(data, &receipt) != nil {
				return "FAIL", "invalid mock receipt"
			}
			d := receipt.Delivery
			if receipt.Principal != user.ID || d.JobID != admitted.JobID || d.TaskID != admitted.TaskID || d.ChatID != chat || d.Channel != "mock" {
				return "FAIL", "mock receipt owner/audience mismatch"
			}
			if name == "voice-out" {
				if !strings.Contains(d.Voice, marker) {
					return "FAIL", "voice intent absent; mock does not synthesize audio"
				}
			} else if name == "voice-in" && strings.TrimSpace(d.Text) != marker+" 42" {
				return "FAIL", "agent did not retain the number from the voice transcript"
			} else if name != "voice-in" && strings.TrimSpace(d.Text) != marker {
				return "FAIL", "mock response differs from unique marker"
			}
			data, err := os.ReadFile(filepath.Join(config.SpoolDir, "mappings", admitted.JobID+".json"))
			var m communication.JobMapping
			if err != nil || json.Unmarshal(data, &m) != nil || m.JobID != admitted.JobID || m.PrincipalID != user.ID || m.TaskID != admitted.TaskID || m.Status != "completed" || m.SessionID == "" || m.RunID == "" {
				return "FAIL", "real agent execution not proven"
			}
			return "PASS", "real agent/provider; mock channel captures reply; no real Telegram send"
		}
		if !os.IsNotExist(err) {
			return "FAIL", "mock receipt unreadable"
		}
		data, err = os.ReadFile(filepath.Join(config.SpoolDir, "mappings", admitted.JobID+".json"))
		var m communication.JobMapping
		if err == nil && json.Unmarshal(data, &m) == nil && m.JobID == admitted.JobID && m.PrincipalID == user.ID && (m.Status == "failed" || m.Status == "cancelled" || m.Status == "interrupted") {
			return "FAIL", "agent terminated before mock delivery"
		}
		select {
		case <-deadline.Done():
			return "FAIL", "mock job timed out; no automatic resubmission"
		case <-ticker.C:
		}
	}
}
