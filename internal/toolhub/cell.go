package toolhub

// Controller-owned CLI sandbox cells. A bounded-cli call no longer execs
// inside the ToolHub container; the controller creates a sibling container —
// the cell — that carries exactly one workspace bind, the read-only cellinit
// helper, no host state, no broker material and no docker socket. Warm tiers
// reuse cells behind a re-inspect + canary guard; anything that drifts is
// killed and audited, never reused.

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"hash/fnv"
	"io"
	"log"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/letya999/hermes-hub/internal/identity"
)

// cliWorkspace is the single bind a cell may carry. Scope none mounts
// nothing (the cell gets a scratch tmpfs at /work); binding binds the
// controller-local path the ToolHub resolved through OpenWorkloadWorkspace;
// principal binds <spaces_root>/<principal>/workspace resolved here — or
// <spaces_root>/<principal>/managed/<environment>/workspace when a managed
// deployment tier is configured and that directory exists.
type cliWorkspace struct {
	Scope  string `json:"scope"`
	Path   string `json:"path,omitempty"`
	Access string `json:"access,omitempty"`
}

// cliBrokeredCred is one credential delivered through the cell-local
// cred-proxy sidecar: the cell env carries only
// http://<proxy>/<host> and the proxy injects the real header on the
// reviewed upstream. The value crosses to the controller in the request
// body — over the bearer-authenticated control channel — but never enters
// container config, argv or logs.
type cliBrokeredCred struct {
	EnvName string `json:"env_name"`
	Host    string `json:"host"`
	Prefix  string `json:"prefix,omitempty"`
	Value   string `json:"value"`
}

// cliExecRequest is the /cli-exec contract. Plan carries the immutable
// definition identity and execution policy the same way /admit does.
type cliExecRequest struct {
	Plan            controllerPlan    `json:"plan"`
	Command         string            `json:"command"`
	Args            []string          `json:"args,omitempty"`
	Env             []string          `json:"env,omitempty"`
	Principal       string            `json:"principal_id"`
	ContextID       string            `json:"context_id"`
	BindingID       string            `json:"binding_id"`
	JobID           string            `json:"job_id,omitempty"`
	Lifecycle       string            `json:"lifecycle,omitempty"`
	ToolboxID       string            `json:"toolbox_id,omitempty"`
	Brokered        []cliBrokeredCred `json:"brokered,omitempty"`
	Stateless       bool              `json:"stateless,omitempty"`
	InstallPackages bool              `json:"install_packages,omitempty"`
	Workspace       cliWorkspace      `json:"workspace,omitempty"`
}

type cliExecResponse struct {
	Receipt  AdmissionReceipt `json:"receipt"`
	Output   string           `json:"output"`
	ExitCode int              `json:"exit_code"`
	// Error carries a completed-but-flagged outcome the caller maps to a
	// typed error — "output-limit" today. Denials stay on HTTP errors.
	Error string `json:"error,omitempty"`
}

// cliReleaseRequest kills warm cells selected by principal/binding/job.
type cliReleaseRequest struct {
	PrincipalID string `json:"principal_id"`
	BindingID   string `json:"binding_id,omitempty"`
	JobID       string `json:"job_id,omitempty"`
	Reason      string `json:"reason,omitempty"`
}

// cliCell is one live sandbox container the controller owns.
type cliCell struct {
	name            string
	key             string // warm reuse key; "" for ephemeral and pool cells
	principal       string
	bindingID       string
	jobID           string
	lifecycle       string
	image           string // image@digest ref the cell was created from
	runtime         string // OCI runtime the create used ("" = daemon default runc)
	workHost        string // host path bound at /work; "" when tmpfs scratch
	workRW          bool
	installPackages bool
	network         string   // internal egress network name, "" = none
	sidecars        []string // companion container names (squid, cred-proxy)
	volumes         []string
	toolset         []string // toolbox image mounts
	canary          string
	created         time.Time
	lastUse         time.Time
	uses            int
	execSlot        chan struct{} // cap-1: one exec per cell at a time
}

// cellMetrics are the pool/lifecycle counters the acceptance criteria name;
// every mutating event also writes a controller audit log line.
type cellMetrics struct {
	creates     int64
	reuses      int64
	canaryFails int64
	rotations   int64
	poolHits    int64
	poolMisses  int64
	poolClaims  int64
	// claimWaitNanos accumulates the wall time pool callers spent waiting
	// for a cell; poolExhaustions counts bounded-wait rejections.
	claimWaitNanos  int64
	poolExhaustions int64
}

type cellRegistry struct {
	cells map[string]*cliCell // warm key -> cell
	pool  map[string][]*cliCell
	// claimedN reserves live-cell slots per image under lock so the pool cap
	// bounds idle+claimed cells; claimed maps busy cells by name so shutdown
	// reaps them too.
	claimedN map[string]int
	claimed  map[string]*cliCell
	metrics  cellMetrics
}

func newCellRegistry() *cellRegistry {
	return &cellRegistry{cells: map[string]*cliCell{}, pool: map[string][]*cliCell{}, claimedN: map[string]int{}, claimed: map[string]*cliCell{}}
}

const (
	cellInitGuest    = "/cellinit"
	cellWorkGuest    = "/work"
	cellHomeGuest    = "/cellhome"
	cellUserTmpGuest = "/usertmp"
	cellCanaryPath   = "/tmp/.hermes-canary"
	cellCanaryName   = ".hermes-canary"
	cellToolsetGuest = "/tools"
	credProxyPort    = 3129
	cellProxyPort    = 3128
)

func (c *genericController) cells() *cellRegistry {
	if c.registry == nil {
		c.registry = newCellRegistry()
	}
	return c.registry
}

// cellWarmKey computes the reuse key a lifecycle tier implies; empty means
// ephemeral (fresh cell every call). The key embeds every trust boundary —
// principal, binding or job — so reuse can never cross callers.
func cellWarmKey(lifecycle, principal, bindingID, jobID, image string, toolset []string) string {
	switch lifecycle {
	case CLILifecycleTask:
		if jobID == "" {
			return ""
		}
		return lifecycle + "|" + principal + "|" + jobID + "|" + image
	case CLILifecycleBinding:
		key := lifecycle + "|" + principal + "|" + bindingID + "|" + image
		if len(toolset) > 0 {
			refs := append([]string(nil), toolset...)
			sort.Strings(refs)
			key += "|" + cellHash(strings.Join(refs, " "))
		}
		return key
	case CLILifecycleToolbox:
		refs := append([]string(nil), toolset...)
		sort.Strings(refs)
		return lifecycle + "|" + principal + "|" + cellHash(image, strings.Join(refs, " "))
	default:
		return ""
	}
}

