package supervisor

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"os"
	"strings"
	"time"
)

// Personal supervisor (#328): an optional, event-driven reviewer for one
// principal's retained runtime. cfg.Decide is the model seam — nil means no
// model calls ever happen, and the deterministic watchdog (#326) plus master
// containment (#329) never depend on it. Requests carry allowlisted fields
// only; decisions are validated server-side before anything applies.
type PersonalRequest struct {
	PrincipalID string          `json:"principal_id"`
	ContextID   string          `json:"context_id"`
	RuntimeID   string          `json:"runtime_id"`
	Generation  string          `json:"generation"`
	State       State           `json:"state"`
	Incidents   []Incident      `json:"incidents"`
	Consumers   []LeaseConsumer `json:"consumers,omitempty"`
	Mandate     int             `json:"mandate"` // remaining decision budget for this chain
}

// PersonalDecision is one narrow, server-validated operation on the scope
// that woke the supervisor. It can never retarget another scope, extend a
// lease past maxPersonalRenew, or chain more than maxPersonalReviews wakes.
type PersonalDecision struct {
	Action     string    `json:"action"` // "", inspect, renew, pause, restart, escalate
	LeaseID    string    `json:"lease_id,omitempty"`
	RenewUntil time.Time `json:"renew_until,omitempty"`
	Reason     string    `json:"reason,omitempty"`
	Review     bool      `json:"review,omitempty"` // request one bounded follow-up wake
}

const (
	maxPersonalRenew   = 30 * time.Minute
	maxPersonalReviews = 2
	personalMandate    = 4
)

// personalWake is one coalesced pending review for an item key; reviews
// counts granted follow-ups so a decision cannot loop itself forever.
type personalWake struct {
	reviews int
}

// personalEntryKey matches runtimeKey for an entry without its binding.
func personalEntryKey(entry *runtimeEntry) string {
	return entry.PrincipalID + "\x00" + entry.ContextID + "\x00" + entry.RuntimeMode
}

// queuePersonalWakeLocked marks a scope for one coalesced review. m.mu held.
func (m *Manager) queuePersonalWakeLocked(key, reason string) {
	if m.cfg.Decide == nil {
		return
	}
	if m.personalPending[key] == nil {
		m.personalPending[key] = &personalWake{}
	}
	select {
	case m.wakeNotify <- struct{}{}:
	default:
	}
}

// personalLoop drains wakes as they arrive; with nothing pending it sleeps on
// the channel — the model is never polled.
func (m *Manager) personalLoop(ctx context.Context) {
	for {
		select {
		case <-ctx.Done():
			return
		case <-m.wakeNotify:
			m.drainPersonal(ctx)
		}
	}
}

// drainPersonal runs each queued scope through Decide once, then clears it.
// With nothing pending — no incidents — the model is never called. The drain
// itself is bounded: a decision that raises another incident stays queued for
// the next drain rather than feeding itself inside this one.
func (m *Manager) drainPersonal(ctx context.Context) {
	if m.cfg.Decide == nil {
		return
	}
	for range 64 {
		m.mu.Lock()
		key := ""
		var wake *personalWake
		for k, w := range m.personalPending {
			if !m.personalRunning[k] {
				key, wake = k, w
				break
			}
		}
		if key == "" {
			m.mu.Unlock()
			return
		}
		delete(m.personalPending, key)
		m.personalRunning[key] = true
		entry := m.items[key]
		var request PersonalRequest
		if entry != nil {
			incidents := make([]Incident, 0, len(entry.incidents))
			for _, incident := range entry.incidents {
				incidents = append(incidents, incident)
			}
			request = PersonalRequest{
				PrincipalID: entry.PrincipalID,
				ContextID:   entry.ContextID,
				RuntimeID:   entry.RuntimeID,
				Generation:  entry.Generation,
				State:       entry.State,
				Incidents:   incidents,
				Consumers:   entry.consumers(),
				Mandate:     personalMandate,
			}
		}
		m.mu.Unlock()
		if request.PrincipalID == "" {
			m.mu.Lock()
			delete(m.personalRunning, key)
			m.mu.Unlock()
			continue
		}
		decision, err := m.cfg.Decide(ctx, request)
		m.mu.Lock()
		delete(m.personalRunning, key)
		if current := m.items[key]; current != nil && err == nil && current.Generation == request.Generation {
			m.applyPersonalLocked(key, current, wake, decision)
		}
		m.mu.Unlock()
	}
}

