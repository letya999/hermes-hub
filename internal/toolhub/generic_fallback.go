package toolhub

// The fallback is intentionally narrow: stock ToolHive remains the proxy and
// lifecycle API, while Docker creates the MCP container with the required
// profile before any untrusted process starts. Stateful workloads use only
// controller-owned named volumes; credentials are validated from an owner
// workspace env-file and handed to Docker without entering ToolHive arguments.

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

const genericBridgeListen = "0.0.0.0:8765"

type genericImageConfig struct {
	Entrypoint []string `json:"Entrypoint"`
	Cmd        []string `json:"Cmd"`
}

func (c *genericController) startDockerRemoteFallback(ctx context.Context, plan controllerPlan) (workload genericWorkload, err error) {
	step := "validate"
	defer func() {
		if step != "complete" {
			slog.Warn("generic Docker fallback did not start", "step", step, "error", err)
		}
	}()
	if c.config.Definition.Workload.Class == Shared && (c.config.Definition.Workload.Stateful || len(c.config.Definition.Credentials) != 0 || len(plan.Execution.Mounts) != 0) {
		return genericWorkload{}, fmt.Errorf("%w: shared Docker fallback cannot retain state or credentials", ErrIsolation)
	}
	for _, mount := range plan.Execution.Mounts {
		if mount.Source != "connection-state" && mount.Source != "job-state" {
			return genericWorkload{}, fmt.Errorf("%w: Docker fallback supports only named state volumes", ErrIsolation)
		}
	}
	if c.config.Definition.Workload.Stateful && len(plan.Execution.Mounts) == 0 {
		return genericWorkload{}, fmt.Errorf("%w: stateful Docker fallback requires a named state volume", ErrIsolation)
	}
	bridgeBinary, err := resolveLinuxBridgeBinary(c.config.BridgeBinary)
	if err != nil {
		return genericWorkload{}, err
	}
	step = "docker-preflight"
	if err := c.requireLocalLinuxDocker(ctx); err != nil {
		return genericWorkload{}, err
	}
	step = "artifact-image"
	imageRef, err := c.resolveArtifactImage(ctx, plan)
	if err != nil {
		return genericWorkload{}, err
	}
	step = "artifact-entrypoint"
	command, err := c.artifactCommand(ctx, imageRef)
	if err != nil {
		return genericWorkload{}, err
	}
	step = "ports"
	tokenBytes := make([]byte, 32)
	if _, err := rand.Read(tokenBytes); err != nil {
		return genericWorkload{}, err
	}
	bridgeToken := hex.EncodeToString(tokenBytes)
	resources := genericFallbackResources(plan.WorkloadID)
	network, proxyName, proxyVol := resources.Network, resources.ProxyName, resources.ProxyVol
	bridgeVol, relayName, relayVol := resources.BridgeVol, resources.RelayName, resources.RelayVol
	bridgeSeed, remoteName := resources.BridgeSeed, resources.RemoteName
	step = "toolhive-state"
	stateVols := make([]string, len(plan.Execution.Mounts))
	for i := range stateVols {
		stateVols[i] = genericStateVolume(plan.WorkloadID, i)
	}
	remoteState := filepath.Join(c.config.StateRoot, ".toolhive-"+plan.WorkloadID)
	if noSymlinkPath(remoteState) == nil {
		return genericWorkload{}, fmt.Errorf("%w: existing ToolHive fallback state", ErrIsolation)
	}
	if err := os.Mkdir(remoteState, 0700); err != nil {
		return genericWorkload{}, err
	}
	started := false
	cleanup := func() {
		cleanupCtx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
		defer cancel()
		_, _ = c.commandEnv(cleanupCtx, fallbackToolHiveEnv(remoteState, nil), c.config.ToolHiveBinary, "rm", remoteName)
		_, _ = c.command(cleanupCtx, "docker", "rm", "--force", plan.WorkloadID, proxyName, relayName, bridgeSeed)
		_, _ = c.command(cleanupCtx, "docker", "network", "rm", network)
		_, _ = c.command(cleanupCtx, "docker", "volume", "rm", proxyVol)
		_, _ = c.command(cleanupCtx, "docker", "volume", "rm", bridgeVol)
		_, _ = c.command(cleanupCtx, "docker", "volume", "rm", relayVol)
		for _, volume := range stateVols {
			_, _ = c.command(cleanupCtx, "docker", "volume", "rm", volume)
		}
		_ = os.RemoveAll(remoteState)
	}
	defer func() {
		if !started {
			cleanup()
		}
	}()
	step = "network"
	if _, err := c.command(ctx, "docker", "network", "create", "--internal", "--label", "hermes-hub.role=generic-mcp", network); err != nil {
		return genericWorkload{}, err
	}
	if _, err := c.command(ctx, "docker", "volume", "create", "--label", "hermes-hub.role=generic-mcp", proxyVol); err != nil {
		return genericWorkload{}, err
	}
	if _, err := c.command(ctx, "docker", "volume", "create", "--label", "hermes-hub.role=generic-mcp", bridgeVol); err != nil {
		return genericWorkload{}, err
	}
	if _, err := c.command(ctx, "docker", "volume", "create", "--label", "hermes-hub.role=generic-mcp", relayVol); err != nil {
		return genericWorkload{}, err
	}
	for _, volume := range stateVols {
		if _, err := c.command(ctx, "docker", "volume", "create", "--label", "hermes-hub.role=generic-mcp-state", volume); err != nil {
			return genericWorkload{}, err
		}
		// Named volumes are root-owned; the MCP runs as 10001. Best-effort
		// chmod so filesystem servers can use /state. Failure is not fatal
		// for read-only tools on a fresh volume.
		_, _ = c.command(ctx, "docker", "run", "--rm", "--user", "0:0", "--network", "none", "--read-only", "--cap-drop", "ALL", "--security-opt", "no-new-privileges=true", "--mount", "type=volume,source="+volume+",target=/state", "--entrypoint", "chmod", imageRef, "0777", "/state")
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
	step = "proxy-create"
	proxyArgs := append([]string{"create", "--name", proxyName, "--network", network, "--read-only", "--user", "31:31", "--cap-drop", "ALL", "--security-opt", "no-new-privileges=true", "--security-opt", "seccomp=" + c.config.SeccompProfile, "--tmpfs", "/tmp:rw,nosuid,nodev,size=64m", "--mount", "type=volume,source=" + proxyVol + ",target=/etc/squid"}, limits...)
	proxyArgs = append(proxyArgs, c.config.Definition.Workload.SidecarImages[0])
	if _, err := c.command(ctx, "docker", proxyArgs...); err != nil {
		return genericWorkload{}, err
	}
	if _, err := c.command(ctx, "docker", "cp", fileName, proxyName+":/etc/squid/squid.conf"); err != nil {
		return genericWorkload{}, err
	}
	if _, err := c.command(ctx, "docker", "network", "connect", "bridge", proxyName); err != nil {
		return genericWorkload{}, err
	}
	step = "proxy-start"
	if _, err := c.command(ctx, "docker", "start", proxyName); err != nil {
		return genericWorkload{}, err
	}
	proxyIP, err := c.proxyIP(ctx, proxyName, network)
	if err != nil {
		return genericWorkload{}, err
	}
	step = "bridge-config"
	configBytes, err := json.Marshal(struct {
		Listen       string   `json:"listen"`
		TokenEnv     string   `json:"token_env"`
		Command      []string `json:"command"`
		AllowedTools []string `json:"allowed_tools"`
	}{genericBridgeListen, "", command, definitionToolNames(c.config.Definition.Tools)})
	if err != nil {
		return genericWorkload{}, err
	}
	configFile, err := os.CreateTemp(c.config.StateRoot, ".bridge-*.json")
	if err != nil {
		return genericWorkload{}, err
	}
	configFileName := configFile.Name()
	defer os.Remove(configFileName)
	if _, err := configFile.Write(configBytes); err != nil {
		configFile.Close()
		return genericWorkload{}, err
	}
	if err := configFile.Close(); err != nil {
		return genericWorkload{}, err
	}
	if err := os.Chmod(configFileName, 0644); err != nil {
		return genericWorkload{}, err
	}
	step = "bridge-seed"
	seedArgs := append([]string{"create", "--name", bridgeSeed, "--network", "none", "--read-only", "--user", "0:0", "--cap-drop", "ALL", "--security-opt", "no-new-privileges=true", "--memory", "32m", "--cpus", "0.1", "--pids-limit", "16", "--mount", "type=volume,source=" + bridgeVol + ",target=/hermes-bridge", "--mount", "type=volume,source=" + relayVol + ",target=/hermes-relay"}, c.config.Definition.Workload.SidecarImages[0])
	if _, err := c.command(ctx, "docker", seedArgs...); err != nil {
		return genericWorkload{}, err
	}
	if _, err := c.command(ctx, "docker", "cp", bridgeBinary, bridgeSeed+":/hermes-bridge/hubctl"); err != nil {
		return genericWorkload{}, err
	}
	if _, err := c.command(ctx, "docker", "cp", configFileName, bridgeSeed+":/hermes-bridge/config.json"); err != nil {
		return genericWorkload{}, err
	}
	if _, err := c.command(ctx, "docker", "cp", bridgeBinary, bridgeSeed+":/hermes-relay/hubctl"); err != nil {
		return genericWorkload{}, err
	}
	if _, err := c.command(ctx, "docker", "rm", bridgeSeed); err != nil {
		return genericWorkload{}, err
	}
	step = "bridge-create"
	bridgeArgs := append([]string{"create", "--name", plan.WorkloadID, "--network", network, "--read-only", "--user", "10001:10001", "--cap-drop", "ALL", "--security-opt", "no-new-privileges=true", "--security-opt", "seccomp=" + c.config.SeccompProfile, "--tmpfs", "/tmp:rw,nosuid,nodev,uid=10001,gid=10001,mode=0700,size=64m", "--tmpfs", "/run:rw,nosuid,nodev,uid=10001,gid=10001,mode=0700,size=16m", "--env", "HOME=/tmp", "--env", "HTTP_PROXY=http://" + proxyIP + ":3128", "--env", "HTTPS_PROXY=http://" + proxyIP + ":3128", "--env", "NO_PROXY=127.0.0.1,localhost", "--entrypoint", "/hermes-bridge/hubctl"}, limits...)
	if len(c.config.Definition.Credentials) > 0 {
		if plan.WorkspacePath == "" {
			return genericWorkload{}, fmt.Errorf("%w: credential-bearing fallback requires an owner workspace", ErrUnauthorized)
		}
		if _, err = readGenericSecrets(filepath.Join(plan.WorkspacePath, "credentials.env"), c.config.Definition); err != nil {
			return genericWorkload{}, err
		}
		bridgeArgs = append(bridgeArgs, "--env-file", filepath.Join(plan.WorkspacePath, "credentials.env"))
	}
	for i, mount := range plan.Execution.Mounts {
		mountSpec := "type=volume,source=" + stateVols[i] + ",target=" + mount.Target
		if mount.ReadOnly {
			mountSpec += ",readonly"
		}
		bridgeArgs = append(bridgeArgs, "--mount", mountSpec)
	}
	bridgeArgs = append(bridgeArgs, "--mount", "type=volume,source="+bridgeVol+",target=/hermes-bridge,readonly", imageRef, "companion", "--config", "/hermes-bridge/config.json")
	if _, err := c.command(ctx, "docker", bridgeArgs...); err != nil {
		return genericWorkload{}, err
	}
	step = "bridge-start"
	if _, err := c.command(ctx, "docker", "start", plan.WorkloadID); err != nil {
		return genericWorkload{}, err
	}
	step = "relay-create"
	relayArgs := append([]string{"create", "--name", relayName, "--network", network, "--publish", "127.0.0.1::8765/tcp", "--read-only", "--user", "10001:10001", "--cap-drop", "ALL", "--security-opt", "no-new-privileges=true", "--security-opt", "seccomp=" + c.config.SeccompProfile, "--tmpfs", "/tmp:rw,nosuid,nodev,size=16m", "--env", "HERMES_BRIDGE_TOKEN=" + bridgeToken, "--entrypoint", "/hermes-relay/hubctl"}, limits...)
	relayArgs = append(relayArgs, "--mount", "type=volume,source="+relayVol+",target=/hermes-relay,readonly", c.config.Definition.Workload.SidecarImages[0], "relay", "--listen", genericBridgeListen, "--target", "http://"+plan.WorkloadID+":8765/mcp", "--token-env", "HERMES_BRIDGE_TOKEN")
	if _, err := c.command(ctx, "docker", relayArgs...); err != nil {
		return genericWorkload{}, err
	}
	if _, err := c.command(ctx, "docker", "network", "connect", "bridge", relayName); err != nil {
		return genericWorkload{}, err
	}
	if _, err := c.command(ctx, "docker", "start", relayName); err != nil {
		return genericWorkload{}, err
	}
	bridgePort, err := c.publishedPort(ctx, relayName)
	if err != nil {
		return genericWorkload{}, err
	}
	remoteURL := "http://127.0.0.1:" + strconv.Itoa(bridgePort) + "/mcp"
	step = "toolhive-remote"
	remoteEnv := fallbackToolHiveEnv(remoteState, map[string]string{"TOOLHIVE_SECRET_BRIDGE_AUTH": "Bearer " + bridgeToken})
	remoteArgs := []string{"run", remoteURL, "--name", remoteName, "--host", "127.0.0.1", "--proxy-port", "0", "--stateless", "--ignore-globally=false", "--remote-forward-headers-secret", "Authorization=BRIDGE_AUTH"}
	if _, err := c.commandEnv(ctx, remoteEnv, c.config.ToolHiveBinary, remoteArgs...); err != nil {
		return genericWorkload{}, err
	}
	endpoint, err := c.toolHiveEndpoint(ctx, remoteState, remoteName)
	if err != nil {
		return genericWorkload{}, err
	}
	ready, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	if err := waitLocalWorkload(ready, func() error {
		request, err := http.NewRequestWithContext(ready, http.MethodGet, endpoint, nil)
		if err != nil {
			return err
		}
		response, err := http.DefaultClient.Do(request)
		if err != nil {
			return err
		}
		response.Body.Close()
		return nil
	}); err != nil {
		return genericWorkload{}, err
	}
	step = "inspection"
	workload = genericWorkload{plan: plan, endpoint: endpoint, proxyName: proxyName, proxyVol: proxyVol, bridgeVol: bridgeVol, stateVols: stateVols, remoteName: remoteName, remoteState: remoteState, bridgePort: bridgePort, relayName: relayName, relayVol: relayVol, dockerFallback: true, imageRef: imageRef}
	if _, err := c.inspect(ctx, workload); err != nil {
		return genericWorkload{}, err
	}
	started = true
	step = "complete"
	return workload, nil
}

type fallbackResources struct {
	Network, ProxyName, ProxyVol, BridgeVol, RelayName, RelayVol, BridgeSeed, RemoteName string
}

func genericFallbackResources(workloadID string) fallbackResources {
	proxyName := workloadID + "-egress"
	relayName := workloadID + "-relay"
	return fallbackResources{
		Network: "hermes-" + workloadID, ProxyName: proxyName, ProxyVol: proxyName + "-config",
		BridgeVol: workloadID + "-bridge", RelayName: relayName, RelayVol: relayName + "-bin",
		BridgeSeed: workloadID + "-bridge-seed", RemoteName: workloadID + "-thv",
	}
}

func genericStateVolume(workloadID string, index int) string {
	return fmt.Sprintf("%s-state-%d", workloadID, index)
}

func fallbackToolHiveEnv(stateRoot string, extra map[string]string) map[string]string {
	env := map[string]string{
		"HOME": stateRoot, "USERPROFILE": stateRoot, "APPDATA": stateRoot, "LOCALAPPDATA": stateRoot,
		"XDG_CONFIG_HOME": stateRoot, "XDG_DATA_HOME": stateRoot, "XDG_STATE_HOME": stateRoot, "XDG_CACHE_HOME": stateRoot,
		"TOOLHIVE_RUNTIME": "docker", "TOOLHIVE_SKIP_UPDATE_CHECK": "true", "TOOLHIVE_USAGE_METRICS_ENABLED": "false",
		"TOOLHIVE_SECRETS_PROVIDER": "environment",
	}
	for key, value := range extra {
		env[key] = value
	}
	return env
}

func (c *genericController) publishedPort(ctx context.Context, name string) (int, error) {
	body, err := c.command(ctx, "docker", "port", name, "8765/tcp")
	if err != nil {
		return 0, err
	}
	line := strings.TrimSpace(strings.SplitN(string(body), "\n", 2)[0])
	_, port, err := net.SplitHostPort(line)
	if err != nil {
		return 0, fmt.Errorf("%w: relay port mapping", ErrIsolation)
	}
	value, err := strconv.Atoi(port)
	if err != nil || value < 1 || value > 65535 {
		return 0, fmt.Errorf("%w: relay port mapping", ErrIsolation)
	}
	return value, nil
}

func parseToolHiveLogEndpoint(path string) string {
	body, err := os.ReadFile(path) // #nosec G304 -- controller-owned ToolHive state file.
	if err != nil {
		return ""
	}
	for _, line := range strings.Split(string(body), "\n") {
		var entry struct {
			Endpoint string `json:"endpoint"`
			URL      string `json:"url"`
		}
		if json.Unmarshal([]byte(line), &entry) != nil {
			continue
		}
		raw := entry.Endpoint
		if raw == "" {
			raw = entry.URL
		}
		if strings.HasPrefix(raw, "http://localhost:") {
			raw = "http://127.0.0.1:" + strings.TrimPrefix(raw, "http://localhost:")
		}
		if ValidateBackendEndpoint(raw) == nil && strings.HasPrefix(raw, "http://127.0.0.1:") {
			return raw
		}
	}
	return ""
}

func (c *genericController) toolHiveEndpoint(ctx context.Context, stateRoot, name string) (string, error) {
	if endpoint := parseToolHiveLogEndpoint(filepath.Join(stateRoot, "toolhive", "logs", name+".log")); endpoint != "" {
		return endpoint, nil
	}
	body, err := c.commandEnv(ctx, fallbackToolHiveEnv(stateRoot, nil), c.config.ToolHiveBinary, "list", "--all", "--format", "json")
	if err != nil {
		return "", err
	}
	var rows []struct {
		Name string `json:"name"`
		URL  string `json:"url"`
	}
	if json.Unmarshal(body, &rows) != nil {
		return "", fmt.Errorf("%w: ToolHive endpoint missing", ErrIsolation)
	}
	for _, row := range rows {
		if row.Name != name {
			continue
		}
		endpoint := row.URL
		if strings.HasPrefix(endpoint, "http://localhost:") {
			endpoint = "http://127.0.0.1:" + strings.TrimPrefix(endpoint, "http://localhost:")
		}
		if ValidateBackendEndpoint(endpoint) == nil && strings.HasPrefix(endpoint, "http://127.0.0.1:") {
			return endpoint, nil
		}
	}
	return "", fmt.Errorf("%w: ToolHive endpoint missing", ErrIsolation)
}

func (c *genericController) requireLocalLinuxDocker(ctx context.Context) error {
	host, err := c.command(ctx, "docker", "context", "inspect", "--format", "{{(index .Endpoints \"docker\").Host}}")
	if err != nil || (!strings.HasPrefix(strings.TrimSpace(string(host)), "npipe://") && !strings.HasPrefix(strings.TrimSpace(string(host)), "unix://")) {
		return fmt.Errorf("%w: local Docker context required", ErrIsolation)
	}
	osType, err := c.command(ctx, "docker", "info", "--format", "{{.OSType}}")
	if err != nil || strings.TrimSpace(string(osType)) != "linux" {
		return fmt.Errorf("%w: local Linux Docker runtime required", ErrIsolation)
	}
	return nil
}

func (c *genericController) resolveArtifactImage(ctx context.Context, plan controllerPlan) (string, error) {
	pinned := plan.Image + "@" + plan.Digest
	if id, err := c.command(ctx, "docker", "image", "inspect", pinned, "--format", "{{.Id}}"); err == nil && dockerImageIDPattern.MatchString(strings.TrimSpace(string(id))) {
		return pinned, nil
	}
	// docker load tags the local name. The daemon image ID can differ from
	// the OCI manifest digest recorded at review.
	if id, err := c.command(ctx, "docker", "image", "inspect", plan.Image, "--format", "{{.Id}}"); err == nil && dockerImageIDPattern.MatchString(strings.TrimSpace(string(id))) {
		return plan.Image, nil
	}
	if _, err := c.command(ctx, "docker", "image", "inspect", pinned); err == nil {
		return pinned, nil
	}
	return "", fmt.Errorf("%w: reviewed artifact image unavailable", ErrIsolation)
}

func artifactImageMatches(configImage string, plan controllerPlan, resolved string) bool {
	pinned := plan.Image + "@" + plan.Digest
	for _, candidate := range []string{pinned, plan.Image, plan.Image + ":latest", resolved} {
		if candidate != "" && configImage == candidate {
			return true
		}
	}
	return false
}

func (c *genericController) artifactCommand(ctx context.Context, imageRef string) ([]string, error) {
	if c.config.Definition.Source.Command != "" {
		command := append([]string{c.config.Definition.Source.Command}, c.config.Definition.Source.Args...)
		return validateArtifactCommand(command)
	}
	body, err := c.command(ctx, "docker", "image", "inspect", imageRef, "--format", "{{json .Config}}")
	if err != nil {
		return nil, fmt.Errorf("%w: immutable artifact image unavailable", ErrIsolation)
	}
	var config genericImageConfig
	if json.Unmarshal(body, &config) != nil {
		return nil, ErrIsolation
	}
	command := append(append([]string(nil), config.Entrypoint...), config.Cmd...)
	return validateArtifactCommand(command)
}

func validateArtifactCommand(command []string) ([]string, error) {
	if len(command) == 0 || len(command) > 32 || !validCommand(command[0]) {
		return nil, fmt.Errorf("%w: immutable artifact entrypoint required", ErrIsolation)
	}
	for _, arg := range command {
		if len(arg) > 256 || strings.ContainsAny(arg, "\x00\r\n") || strings.Contains(arg, "${") {
			return nil, fmt.Errorf("%w: unsafe artifact entrypoint", ErrIsolation)
		}
	}
	return command, nil
}

func definitionToolNames(tools []ToolSpec) []string {
	names := make([]string, 0, len(tools))
	for _, tool := range tools {
		names = append(names, tool.Name)
	}
	return names
}
