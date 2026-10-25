package compaction

import (
	"encoding/json"
	"os"
)

// ManagedContext is the runtime-side projection of the normalized context
// declaration the stack renders into HUB_MANAGED_CONTEXT. It is a read view,
// never the authority — validation happened before render.
type ManagedContext struct {
	Compression    map[string]any `json:"compression,omitempty"`
	CompactEngine  string         `json:"compact_engine,omitempty"`
	CompactEngines []string       `json:"compact_engines,omitempty"`
	GoalsEngine    string         `json:"goals_engine,omitempty"`
	GoalsJudge     string         `json:"goals_judge,omitempty"`
	GoalsMaxTurns  int            `json:"goals_max_turns,omitempty"`
	SummaryModel   string         `json:"summary_model,omitempty"`
	JudgeModel     string         `json:"judge_model,omitempty"`
}

// ManagedContextFromEnv decodes the rendered declaration. An absent or
// unreadable env means "no declaration" — the zero value carries defaults.
func ManagedContextFromEnv() *ManagedContext {
	raw := os.Getenv("HUB_MANAGED_CONTEXT")
	if raw == "" {
		return &ManagedContext{}
	}
	var mc ManagedContext
	if err := json.Unmarshal([]byte(raw), &mc); err != nil {
		return &ManagedContext{}
	}
	return &mc
}

// SweepGate resolves the declared hub_sweep policy into the runtime gate.
// Undeclared spaces keep the reviewed defaults — sweep off, default bound,
// precompute on — so an absent declaration changes nothing.
func (m *ManagedContext) SweepGate() Gate {
	g := Gate{Threshold: DefaultSweepThreshold, CooldownSeconds: 1800, Precompute: true}
	sweep, _ := m.Compression["hub_sweep"].(map[string]any)
	if sweep == nil {
		return g
	}
	if v, ok := sweep["enabled"].(bool); ok {
		g.Enabled = v
	}
	if v, ok := sweep["threshold_tokens"].(float64); ok && v > 0 {
		g.Threshold = int64(v)
	}
	if v, ok := sweep["cooldown_seconds"].(float64); ok && v > 0 {
		g.CooldownSeconds = int64(v)
	}
	if v, ok := sweep["precompute"].(bool); ok {
		g.Precompute = v
	}
	return g
}

// HubCompactGranted reports whether the declaration admits the hub engine:
// an explicit native pin closes the hub path everywhere.
func (m *ManagedContext) HubCompactGranted() bool {
	if m.CompactEngine == EngineNative {
		return false
	}
	if len(m.CompactEngines) > 0 {
		for _, e := range m.CompactEngines {
			if e == EngineHub {
				return true
			}
		}
		return false
	}
	return true
}

// CompactEngineFor resolves one manual request's engine: the declared
// preference (auto -> hub) unless the caller asked for a concrete engine,
// bounded by the granted engines — a pin grants only its own engine, a
// declared list grants its members, and undeclared grants both.
func (m *ManagedContext) CompactEngineFor(requested string) (string, bool) {
	engine := requested
	if engine == "" || engine == "auto" {
		engine = m.CompactEngine
		if engine == "" || engine == "auto" {
			engine = EngineHub
		}
	}
	if engine != EngineHub && engine != EngineNative {
		return "", false
	}
	granted := m.CompactEngines
	if len(granted) == 0 {
		if m.CompactEngine == EngineHub || m.CompactEngine == EngineNative {
			granted = []string{m.CompactEngine}
		} else {
			granted = []string{EngineHub, EngineNative}
		}
	}
	for _, e := range granted {
		if e == engine {
			return engine, true
		}
	}
	return "", false
}

// SweepThreshold returns the declared hub_sweep bound and whether it was
// declared at all — the status surface's declared|default provenance.
func (m *ManagedContext) SweepThreshold() (int64, bool) {
	if m == nil {
		return 0, false
	}
	sweep, _ := m.Compression["hub_sweep"].(map[string]any)
	if sweep == nil {
		return 0, false
	}
	value, ok := sweep["threshold_tokens"].(float64)
	if !ok || value <= 0 {
		return 0, false
	}
	return int64(value), true
}
