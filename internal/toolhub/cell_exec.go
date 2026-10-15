package toolhub

import (
	"context"
	"fmt"
	"github.com/letya999/hermes-hub/internal/identity"
	"github.com/letya999/hermes-hub/internal/toolhub/api"
	"io"
	"strconv"
	"strings"
	"time"
)

// validateExecRequest is the /cli-exec admission check: plan shape, ceiling
// bounds, approval, identity and workspace policy are all enforced before any
// docker call happens.
func (c *genericController) validateExecRequest(req api.CliExecRequest) error {
	cfg := c.config.CLI
	if cfg == nil {
		return fmt.Errorf("%w: CLI cells are not configured", api.ErrIsolation)
	}
	plan := req.Plan
	if !identity.ValidID(plan.WorkloadID) || !identity.ValidID(plan.DefinitionID) || !api.VersionPattern.MatchString(plan.DefinitionVersion) {
		return fmt.Errorf("%w: cli plan identity", api.ErrInvalid)
	}
	if !identity.ValidID(req.Principal) || !identity.ValidID(req.ContextID) || !identity.ValidID(req.BindingID) {
		return fmt.Errorf("%w: cli call identity", api.ErrInvalid)
	}
	if req.JobID != "" && !identity.ValidID(req.JobID) {
		return fmt.Errorf("%w: cli job id", api.ErrInvalid)
	}
	if len(plan.CredentialMounts) != 0 || len(plan.Execution.Mounts) != 0 || len(plan.Execution.TCPForwards) != 0 || plan.WorkspacePath != "" || len(plan.SidecarImages) != 0 || plan.ToolHiveVersion != "" {
		return fmt.Errorf("%w: cli plan carries container-mcp fields", api.ErrInvalid)
	}
	if !api.ExecutionWithin(plan.Execution, cfg.Ceiling) {
		return fmt.Errorf("%w: cli plan exceeds the configured ceiling", api.ErrUnauthorized)
	}
	if req.InstallPackages && (!cfg.AllowPackageInstall || req.Stateless || req.Lifecycle == api.CLILifecycleSharedPool || req.Lifecycle == api.CLILifecycleToolbox) {
		return fmt.Errorf("%w: package installation needs operator approval and a private cell", api.ErrUnauthorized)
	}
	for _, host := range plan.Execution.Egress {
		if !api.Contains(cfg.AllowedEgress, host) && !api.Contains(cfg.AllowedEgress, "*") {
			return fmt.Errorf("%w: cli egress %q is not operator-approved", api.ErrUnauthorized, host)
		}
	}
	// Catalog commands are bare names on the tools image; artifact plans carry
	// image+digest and an absolute guest command path.
	commandOK := api.CommandPattern.MatchString(req.Command) && !strings.ContainsAny(req.Command, "/")
	if req.Plan.Image != "" {
		commandOK = strings.HasPrefix(req.Command, "/") && api.ValidCommand(req.Command)
	}
	if !commandOK || len(req.Args) > 128 {
		return fmt.Errorf("%w: cli command shape", api.ErrInvalid)
	}
	for _, arg := range req.Args {
		if arg == "" || len(arg) > 256 || strings.ContainsAny(arg, "\x00\r\n") {
			return fmt.Errorf("%w: cli argument", api.ErrInvalid)
		}
	}
	if len(req.Env) > 64 {
		return fmt.Errorf("%w: too many cli env vars", api.ErrInvalid)
	}
	for _, pair := range req.Env {
		key, value, ok := strings.Cut(pair, "=")
		// Proxy and loader env are controller-owned: the cell sets its own
		// proxy vars and a caller-set value would silently bypass the cell's
		// allowlisted egress or LD_PRELOAD a binary.
		if !ok || !api.CredentialPattern.MatchString(key) || strings.HasSuffix(key, "_PROXY") || strings.HasPrefix(key, "NO_PROXY") || key == "PATH" || key == "HOME" || strings.HasPrefix(key, "LD_") || len(value) > 16384 || strings.ContainsAny(value, "\x00\r\n") {
			return fmt.Errorf("%w: cli env %q", api.ErrUnauthorized, key)
		}
	}
	switch req.Lifecycle {
	case "", api.CLILifecycleEphemeral:
	case api.CLILifecycleTask:
		if req.JobID == "" {
			return fmt.Errorf("%w: task cells need a job id", api.ErrInvalid)
		}
		if req.Workspace.Scope != "" && req.Workspace.Scope != "none" {
			return fmt.Errorf("%w: task cells carry no workspace bind", api.ErrInvalid)
		}
	case api.CLILifecycleBinding:
	case api.CLILifecycleToolbox:
		if req.ToolboxID == "" {
			return fmt.Errorf("%w: toolbox cells need a toolbox id", api.ErrInvalid)
		}
	case api.CLILifecycleSharedPool:
		if !req.Stateless || (req.Workspace.Scope != "" && req.Workspace.Scope != "none") {
			return fmt.Errorf("%w: pool cells admit stateless workspace-free tools only", api.ErrUnauthorized)
		}
	default:
		return fmt.Errorf("%w: unknown cli lifecycle %q", api.ErrInvalid, req.Lifecycle)
	}
	if len(req.Brokered) > 8 {
		return fmt.Errorf("%w: too many brokered credentials", api.ErrInvalid)
	}
	for _, cred := range req.Brokered {
		if !api.CredentialPattern.MatchString(cred.EnvName) || !api.HostPattern.MatchString(cred.Host) || cred.Value == "" || len(cred.Value) > 16384 || strings.ContainsAny(cred.Value, "\x00\r\n") || len(cred.Prefix) > 128 {
			return fmt.Errorf("%w: brokered credential shape", api.ErrInvalid)
		}
	}
	if !api.PlanApproved(cfg, req.Plan, req.Command) {
		return fmt.Errorf("%w: cli plan is not operator-approved", api.ErrUnauthorized)
	}
	return nil
}

