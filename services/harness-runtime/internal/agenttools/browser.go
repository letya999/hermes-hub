package agenttools

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"net/http"
	"net/url"
	"os"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/letya999/hermes-hub/internal/identity"
	"github.com/letya999/hermes-hub/internal/toolhub/api"
)

type browserRun struct {
	owner   string
	profile string
	session *mcp.ClientSession
	mu      sync.Mutex
}

var browserEndpoint = func(user, profile string) string {
	port := "8931"
	if profile == "guest" {
		port = "8932"
	}
	return "http://browser-" + user + ":" + port + "/mcp"
}

var browserConnect = func(ctx context.Context, endpoint string) (*mcp.ClientSession, error) {
	client := mcp.NewClient(&mcp.Implementation{Name: "hermes-hub-browser", Version: "1.0.0"}, nil)
	transport := http.DefaultTransport.(*http.Transport).Clone()
	transport.Proxy = nil
	httpClient := &http.Client{Transport: api.LegacyMCPRoundTripper{Base: transport}, Timeout: 120 * time.Second}
	return client.Connect(ctx, &mcp.StreamableClientTransport{Endpoint: endpoint, HTTPClient: httpClient, DisableStandaloneSSE: true}, nil)
}

type browserRuntimeGrant struct {
	Persistent bool `json:"persistent"`
	Guest      bool `json:"guest"`
	Act        bool `json:"act"`
}

func browserGrant() browserRuntimeGrant {
	var grant browserRuntimeGrant
	_ = json.Unmarshal([]byte(os.Getenv("HUB_MANAGED_BROWSER")), &grant)
	return grant
}

func browserAddress(raw string) bool {
	u, err := url.Parse(strings.TrimSpace(raw))
	return err == nil && (u.Scheme == "https" || u.Scheme == "http") && u.Host != "" && u.User == nil && len(raw) <= 2048
}

func (t *Tools) browserCall(ctx context.Context, name string, input Input) (any, error) {
	owner := os.Getenv("HUB_USER_ID")
	if !identity.ValidID(owner) {
		return nil, errors.New("browser owner unavailable")
	}
	grant := browserGrant()
	if name == "browser_open" {
		profile := input.Profile
		if profile == "" {
			profile = "user"
		}
		if !browserAddress(input.URL) || (profile != "user" && profile != "guest") ||
			(profile == "user" && !grant.Persistent) || (profile == "guest" && !grant.Guest) {
			return nil, errors.New("browser target or profile denied")
		}
		ctx, cancel := context.WithTimeout(ctx, 120*time.Second)
		defer cancel()
		session, err := browserConnect(ctx, browserEndpoint(owner, profile))
		if err != nil {
			return nil, errors.New("browser sidecar unavailable")
		}
		result, err := session.CallTool(ctx, &mcp.CallToolParams{Name: "browser_tabs", Arguments: map[string]any{"action": "new", "url": input.URL}})
		if err != nil || result.IsError {
			_ = session.Close()
			return nil, errors.New("browser navigation failed")
		}
		var key [16]byte
		if _, err := rand.Read(key[:]); err != nil {
			_ = session.Close()
			return nil, err
		}
		id := "browser-" + hex.EncodeToString(key[:])
		t.browserMu.Lock()
		if t.browserRuns == nil {
			t.browserRuns = map[string]*browserRun{}
		}
		t.browserRuns[id] = &browserRun{owner: owner, profile: profile, session: session}
		t.browserMu.Unlock()
		return map[string]any{"session": id, "profile": profile, "url": input.URL}, nil
	}
	t.browserMu.Lock()
	run := t.browserRuns[input.Session]
	t.browserMu.Unlock()
	if run == nil || run.owner != owner {
		return nil, errors.New("browser session not found")
	}
	if (run.profile == "user" && !grant.Persistent) || (run.profile == "guest" && !grant.Guest) {
		return nil, errors.New("browser profile denied")
	}
	if name == "browser_act" && !grant.Act {
		return nil, errors.New("browser action denied")
	}
	run.mu.Lock()
	defer run.mu.Unlock()
	if name == "browser_act" && input.Action == "close" {
		ctx, cancel := context.WithTimeout(ctx, 120*time.Second)
		defer cancel()
		result, err := run.session.CallTool(ctx, &mcp.CallToolParams{Name: "browser_tabs", Arguments: map[string]any{"action": "close"}})
		if err != nil || result.IsError {
			return nil, errors.New("browser close failed")
		}
		t.browserMu.Lock()
		delete(t.browserRuns, input.Session)
		t.browserMu.Unlock()
		_ = run.session.Close()
		return map[string]any{"closed": true}, nil
	}
	operation, arguments, err := browserOperation(name, input)
	if err != nil {
		return nil, err
	}
	ctx, cancel := context.WithTimeout(ctx, 120*time.Second)
	defer cancel()
	result, err := run.session.CallTool(ctx, &mcp.CallToolParams{Name: operation, Arguments: arguments})
	if err != nil {
		return nil, errors.New("browser sidecar unavailable")
	}
	var text strings.Builder
	for _, item := range result.Content {
		if part, ok := item.(*mcp.TextContent); ok {
			if text.Len()+len(part.Text) > Limit {
				return nil, errors.New("browser result exceeds limit")
			}
			text.WriteString(part.Text)
		}
	}
	if result.IsError {
		return nil, errors.New("browser operation failed")
	}
	out := map[string]any{"session": input.Session, "text": text.String()}
	if name == "browser_capture" {
		out["path"] = arguments["filename"].(string)
	}
	return out, nil
}

func browserOperation(name string, in Input) (string, map[string]any, error) {
	switch name {
	case "browser_read":
		return "browser_snapshot", map[string]any{}, nil
	case "browser_capture":
		filename := "browser/" + in.Session + ".png"
		operation := "browser_take_screenshot"
		args := map[string]any{"scale": "css"}
		if in.Kind == "pdf" {
			filename, operation = "browser/"+in.Session+".pdf", "browser_pdf_save"
			args = map[string]any{}
		} else if in.Kind != "screenshot" {
			return "", nil, errors.New("invalid browser capture kind")
		}
		args["filename"] = filename
		return operation, args, nil
	case "browser_act":
		if len(in.Target) > 1024 || len(in.Value) > 8192 {
			return "", nil, errors.New("browser action exceeds limit")
		}
		switch in.Action {
		case "click":
			if in.Target != "" {
				return "browser_click", map[string]any{"target": in.Target}, nil
			}
		case "type":
			if in.Target != "" {
				return "browser_type", map[string]any{"target": in.Target, "text": in.Value}, nil
			}
		case "press":
			if in.Value != "" {
				return "browser_press_key", map[string]any{"key": in.Value}, nil
			}
		case "select":
			if in.Target != "" {
				return "browser_select_option", map[string]any{"target": in.Target, "values": []string{in.Value}}, nil
			}
		case "scroll":
			n, err := strconv.Atoi(in.Value)
			if err == nil && n >= -10000 && n <= 10000 {
				return "browser_mouse_wheel", map[string]any{"deltaX": 0, "deltaY": n}, nil
			}
		case "wait":
			if in.Value != "" {
				if seconds, err := strconv.ParseFloat(in.Value, 64); err == nil {
					if !(seconds > 0 && seconds <= 30) {
						return "", nil, errors.New("browser wait outside 0..30 seconds")
					}
					return "browser_wait_for", map[string]any{"time": seconds}, nil
				}
				return "browser_wait_for", map[string]any{"text": in.Value}, nil
			}
		}
	}
	return "", nil, errors.New("invalid browser operation")
}
