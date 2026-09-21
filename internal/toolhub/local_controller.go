package toolhub

import (
	"bytes"
	"context"
	"crypto/subtle"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/netip"
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

// LocalControllerConfig approves one read-only account workload, never arbitrary
// Docker commands. It contains paths and image pins, not account credentials.
type LocalControllerConfig struct {
	User           string         `json:"user"`
	Context        string         `json:"context"`
	Connection     string         `json:"connection"`
	StateRoot      string         `json:"state_root"`
	ToolHiveBinary string         `json:"toolhive_binary"`
	MCPPort        int            `json:"mcp_port"`
	Definition     ToolDefinition `json:"definition"`
}

type localController struct {
	config      LocalControllerConfig
	plan        controllerPlan
	proxyConfig string
	command     func(context.Context, string, ...string) ([]byte, error)
	mu          sync.Mutex // ponytail: one approved account; use per-workload locks for multiple accounts.
}

func newLocalController(config LocalControllerConfig) (*localController, error) {
	if !identity.ValidID(config.User) || !identity.ValidID(config.Context) || !identity.ValidID(config.Connection) || !filepath.IsAbs(config.StateRoot) || !filepath.IsAbs(config.ToolHiveBinary) || config.MCPPort < 1024 || config.MCPPort > 65535 {
		return nil, fmt.Errorf("invalid local controller identity, paths or port")
	}
	config.StateRoot, config.ToolHiveBinary = filepath.Clean(config.StateRoot), filepath.Clean(config.ToolHiveBinary)
	d, err := TelegramDefinition(config.Definition, false)
	if err != nil || len(d.Workload.SidecarImages) != 1 || d.Workload.ToolHiveVersion != "v0.48.0" || !reflect.DeepEqual(d.Execution.Mounts, []Mount{{Source: "connection-state", Target: "/run/connector"}}) {
		return nil, fmt.Errorf("local controller requires the reviewed read-only Telegram plan")
	}
	proxy, err := telegramProxyConfig(d.Execution.Egress)
	if err != nil {
		return nil, err
	}
	e := EffectiveBinding{Definition: d, Binding: ToolBinding{PrincipalID: config.User, ContextID: config.Context}, Connection: &Connection{ConnectionID: config.Connection}}
	workspace, err := OpenWorkloadWorkspace(config.StateRoot, e, "")
	if err != nil {
		return nil, err
	}
	return &localController{config: config, proxyConfig: proxy, command: localCommand,
		plan: controllerPlan{WorkloadID: WorkloadInstanceID(d.DefinitionID, PerUser, config.Context+":"+config.User+":"+config.Connection, ""), DefinitionID: d.DefinitionID, DefinitionVersion: d.Version, Image: d.Source.Image, Digest: d.Source.Digest, ToolHiveVersion: d.Workload.ToolHiveVersion, SidecarImages: d.Workload.SidecarImages, Execution: d.Execution, WorkspacePath: workspace.Path}}, nil
}

// Published by Telegram at https://core.telegram.org/resources/cidr.txt.
// IPv6 is deliberately disabled in this local deployment.
var telegramIPv4 = []string{"91.108.56.0/22", "91.108.4.0/22", "91.108.8.0/22", "91.108.16.0/22", "91.108.12.0/22", "149.154.160.0/20", "91.105.192.0/23", "91.108.20.0/22", "185.76.151.0/24"}

func telegramProxyConfig(egress []string) (string, error) {
	if !reflect.DeepEqual(egress, telegramIPv4) {
		return "", fmt.Errorf("telegram egress must match the reviewed published IPv4 ranges")
	}
	return "http_port 3128\nacl connect method CONNECT\nacl tlsport port 443\nacl telegram dst " + strings.Join(egress, " ") + "\nhttp_access allow connect tlsport telegram\nhttp_access deny all\ncache deny all\naccess_log none\ncache_log /dev/stderr\npid_filename /tmp/squid.pid\n", nil
}

func localCommand(ctx context.Context, binary string, args ...string) ([]byte, error) {
	cmd := exec.CommandContext(ctx, binary, args...) // #nosec G204 -- binary and arguments originate only in the trusted fixed operator plan.
	var output bytes.Buffer
	cmd.Stdout = &output
	cmd.Stderr = io.Discard
	if err := cmd.Run(); err != nil {
		operation := "command"
		if len(args) > 0 {
			operation = args[0]
		}
		return nil, fmt.Errorf("local workload %s %s failed: %w", filepath.Base(binary), operation, err)
	}
	return output.Bytes(), nil
}

func waitLocalWorkload(ctx context.Context, check func() error) error {
	for {
		if err := check(); err == nil {
			return nil
		}
		timer := time.NewTimer(100 * time.Millisecond)
		select {
		case <-ctx.Done():
			timer.Stop()
			return ctx.Err()
		case <-timer.C:
		}
	}
}

func (c *localController) handler(token string) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost || r.URL.Path != "/admit" {
			http.NotFound(w, r)
			return
		}
		if r.Header.Get("Origin") != "" || subtle.ConstantTimeCompare([]byte(r.Header.Get("Authorization")), []byte("Bearer "+token)) != 1 {
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}
		var plan controllerPlan
		decoder := json.NewDecoder(http.MaxBytesReader(w, r.Body, 65536))
		decoder.DisallowUnknownFields()
		if decoder.Decode(&plan) != nil || decoder.Decode(new(any)) != io.EOF || !reflect.DeepEqual(plan, c.plan) {
			http.Error(w, "unapproved plan", http.StatusForbidden)
			return
		}
		c.mu.Lock()
		defer c.mu.Unlock()
		receipt, err := c.inspect(r.Context())
		if err != nil {
			http.Error(w, "workload enforcement unavailable", http.StatusServiceUnavailable)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(receipt)
	})
}

