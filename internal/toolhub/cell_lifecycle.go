package toolhub

import (
	"context"
	"fmt"
	"github.com/letya999/hermes-hub/internal/identity"
	"github.com/letya999/hermes-hub/internal/toolhub/api"
	"log"
	"time"
)

// warmCell returns a verified warm cell for the key. A cached cell that fails
// the reuse guard is destroyed and audited, then a fresh cell takes the slot;
// reuse never crosses a principal or trust boundary because the key embeds
// them and the guard re-verifies image/mounts on every claim.
func (c *genericController) warmCell(ctx context.Context, req api.CliExecRequest, image, key string, toolset []string) (*cliCell, error) {
	c.mu.Lock()
	c.cells()
	cell, ok := c.cells().cells[key]
	c.mu.Unlock()
	if ok {
		if problem := c.cellDrifted(ctx, cell); problem != "" {
			log.Printf("cli-cell: canary drift name=%s key=%s problem=%s — killing", cell.name, key, problem)
			c.mu.Lock()
			c.cells().metrics.canaryFails++
			delete(c.cells().cells, key)
			c.mu.Unlock()
			c.removeCell(context.Background(), cell, "canary-drift:"+problem)
			ok = false
		}
	}
	if ok {
		c.mu.Lock()
		c.cells().metrics.reuses++
		c.mu.Unlock()
		return cell, nil
	}
	// Cap the warm table: evict the oldest idle cell when the registry is
	// full rather than letting binding churn grow without bound.
	cap_ := c.config.MaxActive
	if cap_ < 4 {
		cap_ = 4
	}
	c.mu.Lock()
	for len(c.cells().cells) >= cap_ {
		var oldest *cliCell
		for _, member := range c.cells().cells {
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
		delete(c.cells().cells, oldest.key)
		go c.removeCell(context.Background(), oldest, "evicted-capacity")
	}
	full := len(c.cells().cells) >= cap_
	c.mu.Unlock()
	if full {
		return nil, fmt.Errorf("%w: cli cell table is full", api.ErrIsolation)
	}
	cell, err := c.createCell(ctx, req, image, key, toolset)
	if err != nil {
		c.evictToolbox(req.ToolboxID)
		return nil, err
	}
	c.mu.Lock()
	if existing, raced := c.cells().cells[key]; raced {
		c.mu.Unlock()
		c.removeCell(context.Background(), cell, "superseded")
		return existing, nil
	}
	c.cells().cells[key] = cell
	c.mu.Unlock()
	return cell, nil
}

// createCell runs create -> pre-start inspect -> start -> canary seed ->
// post-start inspect and returns the cell.
func (c *genericController) createCell(ctx context.Context, req api.CliExecRequest, image, key string, toolset []string) (*cliCell, error) {
	cfg := c.config.CLI
	if err := c.cellInitSeed(); err != nil {
		return nil, err
	}
	runtime, err := c.runtimeFlag(ctx)
	if err != nil {
		return nil, err
	}
	workHost, workRW, err := c.cellWorkspaceBind(req)
	if err != nil {
		return nil, err
	}
	lifecycle := req.Lifecycle
	if lifecycle == "" {
		lifecycle = api.CLILifecycleEphemeral
	}
	cell := &cliCell{
		name: "cli-" + cellHash(image+api.RandomNonce()), key: key, principal: req.Principal, bindingID: req.BindingID,
		jobID: req.JobID, lifecycle: lifecycle, image: image, runtime: runtime,
		workHost: workHost, workRW: workRW, toolset: append([]string(nil), toolset...),
		installPackages: req.InstallPackages,
		canary:          api.RandomNonce(), created: time.Now(), lastUse: time.Now(), execSlot: make(chan struct{}, 1),
	}
	egress := realEgress(req.Plan.Execution.Egress)
	if len(egress) > 0 || len(req.Brokered) > 0 {
		if cfg.ProxyImage == "" {
			return nil, fmt.Errorf("%w: egress cells need a proxy image", api.ErrIsolation)
		}
		network, proxies, err := c.cellEgress(ctx, cell, egress, req.Brokered)
		if err != nil {
			return nil, err
		}
		cell.network = network
		for _, s := range proxies {
			cell.sidecars = append(cell.sidecars, s.name)
			if s.volume != "" {
				cell.volumes = append(cell.volumes, s.volume)
			}
		}
	}
	created := false
	defer func() {
		if !created {
			remove, cancel := context.WithTimeout(context.Background(), 30*time.Second)
			c.removeCell(remove, cell, "create-failed")
			cancel()
		}
	}()
	if _, err := c.docker(ctx, c.cellArgv(req, cell, runtime)...); err != nil {
		return nil, fmt.Errorf("%w: cell create: %v", api.ErrIsolation, err)
	}
	if c.scope != nil {
		c.scope.Register(cell.name)
	}
	// The pre-start inspect proves the create-time profile; a drifted daemon
	// answer denies before the cell ever runs.
	container, err := c.inspectCell(ctx, cell.name)
	if err != nil {
		return nil, err
	}
	if problem := cellProfileProblem(container, cell, c.config.SeccompProfile, c.config.StateRoot, false); problem != "" {
		return nil, fmt.Errorf("%w: cell profile: %s", api.ErrIsolation, problem)
	}
	if _, err := c.docker(ctx, "start", cell.name); err != nil {
		return nil, fmt.Errorf("%w: cell start: %v", api.ErrIsolation, err)
	}
	if code, err := c.cellRun(ctx, nil, "exec", cell.name, cellInitGuest, "write", cellCanaryPath, cell.canary); err != nil || code != 0 {
		return nil, fmt.Errorf("%w: cell canary seed", api.ErrIsolation)
	}
	container, err = c.inspectCell(ctx, cell.name)
	if err != nil {
		return nil, err
	}
	if problem := cellProfileProblem(container, cell, c.config.SeccompProfile, c.config.StateRoot, true); problem != "" {
		return nil, fmt.Errorf("%w: started cell profile: %s", api.ErrIsolation, problem)
	}
	created = true
	c.mu.Lock()
	c.cells().metrics.creates++
	c.mu.Unlock()
	log.Printf("cli-cell: create name=%s principal=%s lifecycle=%s image=%s network=%s", cell.name, cell.principal, cell.lifecycle, image, cell.network)
	return cell, nil
}

// removeCell destroys a cell and every per-cell object it owned; the cell is
// dropped from the registry first so no caller can claim it mid-teardown.
func (c *genericController) removeCell(ctx context.Context, cell *cliCell, reason string) {
	if cell == nil {
		return
	}
	c.mu.Lock()
	if cell.key != "" {
		if cur, ok := c.cells().cells[cell.key]; ok && cur == cell {
			delete(c.cells().cells, cell.key)
		}
	}
	for key, list := range c.cells().pool {
		kept := list[:0]
		for _, member := range list {
			if member != cell {
				kept = append(kept, member)
			}
		}
		c.cells().pool[key] = kept
	}
	if _, wasClaimed := c.cells().claimed[cell.name]; wasClaimed {
		delete(c.cells().claimed, cell.name)
		if c.cells().claimedN[cell.image] > 0 {
			c.cells().claimedN[cell.image]--
		}
	}
	c.mu.Unlock()
	log.Printf("cli-cell: kill name=%s principal=%s lifecycle=%s reason=%s", cell.name, cell.principal, cell.lifecycle, reason)
	names := append([]string{cell.name}, cell.sidecars...)
	if _, err := c.docker(ctx, append([]string{"rm", "-f"}, names...)...); err != nil {
		log.Printf("cli-cell: rm %s failed: %v", cell.name, err)
	}
	if cell.network != "" {
		_, _ = c.docker(ctx, "network", "rm", cell.network)
	}
	for _, volume := range cell.volumes {
		_, _ = c.docker(ctx, "volume", "rm", volume)
	}
	if _, err := c.inspectCell(ctx, cell.name); err == nil {
		log.Printf("cli-cell: %s survived forced removal", cell.name)
	}
}

// cellMetricsSnapshot renders the counters /cli-metrics exposes: claim
// latency, canary failures, pool hit rate inputs and rotation counts are the
// acceptance metrics for the warm-reuse ladder.
func (c *genericController) cellMetricsSnapshot() map[string]int64 {
	c.mu.Lock()
	defer c.mu.Unlock()
	m := c.cells().metrics
	return map[string]int64{
		"creates": m.creates, "reuses": m.reuses, "canary_fails": m.canaryFails,
		"rotations": m.rotations, "pool_hits": m.poolHits, "pool_misses": m.poolMisses,
		"pool_claims": m.poolClaims, "pool_exhaustions": m.poolExhaustions,
		"claim_wait_nanos": m.claimWaitNanos,
		"warm_cells":       int64(len(c.cells().cells)),
		"pool_idle": int64(func() (n int) {
			for _, l := range c.cells().pool {
				n += len(l)
			}
			return
		}()),
		"pool_claimed": int64(len(c.cells().claimed)),
	}
}

// releaseCells implements /cli-release: every warm cell matching the selector
// is killed; the audit line carries the reason.
func (c *genericController) releaseCells(ctx context.Context, req api.CliReleaseRequest) {
	if !identity.ValidID(req.PrincipalID) {
		return
	}
	var doomed []*cliCell
	c.mu.Lock()
	for key, cell := range c.cells().cells {
		if cell.principal != req.PrincipalID {
			continue
		}
		if req.BindingID != "" && cell.bindingID != req.BindingID {
			continue
		}
		if req.JobID != "" && cell.jobID != req.JobID {
			continue
		}
		doomed = append(doomed, cell)
		delete(c.cells().cells, key)
	}
	c.mu.Unlock()
	reason := req.Reason
	if reason == "" {
		reason = "release"
	}
	for _, cell := range doomed {
		c.removeCell(ctx, cell, reason)
	}
}

// releaseAllCells kills every cell the controller owns — shutdown path.
func (c *genericController) releaseAllCells(ctx context.Context, reason string) {
	var all []*cliCell
	c.mu.Lock()
	for key, cell := range c.cells().cells {
		all = append(all, cell)
		delete(c.cells().cells, key)
	}
	for key, list := range c.cells().pool {
		all = append(all, list...)
		delete(c.cells().pool, key)
	}
	for name, cell := range c.cells().claimed {
		all = append(all, cell)
		delete(c.cells().claimed, name)
	}
	c.cells().claimedN = map[string]int{}
	c.mu.Unlock()
	for _, cell := range all {
		c.removeCell(ctx, cell, reason)
	}
}

// cleanupCells runs the kill ladder: idle TTL (tier-bounded), max age and
// max reuse rotate cells out; every kill line is audited.
func (c *genericController) cleanupCells(ctx context.Context, now time.Time) {
	cfg := c.config.CLI
	if cfg == nil {
		return
	}
	maxAge := time.Duration(cfg.WarmMaxAgeSeconds) * time.Second
	if maxAge <= 0 {
		maxAge = 24 * time.Hour
	}
	maxReuse := cfg.WarmMaxReuse
	if maxReuse <= 0 {
		maxReuse = 256
	}
	idle := time.Duration(c.config.IdleTTLSeconds) * time.Second
	if idle <= 0 {
		idle = 5 * time.Minute
	}
	var doomed []*cliCell
	var reasons []string
	c.mu.Lock()
	mark := func(cell *cliCell) bool {
		cellIdle := idle
		if cell.lifecycle == api.CLILifecycleTask {
			cellIdle = minDuration(cellIdle, 5*time.Minute)
		}
		switch {
		case now.Sub(cell.lastUse) > cellIdle:
			reasons = append(reasons, "idle-ttl")
		case now.Sub(cell.created) > maxAge:
			reasons = append(reasons, "max-age")
		case cell.uses >= maxReuse:
			reasons = append(reasons, "max-reuse")
		default:
			return false
		}
		return true
	}
	for key, cell := range c.cells().cells {
		if mark(cell) {
			doomed = append(doomed, cell)
			delete(c.cells().cells, key)
		}
	}
	for key, list := range c.cells().pool {
		kept := list[:0]
		for _, cell := range list {
			if mark(cell) {
				doomed = append(doomed, cell)
			} else {
				kept = append(kept, cell)
			}
		}
		c.cells().pool[key] = kept
	}
	c.mu.Unlock()
	for i, cell := range doomed {
		c.mu.Lock()
		c.cells().metrics.rotations++
		c.mu.Unlock()
		c.removeCell(ctx, cell, reasons[i])
	}
}

func minDuration(a, b time.Duration) time.Duration {
	if a < b {
		return a
	}
	return b
}
