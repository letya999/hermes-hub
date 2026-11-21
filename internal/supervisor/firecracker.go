package supervisor

import (
	"context"
	"errors"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"

	"github.com/letya999/hermes-hub/internal/stack"
	"github.com/letya999/hermes-hub/internal/uno"
)

// firecrackerBackend runs spaces inside Firecracker microVMs through an
// Uno-compatible control plane (mini-Uno locally, external Uno on bare
// metal). The guest has no NIC; relays stay on the host docker topology and
// are reachable through the agent's host-side egress allowlist.
type firecrackerBackend struct {
	m      *Manager
	client *uno.Client
}

func newFirecrackerBackend(m *Manager) (ComputeBackend, error) {
	cfg := m.cfg
	if cfg.UnoURL == "" || cfg.UnoToken == "" {
		return nil, errors.New("firecracker backend requires HUB_UNO_URL and HUB_UNO_TOKEN")
	}
	if cfg.UnoImage == "" {
		return nil, errors.New("firecracker backend requires HUB_UNO_IMAGE")
	}
	return &firecrackerBackend{m: m, client: uno.NewClient(cfg.UnoURL, cfg.UnoToken, cfg.HTTP)}, nil
}

// fcRelayPorts maps relay hostnames to their well-known service ports.
var fcRelayPorts = map[string]int{"model-relay": 8318, "toolhub": 8090, "toolhub-relay": 8090, "egress-relay": 8319}

// fcGuestEnv mirrors the managed docker env contract: the two env files
// (managed-runtime.<env>.env + runtime.auth), the RuntimeService environment
// map and the same identity/auth vars runArgs appends. Host paths under the
// space dir are rewritten to the /persist share; paths escaping the space
// have no guest counterpart and are rejected by the caller.
func fcGuestEnv(m *Manager, binding Binding, rt Runtime, port int, settings stack.Settings, envName string) (map[string]string, error) {
	values := map[string]string{}
	load := func(path string) error {
		raw, err := os.ReadFile(path) // #nosec G703 -- paths are the validated binding context root / env file, not request input
		if err != nil {
			return err
		}
		for _, line := range strings.Split(string(raw), "\n") {
			line = strings.TrimSpace(line)
			if line == "" || strings.HasPrefix(line, "#") || !strings.Contains(line, "=") {
				continue
			}
			key, val, _ := strings.Cut(line, "=")
			values[key] = val
		}
		return nil
	}
	if err := load(filepath.Join(binding.ContextRoot, "managed-runtime."+envName+".env")); err != nil {
		return nil, fmt.Errorf("managed env file: %w", err)
	}
	if err := load(binding.EnvFile); err != nil {
		return nil, fmt.Errorf("runtime env file: %w", err)
	}
	if service := stack.RuntimeService(settings, "", binding.ContextRoot); service != nil {
		if envMap, ok := service["environment"].(stack.M); ok {
			for key, val := range envMap {
				values[key] = fmt.Sprint(val)
			}
		}
	}
	for key, value := range map[string]string{
		"HUB_RUNTIME_LISTEN":     "127.0.0.1:" + strconv.Itoa(port),
		"HUB_RUNTIME_AUTH":       binding.runtimeAuth,
		"API_SERVER_KEY":         binding.runtimeAuth,
		"HUB_STATE":              "/state",
		"HUB_WORKSPACE":          "/workspace",
		"HERMES_HOME":            "/state/hermes",
		"HOME":                   "/state/home",
		"HUB_USER_ID":            binding.UserID,
		"HUB_PRINCIPAL_ID":       binding.PrincipalID,
		"HUB_CONTEXT_ID":         binding.ContextID,
		"HUB_RUNTIME_ID":         binding.RuntimeID,
		"HUB_ORGANIZATION_ID":    binding.OrganizationID,
		"HUB_POLICY_VERSION":     binding.PolicyVersion,
		"HUB_RUNTIME_GENERATION": rt.Generation,
		"API_SERVER_ENABLED":     "true",
		"API_SERVER_HOST":        "127.0.0.1",
		"API_SERVER_PORT":        "8642",
	} {
		values[key] = value
	}
	if values["PATH"] == "" {
		values["PATH"] = "/opt/hermes/.venv/bin:/usr/local/bin:/usr/local/sbin:/usr/sbin:/usr/bin:/sbin:/bin"
	}
	root := strings.TrimRight(filepath.ToSlash(binding.ContextRoot), "/")
	for k, v := range values {
		sv := filepath.ToSlash(v)
		if sv == root {
			values[k] = "/persist"
		} else if strings.HasPrefix(sv, root+"/") {
			values[k] = "/persist" + strings.TrimPrefix(sv, root)
		}
	}
	return values, nil
}