type localContainer struct {
	Image      string
	State      struct{ Running bool }
	Config     struct{ User string }
	HostConfig struct {
		NanoCpus   int64
		Memory     int64
		PidsLimit  int
		Privileged bool
	}
	Mounts []struct {
		Type, Source, Destination string
		RW                        bool
	}
	NetworkSettings struct {
		Networks map[string]struct{ IPAddress string }
	}
}

func (c *localController) containers(ctx context.Context) ([]localContainer, error) {
	body, err := c.command(ctx, "docker", "inspect", c.plan.WorkloadID, c.plan.WorkloadID+"-proxy")
	if err != nil {
		return nil, err
	}
	var containers []localContainer
	if json.Unmarshal(body, &containers) != nil || len(containers) != 2 {
		return nil, fmt.Errorf("missing workload containers")
	}
	return containers, nil
}

func (c *localController) inspect(ctx context.Context) (AdmissionReceipt, error) {
	containers, err := c.containers(ctx)
	if err != nil {
		return AdmissionReceipt{}, err
	}
	network := "hermes-" + c.plan.WorkloadID
	for i, container := range containers {
		if !container.State.Running || container.HostConfig.Privileged || container.HostConfig.NanoCpus != int64(c.plan.Execution.CPUMillis)*1000000 || container.HostConfig.Memory != int64(c.plan.Execution.MemoryMiB)*1048576 || container.HostConfig.PidsLimit != c.plan.Execution.MaxPIDs {
			return AdmissionReceipt{}, fmt.Errorf("unenforced workload limits")
		}
		if _, ok := container.NetworkSettings.Networks[network]; !ok || (i == 0 && len(container.NetworkSettings.Networks) != 1) || (i == 1 && len(container.NetworkSettings.Networks) != 2) {
			return AdmissionReceipt{}, fmt.Errorf("unenforced private network")
		}
	}
	if containers[0].Image != c.plan.Digest || containers[0].Config.User != "10001:10001" {
		return AdmissionReceipt{}, fmt.Errorf("unapproved workload image or owner")
	}
	// Remote OCI digest may identify an index, so resolve its actual local image ID.
	sidecarID, err := c.command(ctx, "docker", "image", "inspect", c.plan.SidecarImages[0], "--format", "{{.Id}}")
	if err != nil || strings.TrimSpace(string(sidecarID)) != containers[1].Image {
		return AdmissionReceipt{}, fmt.Errorf("unapproved proxy image")
	}
	expected := []string{c.plan.WorkspacePath, filepath.Join(c.config.StateRoot, "telegram-proxy.conf")}
	for i, target := range []string{"/run/connector", "/etc/squid/squid.conf"} {
		binds := 0
		for _, mount := range containers[i].Mounts {
			if mount.Type != "bind" {
				continue
			}
			binds++
			if filepath.Clean(mount.Source) != filepath.Clean(expected[i]) || mount.Destination != target || mount.RW != (i == 0) {
				return AdmissionReceipt{}, fmt.Errorf("unapproved mount")
			}
		}
		if binds != 1 {
			return AdmissionReceipt{}, fmt.Errorf("missing or extra bind")
		}
	}
	body, err := os.ReadFile(expected[1]) // #nosec G304 -- operator-owned fixed state path, checked below for symlinks.
	if err != nil || string(body) != c.proxyConfig || noSymlinkPath(expected[1]) != nil {
		return AdmissionReceipt{}, fmt.Errorf("unapproved egress configuration")
	}
	var networks []struct{ Internal bool }
	body, err = c.command(ctx, "docker", "network", "inspect", network)
	if err != nil || json.Unmarshal(body, &networks) != nil || len(networks) != 1 || !networks[0].Internal {
		return AdmissionReceipt{}, fmt.Errorf("network is not isolated")
	}
	return AdmissionReceipt{WorkloadID: c.plan.WorkloadID, State: "running", Enforced: true, ImageDigest: c.plan.Digest, SidecarImages: c.plan.SidecarImages, Execution: c.plan.Execution}, nil
}

