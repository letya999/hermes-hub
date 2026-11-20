package toolhub

import (
	"context"
	"fmt"
	"strconv"
	"strings"
	"time"

	spawnv1 "github.com/letya999/hermes-hub/contracts/spawn/v1"
	"github.com/letya999/hermes-hub/internal/identity"
	"github.com/letya999/hermes-hub/internal/toolhub/api"
)

// validateExecRequest is the /cli-exec admission check: plan shape, ceiling
// bounds, approval, identity and workspace policy are all enforced before any
// worker claim happens — the admission contract is unchanged, only the
// executor moved behind spawn-controller (CHG-0336 S6).
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

// execCLI is the synchronous /cli-exec body: validate, place on a lifecycle
// tier, claim a spawn-controller worker, exec, release. The executor moved
// behind contracts/spawn/v1 — admission, image pinning and the toolbox
// member resolution stay here.
func (c *genericController) execCLI(ctx context.Context, req api.CliExecRequest) (api.CliExecResponse, error) {
	if err := c.validateExecRequest(req); err != nil {
		return api.CliExecResponse{}, err
	}
	if c.spawn == nil {
		return api.CliExecResponse{}, errSpawnUnavailable()
	}
	image, err := c.cellImageRef(ctx, req)
	if err != nil {
		return api.CliExecResponse{}, err
	}
	toolset, err := c.resolveToolbox(ctx, req.ToolboxID)
	if err != nil {
		return api.CliExecResponse{}, err
	}
	key := cellWarmKey(req.Lifecycle, req.Principal, req.BindingID, req.JobID, image, toolset)
	if key != "" && req.InstallPackages {
		key += "|install-packages"
	}
	if key == "" {
		// Ephemeral and shared-pool calls get a fresh worker every time —
		// the cross-user per-image pool is gone (S6): a unique claim key
		// can never match a different caller's worker.
		key = "ephemeral|" + api.RandomNonce()
		held, err := c.claimWorker(ctx, req, image, key, toolset)
		if err != nil {
			c.evictToolbox(req.ToolboxID)
			return api.CliExecResponse{}, fmt.Errorf("%w: worker claim: %v", api.ErrIsolation, err)
		}
		defer c.releaseHeld(held, "ephemeral-done")
		return c.execInWorker(ctx, held, req)
	}
	held, err := c.heldWorkerFor(ctx, req, image, key, toolset)
	if err != nil {
		return api.CliExecResponse{}, err
	}
	defer func() { <-held.execSlot }()
	return c.execInWorker(ctx, held, req)
}

