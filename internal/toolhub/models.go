package toolhub

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/url"
	"os"
	"strings"
	"time"

	"github.com/letya999/hermes-hub/internal/identity"
	"github.com/letya999/hermes-hub/internal/toolhub/api"
)

type modelRelayAdmin struct {
	URL   string `json:"url"`
	Admin string `json:"admin"`
}

// The admin route is resolved from the authenticated principal on every call.
// Only the stack-owned relay alias is accepted; neither tool arguments nor a
// stale sibling enrollment can redirect the bearer to another service.
func modelRelayAdminFor(principal string) (modelRelayAdmin, bool) {
	if !identity.ValidID(principal) {
		return modelRelayAdmin{}, false
	}
	var entry modelRelayAdmin
	if principal == os.Getenv("HUB_USER_ID") {
		entry = modelRelayAdmin{URL: os.Getenv("HUB_MODEL_RELAY_URL"), Admin: os.Getenv("HUB_MODEL_RELAY_ADMIN")}
	} else if path := os.Getenv("HUB_MODEL_RELAY_ADMINS_FILE"); path != "" {
		body, err := os.ReadFile(path) // #nosec G703 -- operator-owned fixed configuration path
		if err != nil || len(body) > 1<<20 {
			return modelRelayAdmin{}, false
		}
		var entries map[string]modelRelayAdmin
		if json.Unmarshal(body, &entries) != nil {
			return modelRelayAdmin{}, false
		}
		entry = entries[principal]
	}
	want := "http://model-relay-" + principal + ":8318"
	return entry, entry.URL == want && strings.TrimSpace(entry.Admin) != ""
}

func (g *Gateway) execModel(ctx context.Context, auth identity.Envelope, tool api.ToolSpec, args map[string]any) (api.BackendResult, error) {
	entry, ok := modelRelayAdminFor(auth.PrincipalID)
	if !ok {
		return api.BackendResult{IsError: true, Structured: map[string]any{"error": "unavailable"}}, nil
	}
	client := g.ModelHTTP
	if client == nil {
		transport := http.DefaultTransport.(*http.Transport).Clone()
		transport.Proxy = nil
		client = &http.Client{Transport: transport, Timeout: 15 * time.Second}
	}
	copyClient := *client
	copyClient.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	client = &copyClient
	call := func(method, path string, payload any) (map[string]any, error) {
		var body io.Reader
		if payload != nil {
			encoded, err := json.Marshal(payload)
			if err != nil {
				return nil, err
			}
			body = bytes.NewReader(encoded)
		}
		// URL and path are fixed above. Refuse malformed configuration even
		// if an alternate caller changes the validation rule later.
		target, err := url.Parse(entry.URL + path)
		if err != nil || target.Hostname() != "model-relay-"+auth.PrincipalID {
			return nil, api.ErrUnauthorized
		}
		req, err := http.NewRequestWithContext(ctx, method, target.String(), body) // #nosec G704 -- validated fixed relay alias
		if err != nil {
			return nil, err
		}
		req.Header.Set("Authorization", "Bearer "+entry.Admin)
		if payload != nil {
			req.Header.Set("Content-Type", "application/json")
		}
		resp, err := client.Do(req) // #nosec G704 -- validated fixed relay alias
		if err != nil {
			return nil, err
		}
		defer resp.Body.Close()
		if resp.StatusCode != http.StatusOK {
			return nil, api.ErrUnauthorized
		}
		var out map[string]any
		decoder := json.NewDecoder(io.LimitReader(resp.Body, 1<<20))
		if err := decoder.Decode(&out); err != nil || out == nil {
			return nil, errors.New("invalid model relay response")
		}
		return out, nil
	}
	var result map[string]any
	var err error
	switch tool.Name {
	case "model_list":
		result, err = call(http.MethodGet, "/v1/registry", nil)
		if err == nil {
			result["current"], err = call(http.MethodGet, "/v1/model", nil)
		}
	case "model_switch":
		name := strings.TrimSpace(api.ArgString(args, "name"))
		if name == "" || len(name) > 256 {
			return api.BackendResult{}, api.ErrInvalid
		}
		result, err = call(http.MethodPut, "/v1/model", map[string]string{"model": name})
	default:
		return api.BackendResult{}, api.ErrInvalid
	}
	if err != nil {
		code := "unavailable"
		if errors.Is(err, api.ErrUnauthorized) {
			code = "forbidden"
		}
		return api.BackendResult{IsError: true, Structured: map[string]any{"error": code}}, nil
	}
	encoded, _ := json.Marshal(result)
	return api.BackendResult{Structured: result, Text: string(encoded)}, nil
}