func cellHash(values ...string) string {
	h := sha256.New()
	for _, v := range values {
		h.Write([]byte(v))
		h.Write([]byte{0})
	}
	return hex.EncodeToString(h.Sum(nil))[:24]
}

// cellImageRef resolves the cell image: owner artifact plans pin their own
// image+manifest digest; catalog commands run on the configured tools image.
// Toolbox cells always run the tools image as their base: member artifacts
// mount read-only under /tools/<i>, and an ELF interpreter resolves against
// the cell rootfs — a scratch member base would leave dynamic binaries
// without a loader. Every returned ref is the daemon's image ID —
// create-by-ID so a retagged local name cannot swap content between
// resolution and create.
func (c *genericController) cellImageRef(ctx context.Context, req cliExecRequest) (string, error) {
	if req.Plan.Image != "" && req.Lifecycle != CLILifecycleToolbox {
		if !digestPattern.MatchString(req.Plan.Digest) {
			return "", fmt.Errorf("%w: CLI image plan needs a manifest digest", ErrInvalid)
		}
		// image@manifest-digest resolves when the daemon knows the repo
		// digest (pulled/pushed or containerd-store loads); a locally loaded
		// quarantine image carries only the tag, so the name falls back to
		// it — the same resolution the container-MCP path performs.
		if resolved, err := c.resolveCellImage(ctx, req.Plan.Image+"@"+req.Plan.Digest); err == nil {
			return resolved, nil
		}
		return c.resolveCellImage(ctx, req.Plan.Image)
	}
	return c.resolveCellImage(ctx, c.config.CLI.ToolsImage)
}

// resolveCellImage maps any accepted cell image ref (digest-pinned ref, local
// name:tag, or image ID) to the daemon's immutable image ID. Missing images
// fail closed — a cell never runs an unresolved name.
func (c *genericController) resolveCellImage(ctx context.Context, ref string) (string, error) {
	out, err := c.docker(ctx, "image", "inspect", ref, "--format", "{{.Id}}")
	id := strings.TrimSpace(string(out))
	if err != nil || !dockerImageIDPattern.MatchString(id) {
		return "", fmt.Errorf("%w: cli cell image %q unavailable", ErrIsolation, ref)
	}
	return id, nil
}

// resolveToolbox returns the operator-pinned member images for a toolbox id.
// Each configured image@sha256 ref resolves to the daemon image ID the same
// way the cell image does — locally loaded artifacts carry only the tag.
func (c *genericController) resolveToolbox(ctx context.Context, id string) ([]string, error) {
	if id == "" {
		return nil, nil
	}
	members := c.config.CLI.Toolboxes[id]
	if len(members) == 0 {
		return nil, fmt.Errorf("%w: unknown cli toolbox %q", ErrUnauthorized, id)
	}
	c.mu.Lock()
	cached, ok := c.toolboxes[id]
	c.mu.Unlock()
	if ok {
		return append([]string(nil), cached...), nil
	}
	resolved := make([]string, 0, len(members))
	for _, ref := range members {
		image, err := c.resolveCellImage(ctx, ref)
		if err != nil {
			if at := strings.LastIndex(ref, "@"); at > 0 {
				image, err = c.resolveCellImage(ctx, ref[:at])
			}
		}
		if err != nil {
			return nil, err
		}
		resolved = append(resolved, image)
	}
	c.mu.Lock()
	if c.toolboxes == nil {
		c.toolboxes = map[string][]string{}
	}
	c.toolboxes[id] = append([]string(nil), resolved...)
	c.mu.Unlock()
	return resolved, nil
}

// evictToolbox drops the cached member resolution for id; a failed cell
// create can mean a resolved image ID was pruned between resolution and use.
func (c *genericController) evictToolbox(id string) {
	if id == "" {
		return
	}
	c.mu.Lock()
	delete(c.toolboxes, id)
	c.mu.Unlock()
}

// cellWorkspaceBind resolves the request's workspace into the host-side bind
// source; the empty string means the cell mounts nothing at /work.
func (c *genericController) cellWorkspaceBind(req cliExecRequest) (hostPath string, rw bool, err error) {
	cfg := c.config.CLI
	scope := req.Workspace.Scope
	if scope == "" {
		scope = "none"
	}
	switch scope {
	case "none":
		if req.Workspace.Path != "" {
			return "", false, fmt.Errorf("%w: workspace path without a scope", ErrInvalid)
		}
		return "", false, nil
	case "binding":
		access := req.Workspace.Access
		if access == "" {
			access = "rw"
		}
		if access != "rw" && access != "ro" {
			return "", false, fmt.Errorf("%w: binding workspace access", ErrInvalid)
		}
		path, err := filepath.Abs(req.Workspace.Path)
		if err != nil || !containedPath(c.config.StateRoot, path) || noSymlinkPath(path) != nil {
			return "", false, fmt.Errorf("%w: binding workspace outside state root", ErrIsolation)
		}
		info, err := os.Stat(path)
		if err != nil || !info.IsDir() {
			return "", false, fmt.Errorf("%w: binding workspace is missing", ErrIsolation)
		}
		return dockerBindSource(path), access == "rw", nil
	case "principal":
		if cfg.SpacesRoot == "" {
			return "", false, fmt.Errorf("%w: principal workspaces are not configured", ErrIsolation)
		}
		access := req.Workspace.Access
		if access == "" {
			access = "ro"
		}
		if access != "rw" && access != "ro" {
			return "", false, fmt.Errorf("%w: principal workspace access", ErrInvalid)
		}
		path := filepath.Join(cfg.SpacesRoot, req.Principal, "workspace")
		if cfg.Environment != "" {
			// Managed principals keep their real workspace under
			// managed/<env>/workspace; the top-level dir is the unmanaged
			// layout's vestige. Fall back to it when no managed dir exists.
			managed := filepath.Join(cfg.SpacesRoot, req.Principal, "managed", cfg.Environment, "workspace")
			if info, err := os.Stat(managed); err == nil && info.IsDir() {
				path = managed
			}
		}
		if !containedPath(cfg.SpacesRoot, path) || noSymlinkPath(path) != nil {
			return "", false, fmt.Errorf("%w: principal workspace path", ErrIsolation)
		}
		info, err := os.Stat(path)
		if err != nil || !info.IsDir() {
			return "", false, fmt.Errorf("%w: principal workspace is missing", ErrIsolation)
		}
		return dockerBindSource(path), access == "rw", nil
	default:
		return "", false, fmt.Errorf("%w: unknown workspace scope %q", ErrInvalid, scope)
	}
}