// fcMount is one bind inside the guest: src lives under the space share
// (/persist maps the host space dir), dst matches the docker contract.
type fcMount struct {
	dst, src string
	ro       bool
}

// fcGuestMounts reproduces the managed-runtime filesystem contract (now
// built spawn-side, S7c) as guest bind-mounts. Host sources must stay under
// the space dir — a mount that escapes the share has no guest representation
// and fails the spawn.
func (m *Manager) fcGuestMounts(binding Binding, settings stack.Settings, envName, policyPath, effectiveConfig string) ([]fcMount, error) {
	dir, managedDir := binding.ContextRoot, filepath.Join(binding.ContextRoot, "managed", envName)
	mounts := []fcMount{
		{"/state", filepath.Join(managedDir, "runtime"), false},
		{"/state/hermes", stack.RuntimeHermesHome(dir, settings), false},
		{"/state/home", filepath.Join(managedDir, "home"), false},
		{"/state/cache", filepath.Join(managedDir, "cache"), false},
		{"/state/hermes/config.yaml", effectiveConfig, true},
		{"/run/model-config/models.yaml", filepath.Join(dir, "generated", "model-registry", envName, "models.yaml"), true},
		{"/run/model-state", filepath.Join(managedDir, "model-relay"), false},
	}
	if soul := stack.EffectiveSoulPath(dir, settings); fileExists(soul) {
		mounts = append(mounts, fcMount{"/state/hermes/SOUL.md", soul, true})
	}
	if settings.PassportWorkspace != nil {
		for _, pm := range stack.MountPlan(dir, envName, managedDir, settings, *settings.PassportWorkspace) {
			mounts = append(mounts, fcMount{pm.Target, pm.Source, pm.ReadOnly})
		}
	} else {
		mounts = append(mounts,
			fcMount{"/config", filepath.Join(dir, "generated", "config-view", envName), true},
			fcMount{"/workspace", filepath.Join(managedDir, "workspace"), false},
			fcMount{"/config/tool-policy.json", policyPath, true},
		)
		if manifest := filepath.Join(dir, "generated", "tool-manifest."+envName+".json"); fileExists(manifest) {
			mounts = append(mounts, fcMount{"/config/tool-manifest.json", manifest, true})
		}
	}
	if settings.Has("hosting") {
		mounts = append(mounts, fcMount{"/run/hosting.key", filepath.Join(dir, "runtime", "hosting."+envName+".key"), true})
	}
	for _, c := range stack.RuntimeConnections(settings, dir) {
		mounts = append(mounts, fcMount{c.Target, c.Source, true})
	}
	if binding.OrganizationRoot != "" {
		mounts = append(mounts, fcMount{"/org", binding.OrganizationRoot, true})
	}
	root := strings.TrimRight(filepath.ToSlash(dir), "/")
	for i, mt := range mounts {
		src := filepath.ToSlash(mt.src)
		if src == root {
			mounts[i].src = "/persist"
		} else if strings.HasPrefix(src, root+"/") {
			mounts[i].src = "/persist" + strings.TrimPrefix(src, root)
		} else {
			return nil, fmt.Errorf("mount %s source %s escapes the space share", mt.dst, mt.src)
		}
		if !fcShellSafe(mounts[i].src) || !fcShellSafe(mt.dst) {
			return nil, fmt.Errorf("mount %s path %s is not shell-safe", mt.dst, mounts[i].src)
		}
	}
	return mounts, nil
}

// fcShellSafe rejects paths that would break the boot script's single-quote
// quoting or inject shell structure.
func fcShellSafe(path string) bool {
	return path != "" && !strings.ContainsAny(path, "'\n")
}

