package commops

import (
	"sort"
	"strconv"
	"strings"
	"time"
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
	if len(latencies) == 0 && unconfirmed == 0 && failed == 0 && restarts == 0 && avgTokens == 0 {
		return "\nЗдоровье (24ч): пока нет данных."
	}
	parts := []string{}
	if len(latencies) > 0 {
		sort.Slice(latencies, func(i, j int) bool { return latencies[i] < latencies[j] })
		parts = append(parts, "p50 "+latencyLabel(latencies[percentileIndex(len(latencies), 50)])+", p95 "+latencyLabel(latencies[percentileIndex(len(latencies), 95)]))
	}
	parts = append(parts, "неподтверждённых "+strconv.Itoa(unconfirmed)+", ошибок "+strconv.Itoa(failed))
	if restarts > 0 {
		parts = append(parts, "перезапусков рантайма "+strconv.Itoa(restarts))
	}
	if avgTokens > 0 {
		parts = append(parts, "вход ≈"+strconv.FormatInt(avgTokens, 10)+" токенов/вызов")
	}
	return "\nЗдоровье (24ч): " + strings.Join(parts, "; ") + "."
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

func latencyLabel(d time.Duration) string {
	return strconv.FormatFloat(d.Seconds(), 'f', 1, 64) + "с"
}