// execCLI is the synchronous /cli-exec body: validate, place on a cell tier,
// run argv through docker exec, return output + receipt.
func (c *genericController) execCLI(ctx context.Context, req api.CliExecRequest) (api.CliExecResponse, error) {
	if err := c.validateExecRequest(req); err != nil {
		return api.CliExecResponse{}, err
	}
	image, err := c.cellImageRef(ctx, req)
	if err != nil {
		return api.CliExecResponse{}, err
	}
	toolset, err := c.resolveToolbox(ctx, req.ToolboxID)
	if err != nil {
		return api.CliExecResponse{}, err
	}
	if req.Lifecycle == api.CLILifecycleSharedPool {
		return c.execPooled(ctx, req, image)
	}
	key := cellWarmKey(req.Lifecycle, req.Principal, req.BindingID, req.JobID, image, toolset)
	if key != "" && req.InstallPackages {
		key += "|install-packages"
	}
	if key != "" {
		return c.execWarm(ctx, req, image, key, toolset)
	}
	cell, err := c.createCell(ctx, req, image, "", toolset)
	if err != nil {
		c.evictToolbox(req.ToolboxID)
		return api.CliExecResponse{}, err
	}
	defer func() {
		remove, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		c.removeCell(remove, cell, "ephemeral-done")
		cancel()
	}()
	return c.execInCell(ctx, cell, req)
}

// execWarm finds or creates the keyed warm cell, verifies it before reuse and
// execs into it. One exec runs per cell at a time; the exec slot waits inside
// the caller's deadline.
func (c *genericController) execWarm(ctx context.Context, req api.CliExecRequest, image, key string, toolset []string) (api.CliExecResponse, error) {
	cell, err := c.warmCell(ctx, req, image, key, toolset)
	if err != nil {
		return api.CliExecResponse{}, err
	}
	select {
	case cell.execSlot <- struct{}{}:
	case <-ctx.Done():
		return api.CliExecResponse{}, ctx.Err()
	}
	defer func() { <-cell.execSlot }()
	return c.execInCell(ctx, cell, req)
}