// realEgress strips the loopback-only marker hosts; a plan whose egress is
// only loopback runs with --network none.
func realEgress(egress []string) []string {
	out := make([]string, 0, len(egress))
	for _, host := range egress {
		h := strings.ToLower(strings.TrimSpace(host))
		if h == "" || h == "127.0.0.1" || h == "localhost" || h == "::1" {
			continue
		}
		out = append(out, host)
	}
	return out
}

// cellUID picks the cell user: 10001 by default, or a stable per-principal
// uid (20000+) when the operator opted into uid separation — files a rw cell
// writes then carry a principal-derived uid on the host.
func (c *genericController) cellUID(req cliExecRequest, rw bool) string {
	if c.config.CLI.PerPrincipalUID && rw {
		h := fnv.New32a()
		h.Write([]byte(req.Principal))
		uid := 20000 + int(h.Sum32()%30000)
		return strconv.Itoa(uid) + ":" + strconv.Itoa(uid)
	}
	return "10001:10001"
}

// runtimeFlag maps the configured tier onto the docker --runtime flag; empty
// means runc (daemon default). runsc/kata must be listed by the daemon.
func (c *genericController) runtimeFlag(ctx context.Context) (string, error) {
	runtime := c.config.CLI.Runtime
	switch runtime {
	case "", "runc":
		return "", nil
	case "runsc", "kata":
	default:
		return "", fmt.Errorf("%w: unknown cli runtime %q", ErrInvalid, runtime)
	}
	runtimes, err := c.dockerRuntimes(ctx)
	if err != nil {
		return "", err
	}
	if !runtimes[runtime] {
		return "", fmt.Errorf("%w: cli runtime %q is not installed on the daemon", ErrIsolation, runtime)
	}
	return runtime, nil
}

func (c *genericController) dockerRuntimes(ctx context.Context) (map[string]bool, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.runtimesOK {
		return c.runtimes, nil
	}
	body, err := c.docker(ctx, "info", "--format", "{{json .Runtimes}}")
	if err != nil {
		return nil, fmt.Errorf("%w: docker runtime list: %v", ErrIsolation, err)
	}
	var runtimes map[string]json.RawMessage
	if json.Unmarshal(body, &runtimes) != nil {
		return nil, fmt.Errorf("%w: docker runtime list unparseable", ErrIsolation)
	}
	names := map[string]bool{}
	for name := range runtimes {
		names[name] = true
	}
	c.runtimes, c.runtimesOK = names, true
	return names, nil
}

// cellArgv assembles the docker create argv for a cell. Every isolation claim
// the receipt later makes is set here and verified back from inspect.
func (c *genericController) cellArgv(req cliExecRequest, cell *cliCell, runtime string) []string {
	limits := req.Plan.Execution
	args := []string{
		"create", "--name", cell.name,
		"--user", c.cellUID(req, cell.workRW),
		"--read-only",
		"--cap-drop", "ALL",
		"--security-opt", "no-new-privileges=true",
		"--security-opt", "seccomp=" + c.config.SeccompProfile,
		"--tmpfs", "/tmp:rw,noexec,nosuid,nodev,size=64m",
		"--cpus", strconv.FormatFloat(float64(limits.CPUMillis)/1000, 'f', 3, 64),
		"--memory", strconv.Itoa(limits.MemoryMiB) + "m",
		"--memory-swap", strconv.Itoa(limits.MemoryMiB) + "m",
		"--pids-limit", strconv.Itoa(limits.MaxPIDs),
		"--workdir", cellWorkGuest,
	}
	if runtime != "" {
		args = append(args, "--runtime", runtime)
	}
	if cell.installPackages {
		uid, gid, _ := strings.Cut(c.cellUID(req, cell.workRW), ":")
		homeMiB := limits.MemoryMiB / 2
		if homeMiB < 256 {
			homeMiB = 256
		}
		args = append(args, "--tmpfs", cellHomeGuest+":rw,exec,nosuid,nodev,size="+strconv.Itoa(homeMiB)+"m,mode=0700,uid="+uid+",gid="+gid)
		args = append(args, "--tmpfs", cellUserTmpGuest+":rw,exec,nosuid,nodev,size=256m,mode=0700,uid="+uid+",gid="+gid)
	}
	if cell.network == "" {
		args = append(args, "--network", "none")
	} else {
		args = append(args, "--network", cell.network)
	}
	labels := append(c.scopeLabelArgs(),
		"--label", "hermes-hub.cell=true",
		"--label", "hermes-hub.cell.principal="+req.Principal,
		"--label", "hermes-hub.cell.lifecycle="+cell.lifecycle,
	)
	if req.BindingID != "" {
		labels = append(labels, "--label", "hermes-hub.cell.binding="+req.BindingID)
	}
	if req.JobID != "" {
		labels = append(labels, "--label", "hermes-hub.cell.job="+req.JobID)
	}
	args = append(args, labels...)
	// The helper is mounted ro from the controller-visible copy; the daemon
	// resolves the host path so the cell never sees controller state.
	args = append(args, "--mount", "type=bind,source="+c.cellInitHost()+",target="+cellInitGuest+",readonly")
	if cell.workHost != "" {
		mount := "type=bind,source=" + cell.workHost + ",target=" + cellWorkGuest
		if !cell.workRW {
			mount += ",readonly"
		}
		args = append(args, "--mount", mount)
	} else {
		args = append(args, "--tmpfs", cellWorkGuest+":rw,noexec,nosuid,nodev,size=64m")
	}
	for i, ref := range cell.toolset {
		args = append(args, "--mount", "type=image,source="+ref+",target="+cellToolsetGuest+"/"+strconv.Itoa(i)+",readonly")
	}
	args = append(args, "--entrypoint", cellInitGuest, cell.image, "pause")
	return args
}

