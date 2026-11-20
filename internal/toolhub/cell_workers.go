package toolhub

import (
	"context"
	"errors"
	"fmt"
	"time"

	spawnv1 "github.com/letya999/hermes-hub/contracts/spawn/v1"
	"github.com/letya999/hermes-hub/internal/toolhub/api"
	"github.com/letya999/hermes-hub/shared/httpapi"
	"github.com/letya999/hermes-hub/shared/locale"
)

// heldWorker is ToolHub's handle on one claimed spawn-controller worker:
// the lease it renews on reuse and releases when the cell rotates out. It
// replaces the v0 cliCell registry entry — the container itself now lives
// behind the worker contract (CHG-0336 S6).
type heldWorker struct {
	id              string
	lease           string
	key             string // warm reuse key; "" workers are never held
	principal       string
	binding         string
	job             string
	lifecycle       string
	image           string
	installPackages bool
	toolset         []string
	proxy           string // squid endpoint "" when the cell has no egress
	cred            string // cred sidecar alias "" when no brokered creds
	created         time.Time
	lastUse         time.Time
	uses            int
	execSlot        chan struct{} // cap-1: one exec per worker at a time
}

// workerMetrics are the claim/reuse counters /cli-metrics keeps exposing;
// the server-side lifecycle (drift checks, drains) is spawn-controller's.
type workerMetrics struct {
	creates        int64
	reuses         int64
	rotations      int64
	releaseFails   int64
	claimWaitNanos int64
}

// spawnOrg is the installation organization until identity-hub lands —
// the same constant the v0 importer scopes spaces under.
const spawnOrg = "local"

// spawnClaim builds the worker claim from the admitted request: the reuse
// key the warm tiers computed, the resolved image and the full cell shape
// the service declares on the worker spec.
func (c *genericController) spawnClaim(req api.CliExecRequest, image, key string, toolset []string) spawnv1.ClaimWorkerRequest {
	limits := req.Plan.Execution
	scope := req.Workspace.Scope
	if scope == "" {
		scope = "none"
	}
	claim := spawnv1.ClaimWorkerRequest{
		Org:             spawnOrg,
		Owner:           req.Principal,
		CellKey:         key,
		Image:           image,
		Agent:           req.ContextID,
		Principal:       req.Principal,
		BindingID:       req.BindingID,
		JobID:           req.JobID,
		Workspace:       &spawnv1.WorkerWorkspace{Scope: scope, Path: req.Workspace.Path, Access: req.Workspace.Access},
		Skills:          scope == "principal",
		Toolset:         toolset,
		Egress:          limits.Egress,
		InstallPackages: req.InstallPackages,
		Limits: spawnv1.WorkerLimits{
			CPUMillis:      limits.CPUMillis,
			MemoryMiB:      limits.MemoryMiB,
			MaxPIDs:        limits.MaxPIDs,
			TimeoutSeconds: limits.TimeoutSeconds,
			OutputBytes:    int64(limits.OutputBytes),
		},
	}
	for _, cred := range req.Brokered {
		claim.Brokered = append(claim.Brokered, spawnv1.WorkerCred{
			EnvName: cred.EnvName, Host: cred.Host, Prefix: cred.Prefix, Value: cred.Value,
		})
	}
	return claim
}

// claimWorker asks spawn-controller for a ready worker and wraps the
// granted lease in the held handle.
func (c *genericController) claimWorker(ctx context.Context, req api.CliExecRequest, image, key string, toolset []string) (*heldWorker, error) {
	out, err := c.spawn.ClaimWorker(ctx, req.Principal, c.spawnClaim(req, image, key, toolset))
	if err != nil {
		return nil, err
	}
	lifecycle := req.Lifecycle
	if lifecycle == "" {
		lifecycle = api.CLILifecycleEphemeral
	}
	return &heldWorker{
		id: out.Worker.ID, lease: out.Lease, key: key, principal: req.Principal,
		binding: req.BindingID, job: req.JobID,
		lifecycle: lifecycle, image: image, installPackages: req.InstallPackages,
		toolset: append([]string(nil), toolset...), proxy: out.Proxy, cred: out.Cred,
		created: time.Now(), lastUse: time.Now(), execSlot: make(chan struct{}, 1),
	}, nil
}

// errWorkerGone marks a held worker the server drained under the lease:
// the handle drops and the caller re-claims a fresh generation.
var errWorkerGone = errors.New("worker lease lost")

