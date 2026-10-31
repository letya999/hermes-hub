package toolhub

import (
	"context"
	"fmt"
	"github.com/letya999/hermes-hub/internal/identity"
	"github.com/letya999/hermes-hub/internal/toolhub/api"
	"io"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"time"
)

// This is the deliberately small generic adapter. ToolHive remains the
// workload manager; this process only turns an approved plan into a bounded
// ToolHive invocation and checks Docker's returned profile before admission.

type GenericControllerConfig struct {
	HostingFleetRoot    string                    `json:"hosting_fleet_root,omitempty"`
	HostingHelperVolume string                    `json:"hosting_helper_volume,omitempty"`
	HostingOnly         bool                      `json:"hosting_only,omitempty"`
	HostingCellInit     string                    `json:"hosting_cell_init,omitempty"`
	Hosting             map[string]HostingProfile `json:"hosting,omitempty"`
	StateRoot           string                    `json:"state_root"`
	ToolHiveBinary      string                    `json:"toolhive_binary"`
	SeccompProfile      string                    `json:"seccomp_profile"`
	DockerFallback      bool                      `json:"docker_fallback,omitempty"`
	BridgeBinary        string                    `json:"bridge_binary,omitempty"`
	CredentialMountRoot string                    `json:"credential_mount_root,omitempty"`
	Definition          api.ToolDefinition        `json:"definition"`
	DynamicDefinitions  bool                      `json:"dynamic_definitions,omitempty"`
	Budget              *api.WorkloadBudget       `json:"-"`
	MaxActive           int                       `json:"max_active"`
	IdleTTLSeconds      int                       `json:"idle_ttl_seconds"`
	// CLI configures bounded-cli admission: the executor container the
	// process lands in is inspected per call and only approved plans are
	// admitted. Nil disables the CLI path entirely.
	CLI *api.CLIControllerConfig `json:"cli,omitempty"`
}

type genericWorkload struct {
	plan           api.ControllerPlan
	endpoint       string
	proxyName      string
	proxyVol       string
	credName       string
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
	siteInput  func(context.Context, io.Reader, string, ...string) ([]byte, error)
	siteMu     sync.Mutex
	config     GenericControllerConfig
	command    func(context.Context, string, ...string) ([]byte, error)
	commandEnv func(context.Context, map[string]string, string, ...string) ([]byte, error)
	// commandExit runs a binary with streaming output and returns its exit
	// code; `docker exec` needs the tool's own exit status, which the
	// output-only command seam cannot carry.
	commandExit func(context.Context, io.Writer, string, ...string) (int, error)
	// commandStdin feeds a payload on stdin — the cred-proxy rules channel:
	// the sidecar rootfs is read-only so docker cp cannot reach its tmpfs.
	commandStdin func(context.Context, io.Reader, string, ...string) ([]byte, error)
	scope        *api.DockerScope
	mu           sync.Mutex // ponytail: one controller lock; split only after measured contention.
	workloads    map[string]genericWorkload
	registry     *cellRegistry
	cellInitOK   bool            // cellinit seeded into the state root for sibling binds
	runtimes     map[string]bool // daemon runtime list, cached after first read
	runtimesOK   bool
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
	return c.scope.RunScoped(ctx, c.command, "docker", args...)
}

// dockerStdin is docker() with a stdin payload, under the same scope guard:
// the wrapped command still carries docker argv through GuardDocker.
func (c *genericController) dockerStdin(ctx context.Context, stdin io.Reader, args ...string) ([]byte, error) {
	run := c.commandStdin
	if run == nil {
		run = api.LocalCommandInput
	}
	if c.scope == nil {
		return run(ctx, stdin, "docker", args...)
	}
	return c.scope.RunScoped(ctx, func(ctx context.Context, _ string, scoped ...string) ([]byte, error) {
		return run(ctx, stdin, "docker", scoped...)
	}, "docker", args...)
}

func (c *genericController) toolhive(ctx context.Context, args ...string) ([]byte, error) {
	if c.scope == nil {
		return c.command(ctx, c.config.ToolHiveBinary, args...)
	}
	return c.scope.RunScoped(ctx, c.command, c.config.ToolHiveBinary, args...)
}

// scopeLabel is the hermes-hub.scope marker stamped on every object the
// controller spawns; the guard later proves ownership through it. Empty when
// the deployment carries no scope identity (socketless or unscoped test).
func (c *genericController) scopeLabel() string {
	if c.scope == nil {
		return ""
	}
	return "hermes-hub.scope=" + c.scope.Project
}

