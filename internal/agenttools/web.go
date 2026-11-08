package agenttools

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net/url"
	"os/exec"
	"strings"
	"time"
)

var webRunner = func(ctx context.Context, payload []byte) ([]byte, error) {
	cmd := exec.CommandContext(ctx, "hub-sidecar")
	cmd.WaitDelay = time.Second
	cmd.Stdin = bytes.NewReader(payload)
	out := &boundedWebOutput{}
	cmd.Stdout = out
	if err := cmd.Run(); err != nil {
		return nil, err
	}
	return out.Bytes(), nil
}

type boundedWebOutput struct{ bytes.Buffer }

func (b *boundedWebOutput) Write(p []byte) (int, error) {
	if b.Len()+len(p) > 2<<20 {
		return 0, errors.New("web result exceeds limit")
	}
	return b.Buffer.Write(p)
}

func (t *Tools) webCall(ctx context.Context, name string, input Input) (any, error) {
	args := map[string]any{"op": name}
	switch name {
	case "web_search", "web_search_multi":
		query := strings.TrimSpace(input.Query)
		if query == "" || len(query) > 1000 {
			return nil, errors.New("invalid web query")
		}
		limit := input.Limit
		if limit == 0 {
			limit = 5
		}
		if limit < 1 || limit > 25 {
			return nil, errors.New("invalid web result limit")
		}
		args["query"], args["limit"] = query, limit
		if name == "web_search_multi" {
			if len(input.Providers) > 8 {
				return nil, errors.New("too many web providers")
			}
			for _, provider := range input.Providers {
				if provider == "" || len(provider) > 40 || strings.Trim(provider, "abcdefghijklmnopqrstuvwxyz0123456789-") != "" {
					return nil, errors.New("invalid web provider")
				}
			}
			args["providers"] = input.Providers
		}
	case "web_fetch":
		address := strings.TrimSpace(input.URL)
		parsed, err := url.Parse(address)
		if err != nil || len(address) > 2048 || parsed.Scheme != "https" || parsed.Host == "" || parsed.User != nil || parsed.Fragment != "" {
			return nil, errors.New("invalid web URL")
		}
		args["url"] = address
	case "web_providers":
	case "web_cache":
		if input.Action != "status" && input.Action != "prune" && input.Action != "clear" {
			return nil, errors.New("invalid web cache action")
		}
		args["action"] = input.Action
	default:
		return nil, errors.New("invalid web operation")
	}
	payload, err := json.Marshal(args)
	if err != nil {
		return nil, err
	}
	ctx, cancel := context.WithTimeout(ctx, 65*time.Second)
	defer cancel()
	body, err := webRunner(ctx, payload)
	if err != nil || len(body) > 2<<20 {
		return nil, errors.New("web provider unavailable")
	}
	var response struct {
		OK     bool           `json:"ok"`
		Result map[string]any `json:"result"`
	}
	if json.Unmarshal(body, &response) != nil || !response.OK || response.Result == nil {
		return nil, errors.New("web provider unavailable")
	}
	if response.Result["success"] == false {
		return map[string]any{"success": false, "error": "provider_unavailable"}, nil
	}
	return response.Result, nil
}
