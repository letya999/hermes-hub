package communication

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"strings"
	"sync"
	"time"
)

// Provider quota is owned by the space's model-relay, which holds the CLIProxy
// management key and renders the subscription lines. The gateway only calls
// the admin-authenticated GET /v1/quota endpoint and caches the answer;
// unmanaged deployments without HUB_MODEL_RELAY_URL report the same honest
// "not configured" line /model already uses.
const quotaCacheTTL = 5 * time.Minute

// quotaTracker caches rendered /usage quota lines behind their own lock so
// the fetch path never touches gateway state.
type quotaTracker struct {
	mu    sync.Mutex
	cache []string
	at    time.Time
}

// quotaLines returns rendered subscription-limit lines for /usage. Missing
// relay configuration is silent; fetch failures return one honest line.
func (g *Gateway) quotaLines(ctx context.Context) []string {
	base := strings.TrimRight(strings.TrimSpace(os.Getenv("HUB_MODEL_RELAY_URL")), "/")
	if base == "" {
		return []string{"Лимиты подписки: источник не настроен на сервере."}
	}
	g.quota.mu.Lock()
	cached, at := g.quota.cache, g.quota.at
	g.quota.mu.Unlock()
	if !at.IsZero() && g.now().Sub(at) < quotaCacheTTL {
		return cached
	}
	lines, err := fetchQuotaLines(ctx, base, strings.TrimSpace(os.Getenv("HUB_MODEL_RELAY_ADMIN")))
	if err != nil {
		lines = []string{"Лимиты подписки: недоступно (" + err.Error() + ")."}
	}
	g.quota.mu.Lock()
	g.quota.cache, g.quota.at = lines, g.now()
	g.quota.mu.Unlock()
	return lines
}

func fetchQuotaLines(ctx context.Context, base, admin string) ([]string, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, base+"/v1/quota", nil)
	if err != nil {
		return nil, err
	}
	if admin != "" {
		req.Header.Set("Authorization", "Bearer "+admin)
	}
	client := &http.Client{Timeout: 15 * time.Second, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	resp, err := client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("релей вернул HTTP %d", resp.StatusCode)
	}
	var payload struct {
		Lines []string `json:"lines"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&payload); err != nil || len(payload.Lines) == 0 {
		return nil, fmt.Errorf("квота не распознана")
	}
	return payload.Lines, nil
}
