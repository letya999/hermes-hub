package toolhub

import (
	"context"
	"crypto/subtle"
	"encoding/json"
	"errors"
	"fmt"
	"github.com/letya999/hermes-hub/internal/identity"
	"github.com/letya999/hermes-hub/internal/toolhub/api"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

func (c *genericController) handler(token string) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost || r.Header.Get("Origin") != "" || subtle.ConstantTimeCompare([]byte(r.Header.Get("Authorization")), []byte("Bearer "+token)) != 1 {
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}
		if r.URL.Path == "/release" {
			var release api.ControllerRelease
			decoder := json.NewDecoder(http.MaxBytesReader(w, r.Body, 4096))
			decoder.DisallowUnknownFields()
			if decoder.Decode(&release) != nil || !identity.ValidID(release.WorkloadID) {
				http.Error(w, "invalid release", http.StatusForbidden)
				return
			}
			c.mu.Lock()
			if workload, ok := c.workloads[release.WorkloadID]; ok {
				if err := c.removeWorkload(r.Context(), workload, workload.plan.Definition.Workload.Stateful && !release.Purge); err != nil {
					c.mu.Unlock()
					http.Error(w, "workload release failed", http.StatusServiceUnavailable)
					return
				}
				delete(c.workloads, release.WorkloadID)
				c.config.Budget.Release(release.WorkloadID)
			} else if release.Purge {
				if err := c.purgeWorkloadByName(r.Context(), release.WorkloadID); err != nil {
					c.mu.Unlock()
					http.Error(w, "workload purge failed", http.StatusServiceUnavailable)
					return
				}
			}
			c.mu.Unlock()
			w.WriteHeader(http.StatusNoContent)
			return
		}
		if r.URL.Path == "/cli-metrics" {
			w.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(w).Encode(c.cellMetricsSnapshot())
			return
		}
		if r.URL.Path == "/cli-release" {
			var release api.CliReleaseRequest
			decoder := json.NewDecoder(http.MaxBytesReader(w, r.Body, 4096))
			decoder.DisallowUnknownFields()
			if decoder.Decode(&release) != nil {
				http.Error(w, "invalid cli release", http.StatusForbidden)
				return
			}
			c.releaseCells(r.Context(), release)
			w.WriteHeader(http.StatusNoContent)
			return
		}
		if r.URL.Path == "/cli-exec" {
			var request api.CliExecRequest
			decoder := json.NewDecoder(http.MaxBytesReader(w, r.Body, 4<<20))
			decoder.DisallowUnknownFields()
			if decoder.Decode(&request) != nil || decoder.Decode(new(any)) != io.EOF {
				http.Error(w, "invalid cli request", http.StatusForbidden)
				return
			}
			response, err := c.execCLI(r.Context(), request)
			if err != nil {
				status := http.StatusForbidden
				if !errors.Is(err, api.ErrInvalid) && !errors.Is(err, api.ErrUnauthorized) {
					status = http.StatusServiceUnavailable
				}
				http.Error(w, "cli exec denied: "+api.PublicPrepareError(err), status)
				return
			}
			w.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(w).Encode(response)
			return
		}
		if r.URL.Path != "/admit" {
			http.NotFound(w, r)
			return
		}
		var plan api.ControllerPlan
		decoder := json.NewDecoder(http.MaxBytesReader(w, r.Body, 2<<20))
		decoder.DisallowUnknownFields()
		if decoder.Decode(&plan) != nil || decoder.Decode(new(any)) != io.EOF {
			http.Error(w, "invalid plan", http.StatusForbidden)
			return
		}
		if plan.Command != "" {
			// Bounded-cli plans moved to /cli-exec: the controller now
			// creates the sandbox cell rather than attesting this executor.
			http.Error(w, "unapproved plan: bounded-cli executes through /cli-exec", http.StatusForbidden)
			return
		}
		if err := c.validatePlan(plan); err != nil {
			http.Error(w, "unapproved plan: "+api.PublicPrepareError(err), http.StatusForbidden)
			return
		}
		if !c.config.DynamicDefinitions {
			plan.Definition = c.config.Definition
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
			http.Error(w, "workload enforcement unavailable: "+err.Error(), http.StatusServiceUnavailable)
			return
		}
		c.workloads[plan.WorkloadID] = workload
		writeGenericReceipt(w, workload)
	})
}

