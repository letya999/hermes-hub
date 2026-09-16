package toolhub

// This is the deliberately small generic adapter. ToolHive remains the
// workload manager; this process only turns an approved plan into a bounded
// ToolHive invocation and checks Docker's returned profile before admission.

import (
	"bytes"
	"context"
	"crypto/subtle"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/letya999/hermes-hub/internal/identity"
)

type GenericControllerConfig struct {
	StateRoot      string          `json:"state_root"`
	ToolHiveBinary string          `json:"toolhive_binary"`
	SeccompProfile string          `json:"seccomp_profile"`
	DockerFallback bool            `json:"docker_fallback,omitempty"`
	BridgeBinary   string          `json:"bridge_binary,omitempty"`
	Definition     ToolDefinition  `json:"definition"`
	Budget         *WorkloadBudget `json:"-"`
	MaxActive      int             `json:"max_active"`
	IdleTTLSeconds int             `json:"idle_ttl_seconds"`
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
	mu         sync.Mutex // ponytail: one controller lock; split only after measured contention.
	workloads  map[string]genericWorkload
}

type genericContainer struct {
	Image  string
	State  struct{ Running bool }
	Config struct {
		User  string
		Image string
		Env   []string
	}
	HostConfig struct {
		NanoCpus       int64
		Memory         int64
		PidsLimit      int64
		Privileged     bool
		ReadonlyRootfs bool
		NetworkMode    string
		PidMode        string
		CapAdd         []string
		CapDrop        []string
		SecurityOpt    []string
		Binds          []string
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
	if config.BridgeBinary != "" {
		config.BridgeBinary = filepath.Clean(config.BridgeBinary)
	}
	if err := ValidateTrustedArtifactDefinition(config.Definition); err != nil {
		return nil, err
	}
	if config.Definition.Transport != ContainerMCP || len(config.Definition.Workload.SidecarImages) != 1 {
		return nil, fmt.Errorf("%w: generic controller requires one ContainerMCP proxy image", ErrInvalid)
	}
	if config.Definition.Workload.Class == Shared && len(config.Definition.Credentials) != 0 {
		return nil, fmt.Errorf("%w: shared generic MCP cannot retain credentials", ErrUnauthorized)
	}
	if noSymlinkPath(config.StateRoot) != nil || noSymlinkPath(config.SeccompProfile) != nil {
		return nil, ErrIsolation
	}
	return &genericController{config: config, command: localCommand, commandEnv: isolatedCommandEnv, workloads: map[string]genericWorkload{}}, nil
}

// isolatedCommandEnv prevents the trusted ToolHive control process from
// inheriting arbitrary host credentials. Only Docker locality and the OS
// runtime essentials cross the controller boundary; per-workload secret refs
// are appended explicitly by the caller.
func isolatedToolHiveEnviron(extra map[string]string) []string {
	overridden := map[string]bool{}
	for key := range extra {
		overridden[strings.ToUpper(key)] = true
	}
	allowed := []string{"PATH", "SystemRoot", "WINDIR", "USERPROFILE", "HOME", "APPDATA", "LOCALAPPDATA", "TEMP", "TMP", "DOCKER_HOST", "DOCKER_CONTEXT"}
	env := make([]string, 0, len(allowed)+len(extra))
	for _, key := range allowed {
		if overridden[strings.ToUpper(key)] {
			continue
		}
		if value, ok := os.LookupEnv(key); ok {
			env = append(env, key+"="+value)
		}
	}
	for key, value := range extra {
		env = append(env, key+"="+value)
	}
	return env
}

func isolatedCommandEnv(ctx context.Context, env map[string]string, binary string, args ...string) ([]byte, error) {
	cmd := exec.CommandContext(ctx, binary, args...) // #nosec G204 -- binary and args are fixed by the reviewed controller plan.
	cmd.Env = isolatedToolHiveEnviron(env)
	var output bytes.Buffer
	cmd.Stdout, cmd.Stderr = &output, &output
	if err := cmd.Run(); err != nil {
		return nil, fmt.Errorf("isolated ToolHive command failed: %w: %s", err, strings.TrimSpace(output.String()))
	}
	return output.Bytes(), nil
}

func localCommandEnv(ctx context.Context, env map[string]string, binary string, args ...string) ([]byte, error) {
	cmd := exec.CommandContext(ctx, binary, args...) // #nosec G204 -- binary and args are fixed by the reviewed controller plan.
	cmd.Env = os.Environ()
	for key, value := range env {
		cmd.Env = append(cmd.Env, key+"="+value)
	}
	var output bytes.Buffer
	cmd.Stdout, cmd.Stderr = &output, io.Discard
	if err := cmd.Run(); err != nil {
		return nil, fmt.Errorf("local workload command failed: %w", err)
	}
	return output.Bytes(), nil
}

func readGenericSecrets(path string, definition ToolDefinition) (map[string]string, error) {
	if path == "" {
		return nil, nil
	}
	if filepath.Ext(path) != ".env" || noSymlinkPath(path) != nil {
		return nil, ErrIsolation
	}
	body, err := os.ReadFile(path) // #nosec G304 -- path is the controller-owned workload file.
	if err != nil {
		return nil, err
	}
	allowed := map[string]bool{}
	for _, name := range definition.Environment {
		allowed[name] = true
	}
	for _, input := range definition.Credentials {
		allowed[input.Name] = true
	}
	values := map[string]string{}
	for _, line := range strings.Split(string(body), "\n") {
		if line == "" {
			continue
		}
		key, value, ok := strings.Cut(line, "=")
		if !ok || !allowed[key] || strings.ContainsAny(key, "\r\n") || strings.ContainsAny(value, "\r\n") || value == "" {
			return nil, ErrIsolation
		}
		if _, exists := values[key]; exists {
			return nil, ErrIsolation
		}
		values[key] = value
	}
	for _, input := range definition.Credentials {
		if input.Required && values[input.Name] == "" {
			return nil, fmt.Errorf("%w: required runtime credential missing", ErrUnauthorized)
		}
	}
	return values, nil
}

func (c *genericController) validatePlan(plan controllerPlan) error {
	if !identity.ValidID(plan.WorkloadID) || plan.DefinitionID != c.config.Definition.DefinitionID || plan.DefinitionVersion != c.config.Definition.Version || plan.Image != c.config.Definition.Source.Image || plan.Digest != c.config.Definition.Source.Digest || plan.ToolHiveVersion != c.config.Definition.Workload.ToolHiveVersion || !equalStrings(plan.SidecarImages, c.config.Definition.Workload.SidecarImages) || !reflectExecution(plan.Execution, c.config.Definition.Execution) {
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
	return nil
}

func (c *genericController) handler(token string) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost || r.URL.Path != "/admit" || r.Header.Get("Origin") != "" || subtle.ConstantTimeCompare([]byte(r.Header.Get("Authorization")), []byte("Bearer "+token)) != 1 {
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}
		var plan controllerPlan
		decoder := json.NewDecoder(http.MaxBytesReader(w, r.Body, 65536))
		decoder.DisallowUnknownFields()
		if decoder.Decode(&plan) != nil || decoder.Decode(new(any)) != io.EOF {
			http.Error(w, "invalid plan", http.StatusForbidden)
			return
		}
		if err := c.validatePlan(plan); err != nil {
			http.Error(w, "unapproved plan", http.StatusForbidden)
			return
		}
		c.mu.Lock()
		defer c.mu.Unlock()
		if workload, ok := c.workloads[plan.WorkloadID]; ok {
			if workload.plan.WorkspacePath != plan.WorkspacePath {
				http.Error(w, "workload identity conflict", http.StatusForbidden)
				return
			}
			_ = c.config.Budget.Touch(plan.WorkloadID)
			writeGenericReceipt(w, workload)
			return
		}
		if err := c.config.Budget.Acquire(r.Context(), plan.WorkloadID); err != nil {
			http.Error(w, "workload budget unavailable", http.StatusServiceUnavailable)
			return
		}
		workload, err := c.start(r.Context(), plan)
		if err != nil {
			c.config.Budget.Release(plan.WorkloadID)
			http.Error(w, "workload enforcement unavailable", http.StatusServiceUnavailable)
			return
		}
		c.workloads[plan.WorkloadID] = workload
		writeGenericReceipt(w, workload)
	})
}