// cellInitHost returns the state-root copy of cellinit — the source the
// daemon resolves when a sibling bind mounts the helper.
func (c *genericController) cellInitHost() string {
	return dockerBindSource(filepath.Join(c.config.StateRoot, "cli-cellinit"))
}

// cellInitSeed copies the shipped cellinit into the state root so a sibling
// bind mount can source it. Re-seeded on every controller start so an
// upgraded binary replaces any stale or tampered copy.
func (c *genericController) cellInitSeed() error {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.cellInitOK {
		return nil
	}
	src, err := os.Open(c.config.CLI.CellInit)
	if err != nil {
		return fmt.Errorf("%w: cellinit binary: %v", ErrIsolation, err)
	}
	defer src.Close()
	info, err := src.Stat()
	if err != nil || !info.Mode().IsRegular() {
		return fmt.Errorf("%w: cellinit is not a regular file", ErrIsolation)
	}
	tmp, err := os.CreateTemp(c.config.StateRoot, ".cellinit-*")
	if err != nil {
		return err
	}
	tmpName := tmp.Name()
	defer os.Remove(tmpName)
	if _, err = io.Copy(tmp, io.LimitReader(src, 64<<20)); err != nil {
		_ = tmp.Close()
		return err
	}
	if err = tmp.Close(); err != nil {
		return err
	}
	if err = os.Chmod(tmpName, 0755); err != nil {
		return err
	}
	if err = os.Rename(tmpName, filepath.Join(c.config.StateRoot, "cli-cellinit")); err != nil {
		return err
	}
	c.cellInitOK = true
	return nil
}

// validateExecRequest is the /cli-exec admission check: plan shape, ceiling
// bounds, approval, identity and workspace policy are all enforced before any
// docker call happens.
func (c *genericController) validateExecRequest(req cliExecRequest) error {
	cfg := c.config.CLI
	if cfg == nil {
		return fmt.Errorf("%w: CLI cells are not configured", ErrIsolation)
	}
	plan := req.Plan
	if !identity.ValidID(plan.WorkloadID) || !identity.ValidID(plan.DefinitionID) || !versionPattern.MatchString(plan.DefinitionVersion) {
		return fmt.Errorf("%w: cli plan identity", ErrInvalid)
	}
	if !identity.ValidID(req.Principal) || !identity.ValidID(req.ContextID) || !identity.ValidID(req.BindingID) {
		return fmt.Errorf("%w: cli call identity", ErrInvalid)
	}
	if req.JobID != "" && !identity.ValidID(req.JobID) {
		return fmt.Errorf("%w: cli job id", ErrInvalid)
	}
	if len(plan.CredentialMounts) != 0 || len(plan.Execution.Mounts) != 0 || len(plan.Execution.TCPForwards) != 0 || plan.WorkspacePath != "" || len(plan.SidecarImages) != 0 || plan.ToolHiveVersion != "" {
		return fmt.Errorf("%w: cli plan carries container-mcp fields", ErrInvalid)
	}
	if !executionWithin(plan.Execution, cfg.Ceiling) {
		return fmt.Errorf("%w: cli plan exceeds the configured ceiling", ErrUnauthorized)
	}
	if req.InstallPackages && (!cfg.AllowPackageInstall || req.Stateless || req.Lifecycle == CLILifecycleSharedPool || req.Lifecycle == CLILifecycleToolbox) {
		return fmt.Errorf("%w: package installation needs operator approval and a private cell", ErrUnauthorized)
	}
	for _, host := range plan.Execution.Egress {
		if !contains(cfg.AllowedEgress, host) && !contains(cfg.AllowedEgress, "*") {
			return fmt.Errorf("%w: cli egress %q is not operator-approved", ErrUnauthorized, host)
		}
	}
	// Catalog commands are bare names on the tools image; artifact plans carry
	// image+digest and an absolute guest command path.
	commandOK := commandPattern.MatchString(req.Command) && !strings.ContainsAny(req.Command, "/")
	if req.Plan.Image != "" {
		commandOK = strings.HasPrefix(req.Command, "/") && validCommand(req.Command)
	}
	if !commandOK || len(req.Args) > 128 {
		return fmt.Errorf("%w: cli command shape", ErrInvalid)
	}
	for _, arg := range req.Args {
		if arg == "" || len(arg) > 256 || strings.ContainsAny(arg, "\x00\r\n") {
			return fmt.Errorf("%w: cli argument", ErrInvalid)
		}
	}
	if len(req.Env) > 64 {
		return fmt.Errorf("%w: too many cli env vars", ErrInvalid)
	}
	for _, pair := range req.Env {
		key, value, ok := strings.Cut(pair, "=")
		// Proxy and loader env are controller-owned: the cell sets its own
		// proxy vars and a caller-set value would silently bypass the cell's
		// allowlisted egress or LD_PRELOAD a binary.
		if !ok || !credentialPattern.MatchString(key) || strings.HasSuffix(key, "_PROXY") || strings.HasPrefix(key, "NO_PROXY") || key == "PATH" || key == "HOME" || strings.HasPrefix(key, "LD_") || len(value) > 16384 || strings.ContainsAny(value, "\x00\r\n") {
			return fmt.Errorf("%w: cli env %q", ErrUnauthorized, key)
		}
	}
	switch req.Lifecycle {
	case "", CLILifecycleEphemeral:
	case CLILifecycleTask:
		if req.JobID == "" {
			return fmt.Errorf("%w: task cells need a job id", ErrInvalid)
		}
		if req.Workspace.Scope != "" && req.Workspace.Scope != "none" {
			return fmt.Errorf("%w: task cells carry no workspace bind", ErrInvalid)
		}
	case CLILifecycleBinding:
	case CLILifecycleToolbox:
		if req.ToolboxID == "" {
			return fmt.Errorf("%w: toolbox cells need a toolbox id", ErrInvalid)
		}
	case CLILifecycleSharedPool:
		if !req.Stateless || (req.Workspace.Scope != "" && req.Workspace.Scope != "none") {
			return fmt.Errorf("%w: pool cells admit stateless workspace-free tools only", ErrUnauthorized)
		}
	default:
		return fmt.Errorf("%w: unknown cli lifecycle %q", ErrInvalid, req.Lifecycle)
	}
	if len(req.Brokered) > 8 {
		return fmt.Errorf("%w: too many brokered credentials", ErrInvalid)
	}
	for _, cred := range req.Brokered {
		if !credentialPattern.MatchString(cred.EnvName) || !hostPattern.MatchString(cred.Host) || cred.Value == "" || len(cred.Value) > 16384 || strings.ContainsAny(cred.Value, "\x00\r\n") || len(cred.Prefix) > 128 {
			return fmt.Errorf("%w: brokered credential shape", ErrInvalid)
		}
	}
	if !planApproved(cfg, req.Plan, req.Command) {
		return fmt.Errorf("%w: cli plan is not operator-approved", ErrUnauthorized)
	}
	return nil
}

