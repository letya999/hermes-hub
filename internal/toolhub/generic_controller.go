package toolhub

import (
	"context"
	"fmt"
	"github.com/letya999/hermes-hub/internal/identity"
	"io"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

// This is the deliberately small generic adapter. ToolHive remains the
// workload manager; this process only turns an approved plan into a bounded
// ToolHive invocation and checks Docker's returned profile before admission.

type GenericControllerConfig struct {
	StateRoot           string          `json:"state_root"`
	ToolHiveBinary      string          `json:"toolhive_binary"`
	SeccompProfile      string          `json:"seccomp_profile"`
	DockerFallback      bool            `json:"docker_fallback,omitempty"`
	BridgeBinary        string          `json:"bridge_binary,omitempty"`
	CredentialMountRoot string          `json:"credential_mount_root,omitempty"`
	Definition          ToolDefinition  `json:"definition"`
	DynamicDefinitions  bool            `json:"dynamic_definitions,omitempty"`
	Budget              *WorkloadBudget `json:"-"`
	MaxActive           int             `json:"max_active"`
	IdleTTLSeconds      int             `json:"idle_ttl_seconds"`
	// CLI configures bounded-cli admission: the executor container the
	// process lands in is inspected per call and only approved plans are
	// admitted. Nil disables the CLI path entirely.
	CLI *CLIControllerConfig `json:"cli,omitempty"`
}

// CLIControllerConfig is the operator-approved envelope for bounded-cli cell
// execution. Every admitted call runs in a sibling container the controller
// creates, inspects and destroys; nothing here is model-supplied.
type CLIControllerConfig struct {
	AllowPackageInstall bool `json:"allow_package_install,omitempty"`
	// ToolsImage is the digest-pinned image catalog commands run on.
	ToolsImage string `json:"tools_image"`
	// CellInit is the host path of the statically-linked cellinit helper;
	// it is copied into the state root and bind-mounted read-only as the
	// cell's PID 1 and canary agent.
	CellInit string `json:"cell_init"`
	// ProxyImage is the digest-pinned egress proxy image; required when any
	// admitted plan declares non-loopback egress or brokered credentials.
	ProxyImage string `json:"proxy_image,omitempty"`
	// Runtime selects the OCI runtime tier: empty or "runc" (default),
	// "runsc" or "kata"; the daemon must list the runtime before a cell
	// may start on it.
	Runtime string `json:"runtime,omitempty"`
	// PerPrincipalUID maps each principal to a stable cell uid (20000+) so
	// files a read-write cell leaves in a workspace carry a per-principal
	// owner instead of the shared cli uid.
	PerPrincipalUID bool `json:"per_principal_uid,omitempty"`
	// SpacesRoot is the host root for principal workspaces; required when a
	// definition requests a principal-scoped workspace bind.
	SpacesRoot string `json:"spaces_root,omitempty"`
	// Environment names the managed deployment tier (dev/prod). When set and
	// <spaces_root>/<principal>/managed/<environment>/workspace exists, the
	// principal scope binds that managed workspace; otherwise it falls back
	// to <spaces_root>/<principal>/workspace for unmanaged principals.
	Environment string `json:"environment,omitempty"`
	// AllowedEgress is the union of egress hosts any CLI plan may declare.
	// Cells with non-loopback egress get a per-cell internal network plus a
	// CONNECT-allowlisted Squid proxy; all other cells are network none.
	AllowedEgress []string `json:"allowed_egress"`
	// Ceiling bounds every admitted execution policy; the plan may be
	// stricter but never looser.
	Ceiling ExecutionPolicy `json:"ceiling"`
	// Approved are the immutable CLI plans the controller will attest:
	// definition id+version, command, optional image+digest and the exact
	// execution policy.
	Approved []CLIApproval `json:"approved"`
	// UserCommands admits owner-registered catalog command plans: the bare
	// command name must be listed and the policy must fit Ceiling. Entries
	// are reviewed for exec-capability (hooks, preprocessors, subcommands
	// that spawn binaries); a binary that can run other code never belongs
	// here. Image-carrying artifact plans are never admitted this way.
	UserCommands []string `json:"user_commands,omitempty"`
	// Toolboxes are operator-pinned toolsets for toolbox-lifecycle cells:
	// name -> digest-pinned member images mounted read-only at /tools/<i>.
	Toolboxes map[string][]string `json:"toolboxes,omitempty"`
	// PoolSize caps live cells (idle + claimed) per stateless pool key;
	// callers past the cap wait up to PoolClaimWaitSeconds then fail closed.
	PoolSize             int `json:"pool_size,omitempty"`
	PoolClaimWaitSeconds int `json:"pool_claim_wait_seconds,omitempty"`
	// WarmMaxAgeSeconds and WarmMaxReuse bound warm-cell reuse; a cell past
	// either limit is rotated before another exec claims it.
	WarmMaxAgeSeconds int `json:"warm_max_age_seconds,omitempty"`
	WarmMaxReuse      int `json:"warm_max_reuse,omitempty"`
}

// CLIApproval pins one admitted bounded-cli plan shape.
type CLIApproval struct {
	DefinitionID      string          `json:"definition_id"`
	DefinitionVersion string          `json:"definition_version"`
	Command           string          `json:"command"`
	Image             string          `json:"image,omitempty"`
	Digest            string          `json:"digest,omitempty"`
	Execution         ExecutionPolicy `json:"execution"`
}

type genericWorkload struct {
	plan           controllerPlan
	endpoint       string
	proxyName      string
	proxyVol       string
	remoteName     string
	remoteState    string
	bridgeVol      string
	stateVols      []string
	bridgePort     int
	relayName      string
	relayVol       string
	dockerFallback bool
	imageRef       string
}

type genericController struct {
	config     GenericControllerConfig
	command    func(context.Context, string, ...string) ([]byte, error)
	commandEnv func(context.Context, map[string]string, string, ...string) ([]byte, error)
	// commandExit runs a binary with streaming output and returns its exit
	// code; `docker exec` needs the tool's own exit status, which the
	// output-only command seam cannot carry.
	commandExit func(context.Context, io.Writer, string, ...string) (int, error)
	scope       *dockerScope
	mu          sync.Mutex // ponytail: one controller lock; split only after measured contention.
	workloads   map[string]genericWorkload
	registry    *cellRegistry
	cellInitOK  bool            // cellinit seeded into the state root for sibling binds
	runtimes    map[string]bool // daemon runtime list, cached after first read
	runtimesOK  bool
	// toolboxes caches resolveToolbox results: every exec asks for the pin
	// list, and each member costs a docker inspect round trip. Entries are
	// evicted when a cell create fails so a pruned image re-resolves.
	toolboxes map[string][]string
}

// docker runs one docker argv through the project-scope guard: inspect, exec,
// logs and lifecycle verbs on foreign containers, networks or volumes are
// denied before the socket sees them. Other binaries only get the operand
// check — ToolHive removes containers by caller-shaped workload ids.
func (c *genericController) docker(ctx context.Context, args ...string) ([]byte, error) {
	if c.scope == nil {
		return c.command(ctx, "docker", args...)
	}
	return c.scope.runScoped(ctx, c.command, "docker", args...)
}

func (c *genericController) toolhive(ctx context.Context, args ...string) ([]byte, error) {
	if c.scope == nil {
		return c.command(ctx, c.config.ToolHiveBinary, args...)
	}
	return c.scope.runScoped(ctx, c.command, c.config.ToolHiveBinary, args...)
}

// scopeLabel is the hermes-hub.scope marker stamped on every object the
// controller spawns; the guard later proves ownership through it. Empty when
// the deployment carries no scope identity (socketless or unscoped test).
func (c *genericController) scopeLabel() string {
	if c.scope == nil {
		return ""
	}
	return "hermes-hub.scope=" + c.scope.project
}

func (c *genericController) scopeLabelArgs() []string {
	if label := c.scopeLabel(); label != "" {
		return []string{"--label", label}
	}
	return nil
}

type genericContainer struct {
	Image string
	State struct {
		Running  bool
		ExitCode int
	}
	Config struct {
		User   string
		Image  string
		Env    []string
		Labels map[string]string
	}
	HostConfig struct {
		NanoCpus       int64
		Memory         int64
		PidsLimit      int64
		Privileged     bool
		ReadonlyRootfs bool
		NetworkMode    string
		PidMode        string
		Runtime        string
		CapAdd         []string
		CapDrop        []string
		SecurityOpt    []string
		Binds          []string
		// Tmpfs carries --tmpfs declarations on containerd-store daemons:
		// the classic store also lists them in .Mounts, Desktop does not.
		Tmpfs map[string]string
	}
	Mounts          []genericMount
	NetworkSettings struct {
		Networks map[string]struct{ IPAddress string }
	}
}

type genericMount struct {
	Type, Name, Source, Destination string
	RW                              bool
}

func newGenericController(config GenericControllerConfig) (*genericController, error) {
	if !filepath.IsAbs(config.StateRoot) || !filepath.IsAbs(config.ToolHiveBinary) || !filepath.IsAbs(config.SeccompProfile) {
		return nil, fmt.Errorf("%w: generic controller paths", ErrInvalid)
	}
	if config.BridgeBinary != "" {
		if err := ValidateLinuxBridgeBinary(config.BridgeBinary); err != nil {
			return nil, err
		}
	}
	if config.Budget == nil {
		if config.MaxActive < 1 || config.IdleTTLSeconds < 1 {
			return nil, fmt.Errorf("%w: generic controller budget", ErrInvalid)
		}
		budget, err := NewWorkloadBudget(config.MaxActive, time.Duration(config.IdleTTLSeconds)*time.Second)
		if err != nil {
			return nil, err
		}
		config.Budget = budget
	}
	config.StateRoot = filepath.Clean(config.StateRoot)
	config.ToolHiveBinary = filepath.Clean(config.ToolHiveBinary)
	config.SeccompProfile = filepath.Clean(config.SeccompProfile)
	if config.CredentialMountRoot != "" {
		if !filepath.IsAbs(config.CredentialMountRoot) || noSymlinkPath(config.CredentialMountRoot) != nil {
			return nil, ErrIsolation
		}
		config.CredentialMountRoot = filepath.Clean(config.CredentialMountRoot)
	}
	if config.BridgeBinary != "" {
		config.BridgeBinary = filepath.Clean(config.BridgeBinary)
	}
	if !config.DynamicDefinitions {
		if err := validateGenericDefinition(config.Definition); err != nil {
			return nil, err
		}
	}
	if err := validateCLIConfig(config.CLI); err != nil {
		return nil, err
	}
	if noSymlinkPath(config.StateRoot) != nil || noSymlinkPath(config.SeccompProfile) != nil {
		return nil, ErrIsolation
	}
	controller := &genericController{config: config, command: localCommand, commandEnv: isolatedCommandEnv, commandExit: localCommandExit, workloads: map[string]genericWorkload{}, registry: newCellRegistry()}
	controller.scope = newDockerScope(os.Getenv("HUB_DOCKER_SCOPE"), os.Getenv("HUB_DOCKER_AGENT_NET"), controller.command)
	return controller, nil
}

func (c *genericController) validatePlan(plan controllerPlan) error {
	definition := c.config.Definition
	if c.config.DynamicDefinitions {
		definition = plan.Definition
	}
	if err := validateGenericDefinition(definition); err != nil {
		return err
	}
	if !identity.ValidID(plan.WorkloadID) || plan.DefinitionID != definition.DefinitionID || plan.DefinitionVersion != definition.Version || plan.Image != definition.Source.Image || plan.Digest != definition.Source.Digest || plan.ToolHiveVersion != definition.Workload.ToolHiveVersion || !equalStrings(plan.SidecarImages, definition.Workload.SidecarImages) || !reflectExecution(plan.Execution, definition.Execution) {
		return fmt.Errorf("%w: generic plan drift", ErrStale)
	}
	if plan.WorkspacePath != "" {
		path, err := filepath.Abs(plan.WorkspacePath)
		if err != nil || !containedPath(c.config.StateRoot, path) || noSymlinkPath(path) != nil {
			return ErrIsolation
		}
	}
	if len(plan.Execution.Mounts) != 0 {
		if !c.config.DockerFallback {
			return fmt.Errorf("%w: generic host state mounts are not enabled", ErrIsolation)
		}
		for _, mount := range plan.Execution.Mounts {
			if mount.Source != "connection-state" && mount.Source != "job-state" {
				return fmt.Errorf("%w: generic mount source is not a named state volume", ErrIsolation)
			}
		}
	}
	if len(plan.Execution.TCPForwards) != 0 && !c.config.DockerFallback {
		// Only the Docker fallback runs the companion inside the workload
		// network namespace; the stock ToolHive path has no forwarder, so
		// refusing here is the difference between a bounded relay and a
		// silently unmet connectivity promise.
		return fmt.Errorf("%w: tcp forwards require the Docker fallback companion", ErrIsolation)
	}
	if err := c.validateCredentialMounts(plan.CredentialMounts); err != nil {
		return err
	}
	return nil
}

func (c *genericController) validateCredentialMounts(mounts []Mount) error {
	if len(mounts) == 0 {
		return nil
	}
	if c.config.CredentialMountRoot == "" || len(mounts) > 16 {
		return fmt.Errorf("%w: credential mount root is not configured", ErrIsolation)
	}
	for _, mount := range mounts {
		if !filepath.IsAbs(mount.Source) || !validContainerMountTarget(mount.Target) || !containedPath(c.config.CredentialMountRoot, mount.Source) || noSymlinkPath(mount.Source) != nil {
			return fmt.Errorf("%w: unsafe credential mount", ErrIsolation)
		}
		info, err := os.Stat(mount.Source)
		if err != nil || (mount.ReadOnly && !info.Mode().IsRegular()) || (!mount.ReadOnly && (!info.IsDir() || filepath.Base(mount.Source) != "state" || filepath.Dir(mount.Source) == filepath.Clean(c.config.CredentialMountRoot))) {
			return fmt.Errorf("%w: credential mount must be a read-only file or lease state directory", ErrIsolation)
		}
	}
	return nil
}

func validContainerMountTarget(target string) bool {
	return strings.HasPrefix(target, "/") && target != "/" && !strings.ContainsAny(target, "\x00\r\n") && filepath.ToSlash(filepath.Clean(target)) == target
}

func validateGenericDefinition(definition ToolDefinition) error {
	if err := ValidateTrustedArtifactDefinition(definition); err != nil {
		return err
	}
	if definition.Transport != ContainerMCP || len(definition.Workload.SidecarImages) != 1 {
		return fmt.Errorf("%w: generic controller requires one ContainerMCP proxy image", ErrInvalid)
	}
	if definition.Workload.Class == Shared && len(definition.Credentials) != 0 {
		return fmt.Errorf("%w: shared generic MCP cannot retain credentials", ErrUnauthorized)
	}
	return nil
}

// RunGenericController starts the admission endpoint. ToolHive and Docker are
// selected explicitly by the operator; no daemon socket is exposed to MCP
// workloads. The listener is loopback-only unless HUB_CONTROLLER_REMOTE=1
// opts into a bearer-token protected network listener for compose topologies.
func RunGenericController(ctx context.Context, config GenericControllerConfig, listen, token string) error {
	host, _, err := net.SplitHostPort(listen)
	if err != nil || len(token) < 32 || strings.ContainsAny(token, "\r\n") {
		return fmt.Errorf("private token required")
	}
	if host != "127.0.0.1" && os.Getenv("HUB_CONTROLLER_REMOTE") != "1" {
		return fmt.Errorf("loopback address required unless HUB_CONTROLLER_REMOTE=1")
	}
	c, err := newGenericController(config)
	if err != nil {
		return err
	}
	if config.CLI != nil {
		if err := c.cellInitSeed(); err != nil {
			return err
		}
	}
	listener, err := net.Listen("tcp", listen)
	if err != nil {
		return err
	}
	defer listener.Close()
	server := &http.Server{Handler: c.handler(token), ReadHeaderTimeout: 5 * time.Second, IdleTimeout: time.Minute}
	go func() {
		ticker := time.NewTicker(time.Second)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				shutdown, cancel := context.WithTimeout(context.Background(), 5*time.Second)
				_ = server.Shutdown(shutdown)
				cancel()
				c.CleanupIdle(context.Background(), time.Now().Add(365*24*time.Hour))
				c.releaseAllCells(context.Background(), "controller-shutdown")
				return
			case now := <-ticker.C:
				c.CleanupIdle(context.Background(), now)
				c.cleanupCells(context.Background(), now)
			}
		}
	}()
	if err := server.Serve(listener); err != http.ErrServerClosed {
		return err
	}
	return nil
}
