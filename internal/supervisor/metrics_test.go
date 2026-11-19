package supervisor

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	hubruntime "github.com/letya999/hermes-hub/internal/runtime"
)

// metricsPolicy renders the space supervision file the way `space apply`
// writes it: presence of the metrics key opts the watcher in.
func metricsPolicy(t *testing.T, root string, watcher string) {
	t.Helper()
	body := `{"watchers":{"metrics":` + watcher + `}}`
	if err := os.WriteFile(filepath.Join(root, "runtime.prod.policy.json"), []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
}

// statusServer is the runtime's bounded /v1/status plus the health probe the
// health sweep needs: the watcher scrapes the same document a real runtime
// publishes, counted so tests can prove cadence and opt-out.
type statusServer struct {
	*httptest.Server
	calls  *atomic.Int64
	status atomic.Value // hubruntime.RuntimeStatus
}

func newStatusServer(t *testing.T) *statusServer {
	t.Helper()
	calls := &atomic.Int64{}
	srv := &statusServer{calls: calls}
	srv.status.Store(hubruntime.RuntimeStatus{RequestsTotal: 100, HTTP5xxTotal: 0, LatencyP95MS: 10, Samples: 50, ActiveRuns: 0, MCPInflight: 0})
	srv.Server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/v1/status":
			calls.Add(1)
			_ = json.NewEncoder(w).Encode(srv.status.Load())
		case "/v1/health":
			_ = json.NewEncoder(w).Encode(hubruntime.RuntimeHealth{HermesReadiness: "ready", ConnectorHealth: "not_configured", ExternalConnections: "not_probed"})
		default:
			w.WriteHeader(http.StatusOK)
		}
	}))
	t.Cleanup(srv.Close)
	return srv
}

func (s *statusServer) set(status hubruntime.RuntimeStatus) { s.status.Store(status) }

func metricsEntry(t *testing.T, m *Manager, root string) (*runtimeEntry, string) {
	t.Helper()
	b := binding(root)
	if _, err := m.Ensure(context.Background(), b); err != nil {
		t.Fatal(err)
	}
	key := runtimeKey(b)
	m.mu.Lock()
	entry := m.items[key]
	m.mu.Unlock()
	if entry == nil {
		t.Fatal("no entry after Ensure")
	}
	return entry, key
}

// A sustained latency breach opens exactly one incident on the third
// consecutive observation — further breaches refresh it, never re-fire —
// and hysteresis plus a healthy window re-arms the transition.
func TestMetricsWatcherSustainedIncident(t *testing.T) {
	m, root := testManager(t, func(_ context.Context, args ...string) ([]byte, error) {
		if args[0] == "inspect" {
			return nil, os.ErrNotExist
		}
		return []byte("running"), nil
	}, func(context.Context, string, string) error { return nil })
	server := newStatusServer(t)
	metricsPolicy(t, root, `{}`)
	entry, key := metricsEntry(t, m, root)
	m.mu.Lock()
	entry.Address, entry.State, entry.auth = server.URL, Busy, "control"
	m.mu.Unlock()
	server.set(hubruntime.RuntimeStatus{RequestsTotal: 200, HTTP5xxTotal: 0, LatencyP95MS: 6000, Samples: 50})
	now := m.cfg.Now()
	for i := 0; i < 2; i++ {
		m.ReconcileHealth(context.Background(), now.Add(time.Duration(i)*time.Second))
	}
	m.mu.Lock()
	if got := len(m.items[key].incidents); got != 0 {
		t.Fatalf("two breaches opened an incident: %v", m.items[key].incidents)
	}
	breaches := m.items[key].metricBreaches["latency_sustained"]
	m.mu.Unlock()
	if breaches != 2 {
		t.Fatalf("breaches=%d, want 2", breaches)
	}
	for i := 0; i < 4; i++ {
		m.ReconcileHealth(context.Background(), now.Add(time.Duration(3+i)*time.Second))
	}
	m.mu.Lock()
	incident, ok := m.items[key].incidents["latency_sustained"]
	total := len(m.items[key].incidents)
	m.mu.Unlock()
	if !ok || incident.Count != 1 || total != 1 {
		t.Fatalf("incident=%+v total=%d", incident, total)
	}
	blob, err := os.ReadFile(m.signalPath())
	if err != nil || !strings.Contains(string(blob), `"class":"latency_sustained"`) {
		t.Fatalf("signals=%q err=%v", blob, err)
	}
	// Healthy window resets the counter; a fresh breach re-fires exactly once.
	server.set(hubruntime.RuntimeStatus{RequestsTotal: 300, LatencyP95MS: 100, Samples: 50})
	m.ReconcileHealth(context.Background(), now.Add(10*time.Second))
	server.set(hubruntime.RuntimeStatus{RequestsTotal: 400, LatencyP95MS: 6000, Samples: 50})
	for i := 0; i < 3; i++ {
		m.ReconcileHealth(context.Background(), now.Add(time.Duration(11+i)*time.Second))
	}
	m.mu.Lock()
	incident = m.items[key].incidents["latency_sustained"]
	m.mu.Unlock()
	if incident.Count != 2 {
		t.Fatalf("re-armed anomaly did not re-fire: %+v", incident)
	}
}