// heldWorkerFor returns the warm worker for key, reusing the held lease or
// claiming a new generation — the v0 warmCell flow over the contract. A
// held worker the server drained is forgotten and re-claimed.
func (c *genericController) heldWorkerFor(ctx context.Context, req api.CliExecRequest, image, key string, toolset []string) (*heldWorker, error) {
	c.mu.Lock()
	held := c.held[key]
	c.mu.Unlock()
	if held != nil {
		select {
		case held.execSlot <- struct{}{}:
		case <-ctx.Done():
			return nil, ctx.Err()
		}
		if _, err := c.spawn.RenewWorker(ctx, held.id, spawnv1.WorkerLeaseRequest{Org: spawnOrg, Owner: req.Principal, Lease: held.lease}); err != nil {
			<-held.execSlot
			if errors.Is(err, httpapi.ErrNotFound) || errors.Is(err, httpapi.ErrForbidden) {
				c.mu.Lock()
				if c.held[key] == held {
					delete(c.held, key)
				}
				c.mu.Unlock()
				return c.heldWorkerFor(ctx, req, image, key, toolset)
			}
			return nil, fmt.Errorf("%w: worker renew: %v", api.ErrIsolation, err)
		}
		c.mu.Lock()
		c.workerMetrics.reuses++
		c.mu.Unlock()
		return held, nil
	}
	// Cap the held table like the v0 warm cap: evict the oldest idle
	// worker (release drains it server-side) before claiming past it.
	capN := c.config.MaxActive
	if capN < 4 {
		capN = 4
	}
	c.mu.Lock()
	for len(c.held) >= capN {
		var oldest *heldWorker
		for _, member := range c.held {
			select {
			case member.execSlot <- struct{}{}:
				<-member.execSlot
				if oldest == nil || member.lastUse.Before(oldest.lastUse) {
					oldest = member
				}
			default:
			}
		}
		if oldest == nil {
			break
		}
		delete(c.held, oldest.key)
		go c.releaseHeld(oldest, "evicted-capacity") // #nosec G118 -- worker eviction intentionally outlives the caller context
	}
	full := len(c.held) >= capN
	c.mu.Unlock()
	if full {
		return nil, fmt.Errorf("%w: cli cell table is full", api.ErrIsolation)
	}
	waiting := time.Now()
	held, err := c.claimWorker(ctx, req, image, key, toolset)
	c.mu.Lock()
	c.workerMetrics.claimWaitNanos += time.Since(waiting).Nanoseconds()
	c.mu.Unlock()
	if err != nil {
		c.evictToolbox(req.ToolboxID)
		return nil, fmt.Errorf("%w: worker claim: %v", api.ErrIsolation, err)
	}
	c.mu.Lock()
	if existing, raced := c.held[key]; raced {
		c.mu.Unlock()
		go c.releaseHeld(held, "superseded") // #nosec G118 -- losing claim's worker intentionally outlives the caller context
		// The winner's exec slot still serializes execs on the shared
		// worker; claim it before returning or the caller's deferred
		// release would drain a slot nobody filled. The lease renews the
		// same way the hit path does — a drained worker re-plans.
		select {
		case existing.execSlot <- struct{}{}:
		case <-ctx.Done():
			return nil, ctx.Err()
		}
		if _, err := c.spawn.RenewWorker(ctx, existing.id, spawnv1.WorkerLeaseRequest{Org: spawnOrg, Owner: req.Principal, Lease: existing.lease}); err != nil {
			<-existing.execSlot
			if errors.Is(err, httpapi.ErrNotFound) || errors.Is(err, httpapi.ErrForbidden) {
				c.mu.Lock()
				if c.held[key] == existing {
					delete(c.held, key)
				}
				c.mu.Unlock()
				return c.heldWorkerFor(ctx, req, image, key, toolset)
			}
			return nil, fmt.Errorf("%w: worker renew: %v", api.ErrIsolation, err)
		}
		c.mu.Lock()
		c.workerMetrics.reuses++
		c.mu.Unlock()
		return existing, nil
	}
	c.held[key] = held
	c.workerMetrics.creates++
	c.mu.Unlock()
	// The caller's deferred release expects the slot held for the whole
	// exec — take it here, mirroring the reuse path.
	select {
	case held.execSlot <- struct{}{}:
	case <-ctx.Done():
		return nil, ctx.Err()
	}
	return held, nil
}

