package supervisor

import (
	"context"
	"errors"
	"fmt"
	"os"
	"sort"
	"strconv"
	"strings"

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

// fcGuestEnv mirrors the docker env contract: env-file values with every
// ContextRoot prefix rewritten to the virtio-fs mount point, plus the same
// identity/auth map the kubernetes backend injects.
func fcGuestEnv(binding Binding, rt Runtime, port int, contextRoot string) map[string]string {
	values := map[string]string{}
	if raw, err := os.ReadFile(binding.EnvFile); err == nil {
		for _, line := range strings.Split(string(raw), "\n") {
			line = strings.TrimSpace(line)
			if line == "" || strings.HasPrefix(line, "#") || !strings.Contains(line, "=") {
				continue
			}
			key, val, _ := strings.Cut(line, "=")
			if key == "HUB_RUNTIME_AUTH" || key == "API_SERVER_KEY" || key == "HUB_TOOLHUB_AUTH" {
				continue
			}
			values[key] = val
		}
	}
	for key, value := range map[string]string{
		"HUB_RUNTIME_LISTEN":    "0.0.0.0:" + strconv.Itoa(port),
		"HUB_RUNTIME_AUTH":      binding.runtimeAuth,
		"API_SERVER_KEY":        binding.runtimeAuth,
		"HUB_USER_ID":           binding.UserID,
		"HUB_PRINCIPAL_ID":      binding.PrincipalID,
		"HUB_CONTEXT_ID":        binding.ContextID,
		"HUB_RUNTIME_ID":        binding.RuntimeID,
		"HUB_ORGANIZATION_ID":   binding.OrganizationID,
		"HUB_POLICY_VERSION":    binding.PolicyVersion,
		"HUB_RUNTIME_GENERATION": rt.Generation,
		"API_SERVER_ENABLED":    "true",
		"API_SERVER_HOST":       "127.0.0.1",
		"API_SERVER_PORT":       "8642",
	} {
		values[key] = value
	}
	root := strings.TrimRight(contextRoot, "/")
	for k, v := range values {
		if v == root {
			values[k] = "/persist"
		} else if strings.HasPrefix(v, root+"/") {
			values[k] = "/persist" + strings.TrimPrefix(v, root)
		}
	}
	return values
}

// fcGuestBoot mounts the space share and prepares loopback relay aliases.
const fcGuestBoot = "mkdir -p /persist && mountpoint -q /persist || mount -t virtiofs space /persist; " +
	"grep -q 'model-relay' /etc/hosts || echo '127.0.0.1 model-relay toolhub toolhub-relay egress-relay' >> /etc/hosts; " +
	"mkdir -p /persist/managed && true"

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
	if err := m.ensureManagedInfra(ctx, binding, request.Settings, rt.Container, request.Port, rt.Generation); err != nil {
		return ComputeObservation{}, fmt.Errorf("managed topology: %w", err)
	}
	hostArgs, err := m.sandboxRelayHosts(ctx, request.Settings, binding.ContextRoot)
	if err != nil {
		return ComputeObservation{}, err
	}
	var proxies []uno.Proxy
	var ports []string
	for _, arg := range hostArgs {
		if !strings.HasPrefix(arg, "--add-host ") {
			continue
		}
		name, ip, _ := strings.Cut(strings.TrimPrefix(arg, "--add-host "), ":")
		if port, ok := fcRelayPorts[name]; ok && ip != "" {
			proxies = append(proxies, uno.Proxy{GuestPort: port, Target: ip + ":" + strconv.Itoa(port)})
			ports = append(ports, strconv.Itoa(port))
		}
	}
	if len(proxies) == 0 {
		return ComputeObservation{}, errors.New("firecracker backend resolved no relay proxies")
	}
	box, err := f.client.LaunchBox(ctx, uno.LaunchRequest{
		Name:     rt.Container,
		Image:    m.cfg.UnoImage,
		RAMMB:    m.cfg.UnoBoxRAMMB,
		VCPU:     m.cfg.UnoBoxVCPU,
		Shares:   []uno.Share{{Tag: "space", HostPath: binding.ContextRoot}},
		Forwards: []uno.Forward{{GuestPort: request.Port}},
		Proxies:  proxies,
		Env:      map[string]string{"HUB_FC_PROXIES": strings.Join(ports, ",")},
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
	if _, err := f.client.Run(ctx, box.ID, uno.RunRequest{Cmd: fcGuestBoot, TimeoutSeconds: 30, IdempotencyKey: "boot-" + rt.Generation, WaitSeconds: 30}); err != nil {
		f.client.DeleteBox(context.Background(), box.ID)
		return ComputeObservation{}, fmt.Errorf("guest boot: %w", err)
	}
	env := fcGuestEnv(binding, rt, request.Port, binding.ContextRoot)
	serve, err := f.client.Run(ctx, box.ID, uno.RunRequest{
		Cmd:            "cd /persist && exec hub-runtime serve",
		Env:            env,
		TimeoutSeconds: 10,
		IdempotencyKey: "serve-" + rt.Generation,
		WaitSeconds:    10,
	})
	if err != nil {
		f.client.DeleteBox(context.Background(), box.ID)
		return ComputeObservation{}, fmt.Errorf("start runtime: %w", err)
	}
	if serve.Status == "failed" {
		f.client.DeleteBox(context.Background(), box.ID)
		return ComputeObservation{}, fmt.Errorf("start runtime: %s", serve.Stderr)
	}
	return ComputeObservation{ID: box.ID, Address: "http://" + box.Address}, nil
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
	if errors.Is(err, uno.ErrNotFound) {
		return nil
	}
	if err != nil {
		return err
	}
	if err := f.owned(box, runtime); err != nil {
		return err
	}
	return f.client.DeleteBox(ctx, box.ID)
}