func (c *localController) start(ctx context.Context) error {
	if _, err := c.inspect(ctx); err == nil {
		return nil
	}
	// Existing broken containers are not silently replaced or reconfigured.
	if _, err := c.command(ctx, "docker", "inspect", c.plan.WorkloadID); err == nil {
		return fmt.Errorf("existing workload failed enforcement; stop and repair it explicitly")
	}
	version, err := c.command(ctx, c.config.ToolHiveBinary, "version")
	if err != nil || !strings.Contains(string(version), "ToolHive v0.48.0") {
		return fmt.Errorf("ToolHive version mismatch")
	}
	proxyFile := filepath.Join(c.config.StateRoot, "telegram-proxy.conf")
	if noSymlinkPath(c.config.StateRoot) != nil {
		return ErrIsolation
	}
	f, err := os.OpenFile(proxyFile, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0600)
	if os.IsExist(err) {
		existing, readErr := os.ReadFile(proxyFile)
		if readErr != nil || string(existing) != c.proxyConfig {
			return ErrIsolation
		}
	} else if err != nil {
		return err
	} else {
		_, writeErr := io.WriteString(f, c.proxyConfig)
		closeErr := f.Close()
		if writeErr != nil {
			return writeErr
		}
		if closeErr != nil {
			return closeErr
		}
	}
	network := "hermes-" + c.plan.WorkloadID
	if _, err := c.command(ctx, "docker", "network", "create", "--internal", "--label", "hermes-hub.connector="+c.config.Connection, network); err != nil {
		return err
	}
	limits := []string{"--cpus", strconv.FormatFloat(float64(c.plan.Execution.CPUMillis)/1000, 'f', 3, 64), "--memory", strconv.Itoa(c.plan.Execution.MemoryMiB) + "m", "--memory-swap", strconv.Itoa(c.plan.Execution.MemoryMiB) + "m", "--pids-limit", strconv.Itoa(c.plan.Execution.MaxPIDs)}
	args := append([]string{"create", "--name", c.plan.WorkloadID + "-proxy", "--network", network, "--read-only", "--tmpfs", "/tmp:rw,nosuid,nodev,size=64m", "--tmpfs", "/run:rw,nosuid,nodev,uid=31,gid=31,mode=0700,size=16m", "--mount", "type=bind,source=" + dockerBindSource(proxyFile) + ",target=/etc/squid/squid.conf,readonly"}, limits...)
	args = append(args, c.plan.SidecarImages[0])
	if _, err := c.command(ctx, "docker", args...); err != nil {
		return err
	}
	if _, err := c.command(ctx, "docker", "network", "connect", "bridge", c.plan.WorkloadID+"-proxy"); err != nil {
		return err
	}
	if _, err := c.command(ctx, "docker", "start", c.plan.WorkloadID+"-proxy"); err != nil {
		return err
	}
	body, err := c.command(ctx, "docker", "inspect", c.plan.WorkloadID+"-proxy", "--format", "{{json .NetworkSettings.Networks}}")
	var addresses map[string]struct{ IPAddress string }
	if err != nil || json.Unmarshal(body, &addresses) != nil {
		return fmt.Errorf("proxy address unavailable")
	}
	ip, err := netip.ParseAddr(addresses[network].IPAddress)
	if err != nil || !ip.Is4() || !ip.IsPrivate() {
		return fmt.Errorf("invalid proxy address")
	}
	if _, err := c.command(ctx, c.config.ToolHiveBinary, "run", "--name", c.plan.WorkloadID, "--host", "127.0.0.1", "--proxy-port", strconv.Itoa(c.config.MCPPort), "--transport", "stdio", "--proxy-mode", "streamable-http", "--permission-profile", "none", "--network", network, "--isolate-network=false", "--env", "HTTP_PROXY=http://"+ip.String()+":3128", "--volume", dockerBindSource(c.plan.WorkspacePath)+":/run/connector", "--tools", "get_account", "--tools", "list_dialogs", "--tools", "get_messages", "--tools", "search_messages", "--tools", "get_chat_info", c.plan.Digest); err != nil {
		return err
	}
	// ToolHive run returns before its background runner creates the container.
	readyCtx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	if err := waitLocalWorkload(readyCtx, func() error { _, err := c.command(readyCtx, "docker", "inspect", c.plan.WorkloadID); return err }); err != nil {
		return err
	}
	if _, err := c.command(ctx, "docker", append([]string{"update"}, append(limits, c.plan.WorkloadID)...)...); err != nil {
		return err
	}
	return waitLocalWorkload(readyCtx, func() error { _, err := c.inspect(readyCtx); return err })
}

// RunLocalController starts a single opt-in workload and a loopback-only
// enforcement endpoint. It never performs Telegram login or reads account data.
func RunLocalController(ctx context.Context, config LocalControllerConfig, listen, token string) error {
	host, _, err := net.SplitHostPort(listen)
	if err != nil || host != "127.0.0.1" || len(token) < 32 || strings.ContainsAny(token, "\r\n") {
		return fmt.Errorf("loopback address and private token required")
	}
	c, err := newLocalController(config)
	if err != nil {
		return err
	}
	listener, err := net.Listen("tcp", listen)
	if err != nil {
		return err
	}
	defer listener.Close()
	if err := c.start(ctx); err != nil {
		return err
	}
	server := &http.Server{Handler: c.handler(token), ReadHeaderTimeout: 5 * time.Second, IdleTimeout: time.Minute}
	go func() {
		<-ctx.Done()
		shutdown, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_ = server.Shutdown(shutdown)
	}()
	err = server.Serve(listener)
	if err == http.ErrServerClosed {
		return nil
	}
	return err
}
