package compaction

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"os"
	"strings"
	"time"
)

// RelaySummarize is the production Summarize seam: it posts a bounded
// continuity prompt to the managed model relay and returns the assistant
// text.
func RelaySummarize(ctx context.Context, model string, messages []map[string]any) (string, error) {
	return RelaySummarizeFocus(ctx, model, messages, "")
}

// RelaySummarizeFocus is the focus-aware form: a nonempty focus rides the
// system prompt as one literal instruction line — the same weight upstream
// /compress gives its focus argument.
func RelaySummarizeFocus(ctx context.Context, model string, messages []map[string]any, focus string) (string, error) {
	base := strings.TrimRight(os.Getenv("HUB_MANAGED_MODEL_URL"), "/")
	systemPrompt := summarySystemPrompt
	if focus = strings.TrimSpace(focus); focus != "" {
		systemPrompt += "\nPrioritize preserving information related to: " + focus
	}
	payload, err := json.Marshal(map[string]any{
		"model": model,
		"messages": []map[string]any{
			{"role": "system", "content": systemPrompt},
			{"role": "user", "content": flattenTranscript(messages)},
		},
	})
	if err != nil {
		return "", errRelayUnavailable
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, base+"/chat/completions", bytes.NewReader(payload)) // #nosec G704 -- configured managed-relay endpoint
	if err != nil {
		return "", errRelayUnavailable
	}
	req.Header.Set("Content-Type", "application/json")
	if key := os.Getenv("OPENAI_API_KEY"); key != "" {
		// The materialized config authenticates model/compression/goal_judge
		// aux calls with this same bearer (api_key: ${OPENAI_API_KEY}).
		req.Header.Set("Authorization", "Bearer "+key)
	}
	resp, err := relayClient.Do(req) // #nosec G704 -- configured managed-relay endpoint
	if err != nil {
		return "", errRelayUnavailable
	}
	defer resp.Body.Close()
	var out struct {
		Choices []struct {
			Message struct {
				Content string `json:"content"`
			} `json:"message"`
		} `json:"choices"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil || len(out.Choices) == 0 {
		return "", errRelayUnavailable
	}
	return out.Choices[0].Message.Content, nil
}

var errRelayUnavailable = errors.New("summarize relay unavailable")

var relayClient = &http.Client{Timeout: 120 * time.Second}

const summarySystemPrompt = "Compress this conversation transcript into a dense continuity summary for the same agent on the next turn. Keep: the active goal, decisions made, pending items, tool outputs that still matter, and user constraints. Drop: greetings, reasoning, superseded attempts. Write in the transcript's language."

// flattenTranscript renders the transcript into one prompt body.
func flattenTranscript(messages []map[string]any) string {
	var b strings.Builder
	for _, m := range messages {
		role, _ := m["role"].(string)
		text, _ := m["content"].(string)
		if role != "" && text != "" {
			fmt.Fprintf(&b, "\n[%s] %s", role, text)
		}
	}
	return b.String()
}