// fcRelayProxies turns sandboxRelayHosts' --add-host pairs into Uno proxy
// specs. The mux dialed by mini-Uno runs on the control-plane host; relay
// endpoints are published on proxyHost (loopback locally, a routable
// address for external Uno fleets).
func fcRelayProxies(hostArgs []string, proxyHost string) ([]uno.Proxy, []string) {
	var proxies []uno.Proxy
	var ports []string
	for i := 0; i+1 < len(hostArgs); i++ {
		if hostArgs[i] != "--add-host" {
			continue
		}
		name, _, _ := strings.Cut(hostArgs[i+1], ":")
		if port, ok := fcRelayPorts[name]; ok {
			proxies = append(proxies, uno.Proxy{GuestPort: port, Target: net.JoinHostPort(proxyHost, strconv.Itoa(port))})
			ports = append(ports, strconv.Itoa(port))
		}
	}
	return proxies, ports
}

// fcGuestBootScript builds the guest bootstrap shell program: mount the
// share disk, recreate the docker filesystem contract as bind-mounts, alias
// relay names to loopback. File mounts need a pre-created target file.
func fcGuestBootScript(mounts []fcMount) string {
	var b strings.Builder
	b.WriteString("mkdir -p /persist && (mountpoint -q /persist || mount -t ext4 /dev/vdb /persist); ")
	b.WriteString("grep -q 'model-relay' /etc/hosts || echo '127.0.0.1 model-relay toolhub toolhub-relay egress-relay' >> /etc/hosts; ")
	b.WriteString("mkdir -p /persist/managed /state /config /workspace /run/model-config /run/model-state /org; ")
	for _, mt := range mounts {
		// Nested binds need the parent mount present first; targets under a
		// read-only parent still take an rw bind of their own.
		b.WriteString("mkdir -p '")
		b.WriteString(filepath.Dir(mt.dst))
		b.WriteString("' && (grep -q '")
		b.WriteString(mt.dst)
		b.WriteString("' /proc/mounts || { [ -f '")
		b.WriteString(mt.src)
		b.WriteString("' ] && touch '")
		b.WriteString(mt.dst)
		b.WriteString("' || mkdir -p '")
		b.WriteString(mt.dst)
		b.WriteString("'; } && mount -o bind")
		if mt.ro {
			b.WriteString(",ro")
		}
		b.WriteString(" '")
		b.WriteString(mt.src)
		b.WriteString("' '")
		b.WriteString(mt.dst)
		b.WriteString("'); ")
	}
	// The managed contract seals executable import roots under HERMES_HOME
	// with empty read-only overlays (managedExtensionRootTmpfs).
	for _, name := range managedExtensionRootTmpfs {
		dir := "/state/hermes/" + name
		b.WriteString("mkdir -p '")
		b.WriteString(dir)
		b.WriteString("' && (grep -q '")
		b.WriteString(dir)
		b.WriteString("' /proc/mounts || mount -t tmpfs -o ro tmpfs '")
		b.WriteString(dir)
		b.WriteString("'); ")
	}
	b.WriteString("true")
	return b.String()
}

// ensureManagedNetsFC verifies the Compose-owned internal agent network and
// the supervisor control network. Unlike the docker path the control relay is
// spawned after box launch — its upstream is the box's vsock UDS, not a
// container name.
func (m *Manager) ensureManagedNetsFC(ctx context.Context, settings stack.Settings) (string, error) {
	agentNet := managedAgentNetworkName(settings.User, m.environment())
	out, err := m.command(ctx, "network", "inspect", "--format", "{{.Internal}}", agentNet)
	if err != nil {
		return "", fmt.Errorf("managed agent network %s unavailable; run hubctl up for this space first", agentNet)
	}
	if strings.TrimSpace(string(out)) != "true" {
		return "", fmt.Errorf("managed agent network %s is not internal", agentNet)
	}
	if _, err := m.command(ctx, "network", "inspect", "--format", "{{.Id}}", controlNetworkName); err != nil {
		if _, cerr := m.command(ctx, "network", "create", controlNetworkName); cerr != nil {
			return "", fmt.Errorf("managed control network %s unavailable: %w", controlNetworkName, cerr)
		}
	}
	return agentNet, nil
}