// validCellImageRef accepts the three refs a cell image may take: an
// image@sha256 digest pin, a local image ID, or a local name[:tag] the
// daemon resolves to an image ID at create time. Toolbox members stay
// digest-pinned only — caller-controlled surfaces never take a bare tag.
func validCellImageRef(ref string) error {
	if at := strings.LastIndex(ref, "@"); at >= 0 {
		if at == 0 || !digestPattern.MatchString(ref[at+1:]) || strings.ContainsAny(ref[:at], " \t\r\n") || strings.Contains(ref, "..") {
			return fmt.Errorf("%w: cell image %q must be digest-pinned", ErrInvalid, ref)
		}
		return nil
	}
	if dockerImageIDPattern.MatchString(ref) {
		return nil
	}
	if err := validateLocalImageName(ref); err != nil {
		return fmt.Errorf("%w: cell image %q", ErrInvalid, ref)
	}
	return nil
}

// validPinnedImageRef is the caller-visible form: toolbox members and
// artifact plans must pin an image@sha256 manifest digest; daemon-local
// tags would let the approved surface drift silently.
func validPinnedImageRef(ref string) error {
	at := strings.LastIndex(ref, "@")
	if at <= 0 || !digestPattern.MatchString(ref[at+1:]) || strings.ContainsAny(ref[:at], " \t\r\n") || strings.Contains(ref, "..") {
		return fmt.Errorf("%w: cell image %q must be digest-pinned", ErrInvalid, ref)
	}
	return nil
}

