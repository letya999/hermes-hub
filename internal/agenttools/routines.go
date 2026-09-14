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

func (t *Tools) Routine(ctx context.Context, op string, r Input) (map[string]any, error) {
	if t.CommunicationURL == "" {
		return nil, errors.New("routines require HUB_COMMUNICATION_CONTROL_URL")
	}
	principal := os.Getenv("HUB_PRINCIPAL_ID")
	if principal == "" {
		principal = os.Getenv("HUB_USER_ID")
	}
	var method, path string
	var payload any
	switch op {
	case "routine_list":
		method, path = http.MethodGet, "/v1/routines"
	case "routine_create":
		method, path = http.MethodPost, "/v1/routines"
		payload = map[string]any{"schedule_id": r.ID, "timezone": r.Timezone, "expression": r.Expression, "input": r.Text, "job_kind": "agent"}
	case "routine_update":
		method, path = http.MethodPost, "/v1/routines/"+r.ID
		payload = map[string]any{"expression": r.Expression, "input": r.Text}
	case "routine_pause":
		method, path = http.MethodPost, "/v1/routines/"+r.ID+"/pause"
	case "routine_delete":
		method, path = http.MethodDelete, "/v1/routines/"+r.ID
	default:
		return nil, errors.New("unknown routine operation")
	}
	if strings.Contains(path, "//") || strings.Contains(r.ID, "/") {
		return nil, errors.New("invalid schedule id")
	}
	var body io.Reader
	if payload != nil {
		encoded, err := json.Marshal(payload)
		if err != nil {
			return nil, err
		}
		body = bytes.NewReader(encoded)
	}
	req, err := http.NewRequestWithContext(ctx, method, strings.TrimRight(t.CommunicationURL, "/")+path, body)
	if err != nil {
		return nil, err
	}
	if t.CommunicationAuth != "" {
		req.Header.Set("Authorization", "Bearer "+t.CommunicationAuth)
	}
	req.Header.Set("X-Hub-Principal", principal)
	if payload != nil {
		req.Header.Set("Content-Type", "application/json")
	}
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
		return nil, fmt.Errorf("routine hub rejected the request")
	}
	out := map[string]any{"status": "ok"}
	if len(raw) > 0 && json.Valid(raw) {
		var decoded any
		if json.Unmarshal(raw, &decoded) == nil {
			out["result"] = decoded
		}
	}
	return out, nil
}