func (f *firecrackerBackend) Spawn(ctx context.Context, request SpawnRequest) (ComputeObservation, error) {
	m, binding, rt := f.m, request.Binding, request.Runtime
	if !rt.Managed {
		return ComputeObservation{}, errors.New("firecracker backend supports managed runtimes only")
	}
	preparation := binding
	preparation.RuntimeMode = "spawn-files"
	lock := m.lockFor(runtimeKey(preparation))
	lock.Lock()
	defer lock.Unlock()
	envName := m.environment()
	m.prepareSpawnFiles(context.Background(), binding, envName)
	settings := request.Settings
	var identityErr error
	settings, identityErr = m.applyIdentityTemplate(binding, settings)
	if identityErr != nil {
		return ComputeObservation{}, identityErr
	}
	if settings.Environment == "" {
		settings.Environment = envName
	}
	if _, err := stack.MaterializeEffectiveSkills(binding.ContextRoot, settings); err != nil {
		return ComputeObservation{}, err
	}
	effectiveConfig, err := m.materializeHermesConfig(context.Background(), binding, envName)
	if err != nil {
		return ComputeObservation{}, err
	}
	policyPath, err := stack.MaterializeToolPolicySnapshot(binding.ContextRoot, settings)
	if err != nil {
		return ComputeObservation{}, fmt.Errorf("materialize tool policy: %w", err)
	}
	mounts, err := m.fcGuestMounts(binding, settings, envName, policyPath, effectiveConfig)
	if err != nil {
		return ComputeObservation{}, err
	}
	agentNet, err := m.ensureManagedNetsFC(ctx, request.Settings)
	if err != nil {
		return ComputeObservation{}, fmt.Errorf("managed topology: %w", err)
	}
	hostArgs, err := m.sandboxRelayHosts(ctx, request.Settings, binding.ContextRoot)
	if err != nil {
		return ComputeObservation{}, err
	}
	proxies, ports := fcRelayProxies(hostArgs, m.cfg.UnoProxyHost)
	if len(proxies) == 0 {
		return ComputeObservation{}, errors.New("firecracker backend resolved no relay proxies")
	}
	box, err := f.client.LaunchBox(ctx, uno.LaunchRequest{
		Name:     rt.Container,
		Image:    m.cfg.UnoImage,
		RAMMB:    m.cfg.UnoBoxRAMMB,
		VCPU:     m.cfg.UnoBoxVCPU,
		Shares:   []uno.Share{{Tag: "space", HostPath: binding.ContextRoot}},
		Forwards: []uno.Forward{{GuestPort: m.cfg.RuntimePort, HostPort: request.Port}},
		Proxies:  proxies,
		Env: map[string]string{
			"HUB_FC_PROXIES": strings.Join(ports, ","),
			"HUB_FC_INGRESS": strconv.Itoa(m.cfg.RuntimePort),
		},
		Labels: map[string]string{
			"hermes.context":    binding.ContextID,
			"hermes.principal":  binding.PrincipalID,
			"hermes.generation": rt.Generation,
			"hermes.env":        m.environment(),
		},
	})
	if err != nil {
		return ComputeObservation{}, fmt.Errorf("launch box: %w", err)
	}
	if box.Status != "running" {
		return ComputeObservation{}, fmt.Errorf("box failed to start: %s", box.Error)
	}
	// The control relay mounts the box workdir and reaches the guest through
	// the firecracker vsock UDS — the dual-homed inbound path mirrors the
	// docker managed topology, just with a vsock upstream instead of a
	// container name.
	target := "vsock://" + strconv.Itoa(m.cfg.RuntimePort) + box.VsockUDS
	// No host publish: the supervisor's inbound seam is the mini-Uno forward
	// bound on request.Port; publishing the same port would collide.
	if err := m.spawnControlRelay(ctx, binding, rt.Container, request.Port, rt.Generation, agentNet, target, filepath.Dir(box.VsockUDS), false); err != nil {
		f.client.DeleteBox(context.Background(), box.ID) // #nosec G104 -- best-effort; error intentionally ignored
		return ComputeObservation{}, fmt.Errorf("control relay: %w", err)
	}
	if _, err := f.client.Run(ctx, box.ID, uno.RunRequest{Cmd: fcGuestBootScript(mounts), TimeoutSeconds: 30, IdempotencyKey: "boot-" + rt.Generation, WaitSeconds: 30}); err != nil {
		f.client.DeleteBox(context.Background(), box.ID) // #nosec G104 -- best-effort; error intentionally ignored
		return ComputeObservation{}, fmt.Errorf("guest boot: %w", err)
	}
	env, err := fcGuestEnv(m, binding, rt, m.cfg.RuntimePort, settings, envName)
	if err != nil {
		f.client.DeleteBox(context.Background(), box.ID) // #nosec G104 -- best-effort; error intentionally ignored
		return ComputeObservation{}, fmt.Errorf("guest env: %w", err)
	}
	for _, mt := range mounts {
		if mt.dst == "/config/tool-manifest.json" {
			env["HUB_TOOL_MANIFEST"] = "/config/tool-manifest.json"
		}
	}
	// No pgrep in the guest image: the readiness proof is the :RuntimePort
	// listener itself (hex port in /proc/net/tcp); serve warmup can take
	// tens of seconds, so poll and surface the log on failure.
	serve, err := f.client.Run(ctx, box.ID, uno.RunRequest{
		Cmd: "cd /persist && setsid nohup /usr/local/bin/hub-runtime serve >/persist/managed/serve.log 2>&1 < /dev/null & " +
			"for i in $(seq 1 40); do grep -qi ':" + fmt.Sprintf("%04X", m.cfg.RuntimePort) + " ' /proc/net/tcp && exit 0; sleep 2; done; tail -20 /persist/managed/serve.log >&2; exit 1",
		Env:            env,
		TimeoutSeconds: 90,
		IdempotencyKey: "serve-" + rt.Generation,
		WaitSeconds:    90,
	})
	if err != nil {
		f.client.DeleteBox(context.Background(), box.ID) // #nosec G104 -- best-effort; error intentionally ignored
		return ComputeObservation{}, fmt.Errorf("start runtime: %w", err)
	}
	if serve.Status != "succeeded" {
		f.client.DeleteBox(context.Background(), box.ID) // #nosec G104 -- best-effort; error intentionally ignored
		return ComputeObservation{}, fmt.Errorf("start runtime: %s", serve.Stderr)
	}
	// Address stays the control-relay published loopback port — identical to
	// the docker managed topology: supervisor -> 127.0.0.1:port -> relay ->
	// agentNet gateway forward -> guest :RuntimePort.
	return ComputeObservation{ID: box.ID, Address: rt.Address}, nil
}

