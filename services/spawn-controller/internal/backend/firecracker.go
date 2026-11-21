// Firecracker launch backend, moved from v0 internal/supervisor/
// firecracker.go (CHG-0336 S8). Compile-only: nothing constructs it until
// M40.15 selects it behind backend.Backend. The guest contract is the v0
// one — no NIC, /persist share, relay proxies through Uno — consuming the
// resolved Spec where the v0 request carried binding+settings.
// Materialization runs spawn-side in runtime_spec.go; this file only
// applies the finished mounts/env to the guest.
package backend

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"

	"encoding/json"

	"github.com/letya999/hermes-hub/internal/uno"
)

// FirecrackerConfig wires the Uno-backed backend: the v0 Uno* config plus
// the values the supervisor supplied (environment, install owner id,
// spaces root, runtime port, image and resource limits, docker CLI runner
// for the host-side control relay).
type FirecrackerConfig struct {
	UnoURL       string
	UnoToken     string
	UnoImage     string
	UnoBoxRAMMB  int
	UnoBoxVCPU   int
	UnoProxyHost string
	HTTP         *http.Client
	Environment  string
	OwnerID      string
	SpacesRoot   string
	RuntimePort  int
	PIDs         int
	Memory       string
	CPU          string
	Command      func(ctx context.Context, args ...string) ([]byte, error)
}

// Firecracker is the compile-only microVM backend (M40.15).
type Firecracker struct {
	cfg    FirecrackerConfig
	client *uno.Client
}

// NewFirecracker validates the v0 config contract and keeps its HUB_UNO_*
// env resolution (moved out of supervisor.New).
func NewFirecracker(cfg FirecrackerConfig) (*Firecracker, error) {
	if cfg.UnoURL == "" {
		cfg.UnoURL = os.Getenv("HUB_UNO_URL")
	}
	if cfg.UnoToken == "" {
		cfg.UnoToken = os.Getenv("HUB_UNO_TOKEN")
	}
	if cfg.UnoImage == "" {
		cfg.UnoImage = os.Getenv("HUB_UNO_IMAGE")
	}
	if cfg.UnoProxyHost == "" {
		cfg.UnoProxyHost = envOr("HUB_UNO_PROXY_HOST", "127.0.0.1")
	}
	if cfg.HTTP == nil {
		cfg.HTTP = &http.Client{}
	}
	if cfg.UnoURL == "" || cfg.UnoToken == "" {
		return nil, errors.New("firecracker backend requires HUB_UNO_URL and HUB_UNO_TOKEN")
	}
	if cfg.UnoImage == "" {
		return nil, errors.New("firecracker backend requires HUB_UNO_IMAGE")
	}
	if cfg.RuntimePort <= 0 {
		cfg.RuntimePort = 8080
	}
	if cfg.PIDs <= 0 {
		cfg.PIDs = 512
	}
	if cfg.Memory == "" {
		cfg.Memory = "2g"
	}
	if cfg.CPU == "" {
		cfg.CPU = "2"
	}
	return &Firecracker{cfg: cfg, client: uno.NewClient(cfg.UnoURL, cfg.UnoToken, cfg.HTTP)}, nil
}

func envOr(name, fallback string) string {
	if value := strings.TrimSpace(os.Getenv(name)); value != "" {
		return value
	}
	return fallback
}

func (f Firecracker) command(ctx context.Context, args ...string) ([]byte, error) {
	if f.cfg.Command == nil {
		return nil, errors.New("docker runner unavailable")
	}
	return f.cfg.Command(ctx, args...)
}

// fcRelayPorts maps relay hostnames to their well-known service ports.
var fcRelayPorts = map[string]int{"model-relay": 8318, "toolhub": 8090, "toolhub-relay": 8090, "egress-relay": 8319}

