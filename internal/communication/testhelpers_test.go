package communication

// Test-only helpers: staticcheck U1000 counts production callers only, so
// functions whose sole callers are tests live here instead of dead weight
// in production files.

import (
	"context"
	"os/exec"
	"strings"
	"time"

	"github.com/letya999/hermes-hub/internal/locale"
)

// budgetScope returns the scope bucket for tests and inspection surfaces.
func (s *Spool) budgetScope(scopeID string) scopeBudget {
	s.mu.Lock()
	defer s.mu.Unlock()
	if bucket, ok := s.budget.Scopes[scopeID]; ok {
		return *bucket
	}
	return scopeBudget{}
}

func (s *Spool) reservationState(jobID string) string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.budget.Reservations[jobID].State
}

// tickProactive is the proactive pass of the control loop: schedule -> due
// occurrence -> dispatch -> deterministic probing for watcher/check kinds.
func (g *Gateway) tickProactive(now time.Time) { g.tickProactiveContext(context.Background(), now) }

func (g *Gateway) runCanary(ctx context.Context, now time.Time) string {
	return g.opsEngine().Run(ctx, now)
}

func (g *Gateway) canaryToolCheck(jobID string) string {
	return g.opsEngine().ToolCheck(jobID)
}

func (g *Gateway) recordCanary(ctx context.Context, now time.Time, class string) {
	g.opsEngine().Record(ctx, now, class)
}

// mediaReady resolves readiness for either an embedded command worker (binary
// must exist) or a URL-configured sidecar (health endpoint must answer).
func mediaReady(impl any, command, url string) bool {
	if impl == nil {
		return false
	}
	if strings.TrimSpace(url) == "" {
		return commandAvailable(command)
	}
	type healthchecker interface {
		Healthy(context.Context) bool
	}
	if h, ok := impl.(healthchecker); ok {
		ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
		defer cancel()
		return h.Healthy(ctx)
	}
	return true
}

// commandAvailable reports whether the configured worker binary can actually
// run: an empty command or an unresolvable path both mean "not available".
func commandAvailable(command string) bool {
	command = strings.TrimSpace(command)
	if command == "" {
		return false
	}
	_, err := exec.LookPath(command)
	return err == nil
}

func connectionList(user User) string {
	return connectionListWithLang(locale.LangOf(user.WorkspaceDir), user)
}