// execInCell runs the tool argv inside a prepared cell via docker exec;
// secrets arrive only here, in exec-time env, never in container config.
func (c *genericController) execInCell(ctx context.Context, cell *cliCell, req api.CliExecRequest) (api.CliExecResponse, error) {
	limit := req.Plan.Execution.OutputBytes
	timeout := time.Duration(req.Plan.Execution.TimeoutSeconds)*time.Second + 5*time.Second
	home, path := "/tmp", "/usr/local/bin:/usr/bin:/bin:"+cellToolsetGuest
	if cell.installPackages {
		home = cellHomeGuest
		path = home + "/.local/bin:" + home + "/.cargo/bin:" + home + "/go/bin:" + path
	}
	// Cells carrying a toolbox mount the member artifacts read-only under
	// /tools/<i>; expose their conventional bin dirs so a shell (or one tool
	// invoking another) reaches them by name without knowing the mount index.
	for i := range cell.toolset {
		member := cellToolsetGuest + "/" + strconv.Itoa(i)
		path += ":" + member + "/usr/local/bin:" + member + "/usr/bin:" + member + "/bin"
	}
	env := append([]string{
		"PATH=" + path,
		"HOME=" + home,
	}, req.Env...)
	if cell.installPackages {
		env = append(env, "TMPDIR="+cellUserTmpGuest)
	}
	// Brokered credentials surface as the proxy URL, never the secret.
	for _, cred := range req.Brokered {
		env = append(env, cred.EnvName+"=http://"+cell.name+"-cred:"+strconv.Itoa(credProxyPort)+"/"+cred.Host)
	}
	if cell.network != "" && len(realEgress(req.Plan.Execution.Egress)) > 0 {
		ip, err := c.proxyIP(ctx, cell.name+"-proxy", cell.network)
		if err != nil {
			return api.CliExecResponse{}, err
		}
		proxy := "http://" + ip + ":" + strconv.Itoa(cellProxyPort)
		env = append(env, "HTTP_PROXY="+proxy, "HTTPS_PROXY="+proxy, "http_proxy="+proxy, "https_proxy="+proxy, "NO_PROXY=127.0.0.1,"+cell.name+"-cred", "no_proxy=127.0.0.1,"+cell.name+"-cred")
	}
	command := req.Command
	if cell.lifecycle == api.CLILifecycleToolbox {
		// Toolbox commands are guest paths inside the member image mounted at
		// /tools/<i>; the request's plan image selects the member. Toolset
		// entries are resolved daemon IDs, so the plan ref resolves too.
		idx := -1
		want, wantErr := c.resolveCellImage(ctx, req.Plan.Image+"@"+req.Plan.Digest)
		if wantErr != nil {
			want, wantErr = c.resolveCellImage(ctx, req.Plan.Image)
		}
		if wantErr == nil {
			for i, member := range cell.toolset {
				if member == want {
					idx = i
					break
				}
			}
		}
		if idx < 0 {
			return api.CliExecResponse{}, fmt.Errorf("%w: toolbox member image is not in the toolset", api.ErrUnauthorized)
		}
		command = cellToolsetGuest + "/" + strconv.Itoa(idx) + req.Command
	}
	execArgs := []string{"exec"}
	for _, pair := range env {
		execArgs = append(execArgs, "-e", pair)
	}
	execArgs = append(execArgs, "-w", cellWorkGuest, cell.name, command)
	execArgs = append(execArgs, req.Args...)
	output := &api.BoundedOutput{Limit: limit, Exceeded: make(chan struct{})}
	ctx, cancel := context.WithTimeout(ctx, timeout)
	go func() {
		select {
		case <-output.Exceeded:
			cancel()
		case <-ctx.Done():
		}
	}()
	code, err := c.cellRun(ctx, output, execArgs...)
	cancel()
	if cell.lifecycle != api.CLILifecycleEphemeral && cell.lifecycle != api.CLILifecycleSharedPool {
		// A timed-out or crashed tool may have left children behind; sweep
		// them detached so the next claim sees a clean process table.
		sweep, sweepCancel := context.WithTimeout(context.Background(), 10*time.Second)
		_, _ = c.cellRun(sweep, nil, "exec", cell.name, cellInitGuest, "sweep")
		sweepCancel()
	}
	if output.Overflowed() {
		// The tool exceeded its output cap; the response still carries the
		// verified receipt — the failure is the tool's, not isolation's.
		return api.CliExecResponse{Receipt: c.cellReceipt(cell, req), Output: output.String(), ExitCode: code, Error: "output-limit"}, nil
	}
	if err != nil {
		return api.CliExecResponse{}, fmt.Errorf("%w: cell exec: %v", api.ErrIsolation, err)
	}
	cell.uses++
	cell.lastUse = time.Now()
	return api.CliExecResponse{Receipt: c.cellReceipt(cell, req), Output: output.String(), ExitCode: code}, nil
}

