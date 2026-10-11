package agenttools

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"strings"
	"time"
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
	body, headers, err := t.knowledgeFetch(ctx, r.Name, "")
	if err != nil {
		return nil, err
	}
	return map[string]any{"name": r.Name, "content": string(body), "sha256": headers.Get("X-Hub-SHA256"), "modified_at": headers.Get("Last-Modified"), "context_id": headers.Get("X-Hub-Context")}, nil
}

func (t *Tools) knowledgeSearch(ctx context.Context, query string) (map[string]any, error) {
	if strings.TrimSpace(query) == "" || len(query) > 256 {
		return nil, fmt.Errorf("a query of at most 256 bytes is required")
	}
	body, _, err := t.knowledgeFetch(ctx, "", query)
	if err != nil {
		return nil, err
	}
	var result map[string]any
	if err := json.Unmarshal(body, &result); err != nil {
		return nil, err
	}
	return result, nil
}

func (t *Tools) knowledgeGet(ctx context.Context, name string) ([]byte, error) {
	body, _, err := t.knowledgeFetch(ctx, name, "")
	return body, err
}

func (t *Tools) knowledgeFetch(ctx context.Context, name, query string) ([]byte, http.Header, error) {
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()

	path := "/v1/knowledge"
	if name != "" {
		path += "/" + url.PathEscape(name)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, strings.TrimRight(t.CommunicationURL, "/")+path, nil)
	if err != nil {
		return nil, nil, err
	}
	if t.CommunicationAuth != "" {
		req.Header.Set("Authorization", "Bearer "+t.CommunicationAuth)
	}
	principal := os.Getenv("HUB_PRINCIPAL_ID")
	if principal == "" {
		principal = os.Getenv("HUB_USER_ID")
	}
	req.Header.Set("X-Hub-Principal", principal)
	req.Header.Set("X-Hub-Context", runtimeEnvelope().ContextID)
	client := t.HTTP
	if client == nil {
		client = http.DefaultClient
	}
	if query != "" {
		values := req.URL.Query()
		values.Set("q", query)
		req.URL.RawQuery = values.Encode()
	}
	resp, err := client.Do(req)
	if err != nil {
		return nil, nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, nil, fmt.Errorf("knowledge request rejected: %d", resp.StatusCode)
	}
	body, err := io.ReadAll(io.LimitReader(resp.Body, (9<<20)+1))
	if err == nil && len(body) > 9<<20 {
		return nil, nil, fmt.Errorf("knowledge response exceeds size limit")
	}
	return body, resp.Header, err
}
