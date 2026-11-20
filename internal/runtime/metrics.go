package runtime

import (
	"encoding/json"
	"net/http"
	"sort"
	"sync"
	"time"
)

// runtimeMetrics is the bounded operational view the supervisor's metrics
// watcher scrapes: request/5xx counters, a fixed ring of request durations
// and the MCP proxy inflight gauge. Everything here is allowlisted —
// counters only, never payloads, paths or identities.
type runtimeMetrics struct {
	mu          sync.Mutex
	requests    int64
	errors5xx   int64
	ring        [256]int64 // request durations in milliseconds
	head        int
	count       int
	mcpInflight int64
}

// statusRecorder captures the response status so the watcher sees real 5xx
// rates; Unwrap keeps streaming endpoints (ResponseController Flush/Hijack)
// working through the wrapper.
type statusRecorder struct {
	http.ResponseWriter
	status int
}

func (r *statusRecorder) WriteHeader(code int) {
	r.status = code
	r.ResponseWriter.WriteHeader(code)
}

func (r *statusRecorder) Unwrap() http.ResponseWriter { return r.ResponseWriter }

func (m *runtimeMetrics) recordRequest(dur time.Duration, status int) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.requests++
	if status >= 500 {
		m.errors5xx++
	}
	m.ring[m.head] = dur.Milliseconds()
	m.head = (m.head + 1) % len(m.ring)
	if m.count < len(m.ring) {
		m.count++
	}
}

func (m *runtimeMetrics) mcpDelta(delta int64) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.mcpInflight += delta
	if m.mcpInflight < 0 {
		m.mcpInflight = 0
	}
}

// RuntimeStatus is the bounded JSON contract of GET /v1/status — fixed
// allowlisted fields only; the supervisor decodes the same shape.
type RuntimeStatus struct {
	RequestsTotal int64 `json:"requests_total"`
	HTTP5xxTotal  int64 `json:"http_5xx_total"`
	LatencyP95MS  int64 `json:"latency_p95_ms"`
	Samples       int   `json:"samples"`
	ActiveRuns    int   `json:"active_runs"`
	MCPInflight   int64 `json:"mcp_inflight"`
}

func (m *runtimeMetrics) snapshot() RuntimeStatus {
	m.mu.Lock()
	defer m.mu.Unlock()
	status := RuntimeStatus{RequestsTotal: m.requests, HTTP5xxTotal: m.errors5xx, Samples: m.count, MCPInflight: m.mcpInflight}
	if m.count > 0 {
		sorted := make([]int64, m.count)
		for i := 0; i < m.count; i++ {
			sorted[i] = m.ring[i]
		}
		sort.Slice(sorted, func(i, j int) bool { return sorted[i] < sorted[j] })
		status.LatencyP95MS = sorted[(95*(m.count-1))/100]
	}
	return status
}

// metrics returns the handler's counter set, created on first use.
func (s *runtimeHTTP) metrics() *runtimeMetrics {
	s.runMu.Lock()
	defer s.runMu.Unlock()
	if s.counters == nil {
		s.counters = &runtimeMetrics{}
	}
	return s.counters
}

// activeRuns is the only honest session gauge the runtime owns: bound run
// jobs plus admissions waiting on a lane — no invented session count.
func (s *runtimeHTTP) activeRuns() int {
	s.runMu.Lock()
	defer s.runMu.Unlock()
	return len(s.runJobs) + s.pending
}

// status serves GET /v1/status with the bounded counter snapshot under the
// same runtime bearer every other v1 endpoint requires.
func (s *runtimeHTTP) status(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet || !s.authorized(r) {
		writeRuntimeError(w, http.StatusUnauthorized, "unauthorized")
		return
	}
	snapshot := s.metrics().snapshot()
	snapshot.ActiveRuns = s.activeRuns()
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(snapshot)
}