func (c *genericController) cellReceipt(cell *cliCell, req api.CliExecRequest) api.AdmissionReceipt {
	runtime := cell.runtime
	if runtime == "" {
		runtime = "runc"
	}
	receipt := api.AdmissionReceipt{
		WorkloadID: req.Plan.WorkloadID, State: "running", Enforced: true,
		Execution: req.Plan.Execution, Isolation: append([]string(nil), api.CliIsolationScopes...),
		CellID: cell.name, Runtime: runtime,
	}
	if req.Plan.Image != "" {
		receipt.ImageDigest = req.Plan.Digest
	}
	return receipt
}

// cellRun runs a docker argv that may fail with the tool's own exit code
// (exec/write/procs/clean/sweep); output streams through the optional sink.
func (c *genericController) cellRun(ctx context.Context, sink io.Writer, args ...string) (int, error) {
	if c.scope != nil {
		if err := c.scope.GuardDocker(ctx, args); err != nil {
			return -1, err
		}
	}
	runner := c.commandExit
	if runner == nil {
		runner = api.LocalCommandExit
	}
	return runner(ctx, sink, "docker", args...)
}

// execPooled runs a stateless call on a shared pool cell: claim, canary
// reset, exec, sweep, return — a dirty cell is quarantined (killed), never
// returned to the pool.
func (c *genericController) execPooled(ctx context.Context, req api.CliExecRequest, image string) (api.CliExecResponse, error) {
	poolSize := c.config.CLI.PoolSize
	if poolSize < 1 {
		poolSize = 1
	}
	waiting := time.Now()
	defer func() {
		c.mu.Lock()
		c.cells().metrics.claimWaitNanos += time.Since(waiting).Nanoseconds()
		c.mu.Unlock()
	}()
	claimWait := time.Duration(c.config.CLI.PoolClaimWaitSeconds) * time.Second
	if claimWait <= 0 {
		claimWait = 30 * time.Second
	}
	deadline := waiting.Add(claimWait)
	for {
		cell := c.claimPoolCell(image)
		if cell != nil {
			resp, err := c.execPoolOnce(ctx, cell, req)
			if err == errCellDirty {
				c.releasePoolClaim(image, cell)
				c.removeCell(context.Background(), cell, "pool-dirty")
				continue
			}
			return resp, err
		}
		c.mu.Lock()
		live := len(c.cells().pool[image]) + c.cells().claimedN[image]
		if live >= poolSize {
			c.mu.Unlock()
			if time.Now().After(deadline) {
				c.mu.Lock()
				c.cells().metrics.poolExhaustions++
				c.mu.Unlock()
				return api.CliExecResponse{}, fmt.Errorf("%w: pool exhausted", api.ErrIsolation)
			}
			select {
			case <-ctx.Done():
				return api.CliExecResponse{}, ctx.Err()
			case <-time.After(100 * time.Millisecond):
			}
			continue
		}
		c.cells().claimedN[image]++
		c.mu.Unlock()
		cell, err := c.createCell(ctx, req, image, "", nil)
		if err != nil {
			c.releasePoolClaim(image, nil)
			return api.CliExecResponse{}, err
		}
		c.mu.Lock()
		c.cells().claimed[cell.name] = cell
		c.mu.Unlock()
		c.mu.Lock()
		c.cells().metrics.poolMisses++
		c.mu.Unlock()
		return c.execPoolDirect(ctx, cell, req)
	}
}

