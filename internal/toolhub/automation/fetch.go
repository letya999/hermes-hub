package automation

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

func fetchWatch(ctx context.Context, url string) ([]byte, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Cache-Control", "no-cache")
	client := api.PublicHTTPClient(10 * time.Second)
	defer client.CloseIdleConnections()
	resp, err := client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode == http.StatusTooManyRequests {
		secs, _ := strconv.Atoi(strings.TrimSpace(resp.Header.Get("Retry-After")))
		return nil, &routines.RetryAfterError{Cause: fmt.Errorf("probe status %d", resp.StatusCode), Delay: time.Duration(secs) * time.Second}
	}
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
