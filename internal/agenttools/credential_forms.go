package agenttools

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"os"
	"strings"
)

func (t *Tools) credentialForm(ctx context.Context, service string, keys []string) (map[string]any, error) {
	if strings.TrimSpace(t.CommunicationURL) == "" {
		return nil, errors.New("protected credential form requires HUB_COMMUNICATION_CONTROL_URL")
	}
	principal := strings.TrimSpace(os.Getenv("HUB_PRINCIPAL_ID"))
	if principal == "" {
		principal = strings.TrimSpace(os.Getenv("HUB_USER_ID"))
	}
	body, err := json.Marshal(map[string]any{"service": service, "keys": keys})
	if err != nil {
		return nil, err
	}
	defer clear(body)
	request, err := http.NewRequestWithContext(ctx, http.MethodPost, strings.TrimRight(t.CommunicationURL, "/")+"/v1/credential-forms", bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	request.Header.Set("Authorization", "Bearer "+t.CommunicationAuth)
	request.Header.Set("X-Hub-Principal", principal)
	request.Header.Set("Content-Type", "application/json")
	client := t.HTTP
	if client == nil {
		client = http.DefaultClient
	}
	response, err := client.Do(request)
	if err != nil {
		return nil, errors.New("protected credential form unavailable")
	}
	defer response.Body.Close()
	raw, err := io.ReadAll(io.LimitReader(response.Body, 64*1024+1))
	if err != nil || len(raw) > 64*1024 || response.StatusCode/100 != 2 {
		return nil, errors.New("protected credential form unavailable")
	}
	var result map[string]any
	if json.Unmarshal(raw, &result) != nil || strings.TrimSpace(stringValue(result["form_url"])) == "" {
		return nil, errors.New("protected credential form unavailable")
	}
	return result, nil
}

func stringValue(value any) string {
	text, _ := value.(string)
	return text
}