var errCellDirty = fmt.Errorf("pool cell failed reset")

// claimPoolCell takes an idle cell from the pool; it stays claimed (busy)
// until returnPoolCell hands it back or releasePoolClaim drops the slot.
func (c *genericController) claimPoolCell(image string) *cliCell {
	c.mu.Lock()
	defer c.mu.Unlock()
	list := c.cells().pool[image]
	for i, cell := range list {
		select {
		case cell.execSlot <- struct{}{}:
			c.cells().pool[image] = append(list[:i], list[i+1:]...)
			c.cells().claimedN[image]++
			c.cells().claimed[cell.name] = cell
			c.cells().metrics.poolHits++
			c.cells().metrics.poolClaims++
			return cell
		default:
		}
	}
	return nil
}

// releasePoolClaim drops a claimed slot without returning the cell: failed
// creates and dirty cells give their capacity back before removal.
func (c *genericController) releasePoolClaim(image string, cell *cliCell) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.cells().claimedN[image] > 0 {
		c.cells().claimedN[image]--
	}
	if cell != nil {
		delete(c.cells().claimed, cell.name)
	}
}

// returnPoolCell hands a claimed cell back to the idle pool; when the pool is
// already at cap the extra cell is destroyed instead of idling forever.
func (c *genericController) returnPoolCell(cell *cliCell) bool {
	poolSize := c.config.CLI.PoolSize
	if poolSize < 1 {
		poolSize = 1
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.cells().claimedN[cell.image] > 0 {
		c.cells().claimedN[cell.image]--
	}
	delete(c.cells().claimed, cell.name)
	if len(c.cells().pool[cell.image]) >= poolSize {
		return false
	}
	c.cells().pool[cell.image] = append(c.cells().pool[cell.image], cell)
	return true
}

// execPoolDirect execs on a freshly created pool cell (caller holds the only
// reference), then returns the cell to the idle pool.
func (c *genericController) execPoolDirect(ctx context.Context, cell *cliCell, req api.CliExecRequest) (api.CliExecResponse, error) {
	resp, err := c.execInCell(ctx, cell, req)
	if err != nil {
		c.releasePoolClaim(cell.image, cell)
		c.removeCell(context.Background(), cell, "pool-first-failed")
		return api.CliExecResponse{}, err
	}
	if !c.returnPoolCell(cell) {
		c.removeCell(context.Background(), cell, "pool-surplus")
	}
	return resp, nil
}

// execPoolOnce guards the claim with the same drift check warm cells get,
// then execs and returns the cell to the pool only when it verifies clean.
func (c *genericController) execPoolOnce(ctx context.Context, cell *cliCell, req api.CliExecRequest) (api.CliExecResponse, error) {
	defer func() { <-cell.execSlot }()
	if problem := c.cellDrifted(ctx, cell); problem != "" {
		return api.CliExecResponse{}, errCellDirty
	}
	resp, err := c.execInCell(ctx, cell, req)
	if err != nil {
		c.releasePoolClaim(cell.image, cell)
		c.removeCell(context.Background(), cell, "pool-exec-failed")
		return api.CliExecResponse{}, err
	}
	if !c.returnPoolCell(cell) {
		c.removeCell(context.Background(), cell, "pool-surplus")
	}
	return resp, nil
}