func writeGenericReceipt(w http.ResponseWriter, workload genericWorkload) {
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(AdmissionReceipt{WorkloadID: workload.plan.WorkloadID, State: "running", Enforced: true, Endpoint: workload.endpoint, ImageDigest: workload.plan.Digest, SidecarImages: workload.plan.SidecarImages, Execution: workload.plan.Execution})
}

func (c *genericController) start(ctx context.Context, plan controllerPlan) (genericWorkload, error) {
	if _, err := c.command(ctx, c.config.ToolHiveBinary, "version"); err != nil {
		return genericWorkload{}, err
	}
	if len(plan.Execution.Mounts) != 0 && !c.config.DockerFallback {
		return genericWorkload{}, fmt.Errorf("%w: generic host state mounts are not enabled", ErrIsolation)
	}
	help, err := c.command(ctx, c.config.ToolHiveBinary, "run", "--help")
	if err != nil {
		return genericWorkload{}, fmt.Errorf("%w: ToolHive cannot express the required create-time profile", ErrIsolation)
	}
	if !toolHiveSupportsRuntime(string(help)) {
		if !c.config.DockerFallback {
			return genericWorkload{}, fmt.Errorf("%w: ToolHive cannot express the required create-time profile", ErrIsolation)
		}
		return c.startDockerRemoteFallback(ctx, plan)
	}
	if len(plan.Execution.Mounts) != 0 {
		return genericWorkload{}, fmt.Errorf("%w: ToolHive cannot express the required create-time profile", ErrIsolation)
	}
	port, err := reserveLoopbackPort()
	if err != nil {
		return genericWorkload{}, err
	}
	network := "hermes-" + plan.WorkloadID
	proxyName := plan.WorkloadID + "-egress"
	proxyVol := proxyName + "-config"
	started := false
	defer func() {
		if started {
			return
		}
		_, _ = c.command(ctx, c.config.ToolHiveBinary, "rm", plan.WorkloadID)
		_, _ = c.command(ctx, "docker", "rm", "--force", plan.WorkloadID, proxyName)
		_, _ = c.command(ctx, "docker", "network", "rm", network)
		_, _ = c.command(ctx, "docker", "volume", "rm", proxyVol)
	}()
	if _, err := c.command(ctx, "docker", "network", "create", "--internal", "--label", "hermes-hub.role=generic-mcp", network); err != nil {
		return genericWorkload{}, err
	}
	if _, err := c.command(ctx, "docker", "volume", "create", "--label", "hermes-hub.role=generic-mcp", proxyVol); err != nil {
		return genericWorkload{}, err
	}
	proxyConfig, err := genericProxyConfig(plan.Execution.Egress)
	if err != nil {
		return genericWorkload{}, err
	}
	file, err := os.CreateTemp(c.config.StateRoot, ".proxy-*")
	if err != nil {
		return genericWorkload{}, err
	}
	fileName := file.Name()
	defer os.Remove(fileName)
	if _, err = file.WriteString(proxyConfig); err != nil {
		file.Close()
		return genericWorkload{}, err
	}
	if err = file.Close(); err != nil {
		return genericWorkload{}, err
	}
	limits := []string{"--cpus", strconv.FormatFloat(float64(plan.Execution.CPUMillis)/1000, 'f', 3, 64), "--memory", strconv.Itoa(plan.Execution.MemoryMiB) + "m", "--memory-swap", strconv.Itoa(plan.Execution.MemoryMiB) + "m", "--pids-limit", strconv.Itoa(plan.Execution.MaxPIDs)}
	args := append([]string{"create", "--name", proxyName, "--network", network, "--read-only", "--user", "31:31", "--cap-drop", "ALL", "--security-opt", "no-new-privileges=true", "--security-opt", "seccomp=" + c.config.SeccompProfile, "--tmpfs", "/tmp:rw,nosuid,nodev,size=64m", "--mount", "type=volume,source=" + proxyVol + ",target=/etc/squid"}, limits...)
	args = append(args, plan.SidecarImages[0])
	if _, err := c.command(ctx, "docker", args...); err != nil {
		return genericWorkload{}, err
	}
	if _, err := c.command(ctx, "docker", "cp", fileName, proxyName+":/etc/squid/squid.conf"); err != nil {
		return genericWorkload{}, err
	}
	if _, err := c.command(ctx, "docker", "network", "connect", "bridge", proxyName); err != nil {
		return genericWorkload{}, err
	}
	if _, err := c.command(ctx, "docker", "start", proxyName); err != nil {
		return genericWorkload{}, err
	}
	ip, err := c.proxyIP(ctx, proxyName, network)
	if err != nil {
		return genericWorkload{}, err
	}
	toolArgs := []string{"run", "--name", plan.WorkloadID, "--host", "127.0.0.1", "--proxy-port", strconv.Itoa(port), "--transport", "stdio", "--proxy-mode", "streamable-http", "--permission-profile", "none", "--network", network, "--isolate-network=false", "--read-only", "--security-opt", "seccomp=" + c.config.SeccompProfile, "--security-opt", "no-new-privileges=true", "--cap-drop", "ALL", "--user", "10001:10001", "--cpus", strconv.FormatFloat(float64(plan.Execution.CPUMillis)/1000, 'f', 3, 64), "--memory", strconv.Itoa(plan.Execution.MemoryMiB) + "m", "--memory-swap", strconv.Itoa(plan.Execution.MemoryMiB) + "m", "--pids-limit", strconv.Itoa(plan.Execution.MaxPIDs), "--timeout", strconv.Itoa(plan.Execution.TimeoutSeconds), "--output-limit", strconv.Itoa(plan.Execution.OutputBytes), "--env", "HTTP_PROXY=http://" + ip + ":3128", "--env", "HTTPS_PROXY=http://" + ip + ":3128"}
	var secretValues map[string]string
	if plan.WorkspacePath != "" {
		secretPath := filepath.Join(plan.WorkspacePath, "credentials.env")
		if _, statErr := os.Lstat(secretPath); statErr == nil {
			secretValues, err = readGenericSecrets(secretPath, c.config.Definition)
			if err != nil {
				return genericWorkload{}, err
			}
		} else if !os.IsNotExist(statErr) {
			return genericWorkload{}, ErrIsolation
		}
	}
	commandEnv := map[string]string{}
	if len(secretValues) > 0 {
		commandEnv["TOOLHIVE_SECRETS_PROVIDER"] = "environment"
	}
	for key, value := range secretValues {
		ref := "HERMES_" + strings.ToUpper(plan.WorkloadID) + "_" + key
		ref = strings.NewReplacer("-", "_", ".", "_").Replace(ref)
		commandEnv["TOOLHIVE_SECRET_"+ref] = value
		toolArgs = append(toolArgs, "--secret", ref+",target="+key)
	}
	for _, tool := range c.config.Definition.Tools {
		toolArgs = append(toolArgs, "--tools", tool.Name)
	}
	imageRef, err := c.resolveArtifactImage(ctx, plan)
	if err != nil {
		return genericWorkload{}, err
	}
	toolArgs = append(toolArgs, imageRef)
	if _, err := c.commandEnv(ctx, commandEnv, c.config.ToolHiveBinary, toolArgs...); err != nil {
		return genericWorkload{}, err
	}
	if err := waitLocalWorkload(ctx, func() error {
		_, err := c.command(ctx, "docker", "inspect", plan.WorkloadID)
		return err
	}); err != nil {
		return genericWorkload{}, err
	}
	workload := genericWorkload{plan: plan, endpoint: "http://127.0.0.1:" + strconv.Itoa(port) + DefaultEndpointPath, proxyName: proxyName, proxyVol: proxyVol, imageRef: imageRef}
	if _, err := c.inspect(ctx, workload); err != nil {
		return genericWorkload{}, err
	}
	started = true
	return workload, nil
}