func (c *genericController) scopeLabelArgs() []string {
	if label := c.scopeLabel(); label != "" {
		return []string{"--label", label}
	}
	return nil
}

// planLabelArgs adds the ownership label set of the plan being spawned so
// every created object answers a `hermes-hub.connection=<id>` label query —
// the index finalizers and the reconciler purge by.
func (c *genericController) planLabelArgs(plan api.ControllerPlan) []string {
	return append(c.scopeLabelArgs(), plan.Ownership.Args()...)
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
		return nil, fmt.Errorf("%w: generic controller paths", api.ErrInvalid)
	}
	if config.BridgeBinary != "" {
		if err := api.ValidateLinuxBridgeBinary(config.BridgeBinary); err != nil {
			return nil, err
		}
	}
	if config.Budget == nil {
		if config.MaxActive < 1 || config.IdleTTLSeconds < 1 {
			return nil, fmt.Errorf("%w: generic controller budget", api.ErrInvalid)
		}
		budget, err := api.NewWorkloadBudget(config.MaxActive, time.Duration(config.IdleTTLSeconds)*time.Second)
		if err != nil {
			return nil, err
		}
		config.Budget = budget
	}
	config.StateRoot = filepath.Clean(config.StateRoot)
	config.ToolHiveBinary = filepath.Clean(config.ToolHiveBinary)
	config.SeccompProfile = filepath.Clean(config.SeccompProfile)
	if config.CredentialMountRoot != "" {
		if !filepath.IsAbs(config.CredentialMountRoot) || api.NoSymlinkPath(config.CredentialMountRoot) != nil {
			return nil, api.ErrIsolation
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
	if err := api.ValidateCLIConfig(config.CLI); err != nil {
		return nil, err
	}
	if api.NoSymlinkPath(config.StateRoot) != nil || api.NoSymlinkPath(config.SeccompProfile) != nil {
		return nil, api.ErrIsolation
	}
	controller := &genericController{siteInput: siteCommandInput, config: config, command: api.LocalCommand, commandEnv: api.IsolatedCommandEnv, commandExit: api.LocalCommandExit, commandStdin: api.LocalCommandInput, workloads: map[string]genericWorkload{}, registry: newCellRegistry()}
	controller.scope = api.NewDockerScope(os.Getenv("HUB_DOCKER_SCOPE"), os.Getenv("HUB_DOCKER_AGENT_NET"), controller.command)
	return controller, nil
}

func (c *genericController) validatePlan(plan api.ControllerPlan) error {
	definition := c.config.Definition
	if c.config.DynamicDefinitions {
		definition = plan.Definition
	}
	if err := validateGenericDefinition(definition); err != nil {
		return err
	}
	if !identity.ValidID(plan.WorkloadID) || plan.DefinitionID != definition.DefinitionID || plan.DefinitionVersion != definition.Version || plan.Image != definition.Source.Image || plan.Digest != definition.Source.Digest || plan.ToolHiveVersion != definition.Workload.ToolHiveVersion || !api.EqualStrings(plan.SidecarImages, definition.Workload.SidecarImages) || !api.ReflectExecution(plan.Execution, definition.Execution) {
		return fmt.Errorf("%w: generic plan drift", api.ErrStale)
	}
	if plan.WorkspacePath != "" {
		path, err := filepath.Abs(plan.WorkspacePath)
		// Credential staging is an operator-declared root too: the shared
		// tmpfs carries per-call credential material and is deliberately not
		// under the persistent state root, so it must not be treated as an
		// arbitrary workspace escape.
		contained := api.ContainedPath(c.config.StateRoot, path)
		if staging := api.CredentialStagingRoot(); !contained && staging != "" {
			contained = api.ContainedPath(staging, path)
		}
		if err != nil || !contained || api.NoSymlinkPath(path) != nil {
			return api.ErrIsolation
		}
	}
	if len(plan.Execution.Mounts) != 0 {
		if !c.config.DockerFallback {
			return fmt.Errorf("%w: generic host state mounts are not enabled", api.ErrIsolation)
		}
		for _, mount := range plan.Execution.Mounts {
			if mount.Source != "connection-state" && mount.Source != "job-state" {
				return fmt.Errorf("%w: generic mount source is not a named state volume", api.ErrIsolation)
			}
		}
	}
	if len(plan.Execution.TCPForwards) != 0 && !c.config.DockerFallback {
		// Only the Docker fallback runs the companion inside the workload
		// network namespace; the stock ToolHive path has no forwarder, so
		// refusing here is the difference between a bounded relay and a
		// silently unmet connectivity promise.
		return fmt.Errorf("%w: tcp forwards require the Docker fallback companion", api.ErrIsolation)
	}
	if err := c.validateCredentialMounts(plan.CredentialMounts); err != nil {
		return err
	}
	return validateProxyGrants(plan, definition)
}

// validateProxyGrants re-checks the plan's proxy grants against the reviewed
// definition: every grant host must already be a declared egress host, and
// the secret-carrying fields obey the same bounds as the bounded-cli contract.
func validateProxyGrants(plan api.ControllerPlan, definition api.ToolDefinition) error {
	if len(plan.ProxyGrants) > 16 {
		return fmt.Errorf("%w: too many proxy grants", api.ErrInvalid)
	}
	for _, grant := range plan.ProxyGrants {
		if !api.CredentialPattern.MatchString(grant.EnvName) || grant.Value == "" || len(grant.Value) > 16384 || strings.ContainsAny(grant.Value, "\x00\r\n") || len(grant.Prefix) > 128 || strings.ContainsAny(grant.Prefix, "\x00\r\n") {
			return fmt.Errorf("%w: proxy grant shape", api.ErrInvalid)
		}
		for _, host := range grant.Hosts {
			if !api.HostPattern.MatchString(strings.ToLower(host)) || !slices.ContainsFunc(definition.Execution.Egress, func(egress string) bool {
				return strings.EqualFold(strings.TrimSpace(egress), host)
			}) {
				return fmt.Errorf("%w: proxy grant host %q outside declared egress", api.ErrInvalid, host)
			}
		}
	}
	return nil
}

func (c *genericController) validateCredentialMounts(mounts []api.Mount) error {
	if len(mounts) == 0 {
		return nil
	}
	if c.config.CredentialMountRoot == "" || len(mounts) > 16 {
		return fmt.Errorf("%w: credential mount root is not configured", api.ErrIsolation)
	}
	for _, mount := range mounts {
		if !filepath.IsAbs(mount.Source) || !api.ValidContainerMountTarget(mount.Target) || !api.ContainedPath(c.config.CredentialMountRoot, mount.Source) || api.NoSymlinkPath(mount.Source) != nil {
			return fmt.Errorf("%w: unsafe credential mount", api.ErrIsolation)
		}
		info, err := os.Stat(mount.Source)
		if err != nil || (mount.ReadOnly && !info.Mode().IsRegular()) || (!mount.ReadOnly && (!info.IsDir() || filepath.Base(mount.Source) != "state" || filepath.Dir(mount.Source) == filepath.Clean(c.config.CredentialMountRoot))) {
			return fmt.Errorf("%w: credential mount must be a read-only file or lease state directory", api.ErrIsolation)
		}
	}
	return nil
}

func validateGenericDefinition(definition api.ToolDefinition) error {
	if err := api.ValidateTrustedArtifactDefinition(definition); err != nil {
		return err
	}
	if definition.Transport != api.ContainerMCP || len(definition.Workload.SidecarImages) != 1 {
		return fmt.Errorf("%w: generic controller requires one ContainerMCP proxy image", api.ErrInvalid)
	}
	if definition.Workload.Class == api.Shared && !definition.Workload.Sharable {
		for _, input := range definition.Credentials {
			if !input.PerRequest {
				return fmt.Errorf("%w: shared generic MCP cannot retain credentials", api.ErrUnauthorized)
			}
		}
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
	if len(config.Hosting) > 0 {
		if err := c.siteInitSeed(); err != nil {
			return err
		}
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
	shutdownDone := make(chan struct{})
	go func() { // #nosec G118 -- graceful shutdown needs a fresh context after ctx cancellation
		defer close(shutdownDone)
		ticker := time.NewTicker(time.Second)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				if c.config.HostingOnly {
					_ = server.Close()
					cleanup, stop := context.WithTimeout(context.Background(), 30*time.Second)
					c.releaseHostingOnShutdown(cleanup)
					stop()
				}
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
	if config.HostingOnly {
		<-shutdownDone
	}
	return nil
}
