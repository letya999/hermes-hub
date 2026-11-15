package communication

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"log"
	"math"
	"math/rand"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/letya999/hermes-hub/internal/commodel"
	"github.com/letya999/hermes-hub/internal/toolhub/api"
)

// Capture outbox (SPEC-0098 M4): accepted turns and completed-run facts are
// durable spool records under capture/ before the enqueue/delivery
// acknowledges — the producer never writes a vault. Each event posts to
// ToolHub under the service credential and is acked only after ToolHub
// reports the commit or a permanent denial; a lost acknowledgement, an
// outage or a restart leaves the file for the next drain, and ToolHub's
// dedup on producer/event ID makes the replay write once.

// captureEventForTurn mints the accepted-turn record from the durable job —
// every field the endpoint can't verify itself is re-classified there.
func captureEventForTurn(job Job) api.CaptureEvent {
	return api.CaptureEvent{
		Producer: "communication", EventID: "turn-" + job.ID, Kind: "turn",
		JobID: job.ID, UserID: job.UserID, PrincipalID: job.PrincipalID,
		ScopeID: job.ScopeID, Trigger: job.Trigger,
		Assignment: job.AssignmentID != "", Probe: job.Probe,
		Sensitive: job.Sensitive, Untrusted: job.Untrusted,
		Channel: job.Channel, Body: job.Text, ObservedAt: job.CreatedAt,
	}
}

// captureEventForReply mints the completed-run record. The run id comes
// from the durable outcome/mapping — the terminal fact, not the wire.
func captureEventForReply(job Job, outcome commodel.RunOutcome, response string, now time.Time) api.CaptureEvent {
	return api.CaptureEvent{
		Producer: "communication", EventID: "reply-" + job.ID, Kind: "reply",
		JobID: job.ID, RunID: outcome.RunID, UserID: job.UserID, PrincipalID: job.PrincipalID,
		ScopeID: job.ScopeID, Trigger: job.Trigger,
		Assignment: job.AssignmentID != "", Probe: job.Probe,
		Sensitive: job.Sensitive, Untrusted: job.Untrusted,
		Channel: job.Channel, Body: response, ObservedAt: now.UTC(),
	}
}

// capturePath is the event's durable outbox file; spoolFileID keeps a
// forged-length id inside the record class.
func (s *Spool) capturePath(eventID string) string {
	return filepath.Join(s.root, "capture", spoolFileID(eventID)+".json")
}

func (s *Spool) captureAckPath(eventID string) string {
	return filepath.Join(s.root, "capture-acks", spoolFileID(eventID)+".json")
}

func (s *Spool) captureSettled(eventID string) (bool, error) {
	_, err := s.fsys.Lstat(s.captureAckPath(eventID))
	return err == nil, err
}

// writeCaptureLocked persists one event under the spool lock — the
// accepted-turn path runs inside Enqueue, where the lock is already held.
func (s *Spool) writeCaptureLocked(ev api.CaptureEvent) error {
	if settled, err := s.captureSettled(ev.EventID); err == nil && settled {
		return nil
	} else if err != nil && !os.IsNotExist(err) {
		return err
	}
	if _, err := s.fsys.Lstat(s.capturePath(ev.EventID)); err == nil {
		return nil
	} else if !os.IsNotExist(err) {
		return err
	}
	return s.writeJSON(s.capturePath(ev.EventID), ev)
}

// EnqueueCapture is the unlocked variant for the completed-run path, which
// produces events outside the enqueue critical section.
func (s *Spool) EnqueueCapture(ev api.CaptureEvent) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.writeCaptureLocked(ev)
}

// pendingCaptures lists the durable outbox in stable order. A record that no
// longer decodes is left in place — operator triage, never silent loss.
func (s *Spool) pendingCaptures() ([]api.CaptureEvent, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	names, err := s.fsys.ReadDir(filepath.Join(s.root, "capture"))
	if err != nil {
		return nil, err
	}
	sort.Strings(names)
	events := make([]api.CaptureEvent, 0, len(names))
	for _, name := range names {
		if !strings.HasSuffix(name, ".json") {
			continue
		}
		body, err := s.fsys.ReadFile(filepath.Join(s.root, "capture", name))
		if err != nil {
			return nil, err
		}
		var ev api.CaptureEvent
		if err := json.Unmarshal(body, &ev); err != nil || ev.EventID == "" {
			continue
		}
		if settled, err := s.captureSettled(ev.EventID); err == nil && settled {
			continue
		} else if err != nil && !os.IsNotExist(err) {
			return nil, err
		}
		events = append(events, ev)
	}
	return events, nil
}

