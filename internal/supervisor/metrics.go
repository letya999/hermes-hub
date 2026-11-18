package supervisor

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"strconv"
	"time"

	hubruntime "github.com/letya999/hermes-hub/internal/runtime"
)

// The metrics watcher (config.supervision.watchers.metrics) turns the
// runtime's bounded /v1/status plus the docker stats sample into sustained
// anomaly incidents. A class must breach three consecutive observations
// before an incident fires, and only on the transition into the anomaly —
// a still-degrading runtime does not re-alert every sweep, and the personal
// supervisor alone decides what to do about it.
const metricBreachLimit = 3

// sampleRuntimeMetrics runs one metrics-watcher pass after the health sweep:
// per-entry cadence from the space policy, a 2s scrape budget, then one
// locked evaluation. Scrape failures are skips, never incidents — transport
// health is the health watcher's job.
func (m *Manager) sampleRuntimeMetrics(ctx context.Context, now time.Time) {
	type target struct {
		key       string
		address   string
		auth      string
		protected bool
	}
	m.mu.Lock()
	bindings := make(map[string]Binding, len(m.items))
	for key, entry := range m.items {
		bindings[key] = entry.binding
	}
	m.mu.Unlock()
	targets := []target{}
	for key, binding := range bindings {
		watcher, ok := m.loadSupervisionPolicy(binding).Watchers["metrics"]
		if !ok {
			continue
		}
		m.mu.Lock()
		entry := m.items[key]
		due := entry != nil && entry.State != Stopped && entry.Address != "" && entry.auth != "" &&
			(entry.metricsCheckedAt.IsZero() || now.Sub(entry.metricsCheckedAt) >= watcherEvery(watcher.Every))
		address, auth := "", ""
		if due {
			address, auth = entry.Address, entry.auth
			entry.metricsCheckedAt = now
		}
		m.mu.Unlock()
		if due {
			targets = append(targets, target{key: key, address: address, auth: auth, protected: watcher.Protected})
		}
	}
	for _, t := range targets {
		probeCtx, cancel := context.WithTimeout(ctx, 2*time.Second)
		snap, err := m.runtimeStatusProbe(probeCtx, t.address, t.auth)
		cancel()
		if err != nil {
			continue
		}
		m.mu.Lock()
		if entry := m.items[t.key]; entry != nil {
			m.evaluateRuntimeMetricsLocked(entry, snap, t.protected)
		}
		m.mu.Unlock()
	}
}

// watcherEvery parses the policy cadence; an unparseable or non-positive
// value means "every sweep" — the key's presence already opted the watcher in.
func watcherEvery(raw string) time.Duration {
	if every, err := time.ParseDuration(raw); err == nil && every > 0 {
		return every
	}
	return 0
}

// runtimeStatusProbe fetches the runtime's bounded status document — the
// same bearer and bounded decode the health probe uses.
func (m *Manager) runtimeStatusProbe(ctx context.Context, address, auth string) (hubruntime.RuntimeStatus, error) {
	var status hubruntime.RuntimeStatus
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, address+"/v1/status", nil)
	if err != nil {
		return status, err
	}
	request.Header.Set("Authorization", "Bearer "+auth)
	response, err := m.cfg.HTTP.Do(request)
	if err != nil {
		return status, err
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK || json.NewDecoder(http.MaxBytesReader(nil, response.Body, 16*1024)).Decode(&status) != nil {
		return status, errors.New("runtime status unavailable")
	}
	return status, nil
}

// evaluateRuntimeMetricsLocked updates one entry's consecutive-breach
// counters and fires an incident only on the transition into the anomaly.
// Between the over and under watermarks a counter holds — hysteresis keeps a
// flapping gauge from toggling the alert. m.mu must be held.
func (m *Manager) evaluateRuntimeMetricsLocked(entry *runtimeEntry, snap hubruntime.RuntimeStatus, protected bool) {
	quota, _ := strconv.ParseFloat(m.cfg.CPU, 64)
	stat := entry.lastStat
	prev := entry.metricPrev
	entry.metricPrev, entry.lastMetrics = snap, snap
	if entry.metricBreaches == nil {
		entry.metricBreaches = map[string]int{}
	}
	reqDelta := snap.RequestsTotal - prev.RequestsTotal
	errDelta := snap.HTTP5xxTotal - prev.HTTP5xxTotal
	rule := []struct {
		class string
		over  bool
		under bool
	}{
		{"cpu_sustained", quota > 0 && stat.CPUPercent > 0.90*quota*100, quota > 0 && stat.CPUPercent < 0.75*quota*100},
		{"memory_sustained", stat.MemLimitBytes > 0 && float64(stat.MemBytes)/float64(stat.MemLimitBytes) > 0.85, stat.MemLimitBytes > 0 && float64(stat.MemBytes)/float64(stat.MemLimitBytes) < 0.70},
		{"latency_sustained", snap.Samples >= 20 && snap.LatencyP95MS > 5000, snap.Samples >= 20 && snap.LatencyP95MS < 3000},
		{"http_5xx_sustained", reqDelta >= 20 && float64(errDelta)/float64(reqDelta) > 0.05, reqDelta >= 20 && float64(errDelta)/float64(reqDelta) < 0.02},
		{"session_saturated", snap.ActiveRuns >= 5, snap.ActiveRuns <= 3},
		{"mcp_saturated", snap.MCPInflight >= 16, snap.MCPInflight <= 8},
	}
	for _, r := range rule {
		if r.over {
			entry.metricBreaches[r.class]++
		} else if r.under {
			entry.metricBreaches[r.class] = 0
		}
		if entry.metricBreaches[r.class] == metricBreachLimit && !protected {
			m.recordIncidentLocked(entry, r.class)
		}
	}
}
