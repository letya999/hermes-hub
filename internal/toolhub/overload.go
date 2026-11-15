package toolhub

import (
	"log"
	"net/http"
	"sync/atomic"
	"time"
)

// Overload is explicit state: interactive request latency is the protected
// path, background traffic sheds cheaply before work is admitted. The gate
// self-recovers — a quiet interactive path lowers the EMA and background
// traffic resumes (SRE "Handling Overload"; the producer applies the
// matching adaptive throttle in internal/communication).
const (
	// interactiveEMAThresholdNs: capture storms pushed decide from 1-3ms
	// into tens of seconds; 40ms is an order above normal, an order below
	// user-visible damage.
	interactiveEMAThresholdNs = int64(40 * time.Millisecond)
	// backgroundInFlightCap bounds parallel background handlers; producers
	// post serially today, the cap covers parallel producers later.
	backgroundInFlightCap = int32(4)
	// emaShift is the EMA smoothing shift (alpha = 1/8).
	emaShift = 3
	// shedRetryAfterSeconds is the producer's hint to let the EMA decay.
	shedRetryAfterSeconds = "5"
)

type loadGate struct {
	interactiveInflight atomic.Int32
	interactiveEMA      atomic.Int64 // nanoseconds; 0 = no samples yet
	backgroundInflight  atomic.Int32
	shedding            atomic.Bool // rising-edge marker for the log line
}

func (l *loadGate) overloaded() bool {
	ema := l.interactiveEMA.Load()
	return l.backgroundInflight.Load() > backgroundInFlightCap ||
		(ema > 0 && ema > interactiveEMAThresholdNs)
}

// interactive wraps a request whose latency is protected. Its duration
// feeds the EMA every background admission decision reads.
func (l *loadGate) interactive(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		l.interactiveInflight.Add(1)
		start := time.Now()
		defer func() {
			d := time.Since(start).Nanoseconds()
			l.interactiveInflight.Add(-1)
			for {
				ema := l.interactiveEMA.Load()
				nextEMA := d
				if ema > 0 {
					nextEMA = ema - (ema >> emaShift) + (d >> emaShift)
				}
				if l.interactiveEMA.CompareAndSwap(ema, nextEMA) {
					return
				}
			}
		}()
		next.ServeHTTP(w, r)
	})
}

// background wraps work that must lose to interactive traffic: when the
// gate is overloaded the request sheds before doing any work. Refusing
// early is the only refusal that is actually cheap (SRE ch.21).
func (l *loadGate) background(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if l.overloaded() {
			if l.shedding.CompareAndSwap(false, true) {
				log.Printf("toolhub overloaded: background traffic shedding (interactive_ema_ms=%d background_inflight=%d)",
					l.interactiveEMA.Load()/int64(time.Millisecond), l.backgroundInflight.Load())
			}
			w.Header().Set("Retry-After", shedRetryAfterSeconds)
			http.Error(w, "toolhub overloaded", http.StatusTooManyRequests)
			return
		}
		l.shedding.Store(false)
		l.backgroundInflight.Add(1)
		defer l.backgroundInflight.Add(-1)
		next.ServeHTTP(w, r)
	})
}

// Overloaded reports the serving plane's shed state; the in-process
// automation host reads it to defer tick passes while the interactive
// path is hot.
func (g *Gateway) Overloaded() bool {
	return g.gate().overloaded()
}

// gate returns this gateway's load gate, built on first use so literal
// &Gateway{} constructions (tests, relays) need no wiring.
func (g *Gateway) gate() *loadGate {
	if v := g.loadGate.Load(); v != nil {
		return v
	}
	v := &loadGate{}
	if g.loadGate.CompareAndSwap(nil, v) {
		return v
	}
	return g.loadGate.Load()
}