// ackCapture marks a committed or permanently denied event as settled before
// dropping the outbox copy. A transient failure never reaches this path.
func (s *Spool) ackCapture(eventID string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := s.writeJSON(s.captureAckPath(eventID), eventID); err != nil {
		return err
	}
	return s.fsys.Remove(s.capturePath(eventID))
}

// A job or terminal receipt may survive a crash just before its capture
// outbox write. Rebuild from those durable facts; settled IDs stay settled.
func (g *Gateway) recoverCaptureOutbox() error {
	for _, dir := range []string{"pending", "running", "done", "failed"} {
		names, err := g.spool.fsys.ReadDir(filepath.Join(g.spool.root, dir))
		if err != nil {
			return err
		}
		for _, name := range names {
			if !strings.HasSuffix(name, ".json") {
				continue
			}
			body, err := g.spool.fsys.ReadFile(filepath.Join(g.spool.root, dir, name))
			if err != nil {
				return err
			}
			var job Job
			if strictEventJSON(body, &job) != nil || job.ID == "" {
				return api.ErrInvalid
			}
			if !job.CaptureExpected {
				continue
			}
			if err := g.captureTurn(job); err != nil {
				return err
			}
			if dir != "done" {
				continue
			}
			id := "reply-" + job.ID
			if settled, err := g.spool.captureSettled(id); err == nil && settled {
				continue
			} else if err != nil && !os.IsNotExist(err) {
				return err
			}
			if _, err := g.spool.fsys.Lstat(g.spool.capturePath(id)); err == nil {
				continue
			} else if !os.IsNotExist(err) {
				return err
			}
			mapping, found, err := g.spool.Mapping(job.ID)
			if err != nil {
				return err
			}
			if !found || mapping.Status != "completed" || mapping.LastEventID == "" {
				continue
			}
			outcome, err := g.spool.savedTerminalOutcome(mapping)
			if err != nil {
				return err
			}
			if err := g.captureReply(job, outcome); err != nil {
				return err
			}
		}
	}
	return nil
}

// capturePump is the outbox drain loop: a new event kicks an immediate
// pass, the ticker sweeps whatever a crash or outage left pending.
func (g *Gateway) capturePump(ctx context.Context) {
	ticker := time.NewTicker(5 * time.Second)
	defer ticker.Stop()
	g.pumpCaptures(ctx)
	for {
		select {
		case <-ctx.Done():
			return
		case <-g.captureKick:
			g.pumpCaptures(ctx)
		case <-ticker.C:
			g.pumpCaptures(ctx)
		}
	}
}

// kickCapture wakes the pump without blocking the producer.
func (g *Gateway) kickCapture() {
	if g.captureKick == nil {
		return
	}
	select {
	case g.captureKick <- struct{}{}:
	default:
	}
}

// Adaptive-throttle constants (SRE "Handling Overload", client-side
// throttling): the producer stops pushing once requests outrun accepts by
// the K multiplier, rejects locally instead of charging a saturated
// backend for turning work away. The window decays over roughly two
// minutes of traffic; P is capped below 1 so the pass always keeps a
// probe alive to notice recovery.
const (
	captureThrottleK      = 2.0
	captureThrottleWindow = 2 * time.Minute
	captureThrottleMaxP   = 0.9
	// captureThrottleMinReq: the ratio needs a minimum sample — a cold
	// window (requests=1, accepts=0) would otherwise shed on the prior
	// alone, before the pass learned anything about the backend.
	captureThrottleMinReq = 8.0
	// captureFailStop ends a pass after this many consecutive rejects or
	// transport failures; the sweep ticker retries.
	captureFailStop = 3
	// captureBacklogLogDepth is the depth that makes a pass log its
	// backlog — the outbox is observable before latency is.
	captureBacklogLogDepth = 32
	// captureMaxAge settles an event whose commit window is long dead:
	// the fact it carries references a job nobody can recover, and an
	// endless retry is a wedge, not durability. The ack record remains
	// the audit trail (recorded intentional non-outcome).
	captureMaxAge = 24 * time.Hour
)

