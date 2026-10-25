package runtime

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"os"
	"strings"
	"time"
)

// The judge prompt adapts upstream's strict verdict contract: DONE demands
// concrete evidence — a bare "done" claim is not a completion (SPEC-0074 R3).
const goalJudgeSystemPrompt = `You are a strict judge evaluating whether an autonomous agent has achieved a user's stated goal. You receive the goal text and the agent's most recent response. Decide one of three verdicts.

DONE — the goal is fully satisfied:
- The response explicitly confirms the goal was completed, AND
- The response shows concrete evidence the deliverable exists (a command result, file contents excerpt, test output).
A bare claim ("done", "all requirements met", "everything works") is NOT done — return CONTINUE.

BLOCKED — the goal cannot be satisfied as stated:
- The response explains the goal is genuinely unachievable (impossible, out of scope), or
- Progress is blocked and the next step needs user input.

CONTINUE — not done, and there is a concrete next step the agent can take right now. This is the default when in doubt.

Reply ONLY with a single JSON object on one line:
{"verdict": "done", "reason": "<one sentence>", "evidence": "<short excerpt proving the deliverable>"}
{"verdict": "blocked", "reason": "<one sentence>", "evidence": ""}
{"verdict": "continue", "reason": "<one sentence>", "evidence": ""}`

// goalVerdict is the parsed judge outcome; Parse/Transport classify the two
// failure streaks upstream tracks separately.
type goalVerdict struct {
	Verdict   string
	Reason    string
	Evidence  string
	Parse     bool
	Transport bool
}

// goalJudgeRelay posts the strict prompt to the managed model relay, the same
// transport the compaction summarizer uses.
func goalJudgeRelay(ctx context.Context, model, goal, response string) (string, error) {
	base := strings.TrimRight(os.Getenv("HUB_MANAGED_MODEL_URL"), "/")
	if base == "" {
		return "", errGoalJudgeUnavailable
	}
	payload, _ := json.Marshal(map[string]any{
		"model": model,
		"messages": []map[string]any{
			{"role": "system", "content": goalJudgeSystemPrompt},
			{"role": "user", "content": "Goal:\n" + goal + "\n\nAgent's most recent response:\n" + response},
		},
	})
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, base+"/chat/completions", bytes.NewReader(payload)) // #nosec G704 -- configured managed-relay endpoint
	if err != nil {
		return "", errGoalJudgeUnavailable
	}
	req.Header.Set("Content-Type", "application/json")
	if key := os.Getenv("OPENAI_API_KEY"); key != "" {
		// Same bearer the materialized goal_judge aux block authenticates with.
		req.Header.Set("Authorization", "Bearer "+key)
	}
	client := &http.Client{Timeout: 60 * time.Second}
	resp, err := client.Do(req) // #nosec G704 -- configured managed-relay endpoint
	if err != nil {
		return "", errGoalJudgeUnavailable
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
		return "", errGoalJudgeUnavailable
	}
	return out.Choices[0].Message.Content, nil
}

var errGoalJudgeUnavailable = errors.New("goal judge relay unavailable")

// parseGoalVerdict extracts the verdict object from the judge reply.
func parseGoalVerdict(raw string) goalVerdict {
	start := strings.Index(raw, "{")
	end := strings.LastIndex(raw, "}")
	var data map[string]any
	if start >= 0 && end > start {
		_ = json.Unmarshal([]byte(raw[start:end+1]), &data)
	}
	verdict, _ := data["verdict"].(string)
	reason, _ := data["reason"].(string)
	evidence, _ := data["evidence"].(string)
	return goalVerdict{
		Verdict:  strings.ToLower(strings.TrimSpace(verdict)),
		Reason:   strings.TrimSpace(reason),
		Evidence: strings.TrimSpace(evidence),
	}
}
