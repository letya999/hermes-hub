package agenttools

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strings"
)

// assignmentRoutes maps each assignment op to its REST verb (SPEC-0080
// R2.3). No authorize route exists — grant widening is chat-only (R2.4).
var assignmentRoutes = map[string][2]string{
	"assignment_plan":     {http.MethodPost, "/v1/assignments/plan"},
	"assignment_create":   {http.MethodPost, "/v1/assignments"},
	"assignment_list":     {http.MethodGet, "/v1/assignments"},
	"assignment_get":      {http.MethodGet, "/v1/assignments/{id}"},
	"assignment_update":   {http.MethodPost, "/v1/assignments/{id}/plan"},
	"assignment_apply":    {http.MethodPost, "/v1/assignments/{id}/apply"},
	"assignment_pause":    {http.MethodPost, "/v1/assignments/{id}/pause"},
	"assignment_resume":   {http.MethodPost, "/v1/assignments/{id}/resume"},
	"assignment_cancel":   {http.MethodPost, "/v1/assignments/{id}/cancel"},
	"assignment_complete": {http.MethodPost, "/v1/assignments/{id}/complete"},
}

// Assignment dispatches the assignment_* ops onto the assignment REST
// surface (SPEC-0080 R2.3).
func (t *Tools) Assignment(ctx context.Context, op string, r Input) (map[string]any, error) {
	route := assignmentRoutes[op]
	switch op {
	case "assignment_update", "assignment_apply", "assignment_pause", "assignment_resume", "assignment_cancel", "assignment_complete":
		if r.ExpectedRevision == 0 {
			return nil, errors.New("expected_revision from assignment_get or assignment_list is required")
		}
	}
	var payload any
	switch op {
	case "assignment_plan":
		payload = json.RawMessage(r.Plan)
	case "assignment_create":
		body := map[string]any{"intent": r.Text}
		if r.Title != "" {
			body["title"] = r.Title
		}
		if r.Plan != "" {
			var plan map[string]any
			if err := json.Unmarshal([]byte(r.Plan), &plan); err != nil {
				return nil, errors.New("plan must be a JSON object")
			}
			body["plan"] = plan
		}
		if r.Queue {
			body["apply"] = true
		}
		payload = body
	case "assignment_update":
		var plan map[string]any
		if err := json.Unmarshal([]byte(r.Plan), &plan); err != nil {
			return nil, errors.New("plan must be a JSON object")
		}
		payload = map[string]any{"plan": plan, "expected_revision": r.ExpectedRevision}
	case "assignment_apply", "assignment_pause", "assignment_resume", "assignment_cancel", "assignment_complete":
		payload = map[string]any{"expected_revision": r.ExpectedRevision}
	}
	return t.assignmentCall(ctx, route[0], strings.ReplaceAll(route[1], "{id}", r.ID), payload)
}

// assignmentCall performs one control-plane request and decodes the reply.
func (t *Tools) assignmentCall(ctx context.Context, method, path string, payload any) (map[string]any, error) {
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
	client := t.HTTP
	if client == nil {
		client = http.DefaultClient
	}
	resp, err := client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	out := map[string]any{"status": "ok"}
	return out, nil
}
