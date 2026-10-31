package supervisor

import (
	"context"
	"fmt"
	"strings"
	"time"
)

// DefaultBrowserIdleTTL keeps a sidecar warm through short gaps after the
// last runtime for its space goes away; Chromium's cold start is seconds, so
// grace is cheap compared to re-booting the whole display stack.
const DefaultBrowserIdleTTL = 15 * time.Minute

// browserSidecarName is the compose-owned container
// (<project>-<service>-1) that owns a space's browser display stack. Compose
// keeps owning its shape; the supervisor only toggles its run state.
func (m *Manager) browserSidecarName(userID string) string {
	return "hermes-hub-" + userID + "-" + m.environment() + "-browser-" + userID + "-1"
}

// ensureBrowserSidecar implements SPEC-0083 R2: a browser/meet space's
// sidecar must be running before a spawned runtime probes readiness.
// `docker start` is idempotent — running is a no-op, stopped resumes.
func (m *Manager) ensureBrowserSidecar(ctx context.Context, userID string) error {
	name := m.browserSidecarName(userID)
	if out, err := m.command(ctx, "start", name); err != nil {
		return fmt.Errorf("start browser sidecar %s (run hubctl up for this space): %w: %s", name, err, firstLine(string(out)))
	}
	return nil
}

// ReapIdleBrowserSidecars implements SPEC-0083 R3: a browser sidecar exists
// to serve its space's runtime. When no runtime is alive in its project the
// sidecar is stopped after BrowserIdleTTL of grace. `docker stop` is a
// graceful SIGTERM (Chromium flushes its profile) and `restart:
// unless-stopped` honours a manual stop. Deadlines live in memory: a
// supervisor restart grants one grace period, never a premature stop.
func (m *Manager) ReapIdleBrowserSidecars(ctx context.Context, now time.Time) {
	out, err := m.command(ctx, "ps", "--filter", "label=com.docker.compose.project",
		"--format", "{{.Names}} {{.Label \"com.docker.compose.service\"}} {{.Label \"com.docker.compose.project\"}}")
	if err != nil {
		return // best-effort; next sweep retries
	}
	m.mu.Lock()
	live := make(map[string]bool, len(m.items)*2)
	for _, item := range m.items {
		if item.State != Stopped {
			live[item.binding.UserID] = true
			live[item.binding.ContextID] = true
		}
	}
	m.mu.Unlock()
	if m.sidecarIdle == nil {
		m.sidecarIdle = map[string]time.Time{}
	}
	envSuffix := "-" + m.environment()
	sidecars := map[string]string{} // container name -> compose project
	runtimeProjects := map[string]bool{}
	for _, line := range strings.Split(strings.TrimSpace(string(out)), "\n") {
		fields := strings.Fields(line)
		if len(fields) != 3 || !strings.HasPrefix(fields[2], "hermes-hub-") || !strings.HasSuffix(fields[2], envSuffix) {
			continue // not a space project this environment owns (R3.4)
		}
		if strings.HasPrefix(fields[1], "browser-") {
			sidecars[fields[0]] = fields[2]
		}
		if fields[1] == "hermes-runtime" {
			runtimeProjects[fields[2]] = true
		}
	}
	for name, project := range sidecars {
		user := strings.TrimSuffix(strings.TrimPrefix(project, "hermes-hub-"), envSuffix)
		if runtimeProjects[project] || live[user] {
			delete(m.sidecarIdle, name) // a returning runtime clears a pending stop
			continue
		}
		if _, marked := m.sidecarIdle[name]; !marked {
			m.sidecarIdle[name] = now.Add(m.cfg.BrowserIdleTTL)
		}
	}
	for name, deadline := range m.sidecarIdle {
		if now.Before(deadline) {
			continue
		}
		_, _ = m.command(ctx, "stop", name)
		delete(m.sidecarIdle, name)
	}
}
