package runtime

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"sync"
	"time"

	"github.com/letya999/hermes-hub/internal/stack"
)

// relayMeter keeps the durable per-principal quota counters a model ladder
// declares. Records live in one JSON file beside the relay selection state so
// a restart never resets an owner's window and shared/personal counters never
// cross.
type relayMeter struct {
	mu   sync.Mutex
	path string
	data map[string]relayUsageRecord
	now  func() time.Time
}

type relayUsageRecord struct {
	Until int64 `json:"until"`
	Count int64 `json:"count"`
}

func limitWindow(per string) time.Duration {
	switch per {
	case "5h":
		return 5 * time.Hour
	case "1w":
		return 7 * 24 * time.Hour
	default: // "1d"
		return 24 * time.Hour
	}
}

// load primes the in-memory counters from the durable file. A missing file is
// a fresh window; an unreadable or corrupt one returns an error so admit can
// fail closed instead of granting spend the durable record may already show.
func (m *relayMeter) load() error {
	if m.data != nil {
		return nil
	}
	b, err := os.ReadFile(m.path) // #nosec G304 -- operator-mounted state path
	if errors.Is(err, os.ErrNotExist) {
		m.data = map[string]relayUsageRecord{}
		return nil
	}
	if err != nil {
		return err
	}
	data := map[string]relayUsageRecord{}
	if err := json.Unmarshal(b, &data); err != nil {
		return fmt.Errorf("meter state corrupt: %w", err)
	}
	m.data = data
	return nil
}

func (m *relayMeter) clock() time.Time {
	if m.now != nil {
		return m.now()
	}
	return time.Now()
}

func (m *relayMeter) key(name string, limit stack.ModelLimit, owner string, now int64) string {
	start := now / int64(limitWindow(limit.Per)) * int64(limitWindow(limit.Per))
	scope := "shared"
	if limit.Scope == "personal" {
		scope = owner
	}
	return fmt.Sprintf("%s|%s|%s|%s|%d", name, scope, limit.Of, limit.Per, start)
}

// exhausted reports whether any declared window is already full for the
// principal (shared scope) or this owner (personal scope). It is the cheap
// pre-skip before credential work; admit remains the atomic gate at dispatch.
func (m *relayMeter) exhausted(name string, limits []stack.ModelLimit, owner string) bool {
	if len(limits) == 0 {
		return false
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.load() != nil {
		return true // durable state unreadable: stay closed, never spend blind
	}
	now := m.clock().Unix()
	for _, limit := range limits {
		if rec, ok := m.data[m.key(name, limit, owner, now)]; ok && rec.Count >= limit.Amount && rec.Until > now {
			return true
		}
	}
	return false
}

// admit is the single atomic gate before a provider attempt: load, check
// every declared window, reserve the attempt (one request per window plus the
// conservative token estimate), persist, and only then let dispatch proceed.
// A read, capacity or write failure refuses the attempt — nothing reaches the
// provider unreserved. On a save failure the in-memory charge stays so a
// repeated attempt keeps spending the same reservation conservatively.
func (m *relayMeter) admit(name string, limits []stack.ModelLimit, owner string, body []byte) error {
	if len(limits) == 0 {
		return nil
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	if err := m.load(); err != nil {
		return fmt.Errorf("%w: meter state unavailable", errQuota)
	}
	now := m.clock().Unix()
	for _, limit := range limits {
		if rec, ok := m.data[m.key(name, limit, owner, now)]; ok && rec.Count >= limit.Amount && rec.Until > now {
			return errQuota
		}
	}
	for k, rec := range m.data {
		if rec.Until <= now {
			delete(m.data, k)
		}
	}
	for _, limit := range limits {
		n := int64(1)
		if limit.Of == "tokens" {
			n = estimatedTokens(body)
		}
		key := m.key(name, limit, owner, now)
		rec := m.data[key]
		if rec.Until <= now {
			rec = relayUsageRecord{Until: now/int64(limitWindow(limit.Per))*int64(limitWindow(limit.Per)) + int64(limitWindow(limit.Per))}
		}
		rec.Count += n
		m.data[key] = rec
	}
	if err := m.save(); err != nil {
		return fmt.Errorf("%w: meter state write failed", errQuota)
	}
	return nil
}

var errQuota = errors.New("local model limit spent")

func (m *relayMeter) save() error {
	b, err := json.Marshal(m.data)
	if err != nil {
		return err
	}
	tmp := m.path + ".tmp"
	if err := os.WriteFile(tmp, b, 0600); err != nil {
		return err
	}
	return os.Rename(tmp, m.path) // counters stay conservative on rename failure
}

// estimatedTokens reserves the conservative request cost: body bytes at six
// per token plus the declared completion reserve, matching withinModelBudget.
func estimatedTokens(body []byte) int64 {
	var output struct {
		MaxTokens           int64 `json:"max_tokens"`
		MaxCompletionTokens int64 `json:"max_completion_tokens"`
	}
	_ = json.Unmarshal(body, &output)
	reserve := max(int64(8192), output.MaxTokens, output.MaxCompletionTokens)
	return int64(len(body))/6 + reserve
}
