package supervisor

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

// ContainerStat is one owned container's sampled resource counters for a
// watchdog sweep: allowlisted CPU and memory only, no processes or env.
type ContainerStat struct {
	CPUPercent float64 `json:"cpu_percent,omitempty"`
	MemBytes   int64   `json:"mem_bytes,omitempty"`
}

// WatchdogMetrics reports the supervision loop's own cost so its overhead on
// a 20-user host stays measurable and bounded.
type WatchdogMetrics struct {
	Sweeps      int64     `json:"sweeps"`
	Entries     int       `json:"entries"`
	LastSweepAt time.Time `json:"last_sweep_at"`
	LastSweepMS int64     `json:"last_sweep_ms"`
	LastStatsMS int64     `json:"last_stats_ms,omitempty"`
}

// WatchdogMetrics returns a copy of the sweep counters. Metrics live in
// memory only — the durable record of supervision is the incident ledger.
func (m *Manager) WatchdogMetrics() WatchdogMetrics {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.watchdog
}

// dockerStatsCommand adapts `docker stats --no-stream` to the Stats seam: one
// invocation per sweep covering all owned containers, keyed by name.
func dockerStatsCommand(command func(context.Context, ...string) ([]byte, error)) func(context.Context, []string) (map[string]ContainerStat, error) {
	return func(ctx context.Context, containers []string) (map[string]ContainerStat, error) {
		args := append([]string{"stats", "--no-stream", "--format", "{{json .}}"}, containers...)
		out, err := command(ctx, args...)
		if err != nil {
			return nil, err
		}
		stats := make(map[string]ContainerStat, len(containers))
		for _, line := range strings.Split(strings.TrimSpace(string(out)), "\n") {
			var row struct {
				Name     string `json:"Name"`
				CPUPerc  string `json:"CPUPerc"`
				MemUsage string `json:"MemUsage"`
			}
			if json.Unmarshal([]byte(line), &row) != nil || row.Name == "" {
				continue
			}
			cpu, _ := strconv.ParseFloat(strings.TrimSuffix(row.CPUPerc, "%"), 64)
			stats[row.Name] = ContainerStat{CPUPercent: cpu, MemBytes: parseMemBytes(row.MemUsage)}
		}
		return stats, nil
	}
}

// parseMemBytes reads the used side of docker's "256MiB / 2GiB" counter.
func parseMemBytes(usage string) int64 {
	head, _, _ := strings.Cut(usage, "/")
	fields := strings.Fields(head)
	if len(fields) == 0 {
		return 0
	}
	token := fields[0]
	i := len(token)
	for i > 0 && (token[i-1] < '0' || token[i-1] > '9') && token[i-1] != '.' {
		i--
	}
	value, err := strconv.ParseFloat(token[:i], 64)
	if err != nil {
		return 0
	}
	switch token[i:] {
	case "MiB":
		value *= 1 << 20
	case "GiB":
		value *= 1 << 30
	}
	return int64(value)
}

// Incident is one deduplicated watchdog observation class on a runtime. It is
// persisted with the runtime so a restart keeps the open incident instead of
// re-escalating it as new.
type Incident struct {
	Class   string    `json:"class"`
	Count   int       `json:"count"`
	FirstAt time.Time `json:"first_at"`
	LastAt  time.Time `json:"last_at"`
}

// signalPath is the append-only local watchdog signal journal beside
// supervisor.json; it shares the state root and its ignore rules.
func (m *Manager) signalPath() string {
	return filepath.Join(filepath.Dir(m.statePath), "signals.jsonl")
}

// recordIncidentLocked opens or refreshes one bounded incident per class.
// The first observation appends a single allowlisted line to the local signal
// journal; repeats update the persisted record only. m.mu must be held.
func (m *Manager) recordIncidentLocked(entry *runtimeEntry, class string) {
	now := m.cfg.Now()
	if entry.incidents == nil {
		entry.incidents = map[string]Incident{}
	}
	if incident, ok := entry.incidents[class]; ok {
		incident.Count++
		incident.LastAt = now
		entry.incidents[class] = incident
		return
	}
	entry.incidents[class] = Incident{Class: class, Count: 1, FirstAt: now, LastAt: now}
	file, err := os.OpenFile(m.signalPath(), os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0600)
	if err != nil {
		return
	}
	defer file.Close()
	_ = json.NewEncoder(file).Encode(map[string]string{
		"at":           now.UTC().Format(time.RFC3339),
		"class":        class,
		"principal_id": entry.PrincipalID,
		"context_id":   entry.ContextID,
		"runtime_id":   entry.RuntimeID,
		"generation":   entry.Generation,
	})
}