// execCLI is the synchronous /cli-exec body: validate, place on a cell tier,
// run argv through docker exec, return output + receipt.
func (c *genericController) execCLI(ctx context.Context, req cliExecRequest) (cliExecResponse, error) {
	if err := c.validateExecRequest(req); err != nil {
		return cliExecResponse{}, err
	}
	image, err := c.cellImageRef(ctx, req)
	if err != nil {
		return cliExecResponse{}, err
	}
	toolset, err := c.resolveToolbox(ctx, req.ToolboxID)
	if err != nil {
		return cliExecResponse{}, err
	}
	if req.Lifecycle == CLILifecycleSharedPool {
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
		return cliExecResponse{}, err
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
func (c *genericController) execWarm(ctx context.Context, req cliExecRequest, image, key string, toolset []string) (cliExecResponse, error) {
	cell, err := c.warmCell(ctx, req, image, key, toolset)
	if err != nil {
		return cliExecResponse{}, err
	}
	select {
	case cell.execSlot <- struct{}{}:
	case <-ctx.Done():
		return cliExecResponse{}, ctx.Err()
	}
	defer func() { <-cell.execSlot }()
	return c.execInCell(ctx, cell, req)
}

// warmCell returns a verified warm cell for the key. A cached cell that fails
// the reuse guard is destroyed and audited, then a fresh cell takes the slot;
// reuse never crosses a principal or trust boundary because the key embeds
// them and the guard re-verifies image/mounts on every claim.
func (c *genericController) warmCell(ctx context.Context, req cliExecRequest, image, key string, toolset []string) (*cliCell, error) {
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
		return nil, fmt.Errorf("%w: cli cell table is full", ErrIsolation)
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

// cellDrifted runs the warm-reuse guard: cheap re-inspect of the immutable
// profile plus the in-cell canary (token intact, /tmp clean, no stray
// processes). Any deviation is terminal for the cell.
func (c *genericController) cellDrifted(ctx context.Context, cell *cliCell) string {
	container, err := c.inspectCell(ctx, cell.name)
	if err != nil {
		return "inspect:" + err.Error()
	}
	if problem := cellProfileProblem(container, cell, c.config.SeccompProfile, c.config.StateRoot, true); problem != "" {
		return problem
	}
	if code, err := c.cellRun(ctx, nil, "exec", cell.name, cellInitGuest, "clean", "/tmp", cellCanaryName, cell.canary); err != nil || code != 0 {
		return "canary-tmp"
	}
	if code, err := c.cellRun(ctx, nil, "exec", cell.name, cellInitGuest, "procs"); err != nil || code != 0 {
		return "stray-procs"
	}
	return ""
}

// createCell runs create -> pre-start inspect -> start -> canary seed ->
// post-start inspect and returns the cell.
func (c *genericController) createCell(ctx context.Context, req cliExecRequest, image, key string, toolset []string) (*cliCell, error) {
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
		lifecycle = CLILifecycleEphemeral
	}
	cell := &cliCell{
		name: "cli-" + cellHash(image+randomNonce()), key: key, principal: req.Principal, bindingID: req.BindingID,
		jobID: req.JobID, lifecycle: lifecycle, image: image, runtime: runtime,
		workHost: workHost, workRW: workRW, toolset: append([]string(nil), toolset...),
		installPackages: req.InstallPackages,
		canary:          randomNonce(), created: time.Now(), lastUse: time.Now(), execSlot: make(chan struct{}, 1),
	}
	egress := realEgress(req.Plan.Execution.Egress)
	if len(egress) > 0 || len(req.Brokered) > 0 {
		if cfg.ProxyImage == "" {
			return nil, fmt.Errorf("%w: egress cells need a proxy image", ErrIsolation)
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
		return nil, fmt.Errorf("%w: cell create: %v", ErrIsolation, err)
	}
	if c.scope != nil {
		c.scope.register(cell.name)
	}
	// The pre-start inspect proves the create-time profile; a drifted daemon
	// answer denies before the cell ever runs.
	container, err := c.inspectCell(ctx, cell.name)
	if err != nil {
		return nil, err
	}
	if problem := cellProfileProblem(container, cell, c.config.SeccompProfile, c.config.StateRoot, false); problem != "" {
		return nil, fmt.Errorf("%w: cell profile: %s", ErrIsolation, problem)
	}
	if _, err := c.docker(ctx, "start", cell.name); err != nil {
		return nil, fmt.Errorf("%w: cell start: %v", ErrIsolation, err)
	}
	if code, err := c.cellRun(ctx, nil, "exec", cell.name, cellInitGuest, "write", cellCanaryPath, cell.canary); err != nil || code != 0 {
		return nil, fmt.Errorf("%w: cell canary seed", ErrIsolation)
	}
	container, err = c.inspectCell(ctx, cell.name)
	if err != nil {
		return nil, err
	}
	if problem := cellProfileProblem(container, cell, c.config.SeccompProfile, c.config.StateRoot, true); problem != "" {
		return nil, fmt.Errorf("%w: started cell profile: %s", ErrIsolation, problem)
	}
	created = true
	c.mu.Lock()
	c.cells().metrics.creates++
	c.mu.Unlock()
	log.Printf("cli-cell: create name=%s principal=%s lifecycle=%s image=%s network=%s", cell.name, cell.principal, cell.lifecycle, image, cell.network)
	return cell, nil
}

type cellSidecar struct {
	name   string
	volume string
}

// cellEgress builds the internal network plus the egress proxies the plan
// earned: a Squid CONNECT allowlist when real egress hosts are declared, and
// a cred-proxy when the request carries brokered credentials.
func (c *genericController) cellEgress(ctx context.Context, cell *cliCell, egress []string, brokered []cliBrokeredCred) (string, []cellSidecar, error) {
	cfg := c.config.CLI
	network := "hermes-" + cell.name + "-net"
	ok := false
	cleanup := func() {
		for _, name := range []string{cell.name + "-proxy", cell.name + "-cred"} {
			_, _ = c.docker(context.Background(), "rm", "-f", name)
		}
		_, _ = c.docker(context.Background(), "network", "rm", network)
		_, _ = c.docker(context.Background(), "volume", "rm", cell.name+"-proxy-config")
	}
	defer func() {
		if !ok {
			cleanup()
		}
	}()
	if _, err := c.docker(ctx, append([]string{"network", "create", "--internal", "--label", "hermes-hub.role=cli-cell"}, append(c.scopeLabelArgs(), network)...)...); err != nil {
		return "", nil, err
	}
	var out []cellSidecar
	if len(egress) > 0 {
		proxyImage, err := c.resolveCellImage(ctx, cfg.ProxyImage)
		if err != nil {
			return "", nil, err
		}
		sidecar, err := c.cellSquid(ctx, cell.name, network, egress, proxyImage)
		if err != nil {
			return "", nil, err
		}
		out = append(out, sidecar)
	}
	if len(brokered) > 0 {
		toolsImage, err := c.resolveCellImage(ctx, cfg.ToolsImage)
		if err != nil {
			return "", nil, err
		}
		sidecar, err := c.cellCredProxy(ctx, cell.name, network, brokered, toolsImage)
		if err != nil {
			return "", nil, err
		}
		out = append(out, sidecar)
	}
	ok = true
	return network, out, nil
}

// cellSquid is the per-cell CONNECT allowlist proxy, the same shape the MCP
// workload path already audits: internal net for the cell, bridge for egress,
// config copied into a volume so no file leaves the controller fs writable.
func (c *genericController) cellSquid(ctx context.Context, cellName, network string, egress []string, proxyImage string) (cellSidecar, error) {
	proxyName := cellName + "-proxy"
	proxyVol := cellName + "-proxy-config"
	if _, err := c.docker(ctx, append([]string{"volume", "create", "--label", "hermes-hub.role=cli-cell"}, append(c.scopeLabelArgs(), proxyVol)...)...); err != nil {
		return cellSidecar{}, err
	}
	proxyConfig, err := genericProxyConfig(egress)
	if err != nil {
		return cellSidecar{}, err
	}
	// docker cp preserves the source mode; the allowlist carries no secrets.
	fileName, err := writeWorldReadableTemp(c.config.StateRoot, ".cellproxy-*", proxyConfig)
	if err != nil {
		return cellSidecar{}, err
	}
	defer os.Remove(fileName)
	args := append([]string{"create", "--name", proxyName, "--network", network, "--read-only", "--user", "31:31", "--cap-drop", "ALL", "--security-opt", "no-new-privileges=true", "--security-opt", "seccomp=" + c.config.SeccompProfile, "--tmpfs", "/tmp:rw,nosuid,nodev,size=64m", "--mount", "type=volume,source=" + proxyVol + ",target=/etc/squid"}, c.scopeLabelArgs()...)
	args = append(args, "--cpus", "0.25", "--memory", "256m", "--pids-limit", "64", proxyImage)
	if _, err := c.docker(ctx, args...); err != nil {
		return cellSidecar{}, err
	}
	if c.scope != nil {
		c.scope.register(proxyName)
	}
	if _, err := c.docker(ctx, "cp", fileName, proxyName+":/etc/squid/squid.conf"); err != nil {
		return cellSidecar{}, err
	}
	if _, err := c.docker(ctx, "network", "connect", "bridge", proxyName); err != nil {
		return cellSidecar{}, err
	}
	if _, err := c.docker(ctx, "start", proxyName); err != nil {
		return cellSidecar{}, err
	}
	if err := c.waitProxyListen(ctx, proxyName); err != nil {
		return cellSidecar{}, err
	}
	return cellSidecar{name: proxyName, volume: proxyVol}, nil
}

// cellCredProxy runs cellinit cred-proxy in a throwaway companion and writes
// the header rules into its tmpfs — rules hold the real credential, so the
// file goes through docker cp, never argv/env/inspect.
func (c *genericController) cellCredProxy(ctx context.Context, cellName, network string, brokered []cliBrokeredCred, toolsImage string) (cellSidecar, error) {
	name := cellName + "-cred"
	rules := map[string]map[string]string{}
	for _, cred := range brokered {
		rules[strings.ToLower(cred.Host)] = map[string]string{"Authorization": cred.Prefix + cred.Value}
		log.Printf("cli-cell: brokered credential env=%s host=%s", cred.EnvName, cred.Host)
	}
	body, err := json.Marshal(rules)
	if err != nil {
		return cellSidecar{}, err
	}
	file, err := os.CreateTemp(c.config.StateRoot, ".credrules-*")
	if err != nil {
		return cellSidecar{}, err
	}
	fileName := file.Name()
	defer os.Remove(fileName)
	if _, err = file.Write(body); err != nil {
		file.Close()
		return cellSidecar{}, err
	}
	if err = file.Close(); err != nil {
		return cellSidecar{}, err
	}
	args := append([]string{"create", "--name", name, "--network", network, "--read-only", "--user", "10001:10001", "--cap-drop", "ALL", "--security-opt", "no-new-privileges=true", "--security-opt", "seccomp=" + c.config.SeccompProfile, "--tmpfs", "/tmp:rw,nosuid,nodev,size=16m", "--mount", "type=bind,source=" + c.cellInitHost() + ",target=" + cellInitGuest + ",readonly"}, c.scopeLabelArgs()...)
	args = append(args, "--cpus", "0.25", "--memory", "128m", "--pids-limit", "32", "--entrypoint", cellInitGuest, toolsImage, "cred-proxy", "--rules", "/tmp/rules.json", "--listen", "0.0.0.0:"+strconv.Itoa(credProxyPort))
	if _, err := c.docker(ctx, args...); err != nil {
		return cellSidecar{}, err
	}
	if c.scope != nil {
		c.scope.register(name)
	}
	if _, err := c.docker(ctx, "cp", fileName, name+":/tmp/rules.json"); err != nil {
		return cellSidecar{}, err
	}
	if _, err := c.docker(ctx, "network", "connect", "bridge", name); err != nil {
		return cellSidecar{}, err
	}
	if _, err := c.docker(ctx, "start", name); err != nil {
		return cellSidecar{}, err
	}
	return cellSidecar{name: name}, nil
}

// inspectCell returns the raw inspect record; the caller compares it against
// the profile the create request asked for.
func (c *genericController) inspectCell(ctx context.Context, name string) (genericContainer, error) {
	body, err := c.docker(ctx, "inspect", name)
	if err != nil {
		return genericContainer{}, fmt.Errorf("%w: cell inspect: %v", ErrIsolation, err)
	}
	var containers []genericContainer
	if json.Unmarshal(body, &containers) != nil || len(containers) != 1 {
		return genericContainer{}, fmt.Errorf("%w: cell inspect", ErrIsolation)
	}
	return containers[0], nil
}

// cellProfileProblem names the first violated cell invariant, or "" when the
// inspected container carries every claim the receipt will make. wantRunning
// distinguishes the pre-start inspect (created) from the post-start one.
func cellProfileProblem(container genericContainer, cell *cliCell, seccomp, stateRoot string, wantRunning bool) string {
	if container.State.Running != wantRunning {
		return "state"
	}
	if container.HostConfig.Privileged {
		return "privileged"
	}
	if container.HostConfig.PidMode != "" {
		return "host-pid"
	}
	if !container.HostConfig.ReadonlyRootfs {
		return "writable-root"
	}
	if len(container.HostConfig.CapAdd) != 0 || len(container.HostConfig.CapDrop) != 1 || container.HostConfig.CapDrop[0] != "ALL" {
		return "caps"
	}
	if !contains(container.HostConfig.SecurityOpt, "no-new-privileges=true") || !securityOptHasSeccomp(container.HostConfig.SecurityOpt, seccomp) {
		return "security-opt"
	}
	if container.Config.Image != cell.image {
		return "image"
	}
	user := container.Config.User
	if user == "" || user == "0" || user == "0:0" || user == "root" {
		return "root-user"
	}
	if cell.runtime != "" && container.HostConfig.Runtime != cell.runtime {
		return "runtime"
	}
	wantNet := cell.network
	if wantNet == "" {
		wantNet = "none"
	}
	if container.HostConfig.NetworkMode != wantNet {
		return "network"
	}
	labels := container.Config.Labels
	if labels["hermes-hub.cell"] != "true" || labels["hermes-hub.cell.principal"] != cell.principal || labels["hermes-hub.cell.lifecycle"] != cell.lifecycle {
		return "labels"
	}
	// The mount table must contain nothing the cell did not declare:
	// /cellinit ro bind, /tmp tmpfs, /work (bind or scratch tmpfs) plus
	// optional /tools/<i> image layers. Anything else denies — this is what
	// makes /state, credential stores and the docker socket impossible
	// inside a cell. Tmpfs lands in .Mounts on the classic image store and
	// only in HostConfig.Tmpfs on containerd-store daemons; both count.
	wantMounts := map[string]bool{cellInitGuest: true, "/tmp": true, cellWorkGuest: true}
	if cell.installPackages {
		wantMounts[cellHomeGuest] = true
		wantMounts[cellUserTmpGuest] = true
	}
	for i := range cell.toolset {
		wantMounts[cellToolsetGuest+"/"+strconv.Itoa(i)] = true
	}
	tmpfs := map[string]bool{}
	for path := range container.HostConfig.Tmpfs {
		tmpfs[path] = true
	}
	seen := map[string]bool{}
	for _, mount := range container.Mounts {
		if !wantMounts[mount.Destination] {
			return "mount:" + mount.Destination
		}
		switch mount.Destination {
		case cellInitGuest:
			if mount.Type != "bind" || mount.RW || mount.Source != dockerBindSource(filepath.Join(stateRoot, "cli-cellinit")) {
				return "cellinit-mount"
			}
		case cellWorkGuest:
			if cell.workHost == "" {
				if mount.Type != "tmpfs" {
					return "work-mount"
				}
			} else if mount.Type != "bind" || mount.Source != cell.workHost || mount.RW != cell.workRW {
				return "work-mount"
			}
		case "/tmp":
			if mount.Type != "tmpfs" {
				return "tmp-mount"
			}
		case cellHomeGuest:
			if mount.Type != "tmpfs" {
				return "home-mount"
			}
		case cellUserTmpGuest:
			if mount.Type != "tmpfs" {
				return "usertmp-mount"
			}
		default:
			if mount.Type != "image" || mount.RW {
				return "toolset-mount"
			}
		}
		if mount.Type == "tmpfs" {
			tmpfs[mount.Destination] = true
		}
		seen[mount.Destination] = true
	}
	if !tmpfs["/tmp"] {
		return "tmp-mount"
	}
	if cell.installPackages && !tmpfs[cellHomeGuest] {
		return "home-mount"
	}
	for path := range tmpfs {
		if path != "/tmp" && !(path == cellWorkGuest && cell.workHost == "") && !(path == cellHomeGuest && cell.installPackages) && !(path == cellUserTmpGuest && cell.installPackages) {
			return "tmpfs:" + path
		}
	}
	if cell.workHost == "" && !tmpfs[cellWorkGuest] {
		return "work-mount"
	}
	if !seen[cellInitGuest] || (cell.workHost != "" && !seen[cellWorkGuest]) {
		return "mount-missing"
	}
	for i := range cell.toolset {
		if !seen[cellToolsetGuest+"/"+strconv.Itoa(i)] {
			return "toolset-mount"
		}
	}
	return ""
}

// execInCell runs the tool argv inside a prepared cell via docker exec;
// secrets arrive only here, in exec-time env, never in container config.
func (c *genericController) execInCell(ctx context.Context, cell *cliCell, req cliExecRequest) (cliExecResponse, error) {
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
			return cliExecResponse{}, err
		}
		proxy := "http://" + ip + ":" + strconv.Itoa(cellProxyPort)
		env = append(env, "HTTP_PROXY="+proxy, "HTTPS_PROXY="+proxy, "http_proxy="+proxy, "https_proxy="+proxy, "NO_PROXY=127.0.0.1,"+cell.name+"-cred", "no_proxy=127.0.0.1,"+cell.name+"-cred")
	}
	command := req.Command
	if cell.lifecycle == CLILifecycleToolbox {
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
			return cliExecResponse{}, fmt.Errorf("%w: toolbox member image is not in the toolset", ErrUnauthorized)
		}
		command = cellToolsetGuest + "/" + strconv.Itoa(idx) + req.Command
	}
	execArgs := []string{"exec"}
	for _, pair := range env {
		execArgs = append(execArgs, "-e", pair)
	}
	execArgs = append(execArgs, "-w", cellWorkGuest, cell.name, command)
	execArgs = append(execArgs, req.Args...)
	output := &boundedOutput{limit: limit, exceeded: make(chan struct{})}
	ctx, cancel := context.WithTimeout(ctx, timeout)
	go func() {
		select {
		case <-output.exceeded:
			cancel()
		case <-ctx.Done():
		}
	}()
	code, err := c.cellRun(ctx, output, execArgs...)
	cancel()
	if cell.lifecycle != CLILifecycleEphemeral && cell.lifecycle != CLILifecycleSharedPool {
		// A timed-out or crashed tool may have left children behind; sweep
		// them detached so the next claim sees a clean process table.
		sweep, sweepCancel := context.WithTimeout(context.Background(), 10*time.Second)
		_, _ = c.cellRun(sweep, nil, "exec", cell.name, cellInitGuest, "sweep")
		sweepCancel()
	}
	if output.overflowed() {
		// The tool exceeded its output cap; the response still carries the
		// verified receipt — the failure is the tool's, not isolation's.
		return cliExecResponse{Receipt: c.cellReceipt(cell, req), Output: output.String(), ExitCode: code, Error: "output-limit"}, nil
	}
	if err != nil {
		return cliExecResponse{}, fmt.Errorf("%w: cell exec: %v", ErrIsolation, err)
	}
	cell.uses++
	cell.lastUse = time.Now()
	return cliExecResponse{Receipt: c.cellReceipt(cell, req), Output: output.String(), ExitCode: code}, nil
}