func reserveLoopbackPort() (int, error) {
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return 0, err
	}
	defer listener.Close() // ponytail: Docker takes the port immediately; a longer lease needs a port registry.
	return listener.Addr().(*net.TCPAddr).Port, nil
}

func toolHiveSupportsRuntime(help string) bool {
	for _, flag := range []string{"--read-only", "--security-opt", "--cpus", "--memory", "--pids-limit", "--cap-drop", "--user", "--timeout", "--output-limit"} {
		if !strings.Contains(help, flag) {
			return false
		}
	}
	return true
}

func (c *genericController) proxyIP(ctx context.Context, name, network string) (string, error) {
	body, err := c.command(ctx, "docker", "inspect", name, "--format", "{{json .NetworkSettings.Networks}}")
	if err != nil {
		return "", err
	}
	var networks map[string]struct{ IPAddress string }
	if json.Unmarshal(body, &networks) != nil {
		return "", ErrIsolation
	}
	ip := strings.TrimSpace(networks[network].IPAddress)
	parsed := net.ParseIP(ip)
	if parsed == nil || !parsed.IsPrivate() {
		return "", ErrIsolation
	}
	return ip, nil
}

func (c *genericController) inspect(ctx context.Context, workload genericWorkload) (AdmissionReceipt, error) {
	inspectArgs := []string{"inspect", workload.plan.WorkloadID, workload.proxyName}
	if workload.dockerFallback {
		inspectArgs = append(inspectArgs, workload.relayName)
	}
	body, err := c.command(ctx, "docker", inspectArgs...)
	if err != nil {
		return AdmissionReceipt{}, err
	}
	var containers []genericContainer
	expectedContainers := 2
	if workload.dockerFallback {
		expectedContainers = 3
	}
	if json.Unmarshal(body, &containers) != nil || len(containers) != expectedContainers {
		return AdmissionReceipt{}, ErrIsolation
	}
	network := "hermes-" + workload.plan.WorkloadID
	for i, container := range containers {
		if !container.State.Running || container.HostConfig.Privileged || !container.HostConfig.ReadonlyRootfs || container.HostConfig.NetworkMode != network || container.HostConfig.PidMode != "" || len(container.HostConfig.CapAdd) != 0 || len(container.HostConfig.CapDrop) != 1 || container.HostConfig.CapDrop[0] != "ALL" || !contains(container.HostConfig.SecurityOpt, "no-new-privileges=true") || !securityOptHasSeccomp(container.HostConfig.SecurityOpt, c.config.SeccompProfile) || container.HostConfig.NanoCpus != int64(workload.plan.Execution.CPUMillis)*1000000 || container.HostConfig.Memory != int64(workload.plan.Execution.MemoryMiB)*1048576 || container.HostConfig.PidsLimit != int64(workload.plan.Execution.MaxPIDs) {
			return AdmissionReceipt{}, fmt.Errorf("%w: generic runtime profile container %d", ErrIsolation, i)
		}
		bridgeMountOK := !workload.dockerFallback && len(container.Mounts) == 0
		if workload.dockerFallback {
			bridgeMountOK = fallbackBridgeMountsOK(container.Mounts, workload)
		}
		if i == 0 && (!artifactImageMatches(container.Config.Image, workload.plan, workload.imageRef) || container.Config.User == "" || container.Config.User == "0" || container.Config.User == "0:0" || len(container.HostConfig.Binds) != 0 || (!workload.dockerFallback && len(container.Mounts) != 0) || (workload.dockerFallback && !bridgeMountOK) || len(container.NetworkSettings.Networks) != 1) {
			return AdmissionReceipt{}, ErrIsolation
		}
		if i == 1 && (len(container.HostConfig.Binds) != 0 || len(container.Mounts) != 1 || container.Mounts[0].Type != "volume" || (container.Mounts[0].Name != workload.proxyVol && container.Mounts[0].Source != workload.proxyVol) || container.Mounts[0].Destination != "/etc/squid" || len(container.NetworkSettings.Networks) != 2 || container.Image == "") {
			return AdmissionReceipt{}, ErrIsolation
		}
		if i == 2 && workload.dockerFallback && (container.Config.Image != workload.plan.SidecarImages[0] || container.Config.User == "" || container.Config.User == "0" || container.Config.User == "0:0" || len(container.HostConfig.Binds) != 0 || len(container.Mounts) != 1 || container.Mounts[0].Type != "volume" || (container.Mounts[0].Name != workload.relayVol && container.Mounts[0].Source != workload.relayVol) || container.Mounts[0].Destination != "/hermes-relay" || container.Mounts[0].RW || len(container.NetworkSettings.Networks) != 2 || !envHasBridgeToken(container.Config.Env)) {
			return AdmissionReceipt{}, ErrIsolation
		}
		if workload.dockerFallback && i == 0 && envContainsForwardingSecret(container.Config.Env) {
			return AdmissionReceipt{}, fmt.Errorf("%w: MCP container received a forwarding secret", ErrIsolation)
		}
	}
	return AdmissionReceipt{WorkloadID: workload.plan.WorkloadID, State: "running", Enforced: true, Endpoint: workload.endpoint, ImageDigest: workload.plan.Digest, SidecarImages: workload.plan.SidecarImages, Execution: workload.plan.Execution}, nil
}