// fcGuestEnv mirrors the managed docker env contract (v0 fcGuestEnv): the
// spec's env files, the resolved service environment and the identity/auth
// vars. Host paths under the space dir are rewritten to the /persist
// share; paths escaping the space have no guest counterpart.
func fcGuestEnv(spec Spec, dir, generation string, port int) (map[string]string, error) {
	values := map[string]string{}
	load := func(path string) error {
		raw, err := os.ReadFile(path) // #nosec G703 -- paths are the validated spec env files under the space dir
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
	for _, file := range spec.EnvFiles {
		if err := load(file); err != nil {
			return nil, fmt.Errorf("env file %s: %w", filepath.Base(file), err)
		}
	}
	for _, entry := range spec.Env {
		key, val, ok := strings.Cut(entry, "=")
		if ok {
			values[key] = val
		}
	}
	for key, value := range map[string]string{
		"HUB_RUNTIME_LISTEN":     "127.0.0.1:" + strconv.Itoa(port),
		"HUB_PRINCIPAL_ID":       spec.Owner,
		"HUB_CONTEXT_ID":         spec.Space,
		"HUB_RUNTIME_GENERATION": generation,
		"API_SERVER_ENABLED":     "true",
		"API_SERVER_HOST":        "127.0.0.1",
		"API_SERVER_PORT":        "8642",
	} {
		values[key] = value
	}
	if values["PATH"] == "" {
		values["PATH"] = "/opt/hermes/.venv/bin:/usr/local/bin:/usr/local/sbin:/usr/sbin:/usr/bin:/sbin:/bin"
	}
	root := strings.TrimRight(filepath.ToSlash(dir), "/")
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

// fcGuestMounts maps the spec's resolved binds (the v0 fcGuestMounts
// contract) onto the guest share. The model-registry and model-state
// mounts are backend-owned: the relay contract projects them but the
// spec's mount list only carries the workspace/state binds.
func fcGuestMounts(spec Spec, dir, env string) ([]fcMount, error) {
	mounts := []fcMount{
		{"/run/model-config/models.yaml", filepath.Join(dir, "generated", "model-registry", env, "models.yaml"), true},
		{"/run/model-state", filepath.Join(dir, "managed", env, "model-relay"), false},
	}
	for _, m := range spec.Mounts {
		mounts = append(mounts, fcMount{m.Target, m.Source, m.ReadOnly})
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

// fcGuestBootScript builds the guest bootstrap shell program (v0): mount
// the share disk, recreate the docker filesystem contract as bind-mounts,
// alias relay names to loopback. File mounts need a pre-created target
// file.
func fcGuestBootScript(mounts []fcMount, extensionRoots []string) string {
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
	// with empty read-only overlays (v0 managedExtensionRootTmpfs — the
	// spec's /state/hermes tmpfs targets).
	for _, name := range extensionRoots {
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

// ensureManagedNetsFC verifies the Compose-owned internal agent network
// and the control network (v0): unlike the docker path the control relay
// is spawned after box launch — its upstream is the box's vsock UDS.
func (f Firecracker) ensureManagedNetsFC(ctx context.Context, agentNet, control string) error {
	out, err := f.command(ctx, "network", "inspect", "--format", "{{.Internal}}", agentNet)
	if err != nil {
		return fmt.Errorf("managed agent network %s unavailable; run hubctl up for this space first", agentNet)
	}
	if strings.TrimSpace(string(out)) != "true" {
		return fmt.Errorf("managed agent network %s is not internal", agentNet)
	}
	if _, err := f.command(ctx, "network", "inspect", "--format", "{{.Id}}", control); err != nil {
		if _, cerr := f.command(ctx, "network", "create", control); cerr != nil {
			return fmt.Errorf("managed control network %s unavailable: %w", control, cerr)
		}
	}
	return nil
}

// Start runs the v0 Spawn sequence: verify the managed topology, resolve
// relay proxies, launch the box, attach the vsock control relay, boot the
// guest contract and poll the serve listener.
func (f Firecracker) Start(ctx context.Context, spec Spec) (Runtime, error) {
	if !spec.Managed() {
		return Runtime{}, errors.New("firecracker backend supports managed runtimes only")
	}
	generation := spec.Labels["hermes-hub.generation"]
	dir, err := specSpaceDir(f.cfg.SpacesRoot, spec.Space)
	if err != nil {
		return Runtime{}, err
	}
	mounts, err := fcGuestMounts(spec, dir, f.cfg.Environment)
	if err != nil {
		return Runtime{}, err
	}
	agentNet := spec.Networks[0]
	control := "hermes-hub-control"
	if spec.Relay != nil && spec.Relay.Control != "" {
		control = spec.Relay.Control
	}
	if err := f.ensureManagedNetsFC(ctx, agentNet, control); err != nil {
		return Runtime{}, fmt.Errorf("managed topology: %w", err)
	}
	hostArgs, err := sandboxRelayHosts(ctx, f.command, agentNet, spec.RelayHosts)
	if err != nil {
		return Runtime{}, err
	}
	proxies, ports := fcRelayProxies(hostArgs, f.cfg.UnoProxyHost)
	if len(proxies) == 0 {
		return Runtime{}, errors.New("firecracker backend resolved no relay proxies")
	}
	// The guest's runtime port publishes through a mini-Uno forward on an
	// ephemeral loopback host port — the v0 supervisor's allocated port.
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return Runtime{}, err
	}
	hostPort := listener.Addr().(*net.TCPAddr).Port
	_ = listener.Close()
	box, err := f.client.LaunchBox(ctx, uno.LaunchRequest{
		Name:     spec.Key,
		Image:    f.cfg.UnoImage,
		RAMMB:    f.cfg.UnoBoxRAMMB,
		VCPU:     f.cfg.UnoBoxVCPU,
		Shares:   []uno.Share{{Tag: "space", HostPath: dir}},
		Forwards: []uno.Forward{{GuestPort: f.cfg.RuntimePort, HostPort: hostPort}},
		Proxies:  proxies,
		Env: map[string]string{
			"HUB_FC_PROXIES": strings.Join(ports, ","),
			"HUB_FC_INGRESS": strconv.Itoa(f.cfg.RuntimePort),
		},
		Labels: map[string]string{
			"hermes.context":    spec.Space,
			"hermes.principal":  spec.Owner,
			"hermes.generation": generation,
			"hermes.env":        f.cfg.Environment,
			"hermes.owner":      f.cfg.OwnerID,
		},
	})
	if err != nil {
		return Runtime{}, fmt.Errorf("launch box: %w", err)
	}
	if box.Status != "running" {
		return Runtime{}, fmt.Errorf("box failed to start: %s", box.Error)
	}
	// The control relay mounts the box workdir and reaches the guest through
	// the firecracker vsock UDS — the dual-homed inbound path mirrors the
	// docker managed topology, just with a vsock upstream instead of a
	// container name.
	target := "vsock://" + strconv.Itoa(f.cfg.RuntimePort) + box.VsockUDS
	// No host publish: the supervisor's inbound seam is the mini-Uno forward;
	// publishing the same port would collide.
	if err := f.spawnControlRelay(ctx, spec, agentNet, control, target, filepath.Dir(box.VsockUDS)); err != nil {
		f.client.DeleteBox(context.Background(), box.ID) // #nosec G104 -- best-effort; error intentionally ignored
		return Runtime{}, fmt.Errorf("control relay: %w", err)
	}
	if _, err := f.client.Run(ctx, box.ID, uno.RunRequest{Cmd: fcGuestBootScript(mounts, kubeExtensionRoots(spec)), TimeoutSeconds: 30, IdempotencyKey: "boot-" + generation, WaitSeconds: 30}); err != nil {
		f.client.DeleteBox(context.Background(), box.ID) // #nosec G104 -- best-effort; error intentionally ignored
		return Runtime{}, fmt.Errorf("guest boot: %w", err)
	}
	env, err := fcGuestEnv(spec, dir, generation, f.cfg.RuntimePort)
	if err != nil {
		f.client.DeleteBox(context.Background(), box.ID) // #nosec G104 -- best-effort; error intentionally ignored
		return Runtime{}, fmt.Errorf("guest env: %w", err)
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
		Cmd: "cd /persist && setsid nohup /usr/local/bin/harness-runtime serve >/persist/managed/serve.log 2>&1 < /dev/null & " +
			"for i in $(seq 1 40); do grep -qi ':" + fmt.Sprintf("%04X", f.cfg.RuntimePort) + " ' /proc/net/tcp && exit 0; sleep 2; done; tail -20 /persist/managed/serve.log >&2; exit 1",
		Env:            env,
		TimeoutSeconds: 90,
		IdempotencyKey: "serve-" + generation,
		WaitSeconds:    90,
	})
	if err != nil {
		f.client.DeleteBox(context.Background(), box.ID) // #nosec G104 -- best-effort; error intentionally ignored
		return Runtime{}, fmt.Errorf("start runtime: %w", err)
	}
	if serve.Status != "succeeded" {
		f.client.DeleteBox(context.Background(), box.ID) // #nosec G104 -- best-effort; error intentionally ignored
		return Runtime{}, fmt.Errorf("start runtime: %s", serve.Stderr)
	}
	// Address stays the mini-Uno forward's loopback port — identical to the
	// docker managed topology: caller -> 127.0.0.1:port -> vsock relay ->
	// agentNet gateway forward -> guest :RuntimePort.
	return Runtime{ID: box.ID, Space: spec.Space, Agent: spec.Agent, State: "running", Address: "http://127.0.0.1:" + strconv.Itoa(hostPort), Name: spec.Key, HostPort: hostPort}, nil
}

// spawnControlRelay is the v0 spawnControlRelay for the vsock variant:
// same dual-homed sidecar, no host publish — the mini-Uno forward is the
// inbound seam.
func (f Firecracker) spawnControlRelay(ctx context.Context, spec Spec, agentNet, control, target, mountDir string) error {
	relay := spec.Key + "-ctl"
	image := spec.Image
	args := []string{"run", "-d", "--name", relay, "--network", agentNet, "--restart=no", "--log-driver", "local", "--log-opt", "max-size=10m", "--log-opt", "max-file=3", "--read-only", "--init", "--user", "10001:10001", "--cap-drop", "ALL", "--security-opt", "no-new-privileges:true", "--pids-limit", strconv.Itoa(f.cfg.PIDs), "--memory", f.cfg.Memory, "--cpus", f.cfg.CPU, "--tmpfs", "/tmp:uid=10001,gid=10001,mode=1777", "-e", "HUB_CONTROL_RELAY_TARGET=" + target, "--label", "hermes-hub.owner=" + f.cfg.OwnerID, "--label", "hermes-hub.context=" + fcContextKey(spec), "--label", "hermes-hub.generation=" + spec.Labels["hermes-hub.generation"], "--label", "hermes-hub.role=control-relay", "--entrypoint", "hub-runtime"}
	if mountDir != "" {
		args = append(args, "-v", mountDir+":"+mountDir)
	}
	args = append(args, image, "control-relay")
	if out, err := f.command(ctx, args...); err != nil {
		f.reclaimSidecarName(ctx, spec, relay)
		if retryOut, retryErr := f.command(ctx, args...); retryErr != nil {
			return fmt.Errorf("start control relay: %w: %s; retry: %s", retryErr, firstLine(retryOut), firstLine(out))
		}
	}
	if out, err := f.command(ctx, "network", "connect", control, relay); err != nil {
		_, _ = f.command(ctx, "rm", "-f", relay)
		return fmt.Errorf("attach control relay: %w: %s", err, firstLine(out))
	}
	return nil
}

// fcContextKey is the v0 context-hash the control relay's labels carry.
func fcContextKey(spec Spec) string {
	sum := kubeHash(spec.Owner + "\x00" + spec.Space + "\x00gateway")
	return fmt.Sprintf("%x", sum)
}

// reclaimSidecarName removes a stale same-name relay only when its labels
// prove this backend owns it (v0 reclaimSidecarName).
func (f Firecracker) reclaimSidecarName(ctx context.Context, spec Spec, relay string) {
	out, err := f.command(ctx, "inspect", "--format", "{{json .Config.Labels}}", relay)
	if err != nil {
		return
	}
	var labels map[string]string
	if json.Unmarshal(out, &labels) != nil {
		return
	}
	if labels["hermes-hub.owner"] != f.cfg.OwnerID || labels["hermes-hub.role"] != "control-relay" {
		return
	}
	_, _ = f.command(ctx, "rm", "-f", relay)
}

func (f Firecracker) findBox(ctx context.Context, name string) (uno.Box, error) {
	if f.client == nil {
		return uno.Box{}, errors.New("firecracker backend unconfigured")
	}
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

// VerifyWorker re-checks the box's owner labels against the spec (v0
// Verify): context and principal must match the claim.
func (f Firecracker) VerifyWorker(ctx context.Context, id string, spec Spec) error {
	box, err := f.findBox(ctx, id)
	if err != nil {
		return err
	}
	if box.Labels["hermes.context"] != spec.Space || box.Labels["hermes.principal"] != spec.Owner {
		return fmt.Errorf("box %s ownership mismatch", box.ID)
	}
	return nil
}

// Inspect maps a box to the backend Runtime surface. With only the id the
// provable fence is the owner label — the context fence needs the claim
// row's tenant, which M40.15 restores.
func (f Firecracker) Inspect(ctx context.Context, id string) (Runtime, error) {
	box, err := f.findBox(ctx, id)
	if err != nil {
		return Runtime{}, err
	}
	if f.cfg.OwnerID != "" && box.Labels["hermes.owner"] != "" && box.Labels["hermes.owner"] != f.cfg.OwnerID {
		return Runtime{}, fmt.Errorf("box %s ownership mismatch", box.ID)
	}
	addr := ""
	if box.Address != "" {
		addr = "http://" + box.Address
	}
	return Runtime{ID: box.ID, Name: box.Name, State: box.Status, Address: addr}, nil
}

// Stop deletes the box and its same-generation control relay sidecar (v0
// Remove). The sidecar fence degrades to owner+role labels under the
// name-only id — the scope+generation check returns with the claim row.
func (f Firecracker) Stop(ctx context.Context, id string) error {
	box, err := f.findBox(ctx, id)
	var result error
	if err == nil {
		if f.cfg.OwnerID != "" && box.Labels["hermes.owner"] != "" && box.Labels["hermes.owner"] != f.cfg.OwnerID {
			return fmt.Errorf("box %s ownership mismatch", box.ID)
		}
		result = f.client.DeleteBox(ctx, box.ID)
	} else if !errors.Is(err, uno.ErrNotFound) {
		return err
	}
	if sidecarErr := f.removeOwnedSidecar(ctx, id); sidecarErr != nil {
		result = errors.Join(result, sidecarErr)
	}
	return result
}

// removeOwnedSidecar removes <name>-ctl when its labels prove this install
// owns it (v0): an unverifiable container is never removed.
func (f Firecracker) removeOwnedSidecar(ctx context.Context, name string) error {
	relay := name + "-ctl"
	out, err := f.command(ctx, "inspect", "--format", "{{.Id}}", relay)
	container := strings.TrimSpace(string(out))
	if err != nil || len(container) != 64 {
		return nil
	}
	labelsOut, err := f.command(ctx, "inspect", "--format", "{{json .Config.Labels}}", container)
	if err != nil {
		return nil
	}
	var labels map[string]string
	if json.Unmarshal(labelsOut, &labels) != nil {
		return nil
	}
	if labels["hermes-hub.owner"] != f.cfg.OwnerID || labels["hermes-hub.role"] != "control-relay" || f.cfg.OwnerID == "" {
		return errors.New("control relay ownership mismatch")
	}
	if _, err := f.command(ctx, "rm", "-f", container); err != nil {
		return errors.New("owned control relay removal failed")
	}
	return nil
}

// List inventories this install's boxes (v0 had no fc orphan inventory;
// the label fence is the owner label).
func (f Firecracker) List(ctx context.Context) ([]Runtime, error) {
	boxes, err := f.client.ListBoxes(ctx)
	if err != nil {
		return nil, err
	}
	var out []Runtime
	for _, box := range boxes {
		if f.cfg.OwnerID != "" && box.Labels["hermes.owner"] != "" && box.Labels["hermes.owner"] != f.cfg.OwnerID {
			continue
		}
		out = append(out, Runtime{ID: box.ID, Name: box.Name, State: box.Status})
	}
	return out, nil
}

// Exec is unsupported on the microVM backend: v0 had no exec surface and
// M40.15 decides whether workers exist there at all.
func (f Firecracker) Exec(context.Context, string, ExecSpec) (ExecResult, error) {
	return ExecResult{}, errors.New("firecracker backend does not exec")
}