// execInWorker runs the tool argv inside the claimed worker through the
// contract; secrets arrive only here, in exec-time env, never in the
// worker spec — the moved v0 execInCell env construction.
func (c *genericController) execInWorker(ctx context.Context, held *heldWorker, req api.CliExecRequest) (api.CliExecResponse, error) {
	limit := int64(req.Plan.Execution.OutputBytes)
	timeout := time.Duration(req.Plan.Execution.TimeoutSeconds)*time.Second + 5*time.Second
	home, path := "/tmp", "/usr/local/bin:/usr/bin:/bin:"+cellToolsetGuest
	if held.installPackages {
		home = cellHomeGuest
		path = home + "/.local/bin:" + home + "/.cargo/bin:" + home + "/go/bin:" + path
	}
	// Cells carrying a toolbox mount the member artifacts read-only under
	// /tools/<i>; expose their conventional bin dirs so a shell (or one tool
	// invoking another) reaches them by name without knowing the mount index.
	for i := range held.toolset {
		member := cellToolsetGuest + "/" + strconv.Itoa(i)
		path += ":" + member + "/usr/local/bin:" + member + "/usr/bin:" + member + "/bin"
	}
	env := map[string]string{"PATH": path, "HOME": home}
	for _, pair := range req.Env {
		key, value, _ := strings.Cut(pair, "=")
		env[key] = value
	}
	if held.installPackages {
		env["TMPDIR"] = cellUserTmpGuest
	}
	// Brokered credentials surface as the proxy URL, never the secret.
	for _, cred := range req.Brokered {
		env[cred.EnvName] = "http://" + held.cred + ":" + strconv.Itoa(credProxyPort) + "/" + cred.Host
	}
	if held.proxy != "" {
		env["HTTP_PROXY"] = held.proxy
		env["HTTPS_PROXY"] = held.proxy
		env["http_proxy"] = held.proxy
		env["https_proxy"] = held.proxy
		noProxy := "127.0.0.1"
		if held.cred != "" {
			noProxy += "," + held.cred
		}
		env["NO_PROXY"], env["no_proxy"] = noProxy, noProxy
	}
	command := req.Command
	if held.lifecycle == api.CLILifecycleToolbox {
		// Toolbox commands are guest paths inside the member image mounted at
		// /tools/<i>; the request's plan image selects the member. Toolset
		// entries are resolved daemon IDs, so the plan ref resolves too.
		idx := -1
		want, wantErr := c.resolveCellImage(ctx, req.Plan.Image+"@"+req.Plan.Digest)
		if wantErr != nil {
			want, wantErr = c.resolveCellImage(ctx, req.Plan.Image)
		}
		if wantErr == nil {
			for i, member := range held.toolset {
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
	// The call's deadline follows the exec's own bound plus margin for the
	// server round trip — the HTTP client carries no fixed timeout, so the
	// deadline is what actually bounds the call.
	execCtx, cancel := context.WithTimeout(ctx, timeout+15*time.Second)
	defer cancel()
	res, err := c.spawn.ExecWorker(execCtx, held.id, spawnv1.WorkerExecRequest{
		Org: spawnOrg, Owner: held.principal, Lease: held.lease,
		Argv:      append([]string{command}, req.Args...),
		Env:       env,
		Dir:       cellWorkGuest,
		TimeoutMs: timeout.Milliseconds(),
		MaxOutput: limit,
	})
	if held.lifecycle != api.CLILifecycleEphemeral && held.lifecycle != api.CLILifecycleSharedPool {
		// A timed-out or crashed tool may have left children behind; sweep
		// them so the next claim sees a clean process table — the v0
		// detached `cellinit sweep`, now through the contract.
		sweep, sweepCancel := context.WithTimeout(context.Background(), 10*time.Second)
		_, _ = c.spawn.ExecWorker(sweep, held.id, spawnv1.WorkerExecRequest{
			Org: spawnOrg, Owner: held.principal, Lease: held.lease,
			Argv: []string{cellInitGuest, "sweep"}, TimeoutMs: 8000,
		})
		sweepCancel()
	}
	combined := string(res.Stdout) + string(res.Stderr)
	truncated := res.StdoutTruncated || res.StderrTruncated
	if int64(len(combined)) > limit {
		combined, truncated = combined[:limit], true
	}
	if truncated {
		// The tool exceeded its output cap; the response still carries the
		// verified receipt — the failure is the tool's, not isolation's.
		return api.CliExecResponse{Receipt: c.workerReceipt(held, req), Output: combined, ExitCode: res.ExitCode, Error: "output-limit"}, nil
	}
	if err != nil {
		return api.CliExecResponse{}, fmt.Errorf("%w: cell exec: %v", api.ErrIsolation, err)
	}
	held.uses++
	held.lastUse = time.Now()
	return api.CliExecResponse{Receipt: c.workerReceipt(held, req), Output: combined, ExitCode: res.ExitCode}, nil
}

// workerReceipt is the moved cellReceipt: every isolation claim now names
// the spawn-controller worker the exec ran in.
func (c *genericController) workerReceipt(held *heldWorker, req api.CliExecRequest) api.AdmissionReceipt {
	runtime := c.config.CLI.Runtime
	if runtime == "" {
		runtime = "runc"
	}
	receipt := api.AdmissionReceipt{
		WorkloadID: req.Plan.WorkloadID, State: "running", Enforced: true,
		Execution: req.Plan.Execution, Isolation: append([]string(nil), api.CliIsolationScopes...),
		CellID: held.id, Runtime: runtime,
	}
	if req.Plan.Image != "" {
		receipt.ImageDigest = req.Plan.Digest
	}
	return receipt
}
