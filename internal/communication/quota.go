package communication

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/letya999/hermes-hub/internal/locale"
)

// Provider quota is owned by the space's model-relay, which holds the CLIProxy
// management key and renders the subscription lines. The gateway only calls
// the admin-authenticated GET /v1/quota endpoint and caches the answer;
// unmanaged deployments without HUB_MODEL_RELAY_URL report the same honest
// "not configured" line /model already uses.
const quotaCacheTTL = 5 * time.Minute

// quotaTracker caches rendered /usage quota lines behind their own lock so
// the fetch path never touches gateway state. Entries are keyed by the
// resolved relay identity — principals on different relays (shared gateway)
// must never see each other's subscription snapshot.
type quotaTracker struct {
	mu    sync.Mutex
	cache map[string]quotaEntry
}

type quotaEntry struct {
	lines []string
	at    time.Time
}

// quotaLines returns rendered subscription-limit lines for /usage. The relay
// is resolved exactly like /model: the single-owner env pair, else the shared
// admins file keyed by principal. Missing relay configuration is silent;
// fetch failures return one honest line.
func (g *Gateway) quotaLines(ctx context.Context, lang locale.Lang, principals ...string) []string {
	principal := ""
	if len(principals) > 0 {
		principal = principals[0]
	}
	base, admin := modelRelayFor(principal)
	if base == "" {
		return []string{locale.T(lang, "quota.unconfigured")}
	}
	key := base + "\x00" + admin
	g.quota.mu.Lock()
	cached, ok := g.quota.cache[key]
	g.quota.mu.Unlock()
	if ok && g.now().Sub(cached.at) < quotaCacheTTL {
		return cached.lines
	}
	lines, err := fetchQuotaLines(ctx, strings.TrimRight(base, "/"), admin, lang)
	if err != nil {
		lines = []string{locale.T(lang, "quota.unavailable", err.Error())}
	}
	g.quota.mu.Lock()
	if g.quota.cache == nil {
		g.quota.cache = map[string]quotaEntry{}
	}
	g.quota.cache[key] = quotaEntry{lines: lines, at: g.now()}
	g.quota.mu.Unlock()
	return lines
}

func fetchQuotaLines(ctx context.Context, base, admin string, lang locale.Lang) ([]string, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, base+"/v1/quota", nil) // #nosec G704 -- configured internal quota endpoint
	if err != nil {
		return nil, err
	}
	if admin != "" {
		req.Header.Set("Authorization", "Bearer "+admin)
	}
	client := &http.Client{Timeout: 15 * time.Second, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	resp, err := client.Do(req) // #nosec G704 -- configured internal quota endpoint
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("%s", locale.T(lang, "quota.http-error", resp.StatusCode))
	}
	var payload struct {
		Lines []string `json:"lines"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&payload); err != nil || len(payload.Lines) == 0 {
		return nil, fmt.Errorf("%s", locale.T(lang, "quota.unparsed"))
	}
	return payload.Lines, nil
}