// The watcher is opt-in: without the metrics key no scrape ever happens,
// and `protected` counts breaches without ever waking anyone.
func TestMetricsWatcherOptInAndProtected(t *testing.T) {
	build := func(t *testing.T, policy string) (*Manager, *statusServer, string, string) {
		m, root := testManager(t, func(_ context.Context, args ...string) ([]byte, error) {
			if args[0] == "inspect" {
				return nil, os.ErrNotExist
			}
			return []byte("running"), nil
		}, func(context.Context, string, string) error { return nil })
		server := newStatusServer(t)
		server.set(hubruntime.RuntimeStatus{RequestsTotal: 200, LatencyP95MS: 6000, Samples: 50})
		if policy != "" {
			metricsPolicy(t, root, policy)
		}
		entry, key := metricsEntry(t, m, root)
		m.mu.Lock()
		entry.Address, entry.State, entry.auth = server.URL, Busy, "control"
		m.mu.Unlock()
		return m, server, root, key
	}
	// No metrics key — zero scrapes no matter how anomalous the runtime looks.
	m, server, _, _ := build(t, "")
	for i := 0; i < 3; i++ {
		m.ReconcileHealth(context.Background(), m.cfg.Now().Add(time.Duration(i)*time.Second))
	}
	if got := server.calls.Load(); got != 0 {
		t.Fatalf("disabled watcher scraped %d times", got)
	}
	// Protected observes and counts but opens no incident.
	m, server, _, key := build(t, `{"protected":true}`)
	for i := 0; i < 4; i++ {
		m.ReconcileHealth(context.Background(), m.cfg.Now().Add(time.Duration(i)*time.Second))
	}
	m.mu.Lock()
	incidents := len(m.items[key].incidents)
	breaches := m.items[key].metricBreaches["latency_sustained"]
	m.mu.Unlock()
	if incidents != 0 {
		t.Fatalf("protected watcher opened incidents")
	}
	if breaches != 4 || server.calls.Load() != 4 {
		t.Fatalf("protected observer: breaches=%d calls=%d", breaches, server.calls.Load())
	}
	// A scrape failure is a skipped observation, never an incident.
	m, server, _, key = build(t, `{"every":"1h"}`)
	server.Close()
	m.ReconcileHealth(context.Background(), m.cfg.Now())
	m.mu.Lock()
	if got := len(m.items[key].incidents); got != 0 {
		t.Fatalf("failed scrape opened %d incidents", got)
	}
	m.mu.Unlock()
}

