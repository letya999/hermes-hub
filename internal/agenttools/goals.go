package agenttools

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"strings"
)

// Goal is the agent-facing goal-mode surface (SPEC-0074): it forwards one
// verb to the communication hub's /v1/goals, which mutates the same durable
// record the channel command uses. Principal and conversation are bound
// server-side / from this runtime's env — tool args can never steer them.
func (t *Tools) Goal(ctx context.Context, op string, r Input) (map[string]any, error) {
	if strings.Contains(t.CommunicationURL, "${") {
		return nil, errors.New("goal tools require HUB_COMMUNICATION_CONTROL_URL")
	}
	if strings.Contains(t.CommunicationAuth, "${") {
		return nil, errors.New("goal control authorization is unavailable")
	}
	principal := os.Getenv("HUB_PRINCIPAL_ID")
	payload := map[string]any{"op": strings.TrimPrefix(op, "goal_"), "conversation_id": os.Getenv("HUB_CONVERSATION_ID")}
	if op == "goal_set" {
		payload["text"] = strings.TrimSpace(r.Text)
	}
	body, err := json.Marshal(payload)
	if err != nil {
		return nil, err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost,
		strings.TrimRight(t.CommunicationURL, "/")+"/v1/goals", bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Authorization", "Bearer "+t.CommunicationAuth)
	req.Header.Set("X-Hub-Principal", principal)
	req.Header.Set("Content-Type", "application/json")
	client := t.HTTP
	if client == nil {
		client = http.DefaultClient
	}
	resp, err := client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	raw, err := io.ReadAll(io.LimitReader(resp.Body, 64*1024))
	if err != nil {
		return nil, err
	}
	if resp.StatusCode/100 != 2 {
		return nil, fmt.Errorf("goal hub rejected the request (HTTP %d)", resp.StatusCode)
	}
	var out map[string]any
	if err := json.Unmarshal(raw, &out); err != nil {
		return nil, errors.New("goal reply is not valid JSON")
	}
	return out, nil
}