func fallbackBridgeMountsOK(mounts []genericMount, workload genericWorkload) bool {
	if len(mounts) != len(workload.stateVols)+1 {
		return false
	}
	bridge := false
	states := make([]bool, len(workload.stateVols))
	for _, mount := range mounts {
		if mount.Type != "volume" {
			return false
		}
		if mount.Destination == "/hermes-bridge" {
			if bridge || mount.RW || (mount.Name != workload.bridgeVol && mount.Source != workload.bridgeVol) {
				return false
			}
			bridge = true
			continue
		}
		matched := false
		for i, declared := range workload.plan.Execution.Mounts {
			if mount.Destination == declared.Target && (mount.Name == workload.stateVols[i] || mount.Source == workload.stateVols[i]) && mount.RW != declared.ReadOnly {
				if states[i] {
					return false
				}
				states[i], matched = true, true
				break
			}
		}
		if !matched {
			return false
		}
	}
	if !bridge {
		return false
	}
	for _, present := range states {
		if !present {
			return false
		}
	}
	return true
}

func (c *genericController) CleanupIdle(ctx context.Context, now time.Time) []string {
	c.mu.Lock()
	defer c.mu.Unlock()
	ids := c.config.Budget.CleanupIdle(now)
	for _, id := range ids {
		workload, ok := c.workloads[id]
		if !ok {
			continue
		}
		c.removeWorkload(ctx, workload, c.config.Definition.Workload.Stateful)
		delete(c.workloads, id)
	}
	return ids
}

