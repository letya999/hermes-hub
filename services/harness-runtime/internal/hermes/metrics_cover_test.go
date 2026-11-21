package hermes

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

// /v1/status publishes the bounded counter snapshot the supervisor's
// metrics watcher scrapes — the same path the live acceptance exercised.
func TestRuntimeMetricsSnapshotAndStatus(t *testing.T) {
	m := &runtimeMetrics{}
	for i, ms := range []time.Duration{10, 20, 30, 300, 40} {
		status := 200
		if i == 3 {
			status = 503
		}
		m.recordRequest(ms*time.Millisecond, status)
	}
	m.mcpDelta(2)
	m.mcpDelta(-5) // clamped at zero? no: 2-5 -> 0
	snap := m.snapshot()
	if snap.RequestsTotal != 5 || snap.HTTP5xxTotal != 1 || snap.Samples != 5 {
		t.Fatalf("snapshot: %+v", snap)
	}
	// sorted[95*(5-1)/100] = index 3 → the 4th smallest sample.
	if snap.LatencyP95MS != 40 {
		t.Fatalf("p95 = %d want 40", snap.LatencyP95MS)
	}
	if snap.MCPInflight != 0 {
		t.Fatalf("mcpInflight clamp: %d", snap.MCPInflight)
	}
	// Empty snapshot is a valid zero-state response.
	if empty := (&runtimeMetrics{}).snapshot(); empty.Samples != 0 || empty.LatencyP95MS != 0 {
		t.Fatalf("empty snapshot: %+v", empty)
	}
}

func TestStatusEndpointServesSnapshot(t *testing.T) {
	t.Setenv("HUB_RUNTIME_AUTH", "tok")
	s := &runtimeHTTP{runJobs: map[string]string{"j1": "r1"}, pending: 1}
	s.metrics().recordRequest(5*time.Millisecond, 200)
	req := httptest.NewRequest(http.MethodGet, "/v1/status", nil)
	rec := httptest.NewRecorder()
	s.status(rec, req)
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("unauthenticated status: %d", rec.Code)
	}
	req = httptest.NewRequest(http.MethodGet, "/v1/status", nil)
	req.Header.Set("Authorization", "Bearer tok")
	rec = httptest.NewRecorder()
	s.status(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("status: %d", rec.Code)
	}
	var got RuntimeStatus
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatal(err)
	}
	if got.RequestsTotal != 1 || got.LatencyP95MS != 5 || got.ActiveRuns != 2 {
		t.Fatalf("status body: %+v", got)
	}
	// Non-GET is denied.
	rec = httptest.NewRecorder()
	s.status(rec, httptest.NewRequest(http.MethodPost, "/v1/status", nil))
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("POST status: %d", rec.Code)
	}
}
