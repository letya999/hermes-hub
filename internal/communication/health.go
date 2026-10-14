package communication

import (
	"context"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"
)

// healthLines renders the caller-scoped operational block for /status
// (SPEC-0053 R3.6): last-24h received→delivered latency percentiles, the
// unconfirmed and failed job counts, the caller runtime's restart count from
// supervisor state, and average input tokens per call from the usage journal.
func (g *Gateway) healthLines(user User) string {
	since := g.now().UTC().Add(-24 * time.Hour)
	var latencies []time.Duration
	unconfirmed, failed := 0, 0
	if entries, err := os.ReadDir(filepath.Join(g.spool.root, "mappings")); err == nil {
		for _, entry := range entries {
			if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".json") {
				continue
			}
			g.spool.mu.Lock()
			mapping, err := g.spool.loadMappingLocked(strings.TrimSuffix(entry.Name(), ".json"))
			g.spool.mu.Unlock()
			if err != nil || mapping.UserID != user.ID {
				continue
			}
			switch {
			case mapping.Status == "uncertain":
				unconfirmed++
			case mapping.Status == "failed" && !mapping.UpdatedAt.Before(since):
				failed++
			}
			if terminalStatus(mapping.Status) && !mapping.TerminalAt.IsZero() && !mapping.TerminalAt.Before(since) && !mapping.CreatedAt.IsZero() {
				latencies = append(latencies, mapping.TerminalAt.Sub(mapping.CreatedAt))
			}
		}
	}
	restarts := 0
	if surface := g.healthSurface(user); surface != nil {
		restarts = surface.Restarts
	}
	var avgTokens int64
	if totals, err := g.spool.UsageTotalsSince("user:"+user.ID, since, ""); err == nil && totals.Jobs > 0 {
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

// healthSurface reuses the readiness surface — its additive Restarts field is
// the supervisor's durable counter; unreachable/absent means the line omits
// restarts rather than reporting a fabricated zero.
func (g *Gateway) healthSurface(user User) *runtimeSurface {
	if strings.TrimSpace(g.config.RuntimeURL) == "" || strings.TrimSpace(g.config.RuntimeAuth) == "" {
		return nil
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if g.config.Supervised {
		return g.supervisedSurface(ctx, user)
	}
	return g.residentSurface(ctx)
}
