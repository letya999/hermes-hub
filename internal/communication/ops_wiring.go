package communication

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/letya999/hermes-hub/internal/commops"
)

// canaryJobTimeout bounds a single verification round; a run that has not
// settled inside it counts as the timeout failure class. Tests retune it.
var canaryJobTimeout = 90 * time.Second

// canaryDeliveryGrace bounds the wait for the canary response delivery.
var canaryDeliveryGrace = 15 * time.Second

// opsEngine lazily builds the ops-plane engine. The lazy path keeps
// struct-literal test fixtures working; deps evaluate gateway fields at
// call time.
func (g *Gateway) opsEngine() *commops.Engine {
	g.opsOnce.Do(func() {
		g.ops = commops.New(g.opsDeps())
	})
	return g.ops
}

// configUser finds an enabled configured user by principal ID — the ops
// deps need the full User (task binding, surfaces) where commops carries
// only the owner projection.
func (g *Gateway) configUser(id string) (User, bool) {
	for _, u := range g.config.Users {
		if u.ID == id && u.Enabled {
			return u, true
		}
	}
	return User{}, false
}

// opsDeps adapts gateway capabilities into the ops-plane contract. The ops
// engine cannot see the gateway — only these closures.
func (g *Gateway) opsDeps() commops.Deps {
	return commops.Deps{
		Now:       func() time.Time { return g.now() },
		SpoolRoot: g.spool.root,
		Owner: func() (commops.Owner, bool) {
			for _, u := range g.config.Users {
				if u.Enabled {
					return commops.Owner{ID: u.ID, TelegramIDs: u.TelegramIDs}, true
				}
			}
			return commops.Owner{}, false
		},
		BindTask: func(owner commops.Owner, chatID int64) (any, error) {
			user, ok := g.configUser(owner.ID)
			if !ok {
				return nil, errors.New("owner not configured")
			}
			return g.spool.ResolveTask(user, chatID, 0, true)
		},
		Enqueue: func(ctx context.Context, owner commops.Owner, task any, chatID int64, jobID, idempotency, text string) error {
			user, ok := g.configUser(owner.ID)
			if !ok {
				return errors.New("owner not configured")
			}
			bound, ok := task.(Task)
			if !ok {
				return errors.New("task handle mismatch")
			}
			return g.enqueueChannelJob(ctx, user, bound, "mock", jobID, idempotency, chatID, 0, text, chatID, "")
		},
		MappingStatus: func(jobID string) (string, bool, error) {
			m, found, err := g.spool.Mapping(jobID)
			return m.Status, found, err
		},
		Mappings: func(userID string) []commops.Mapping {
			var out []commops.Mapping
			entries, err := os.ReadDir(filepath.Join(g.spool.root, "mappings"))
			if err != nil {
				return nil
			}
			for _, entry := range entries {
				if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".json") {
					continue
				}
				g.spool.mu.Lock()
				m, err := g.spool.loadMappingLocked(strings.TrimSuffix(entry.Name(), ".json"))
				g.spool.mu.Unlock()
				if err != nil || m.UserID != userID {
					continue
				}
				out = append(out, commops.Mapping{UserID: m.UserID, Status: m.Status, CreatedAt: m.CreatedAt, UpdatedAt: m.UpdatedAt, TerminalAt: m.TerminalAt})
			}
			return out
		},
		UsageTotals: func(scope string, since time.Time) (commops.Totals, error) {
			t, err := g.spool.UsageTotalsSince(scope, since, "")
			return commops.Totals{Jobs: t.Jobs, InputTokens: t.InputTokens}, err
		},
		Restarts: func(owner commops.Owner) (int, bool) {
			user, ok := g.configUser(owner.ID)
			if !ok {
				return 0, false
			}
			if surface := g.healthSurface(user); surface != nil {
				return surface.Restarts, true
			}
			return 0, false
		},
		SurfaceMissingTools: func(ctx context.Context, owner commops.Owner) []string {
			user, ok := g.configUser(owner.ID)
			if !ok {
				return nil
			}
			if strings.TrimSpace(g.config.RuntimeURL) == "" || strings.TrimSpace(g.config.RuntimeAuth) == "" {
				return nil
			}
			var surface *runtimeSurface
			if g.config.Supervised {
				surface = g.supervisedSurface(ctx, user)
			} else {
				surface = g.residentSurface(ctx)
			}
			if surface == nil {
				return nil
			}
			return surface.MissingTools
		},
		MockLink: g.mockLinkClaim,
		MockDelivered: func(deliveryID string) bool {
			_, err := os.Stat(filepath.Join(g.mockRoot(), "mock-sent", spoolFileID(deliveryID)+".json"))
			return err == nil
		},
		NotifyOwner: func(ctx context.Context, idempotency string, chatID int64, text string) {
			_ = g.queueDelivery(ctx, idempotency, chatID, 0, text)
		},
		JobTimeout:      func() time.Duration { return canaryJobTimeout },
		DeliveryGrace:   func() time.Duration { return canaryDeliveryGrace },
		WriteJSONAtomic: atomicJSON,
	}
}

// Shims keep the pre-extraction call sites and tests on their old names.
// New code should use g.opsEngine() directly.

func canaryInterval() time.Duration { return commops.Interval() }

func (g *Gateway) runCanary(ctx context.Context, now time.Time) string {
	return g.opsEngine().Run(ctx, now)
}

func (g *Gateway) canaryToolCheck(jobID string) string {
	return g.opsEngine().ToolCheck(jobID)
}

func (g *Gateway) recordCanary(ctx context.Context, now time.Time, class string) {
	g.opsEngine().Record(ctx, now, class)
}

func (g *Gateway) canaryLoop(ctx context.Context, interval time.Duration) {
	g.opsEngine().Loop(ctx, interval)
}

func (g *Gateway) healthLines(user User) string {
	return g.opsEngine().HealthLines(commops.Owner{ID: user.ID, TelegramIDs: user.TelegramIDs})
}

// healthSurface reuses the readiness surface — its additive Restarts field is
// the supervisor's durable counter; unreachable/absent means the health block
// omits restarts rather than reporting a fabricated zero.
func (g *Gateway) healthSurface(user User) *runtimeSurface {
	if strings.TrimSpace(g.config.RuntimeURL) == "" || strings.TrimSpace(g.config.RuntimeAuth) == "" {
		return nil
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if g.config.Supervised {
		return g.supervisedSurface(ctx, user)
	}
	return g.residentSurface(ctx)
}