func writeGenericReceipt(w http.ResponseWriter, workload genericWorkload) {
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(api.AdmissionReceipt{WorkloadID: workload.plan.WorkloadID, State: "running", Enforced: true, Endpoint: workload.endpoint, ImageDigest: workload.plan.Digest, SidecarImages: workload.plan.SidecarImages, Execution: workload.plan.Execution})
}

func (c *genericController) start(ctx context.Context, plan api.ControllerPlan) (genericWorkload, error) {
	if _, err := c.toolhive(ctx, "version"); err != nil {
		return genericWorkload{}, err
	}
	if err := c.validateCredentialMounts(plan.CredentialMounts); err != nil {
		return genericWorkload{}, err
	}
	if len(plan.Execution.Mounts) != 0 && !c.config.DockerFallback {
		return genericWorkload{}, fmt.Errorf("%w: generic host state mounts are not enabled", api.ErrIsolation)
	}
	if len(plan.Execution.TCPForwards) != 0 && !c.config.DockerFallback {
		return genericWorkload{}, fmt.Errorf("%w: tcp forwards require the Docker fallback companion", api.ErrIsolation)
	}
	help, err := c.toolhive(ctx, "run", "--help")
	if err != nil {
		return genericWorkload{}, fmt.Errorf("%w: ToolHive cannot express the required create-time profile", api.ErrIsolation)
	}
	if !toolHiveSupportsRuntime(string(help)) {
		if !c.config.DockerFallback {
			return genericWorkload{}, fmt.Errorf("%w: ToolHive cannot express the required create-time profile", api.ErrIsolation)
		}
		return c.startDockerRemoteFallback(ctx, plan)
	}
	if plan.Execution.ShmSizeMiB > 0 && !strings.Contains(string(help), "--shm-size") {
		if !c.config.DockerFallback {
			return genericWorkload{}, fmt.Errorf("%w: ToolHive cannot express the workload shm-size budget", api.ErrIsolation)
		}
		return c.startDockerRemoteFallback(ctx, plan)
	}
	if len(plan.CredentialMounts) > 0 && !strings.Contains(string(help), "--volume") {
		if !c.config.DockerFallback {
			return genericWorkload{}, fmt.Errorf("%w: ToolHive cannot express credential mounts", api.ErrIsolation)
		}
		return c.startDockerRemoteFallback(ctx, plan)
	}
	port, err := reserveLoopbackPort()
	if err != nil {
		return genericWorkload{}, err
	}
	network := "hermes-" + plan.WorkloadID
	proxyName := plan.WorkloadID + "-egress"
	proxyVol := proxyName + "-config"
	credName := ""
	started := false
	defer func() {
		if started {
			return
		}
		_, _ = c.toolhive(ctx, "rm", plan.WorkloadID)
		rmArgs := []string{"rm", "--force", plan.WorkloadID, proxyName}
		if credName != "" {
			rmArgs = append(rmArgs, credName)
		}
		_, _ = c.docker(ctx, rmArgs...)
		_, _ = c.docker(ctx, "network", "rm", network)
		_, _ = c.docker(ctx, "volume", "rm", proxyVol)
	}()
	if _, err := c.docker(ctx, append([]string{"network", "create", "--internal", "--label", "hermes-hub.role=generic-mcp"}, append(c.planLabelArgs(plan), network)...)...); err != nil {
		return genericWorkload{}, err
	}
	if _, err := c.docker(ctx, append([]string{"volume", "create", "--label", "hermes-hub.role=generic-mcp"}, append(c.planLabelArgs(plan), proxyVol)...)...); err != nil {
		return genericWorkload{}, err
	}
	proxyConfig, err := api.ProxyConfigFor(plan)
	if err != nil {
		return genericWorkload{}, err
	}
	// docker cp preserves the source mode; the allowlist holds no secrets.
	fileName, err := writeWorldReadableTemp(c.config.StateRoot, ".proxy-*", proxyConfig)
	if err != nil {
		return genericWorkload{}, err
	}
	defer os.Remove(fileName)
	limits := []string{"--cpus", strconv.FormatFloat(float64(plan.Execution.CPUMillis)/1000, 'f', 3, 64), "--memory", strconv.Itoa(plan.Execution.MemoryMiB) + "m", "--memory-swap", strconv.Itoa(plan.Execution.MemoryMiB) + "m", "--pids-limit", strconv.Itoa(plan.Execution.MaxPIDs)}
	args := append([]string{"create", "--name", proxyName, "--network", network, "--read-only", "--user", "31:31", "--cap-drop", "ALL", "--security-opt", "no-new-privileges=true", "--security-opt", "seccomp=" + c.config.SeccompProfile, "--tmpfs", "/tmp:rw,nosuid,nodev,size=64m", "--mount", "type=volume,source=" + proxyVol + ",target=/etc/squid"}, c.planLabelArgs(plan)...)
	args = append(append(args, limits...), plan.SidecarImages[0])
	if _, err := c.docker(ctx, args...); err != nil {
		return genericWorkload{}, err
	}
	if _, err := c.docker(ctx, "cp", fileName, proxyName+":/etc/squid/squid.conf"); err != nil {
		return genericWorkload{}, err
	}
	if _, err := c.docker(ctx, "network", "connect", "bridge", proxyName); err != nil {
		return genericWorkload{}, err
	}
	if _, err := c.docker(ctx, "start", proxyName); err != nil {
		return genericWorkload{}, err
	}
	if err := c.waitProxyListen(ctx, proxyName); err != nil {
		return genericWorkload{}, err
	}
	ip, err := c.proxyIP(ctx, proxyName, network)
	if err != nil {
		return genericWorkload{}, err
	}
	// Proxy-delivered credentials need their sidecar: the workload keeps the
	// hub-managed stub while the cred-proxy injects the real header toward
	// the grant's declared hosts. No sidecar, no admission — fail closed.
	if len(plan.ProxyGrants) > 0 {
		if c.config.CLI == nil {
			return genericWorkload{}, fmt.Errorf("%w: proxy credential delivery requires the cred-proxy toolchain", api.ErrIsolation)
		}
		if info, statErr := os.Stat(c.cellInitHost()); statErr != nil || !info.Mode().IsRegular() {
			return genericWorkload{}, fmt.Errorf("%w: cred-proxy helper is not seeded", api.ErrIsolation)
		}
		toolsImage, err := c.resolveCellImage(ctx, c.config.CLI.ToolsImage)
		if err != nil {
			return genericWorkload{}, err
		}
		var brokered []api.CliBrokeredCred
		for _, grant := range plan.ProxyGrants {
			for _, host := range grant.Hosts {
				brokered = append(brokered, api.CliBrokeredCred{EnvName: grant.EnvName, Host: host, Prefix: grant.Prefix, Value: grant.Value})
			}
		}
		sidecar, err := c.cellCredProxy(ctx, plan.WorkloadID, network, brokered, toolsImage, c.planLabelArgs(plan))
		if err != nil {
			return genericWorkload{}, err
		}
		credName = sidecar.name
		if err := c.waitProxyListenPort(ctx, credName, ":0C39"); err != nil {
			return genericWorkload{}, err
		}
	}
	toolArgs := []string{"run", "--name", plan.WorkloadID, "--host", "127.0.0.1", "--proxy-port", strconv.Itoa(port), "--transport", "stdio", "--proxy-mode", "streamable-http", "--permission-profile", "none", "--network", network, "--isolate-network=false", "--read-only", "--security-opt", "seccomp=" + c.config.SeccompProfile, "--security-opt", "no-new-privileges=true", "--cap-drop", "ALL", "--user", "10001:10001", "--cpus", strconv.FormatFloat(float64(plan.Execution.CPUMillis)/1000, 'f', 3, 64), "--memory", strconv.Itoa(plan.Execution.MemoryMiB) + "m", "--memory-swap", strconv.Itoa(plan.Execution.MemoryMiB) + "m", "--pids-limit", strconv.Itoa(plan.Execution.MaxPIDs), "--timeout", strconv.Itoa(plan.Execution.TimeoutSeconds), "--output-limit", strconv.Itoa(plan.Execution.OutputBytes), "--env", "HTTP_PROXY=http://" + ip + ":3128", "--env", "HTTPS_PROXY=http://" + ip + ":3128"}
	if credName != "" {
		toolArgs = append(toolArgs, "--env", "NO_PROXY=127.0.0.1,"+credName, "--env", "no_proxy=127.0.0.1,"+credName)
	}
	if plan.Execution.ShmSizeMiB > 0 {
		toolArgs = append(toolArgs, "--shm-size", strconv.Itoa(plan.Execution.ShmSizeMiB)+"m")
	}
	var secretValues map[string]string
	if plan.WorkspacePath != "" {
		secretPath := filepath.Join(plan.WorkspacePath, "credentials.env")
		if _, statErr := os.Lstat(secretPath); statErr == nil {
			secretValues, err = api.ReadGenericSecrets(secretPath, plan.Definition)
			if err != nil {
				return genericWorkload{}, err
			}
			for _, input := range plan.Definition.Credentials {
				if secretValues[input.Name] != "" || (input.Delivery != "file" && input.Delivery != "json") {
					continue
				}
				staged, rerr := os.ReadFile(filepath.Join(plan.WorkspacePath, input.Name+".secret"))
				if rerr != nil {
					continue
				}
				value := string(staged)
				if input.Delivery == "file" && value != input.Target {
					// Raw file content needs a bind mount this path cannot
					// express; only the broker lease path may pass as env.
					return genericWorkload{}, fmt.Errorf("%w: file credential %s requires a lease mount", api.ErrIsolation, input.Name)
				}
				secretValues[input.Name] = value
			}
		} else if !os.IsNotExist(statErr) {
			return genericWorkload{}, api.ErrIsolation
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
	for _, tool := range plan.Definition.Tools {
		toolArgs = append(toolArgs, "--tools", tool.Name)
	}
	for _, mount := range plan.CredentialMounts {
		mode := ":ro"
		if !mount.ReadOnly {
			mode = ":rw"
		}
		toolArgs = append(toolArgs, "--volume", api.DockerBindSource(mount.Source)+":"+mount.Target+mode)
	}
	toolArgs = append(toolArgs, proxyGrantEnvArgs(plan.Definition.RuntimeEnvironment, credName, plan.ProxyGrants)...)
	toolArgs = append(toolArgs, api.ProxyEnvironmentArgs(plan.Definition, "http://"+ip+":3128")...)
	imageRef, err := c.resolveArtifactImage(ctx, plan)
	if err != nil {
		return genericWorkload{}, err
	}
	toolArgs = append(toolArgs, imageRef)
	if _, err := c.commandEnv(ctx, commandEnv, c.config.ToolHiveBinary, toolArgs...); err != nil {
		return genericWorkload{}, err
	}
	// ToolHive cannot stamp hub labels; the spawned name registers as owned
	// so later scope-checked inspect/rm see it as ours.
	if c.scope != nil {
		c.scope.Register(plan.WorkloadID)
	}
	if err := api.WaitLocalWorkload(ctx, func() error {
		_, err := c.docker(ctx, "inspect", plan.WorkloadID)
		return err
	}); err != nil {
		return genericWorkload{}, err
	}
	workload := genericWorkload{plan: plan, endpoint: "http://127.0.0.1:" + strconv.Itoa(port) + DefaultEndpointPath, proxyName: proxyName, proxyVol: proxyVol, credName: credName, imageRef: imageRef}
	if _, err := c.inspect(ctx, workload); err != nil {
		return genericWorkload{}, err
	}
	started = true
	return workload, nil
}

// proxyGrantEnvArgs renders the declared runtime env; when a cred sidecar is
// wired, an env value that is a URL at a grant host is rewritten onto the
// sidecar's host-routing endpoint (http://<wl>-cred:3129/<host><path>) so the
// workload reaches that API only through the credential-injecting proxy.
func proxyGrantEnvArgs(environment map[string]string, credName string, grants []api.ProxyGrant) []string {
	if credName == "" {
		return api.RuntimeEnvironmentArgs(api.ToolDefinition{RuntimeEnvironment: environment})
	}
	hosts := map[string]bool{}
	for _, grant := range grants {
		for _, host := range grant.Hosts {
			hosts[strings.ToLower(host)] = true
		}
	}
	rewritten := make(map[string]string, len(environment))
	for name, value := range environment {
		if parsed, err := url.Parse(value); err == nil && hosts[strings.ToLower(parsed.Hostname())] {
			value = "http://" + credName + ":" + strconv.Itoa(credProxyPort) + "/" + parsed.Hostname() + parsed.EscapedPath()
			if parsed.RawQuery != "" {
				value += "?" + parsed.RawQuery
			}
		}
		rewritten[name] = value
	}
	return api.RuntimeEnvironmentArgs(api.ToolDefinition{RuntimeEnvironment: rewritten})
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

// proxyListenTimeout bounds the post-start wait for squid to bind its port;
// tests shrink it to exercise the failure path.
var proxyListenTimeout = 10 * time.Second

// waitProxyListen holds callers until squid has bound its port: docker start
// returns before the daemon finishes its init, and a workload that connects in
// that window sees connection refused instead of a proxy. The probe runs inside
// the proxy container because the internal cell network is unreachable from the
// controller; procfs needs no extra tools in the slim image.
func (c *genericController) waitProxyListen(ctx context.Context, name string) error {
	return c.waitProxyListenPort(ctx, name, ":0C38")
}

// waitProxyListenPort is the port-parameterized wait: 3128 = 0x0C38, the
// cred-proxy's 3129 = 0x0C39 in the procfs local_address column.
func (c *genericController) waitProxyListenPort(ctx context.Context, name, portHex string) error {
	deadline := time.Now().Add(proxyListenTimeout)
	for {
		if _, err := c.docker(ctx, "exec", name, "grep", "-q", portHex, "/proc/net/tcp", "/proc/net/tcp6"); err == nil {
			return nil
		}
		if time.Now().After(deadline) {
			return fmt.Errorf("%w: egress proxy not listening", api.ErrIsolation)
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(100 * time.Millisecond):
		}
	}
}

func (c *genericController) proxyIP(ctx context.Context, name, network string) (string, error) {
	body, err := c.docker(ctx, "inspect", name, "--format", "{{json .NetworkSettings.Networks}}")
	if err != nil {
		return "", err
	}
	var networks map[string]struct{ IPAddress string }
	if json.Unmarshal(body, &networks) != nil {
		return "", api.ErrIsolation
	}
	ip := strings.TrimSpace(networks[network].IPAddress)
	parsed := net.ParseIP(ip)
	if parsed == nil || !parsed.IsPrivate() {
		return "", api.ErrIsolation
	}
	return ip, nil
}