// captureThrottled reports whether the pump should shed this post locally.
// Locally-shed attempts still count as requests — pushing harder against a
// refusing backend only tightens the throttle.
func (g *Gateway) captureThrottled(now time.Time, rand float64) bool {
	t := &g.captureThrottle
	if !t.last.IsZero() {
		f := math.Exp(-float64(now.Sub(t.last)) / float64(captureThrottleWindow))
		t.requests *= f
		t.accepts *= f
	}
	t.last = now
	t.requests++
	if t.requests < captureThrottleMinReq {
		return false
	}
	p := (t.requests - captureThrottleK*t.accepts) / (t.requests + 1)
	if p > captureThrottleMaxP {
		p = captureThrottleMaxP
	}
	return p > 0 && rand < p
}

// captureThrottleRecord scores one posted request: a definitive non-
// overload response counts as an accept, transport errors, 5xx and 429 do
// not.
func (g *Gateway) captureThrottleRecord(accepted bool) {
	if accepted {
		g.captureThrottle.accepts++
	}
}

// pumpCaptures drains the durable capture outbox one pass: each pending
// event is posted to ToolHub and acked only on a committed or permanently
// settled response. The pass stops early when ToolHub is shedding or the
// throttle closes — pending events survive for the next sweep.
func (g *Gateway) pumpCaptures(ctx context.Context) {
	base := strings.TrimRight(strings.TrimSpace(g.config.ToolHubURL), "/")
	if base == "" {
		return
	}
	events, err := g.spool.pendingCaptures()
	if err != nil {
		log.Print("capture outbox read failed")
		return
	}
	if len(events) > captureBacklogLogDepth {
		log.Printf("capture outbox depth=%d oldest=%ds", len(events), int(time.Since(events[0].ObservedAt).Seconds()))
	}
	failures := 0
	for i, ev := range events {
		if ctx.Err() != nil {
			return
		}
		if g.captureThrottled(time.Now(), rand.Float64()) { // #nosec G404 -- throttle jitter, not a security decision.
			break
		}
		if time.Since(ev.ObservedAt) > captureMaxAge {
			if err := g.spool.ackCapture(ev.EventID); err != nil {
				log.Print("capture expiry acknowledgement failed")
			}
			continue // settled as expired — recorded non-outcome
		}
		token, header := g.captureCredential(ev.UserID)
		if token == "" {
			continue // no enrolled credential yet — retry on the next sweep
		}
		status, err := g.postCapture(ctx, base, token, header, ev)
		switch {
		case err != nil || status == http.StatusTooManyRequests:
			// Systemic refusal — dead backend or shed gate. Count toward
			// fail-stop; a per-event status never blocks the siblings
			// behind it (a wedged event must not poison the whole pass).
			g.captureThrottleRecord(false)
			failures++
		case status >= 200 && status < 300 || status == http.StatusBadRequest || status == http.StatusForbidden:
			g.captureThrottleRecord(true)
			failures = 0
			if err := g.spool.ackCapture(ev.EventID); err != nil {
				log.Print("capture acknowledgement failed")
			}
		default:
			// Per-event refusal (stale run, unavailable commit) — stays
			// for the next sweep without holding up the rest of the pass.
			g.captureThrottleRecord(false)
		}
		if failures >= captureFailStop {
			log.Printf("capture outbox pass stopped after %d consecutive refusals, %d events still pending", failures, len(events)-i)
			return
		}
	}
}

// postCapture submits one durable event; the response code — not the wire
// body — decides whether the producer may ack its outbox record.
func (g *Gateway) postCapture(ctx context.Context, base, token, header string, ev api.CaptureEvent) (int, error) {
	body, err := json.Marshal(ev)
	if err != nil {
		return 0, err
	}
	reqCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(reqCtx, http.MethodPost, base+"/v1/memory-capture", bytes.NewReader(body))
	if err != nil {
		return 0, err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+token)
	if header != "" {
		req.Header.Set("X-Hub-Principal", header)
	}
	client := g.captureHTTP
	if client == nil {
		client = http.DefaultClient
	}
	resp, err := client.Do(req)
	if err != nil {
		return 0, err
	}
	defer resp.Body.Close()
	_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 4096))
	return resp.StatusCode, nil
}

// captureCredential uses only the supervised communication service bearer;
// a model's per-principal token cannot mint a transport fact.
func (g *Gateway) captureCredential(userID string) (token, principalHeader string) {
	if g.config.Supervised && g.user(userID).ID == userID {
		return strings.TrimSpace(g.config.RuntimeAuth), userID
	}
	return "", ""
}