func (c *genericController) removeWorkload(ctx context.Context, workload genericWorkload, keepState bool) {
	if workload.remoteName != "" {
		if workload.remoteState != "" {
			_, _ = c.commandEnv(ctx, fallbackToolHiveEnv(workload.remoteState, nil), c.config.ToolHiveBinary, "rm", workload.remoteName)
		} else {
			_, _ = c.command(ctx, c.config.ToolHiveBinary, "rm", workload.remoteName)
		}
	}
	names := []string{workload.plan.WorkloadID, workload.proxyName}
	if workload.relayName != "" {
		names = append(names, workload.relayName)
	}
	_, _ = c.command(ctx, "docker", append([]string{"rm", "--force"}, names...)...)
	_, _ = c.command(ctx, "docker", "network", "rm", "hermes-"+workload.plan.WorkloadID)
	_, _ = c.command(ctx, "docker", "volume", "rm", workload.proxyVol)
	if workload.bridgeVol != "" {
		_, _ = c.command(ctx, "docker", "volume", "rm", workload.bridgeVol)
	}
	if workload.relayVol != "" {
		_, _ = c.command(ctx, "docker", "volume", "rm", workload.relayVol)
	}
	if !keepState {
		for _, volume := range workload.stateVols {
			_, _ = c.command(ctx, "docker", "volume", "rm", volume)
		}
	}
	if workload.remoteState != "" {
		_ = os.RemoveAll(workload.remoteState)
	}
}

