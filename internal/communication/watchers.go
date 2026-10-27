package communication

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/letya999/hermes-hub/internal/routines"
	"github.com/letya999/hermes-hub/internal/toolhub/api"
)

// fetchWatch is the live probe: one GET, caches bypassed, body capped at
// 1 MiB — the deterministic poll never touches the model.
func (g *Gateway) fetchWatch(ctx context.Context, url string) ([]byte, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Cache-Control", "no-cache")
	client := g.watchHTTP
	if client == nil {
		client = api.PublicHTTPClient(10 * time.Second)
		defer client.CloseIdleConnections()
	}
	resp, err := client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode == http.StatusTooManyRequests {
		secs, _ := strconv.Atoi(strings.TrimSpace(resp.Header.Get("Retry-After")))
		return nil, &routines.RetryAfterError{Cause: fmt.Errorf("probe status %d", resp.StatusCode), Delay: time.Duration(secs) * time.Second}
	}
	// GitHub answers secondary rate limits with 403 + Retry-After, and the
	// primary limit with 403 + X-RateLimit-*: both fences must reach the
	// acquisition pool or every tick polls into a guaranteed failure.
	if resp.StatusCode == http.StatusForbidden {
		cause := fmt.Errorf("probe status %d", resp.StatusCode)
		if secs, err := strconv.Atoi(strings.TrimSpace(resp.Header.Get("Retry-After"))); err == nil && secs > 0 {
			return nil, &routines.RetryAfterError{Cause: cause, Delay: time.Duration(secs) * time.Second}
		}
		if strings.TrimSpace(resp.Header.Get("X-RateLimit-Remaining")) == "0" {
			if reset, err := strconv.ParseInt(strings.TrimSpace(resp.Header.Get("X-RateLimit-Reset")), 10, 64); err == nil {
				if delay := time.Until(time.Unix(reset, 0)); delay > 0 {
					return nil, &routines.RetryAfterError{Cause: cause, Delay: delay}
				}
			}
		}
	}
	if resp.StatusCode/100 != 2 {
		return nil, fmt.Errorf("probe status %d", resp.StatusCode)
	}
	body, err := io.ReadAll(io.LimitReader(resp.Body, 1024*1024+1))
	if len(body) > 1024*1024 {
		return nil, errors.New("probe body exceeds size limit")
	}
	return body, err
}

func (g *Gateway) checkBaseline(ctx context.Context, principal string, probe *WatchProbe) (string, error) {
	var body []byte
	var err error
	if probe.Kind == "mcp_tool" {
		body, err = g.toolCallProbe(ctx, principal, probe)
	} else if probe.Kind == "render" || probe.Kind == "service" {
		body, err = g.serviceProbe(ctx, principal, probe)
	} else if strings.HasPrefix(probe.URL, "task:") {
		g.spool.mu.Lock()
		var value string
		value, err = g.spool.taskProbeValueLocked(principal, strings.TrimPrefix(probe.URL, "task:"))
		g.spool.mu.Unlock()
		if err != nil {
			return "", errors.New("check task unreadable")
		}
		body = []byte(value)
	} else {
		body, err = g.fetchWatch(ctx, probe.URL)
	}
	if err != nil {
		return "", errors.New("check source unreachable")
	}
	value, err := probeValue(probe, body)
	if err != nil {
		return "", errors.New("invalid check probe result")
	}
	return value, nil
}