// applyPersonalLocked validates a decision against the woken scope only —
// a decision can never touch another scope's leases, exceed the renewal
// bound, or chain more follow-up wakes than maxPersonalReviews. m.mu held.
func (m *Manager) applyPersonalLocked(key string, entry *runtimeEntry, wake *personalWake, d PersonalDecision) {
	denied := false
	switch d.Action {
	case "", "inspect":
	case "renew":
		if lease, ok := entry.leases[d.LeaseID]; ok && !d.RenewUntil.After(m.cfg.Now().Add(maxPersonalRenew)) {
			lease.ExpiresAt = d.RenewUntil
			entry.leases[d.LeaseID] = lease
		} else {
			denied = true
		}
	case "pause":
		entry.IdleDeadline = m.cfg.Now()
	case "escalate":
		m.recordIncidentLocked(entry, "escalated")
	case "restart":
		entry.personalRestart = true
	}
	if d.Review && wake.reviews < maxPersonalReviews {
		wake.reviews++
		m.personalPending[key] = wake
	}
	outcome := "personal-applied"
	if denied {
		outcome = "personal-denied"
	}
	m.appendPersonalSignal(entry, outcome, d)
}

// appendPersonalSignal records one allowlisted decision line — action class
// and ids only, never model text — so supervision stays auditable.
func (m *Manager) appendPersonalSignal(entry *runtimeEntry, outcome string, d PersonalDecision) {
	file, err := os.OpenFile(m.signalPath(), os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0600)
	if err != nil {
		return
	}
	defer file.Close()
	_ = json.NewEncoder(file).Encode(map[string]string{
		"at":           m.cfg.Now().UTC().Format(time.RFC3339),
		"class":        outcome,
		"principal_id": entry.PrincipalID,
		"context_id":   entry.ContextID,
		"runtime_id":   entry.RuntimeID,
		"generation":   entry.Generation,
		"action":       d.Action,
		"lease_id":     d.LeaseID,
	})
}

var errDecideUnavailable = errors.New("personal decider unavailable")

const personalSystemPrompt = `You review one supervised agent runtime. Reply with exactly one JSON object: {"action":"inspect|renew|pause|restart|escalate","lease_id":"...","renew_until":"<RFC3339, at most 30 minutes ahead>","reason":"<short>","review":false}. Actions: inspect = no change; renew = extend one existing lease; pause = release the runtime early; restart = restart the verified container; escalate = record an escalation incident. Prefer inspect when uncertain.`

// relayDecide is the production Decide seam: one bounded POST of the
// allowlisted request to the managed model relay, strict-JSON parse of the
// reply. Any transport or decode failure returns an error and applies nothing.
func relayDecide(ctx context.Context, req PersonalRequest) (PersonalDecision, error) {
	base := strings.TrimRight(os.Getenv("HUB_MANAGED_MODEL_URL"), "/")
	model := os.Getenv("HUB_PERSONAL_MODEL")
	contextJSON, err := json.Marshal(req)
	if err != nil {
		return PersonalDecision{}, errDecideUnavailable
	}
	payload, err := json.Marshal(map[string]any{
		"model": model,
		"messages": []map[string]string{
			{"role": "system", "content": personalSystemPrompt},
			{"role": "user", "content": string(contextJSON)},
		},
	})
	if err != nil {
		return PersonalDecision{}, errDecideUnavailable
	}
	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, base+"/chat/completions", bytes.NewReader(payload)) // #nosec G704 -- configured managed-relay endpoint
	if err != nil {
		return PersonalDecision{}, errDecideUnavailable
	}
	httpReq.Header.Set("Content-Type", "application/json")
	if key := os.Getenv("OPENAI_API_KEY"); key != "" {
		httpReq.Header.Set("Authorization", "Bearer "+key)
	}
	resp, err := personalRelayClient.Do(httpReq) // #nosec G704 -- configured managed-relay endpoint
	if err != nil {
		return PersonalDecision{}, errDecideUnavailable
	}
	defer resp.Body.Close()
	var out struct {
		Choices []struct {
			Message struct {
				Content string `json:"content"`
			} `json:"message"`
		} `json:"choices"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil || len(out.Choices) == 0 {
		return PersonalDecision{}, errDecideUnavailable
	}
	var decision PersonalDecision
	if json.Unmarshal([]byte(out.Choices[0].Message.Content), &decision) != nil {
		return PersonalDecision{}, errDecideUnavailable
	}
	return decision, nil
}

var personalRelayClient = &http.Client{Timeout: 45 * time.Second}
