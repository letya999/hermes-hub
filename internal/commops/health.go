package commops

import (
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/letya999/hermes-hub/internal/locale"
)

// HealthLines renders the caller-scoped operational block for /status
// (SPEC-0053 R3.6): last-24h received→delivered latency percentiles, the
// unconfirmed and failed job counts, the caller runtime's restart count from
// supervisor state, and average input tokens per call from the usage journal.
func (e *Engine) HealthLines(owner Owner) string {
	since := e.Deps.Now().UTC().Add(-24 * time.Hour)
	var latencies []time.Duration
	unconfirmed, failed := 0, 0
	for _, m := range e.Deps.Mappings(owner.ID) {
		switch {
		case m.Status == "uncertain":
			unconfirmed++
		case m.Status == "failed" && !m.UpdatedAt.Before(since):
			failed++
		}
		if terminal(m.Status) && !m.TerminalAt.IsZero() && !m.TerminalAt.Before(since) && !m.CreatedAt.IsZero() {
			latencies = append(latencies, m.TerminalAt.Sub(m.CreatedAt))
		}
	}
	restarts, ok := e.Deps.Restarts(owner)
	if !ok {
		restarts = 0
	}
	var avgTokens int64
	if totals, err := e.Deps.UsageTotals("user:"+owner.ID, since); err == nil && totals.Jobs > 0 {
		avgTokens = totals.InputTokens / int64(totals.Jobs)
	}
	lang := owner.Lang
	if lang == "" {
		lang = locale.Fallback()
	}
	if len(latencies) == 0 && unconfirmed == 0 && failed == 0 && restarts == 0 && avgTokens == 0 {
		return locale.T(lang, "ops.health-empty")
	}
	parts := []string{}
	if len(latencies) > 0 {
		sort.Slice(latencies, func(i, j int) bool { return latencies[i] < latencies[j] })
		parts = append(parts, locale.T(lang, "ops.health-latency", latencyLabel(lang, latencies[percentileIndex(len(latencies), 50)]), latencyLabel(lang, latencies[percentileIndex(len(latencies), 95)])))
	}
	parts = append(parts, locale.T(lang, "ops.health-jobs", unconfirmed, failed))
	if restarts > 0 {
		parts = append(parts, locale.T(lang, "ops.health-restarts", restarts))
	}
	if avgTokens > 0 {
		parts = append(parts, locale.T(lang, "ops.health-tokens", avgTokens))
	}
	return locale.T(lang, "ops.health-line", strings.Join(parts, "; "))
}

// percentileIndex is the nearest-rank index for a sorted slice.
func percentileIndex(n, p int) int {
	idx := (p*n + 99) / 100
	if idx > n {
		idx = n
	}
	if idx < 1 {
		idx = 1
	}
	return idx - 1
}

func latencyLabel(lang locale.Lang, d time.Duration) string {
	return strconv.FormatFloat(d.Seconds(), 'f', 1, 64) + locale.T(lang, "ops.health-sec")
}
