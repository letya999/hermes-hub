package runtime

import (
	"encoding/json"
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

func (m *relayMeter) load() {
	if m.data != nil {
		return
	}
	m.data = map[string]relayUsageRecord{}
	b, err := os.ReadFile(m.path) // #nosec G304 -- operator-mounted state path
	if err != nil {
		return
	}
	_ = json.Unmarshal(b, &m.data)
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
// principal (shared scope) or this owner (personal scope).
func (m *relayMeter) exhausted(name string, limits []stack.ModelLimit, owner string) bool {
	if len(limits) == 0 {
		return false
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	m.load()
	now := m.clock().Unix()
	for _, limit := range limits {
		if rec, ok := m.data[m.key(name, limit, owner, now)]; ok && rec.Count >= limit.Amount && rec.Until > now {
			return true
		}
	}
	return false
}

// charge reserves the attempt against every declared window: one request each,
// plus the conservative token estimate when a tokens limit exists. A shared
// key budget race stays safe because the counter moves before the next caller
// is admitted.
func (m *relayMeter) charge(name string, limits []stack.ModelLimit, owner string, body []byte) {
	if len(limits) == 0 {
		return
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	m.load()
	now := m.clock().Unix()
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
	m.save()
}

func (m *relayMeter) save() {
	b, err := json.Marshal(m.data)
	if err != nil {
		return
	}
	tmp := m.path + ".tmp"
	if err := os.WriteFile(tmp, b, 0600); err != nil {
		return
	}
	_ = os.Rename(tmp, m.path) // counters stay conservative on rename failure
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
