package hermes

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"os"
	"path"
	"strings"
)

func fetchInbound(ctx context.Context, id string) (string, error) {
	if len(id) < 4 || len(id) > 32 || !strings.HasPrefix(id, "in-") {
		return "", errors.New("invalid inbound ID")
	}
	for _, c := range id[3:] {
		if c < '0' || c > '9' {
			return "", errors.New("invalid inbound ID")
		}
	}
	tokenEnv := os.Getenv("HUB_TOOLHUB_TOKEN_ENV")
	if tokenEnv == "" {
		tokenEnv = "HUB_RUNTIME_AUTH"
	}
	client := &http.Client{Transport: inboundBearer{token: os.Getenv(tokenEnv), base: http.DefaultTransport}}
	body, _ := json.Marshal(map[string]string{"id": id})
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, strings.TrimSuffix(toolHubEndpoint(), "/mcp")+"/v1/inbound-fetch", bytes.NewReader(body))
	if err != nil {
		return "", errors.New("inbound file could not be saved")
	}
	req.Header.Set("Content-Type", "application/json")
	response, err := client.Do(req)
	if err != nil {
		return "", errors.New("inbound file could not be saved")
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		return "", errors.New("inbound file could not be saved")
	}
	var receipt struct {
		Path string `json:"path"`
	}
	if json.NewDecoder(io.LimitReader(response.Body, 4096)).Decode(&receipt) != nil {
		return "", errors.New("inbound file receipt is invalid")
	}
	rel := receipt.Path
	if path.Clean(rel) != rel || (rel != "inbox/people/"+id+"/files" && !strings.HasPrefix(rel, "inbox/people/"+id+"/files/")) {
		return "", errors.New("inbound file receipt is invalid")
	}
	return rel, nil
}

type inboundBearer struct {
	token string
	base  http.RoundTripper
}

func (b inboundBearer) RoundTrip(r *http.Request) (*http.Response, error) {
	copy := r.Clone(r.Context())
	copy.Header.Set("Authorization", "Bearer "+b.token)
	return b.base.RoundTrip(copy)
}
