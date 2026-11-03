package supervisor

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// supervisionPolicy is the space's published config.supervision section as
// the supervisor consumes it (SPEC-0092 R4.2): the render writes it beside
// the runtime env file and the manager re-reads it on every decision, so a
// revision change applies live without a runtime restart.
type supervisionPolicy struct {
	IdleStop string              `json:"idle_stop,omitempty"`
	Restart  string              `json:"restart,omitempty"`
	Watchers map[string]watcherP `json:"watchers,omitempty"`
}

type watcherP struct {
	Protected bool   `json:"protected,omitempty"`
	Every     string `json:"every,omitempty"`
}

// policyPath is the rendered supervision policy beside the runtime env:
// <context>/runtime.<env>.policy.json.
func policyPath(contextRoot, env string) string {
	if strings.TrimSpace(contextRoot) == "" || strings.TrimSpace(env) == "" {
		return ""
	}
	return filepath.Join(contextRoot, "runtime."+env+".policy.json")
}

// loadSupervisionPolicy reads the rendered policy; a missing or malformed
// file degrades to the zero policy — the defaults then govern.
func (m *Manager) loadSupervisionPolicy(binding Binding) supervisionPolicy {
	var p supervisionPolicy
	if path := policyPath(binding.ContextRoot, m.environment()); path != "" {
		if b, err := os.ReadFile(path); err == nil { // #nosec G304 -- path derives from the enrolled context root
			_ = json.Unmarshal(b, &p)
		}
	}
	return p
}

// idleTTL is the space's idle-retention bound: the published supervision
// policy wins, the deployment default remains the fallback.
func (m *Manager) idleTTL(binding Binding) time.Duration {
	if stop, err := time.ParseDuration(m.loadSupervisionPolicy(binding).IdleStop); err == nil && stop > 0 {
		return stop
	}
	return m.cfg.WarmTTL
}