func envContainsForwardingSecret(env []string) bool {
	for _, value := range env {
		upper := strings.ToUpper(value)
		if strings.HasPrefix(upper, "HERMES_BRIDGE_TOKEN=") || strings.Contains(upper, "TOOLHIVE_SECRET") || strings.Contains(upper, "BRIDGE_AUTH=") {
			return true
		}
	}
	return false
}

func envHasBridgeToken(env []string) bool {
	for _, value := range env {
		if strings.HasPrefix(value, "HERMES_BRIDGE_TOKEN=") && len(strings.TrimPrefix(value, "HERMES_BRIDGE_TOKEN=")) >= 32 {
			return true
		}
	}
	return false
}

func genericProxyConfig(egress []string) (string, error) {
	if len(egress) == 0 || len(egress) > 32 {
		return "", ErrIsolation
	}
	var lines []string
	for i, host := range egress {
		host = strings.ToLower(strings.TrimSpace(host))
		if host == "" || strings.ContainsAny(host, "\r\n") {
			return "", ErrIsolation
		}
		if strings.Contains(host, "/") {
			lines = append(lines, fmt.Sprintf("acl hermes_%d dst %s", i, host))
		} else {
			hostOnly, port, ok := strings.Cut(host, ":")
			if !ok {
				hostOnly, port = host, "443"
			}
			if strings.Contains(hostOnly, ".") || hostOnly == "localhost" {
				lines = append(lines, fmt.Sprintf("acl hermes_%d dstdomain %s", i, hostOnly))
			} else {
				return "", ErrIsolation
			}
			lines = append(lines, fmt.Sprintf("acl hermes_port_%d port %s", i, port))
		}
	}
	for i := range egress {
		if strings.Contains(egress[i], "/") {
			lines = append(lines, fmt.Sprintf("http_access allow connect hermes_%d", i))
		} else {
			lines = append(lines, fmt.Sprintf("http_access allow connect hermes_%d hermes_port_%d", i, i))
		}
	}
	return "http_port 3128\nacl connect method CONNECT\n" + strings.Join(lines, "\n") + "\nhttp_access deny all\ncache deny all\naccess_log none\ncache_log /dev/stderr\npid_filename /tmp/squid.pid\n", nil
}

