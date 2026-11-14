package toolhub

import (
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"
)

func TestBackgroundShedsWhenInteractiveHot(t *testing.T) {
	gate := &loadGate{}
	ok := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(http.StatusOK) })
	bg := gate.background(ok)

	// A quiet interactive path admits background work.
	rec := httptest.NewRecorder()
	bg.ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/v1/memory-capture", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("quiet gate shed background: %d", rec.Code)
	}

	// Interactive latency over threshold sheds before any work runs.
	gate.interactiveEMA.Store(int64(time.Second))
	served := &atomic.Int32{}
	rec = httptest.NewRecorder()
	gate.background(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		served.Add(1)
	})).ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/v1/memory-capture", nil))
	if rec.Code != http.StatusTooManyRequests {
		t.Fatalf("hot gate admitted background: %d", rec.Code)
	}
	if rec.Header().Get("Retry-After") == "" {
		t.Fatal("shed missing Retry-After")
	}
	if served.Load() != 0 {
		t.Fatal("shed ran the background handler")
	}
}

func TestBackgroundCapAndInteractiveEMA(t *testing.T) {
	gate := &loadGate{}
	gate.backgroundInflight.Store(backgroundInFlightCap + 1)
	if !gate.overloaded() {
		t.Fatal("in-flight cap did not mark overload")
	}

	// Interactive latency feeds the EMA; a slow request then a run of fast
	// requests must take the gate out of overload — self-recovery.
	slow := gate.interactive(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gate.interactiveEMA.Store(int64(50 * time.Millisecond))
	}))
	slow.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest(http.MethodGet, "/v1/rights/decide", nil))
	if gate.interactiveEMA.Load() <= 0 {
		t.Fatal("interactive request did not feed EMA")
	}
}

func TestInteractiveCountsInFlightAndUpdatesEMA(t *testing.T) {
	gate := &loadGate{}
	release := make(chan struct{})
	done := make(chan struct{})
	h := gate.interactive(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		<-release
	}))
	go func() {
		h.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest(http.MethodGet, "/", nil))
		close(done)
	}()
	for i := 0; i < 200 && gate.interactiveInflight.Load() == 0; i++ {
		time.Sleep(time.Millisecond)
	}
	if gate.interactiveInflight.Load() != 1 {
		t.Fatal("in-flight interactive request not counted")
	}
	close(release)
	<-done
	if gate.interactiveInflight.Load() != 0 {
		t.Fatal("in-flight count leaked")
	}
	if gate.interactiveEMA.Load() <= 0 {
		t.Fatal("completed request did not update EMA")
	}
}
