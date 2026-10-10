package agenttools

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
)

// knowledgeList returns the caller's staged read-only knowledge sources.
func (t *Tools) knowledgeList(ctx context.Context) (map[string]any, error) {
	body, err := t.knowledgeGet(ctx, "")
	if err != nil {
		return nil, err
	}
	var items []any
	if err := json.Unmarshal(body, &items); err != nil {
		return nil, err
	}
	return map[string]any{"items": items}, nil
}

// knowledgeRead returns one staged source as tool output — the content never
// lands in the workspace.
func (t *Tools) knowledgeRead(ctx context.Context, r Input) (map[string]any, error) {
	body, err := t.knowledgeGet(ctx, r.Name)
	if err != nil {
		return nil, err
	}
	return map[string]any{"name": r.Name, "content": string(body)}, nil
}

func (t *Tools) knowledgeGet(ctx context.Context, name string) ([]byte, error) {
	path := "/v1/knowledge"
	if name != "" {
		path += "/" + name
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, strings.TrimRight(t.CommunicationURL, "/")+path, nil)
	if err != nil {
		return nil, err
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("knowledge request rejected: %d", resp.StatusCode)
	}
	return io.ReadAll(resp.Body)
}