// `every` bounds scrape cadence: a not-yet-due entry is skipped without an
// HTTP call.
func TestMetricsWatcherCadence(t *testing.T) {
	m, root := testManager(t, func(_ context.Context, args ...string) ([]byte, error) {
		if args[0] == "inspect" {
			return nil, os.ErrNotExist
		}
		return []byte("running"), nil
	}, func(context.Context, string, string) error { return nil })
	server := newStatusServer(t)
	metricsPolicy(t, root, `{"every":"1h"}`)
	entry, _ := metricsEntry(t, m, root)
	m.mu.Lock()
	entry.Address, entry.State, entry.auth = server.URL, Busy, "control"
	m.mu.Unlock()
	now := m.cfg.Now()
	m.ReconcileHealth(context.Background(), now)
	m.ReconcileHealth(context.Background(), now.Add(30*time.Minute))
	if got := server.calls.Load(); got != 1 {
		t.Fatalf("cadence not honored: %d scrapes", got)
	}
	m.ReconcileHealth(context.Background(), now.Add(61*time.Minute))
	if got := server.calls.Load(); got != 2 {
		t.Fatalf("due re-scrape skipped: %d scrapes", got)
	}
}

// Rule boundaries at unit level: each class breaches only inside its
// threshold + volume guard, and the dead band holds the counter.
func TestMetricsRuleBoundaries(t *testing.T) {
	m, _ := testManager(t, func(context.Context, ...string) ([]byte, error) { return nil, nil }, func(context.Context, string, string) error { return nil })
	m.cfg.CPU = "2"
	entry := &runtimeEntry{Runtime: Runtime{PrincipalID: "alice", ContextID: "alice", State: Busy}, metricBreaches: map[string]int{}}
	evaluate := func(stat ContainerStat, snap hubruntime.RuntimeStatus) map[string]int {
		entry.lastStat = stat
		m.evaluateRuntimeMetricsLocked(entry, snap, false)
		return entry.metricBreaches
	}
	// CPU: >90% of a 2-core quota breaches; between 75% and 90% holds; <75% clears.
	if got := evaluate(ContainerStat{CPUPercent: 190}, hubruntime.RuntimeStatus{})["cpu_sustained"]; got != 1 {
		t.Fatalf("cpu breach=%d", got)
	}
	if got := evaluate(ContainerStat{CPUPercent: 160}, hubruntime.RuntimeStatus{})["cpu_sustained"]; got != 1 {
		t.Fatalf("dead band reset the counter: %d", got)
	}
	if got := evaluate(ContainerStat{CPUPercent: 100}, hubruntime.RuntimeStatus{})["cpu_sustained"]; got != 0 {
		t.Fatalf("healthy cpu held breach: %d", got)
	}
	// Memory ratio against the cgroup limit; absent limit never fires.
	if got := evaluate(ContainerStat{MemBytes: 900, MemLimitBytes: 1000}, hubruntime.RuntimeStatus{})["memory_sustained"]; got != 1 {
		t.Fatalf("mem breach=%d", got)
	}
	entry.metricBreaches = map[string]int{}
	if got := evaluate(ContainerStat{MemBytes: 900}, hubruntime.RuntimeStatus{})["memory_sustained"]; got != 0 {
		t.Fatalf("limit-less memory fired: %d", got)
	}
	// 5xx: volume guard + ratio; insufficient volume holds.
	entry.metricBreaches = map[string]int{}
	if got := evaluate(ContainerStat{}, hubruntime.RuntimeStatus{RequestsTotal: 10, HTTP5xxTotal: 10})["http_5xx_sustained"]; got != 0 {
		t.Fatalf("thin traffic fired: %d", got)
	}
	if got := evaluate(ContainerStat{}, hubruntime.RuntimeStatus{RequestsTotal: 30, HTTP5xxTotal: 12})["http_5xx_sustained"]; got != 1 {
		t.Fatalf("5xx breach=%d", got)
	}
	// Latency needs >=20 samples; sessions and mcp_inflight fire on ceilings.
	entry.metricBreaches = map[string]int{}
	if got := evaluate(ContainerStat{}, hubruntime.RuntimeStatus{LatencyP95MS: 9000, Samples: 5})["latency_sustained"]; got != 0 {
		t.Fatalf("thin-sample p95 fired: %d", got)
	}
	if got := evaluate(ContainerStat{}, hubruntime.RuntimeStatus{ActiveRuns: 5})["session_saturated"]; got != 1 {
		t.Fatalf("session breach=%d", got)
	}
	if got := evaluate(ContainerStat{}, hubruntime.RuntimeStatus{MCPInflight: 16})["mcp_saturated"]; got != 1 {
		t.Fatalf("mcp breach=%d", got)
	}
}