func equalStrings(a, b []string) bool { return strings.Join(a, "\x00") == strings.Join(b, "\x00") }
func reflectExecution(a, b ExecutionPolicy) bool {
	return a.TimeoutSeconds == b.TimeoutSeconds && a.OutputBytes == b.OutputBytes && a.CPUMillis == b.CPUMillis && a.MemoryMiB == b.MemoryMiB && a.MaxPIDs == b.MaxPIDs && equalStrings(a.Egress, b.Egress) && reflectMounts(a.Mounts, b.Mounts)
}
func reflectMounts(a, b []Mount) bool {
	return len(a) == len(b) && func() bool {
		for i := range a {
			if a[i] != b[i] {
				return false
			}
		}
		return true
	}()
}
func contains(values []string, wanted string) bool {
	for _, value := range values {
		if value == wanted {
			return true
		}
	}
	return false
}

func securityOptHasSeccomp(values []string, profilePath string) bool {
	if contains(values, "seccomp="+profilePath) {
		return true
	}
	body, err := os.ReadFile(profilePath) // #nosec G304 -- absolute operator-owned profile validated before use.
	if err != nil {
		return false
	}
	var expected any
	if json.Unmarshal(body, &expected) != nil {
		return false
	}
	for _, value := range values {
		if !strings.HasPrefix(value, "seccomp=") {
			continue
		}
		var actual any
		if json.Unmarshal([]byte(strings.TrimPrefix(value, "seccomp=")), &actual) == nil && reflect.DeepEqual(actual, expected) {
			return true
		}
	}
	return false
}

// RunGenericController starts the loopback admission endpoint. ToolHive and
// Docker are selected explicitly by the operator; no daemon socket is exposed
// to MCP workloads.
func RunGenericController(ctx context.Context, config GenericControllerConfig, listen, token string) error {
	host, _, err := net.SplitHostPort(listen)
	if err != nil || host != "127.0.0.1" || len(token) < 32 || strings.ContainsAny(token, "\r\n") {
		return fmt.Errorf("loopback address and private token required")
	}
	c, err := newGenericController(config)
	if err != nil {
		return err
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
				return
			case now := <-ticker.C:
				c.CleanupIdle(context.Background(), now)
			}
		}
	}()
	if err := server.Serve(listener); err != http.ErrServerClosed {
		return err
	}
	return nil
}