// releaseHeld drops the worker lease; the last hold drains it server-side.
// The audit line carries the rotation reason like the v0 kill log.
func (c *genericController) releaseHeld(held *heldWorker, reason string) {
	if held == nil {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	if err := c.spawn.ReleaseWorker(ctx, held.id, spawnv1.WorkerLeaseRequest{Org: spawnOrg, Owner: held.principal, Lease: held.lease}); err != nil {
		c.mu.Lock()
		c.workerMetrics.releaseFails++
		c.mu.Unlock()
	}
	_ = reason
}

// releaseAllHeld releases every held worker — controller shutdown. The
// server-side drain backstop owns anything a release could not reach.
func (c *genericController) releaseAllHeld(reason string) {
	c.mu.Lock()
	all := make([]*heldWorker, 0, len(c.held))
	for key, held := range c.held {
		all = append(all, held)
		delete(c.held, key)
	}
	c.mu.Unlock()
	for _, held := range all {
		c.releaseHeld(held, reason)
	}
}

// cleanupHeld is the v0 cleanupCells kill ladder on the held registry:
// idle TTL (tier-bounded), max age and max reuse rotate workers out via
// lease release.
func (c *genericController) cleanupHeld(_ context.Context, now time.Time) {
	if c.config.CLI == nil || c.spawn == nil {
		return
	}
	maxAge := time.Duration(c.config.CLI.WarmMaxAgeSeconds) * time.Second
	if maxAge <= 0 {
		maxAge = 24 * time.Hour
	}
	maxReuse := c.config.CLI.WarmMaxReuse
	if maxReuse <= 0 {
		maxReuse = 256
	}
	idle := time.Duration(c.config.IdleTTLSeconds) * time.Second
	if idle <= 0 {
		idle = 5 * time.Minute
	}
	var doomed []*heldWorker
	var reasons []string
	c.mu.Lock()
	for key, held := range c.held {
		heldIdle := idle
		if held.lifecycle == api.CLILifecycleTask {
			heldIdle = minDuration(heldIdle, 5*time.Minute)
		}
		switch {
		case now.Sub(held.lastUse) > heldIdle:
			reasons = append(reasons, "idle-ttl")
		case now.Sub(held.created) > maxAge:
			reasons = append(reasons, "max-age")
		case held.uses >= maxReuse:
			reasons = append(reasons, "max-reuse")
		default:
			continue
		}
		doomed = append(doomed, held)
		delete(c.held, key)
	}
	c.mu.Unlock()
	for i, held := range doomed {
		c.mu.Lock()
		c.workerMetrics.rotations++
		c.mu.Unlock()
		c.releaseHeld(held, reasons[i])
	}
}

// drainWorkers implements /cli-release over the contract: the server
// drains every live worker the selector claims under — the v0 warm-cell
// release, now a tenant-scoped drain call.
func (c *genericController) drainWorkers(ctx context.Context, req api.CliReleaseRequest) {
	if c.spawn == nil {
		return
	}
	c.mu.Lock()
	for key, held := range c.held {
		if held.principal != req.PrincipalID {
			continue
		}
		if req.BindingID != "" && req.BindingID != heldBinding(held) {
			continue
		}
		if req.JobID != "" && req.JobID != heldJob(held) {
			continue
		}
		delete(c.held, key)
	}
	c.mu.Unlock()
	_, _ = c.spawn.DrainWorkers(ctx, req.PrincipalID, spawnv1.DrainWorkersRequest{
		Org: spawnOrg, Owner: req.PrincipalID, BindingID: req.BindingID, JobID: req.JobID,
	})
}

// heldBinding/heldJob report the selector fields a held worker was claimed
// under — kept on the claim itself so drain matching needs no row lookup.
func heldBinding(held *heldWorker) string { return held.binding }
func heldJob(held *heldWorker) string     { return held.job }

// workerMetricsSnapshot renders the counters /cli-metrics exposes: the
// claim-side numbers ToolHub still owns. Pool counters are gone with the
// deleted cross-user pool (S6).
func (c *genericController) workerMetricsSnapshot() map[string]int64 {
	c.mu.Lock()
	defer c.mu.Unlock()
	return map[string]int64{
		"creates": c.workerMetrics.creates, "reuses": c.workerMetrics.reuses,
		"rotations": c.workerMetrics.rotations, "release_fails": c.workerMetrics.releaseFails,
		"claim_wait_nanos": c.workerMetrics.claimWaitNanos,
		"warm_cells":       int64(len(c.held)),
	}
}

func minDuration(a, b time.Duration) time.Duration {
	if a < b {
		return a
	}
	return b
}

// errSpawnUnavailable is the V5 refusal: no spawn-controller configured
// means cells are unavailable while ToolHive MCP keeps serving.
func errSpawnUnavailable() error {
	return fmt.Errorf("%w: %s", api.ErrIsolation, locale.T(locale.Fallback(), "toolhub.cells-unavailable"))
}