func (f *firecrackerBackend) findBox(ctx context.Context, name string) (uno.Box, error) {
	boxes, err := f.client.ListBoxes(ctx)
	if err != nil {
		return uno.Box{}, err
	}
	var stale []uno.Box
	for _, b := range boxes {
		if b.Name != name {
			continue
		}
		if b.Status == "failed" {
			stale = append(stale, b)
			continue
		}
		return b, nil
	}
	if len(stale) > 0 {
		sort.Slice(stale, func(i, j int) bool { return stale[i].CreatedAt.After(stale[j].CreatedAt) })
		return stale[0], nil
	}
	return uno.Box{}, uno.ErrNotFound
}

func (f *firecrackerBackend) owned(box uno.Box, runtime Runtime) error {
	if box.Labels["hermes.context"] != runtime.ContextID {
		return fmt.Errorf("box %s context mismatch", box.ID)
	}
	return nil
}

func (f *firecrackerBackend) Verify(ctx context.Context, runtime Runtime) error {
	box, err := f.findBox(ctx, runtime.Container)
	if err != nil {
		return err
	}
	return f.owned(box, runtime)
}

func (f *firecrackerBackend) Observe(ctx context.Context, runtime Runtime) (ComputeObservation, error) {
	box, err := f.findBox(ctx, runtime.Container)
	if err != nil {
		return ComputeObservation{}, err
	}
	if err := f.owned(box, runtime); err != nil {
		return ComputeObservation{}, err
	}
	addr := runtime.Address
	if box.Address != "" && !strings.HasPrefix(addr, "http") {
		addr = "http://" + box.Address
	}
	return ComputeObservation{State: box.Status, ID: box.ID, Address: addr}, nil
}

func (f *firecrackerBackend) Remove(ctx context.Context, runtime Runtime) error {
	box, err := f.findBox(ctx, runtime.Container)
	var result error
	if err == nil {
		if err := f.owned(box, runtime); err != nil {
			return err
		}
		result = f.client.DeleteBox(ctx, box.ID)
	} else if !errors.Is(err, uno.ErrNotFound) {
		return err
	}
	// The vsock control relay is a same-generation docker sidecar — remove it
	// with the runtime even when the box itself is already gone.
	if sidecarErr := f.m.removeOwnedSidecar(ctx, runtime); sidecarErr != nil {
		result = errors.Join(result, sidecarErr)
	}
	return result
}