func (c *genericController) cellReceipt(cell *cliCell, req cliExecRequest) AdmissionReceipt {
	runtime := cell.runtime
	if runtime == "" {
		runtime = "runc"
	}
	receipt := AdmissionReceipt{
		WorkloadID: req.Plan.WorkloadID, State: "running", Enforced: true,
		Execution: req.Plan.Execution, Isolation: append([]string(nil), cliIsolationScopes...),
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
		if err := c.scope.guardDocker(ctx, args); err != nil {
			return -1, err
		}
	}
	runner := c.commandExit
	if runner == nil {
		runner = localCommandExit
	}
	return runner(ctx, sink, "docker", args...)
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
func (c *genericController) releaseCells(ctx context.Context, req cliReleaseRequest) {
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
		if cell.lifecycle == CLILifecycleTask {
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

// execPooled runs a stateless call on a shared pool cell: claim, canary
// reset, exec, sweep, return — a dirty cell is quarantined (killed), never
// returned to the pool.
func (c *genericController) execPooled(ctx context.Context, req cliExecRequest, image string) (cliExecResponse, error) {
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
				return cliExecResponse{}, fmt.Errorf("%w: pool exhausted", ErrIsolation)
			}
			select {
			case <-ctx.Done():
				return cliExecResponse{}, ctx.Err()
			case <-time.After(100 * time.Millisecond):
			}
			continue
		}
		c.cells().claimedN[image]++
		c.mu.Unlock()
		cell, err := c.createCell(ctx, req, image, "", nil)
		if err != nil {
			c.releasePoolClaim(image, nil)
			return cliExecResponse{}, err
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
func (c *genericController) execPoolDirect(ctx context.Context, cell *cliCell, req cliExecRequest) (cliExecResponse, error) {
	resp, err := c.execInCell(ctx, cell, req)
	if err != nil {
		c.releasePoolClaim(cell.image, cell)
		c.removeCell(context.Background(), cell, "pool-first-failed")
		return cliExecResponse{}, err
	}
	if !c.returnPoolCell(cell) {
		c.removeCell(context.Background(), cell, "pool-surplus")
	}
	return resp, nil
}

// execPoolOnce guards the claim with the same drift check warm cells get,
// then execs and returns the cell to the pool only when it verifies clean.
func (c *genericController) execPoolOnce(ctx context.Context, cell *cliCell, req cliExecRequest) (cliExecResponse, error) {
	defer func() { <-cell.execSlot }()
	if problem := c.cellDrifted(ctx, cell); problem != "" {
		return cliExecResponse{}, errCellDirty
	}
	resp, err := c.execInCell(ctx, cell, req)
	if err != nil {
		c.releasePoolClaim(cell.image, cell)
		c.removeCell(context.Background(), cell, "pool-exec-failed")
		return cliExecResponse{}, err
	}
	if !c.returnPoolCell(cell) {
		c.removeCell(context.Background(), cell, "pool-surplus")
	}
	return resp, nil
}
