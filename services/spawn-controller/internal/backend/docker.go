// Docker launch backend, moved from v0 internal/supervisor/{backend,ensure}.go
// and sandbox.go; the v0 copy is deleted in CHG-0336 S8. Docker CLI via
// os/exec with argv built from the Spec: no shell, no Docker SDK.
package backend

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"time"
)

// DockerConfig wires the docker CLI backend. ContainerPrefix and
// NetworkPrefix namespace everything the backend creates; Labels are added
// to every container and network (tests mark them for label-scoped
// cleanup). Trusted false accepts only runsc specs (X6).
type DockerConfig struct {
	Docker          string
	ContainerPrefix string
	NetworkPrefix   string
	Labels          map[string]string
	Trusted         bool
}

// NewDocker returns the docker launch backend.
func NewDocker(cfg DockerConfig) Backend {
	if cfg.Docker == "" {
		cfg.Docker = "docker"
	}
	if cfg.ContainerPrefix == "" {
		cfg.ContainerPrefix = "hermes-hub-rt-"
	}
	if cfg.NetworkPrefix == "" {
		cfg.NetworkPrefix = "hermes-hub-net-"
	}
	return &dockerBackend{cfg: cfg}
}

// dockerManagedLabel marks the workloads this backend owns; List never
// returns a container without it.
const dockerManagedLabel = "hermes-hub.managed"

type dockerBackend struct {
	cfg DockerConfig
}

// sanitizeName maps an arbitrary spec field to a docker-safe name fragment:
// alphanumerics plus -_. are kept (case preserved so spec keys round-trip),
// anything else becomes a dash.
func sanitizeName(s string) string {
	var b strings.Builder
	for _, r := range s {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9', r == '-', r == '_', r == '.':
			b.WriteRune(r)
		default:
			b.WriteByte('-')
		}
	}
	return b.String()
}

// DockerNetworkName is the per-space network an edge resolves to; sidecars
// join the same names to reach the workload.
func DockerNetworkName(prefix, space, edge string) string {
	return prefix + sanitizeName(space) + "-" + sanitizeName(edge)
}

func (d *dockerBackend) container(id string) string {
	return d.cfg.ContainerPrefix + sanitizeName(id)
}

// command runs the docker CLI with a bound. env carries full NAME=value
// pairs into the CLI's own process environment — the only channel spec env
// values travel through, keeping them out of argv.
func (d *dockerBackend) command(ctx context.Context, env []string, args ...string) ([]byte, error) {
	callCtx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	cmd := exec.CommandContext(callCtx, d.cfg.Docker, args...) // #nosec G204 -- configured docker binary, argv built internally
	if len(env) > 0 {
		cmd.Env = append(os.Environ(), env...)
	}
	return cmd.CombinedOutput()
}

func (d *dockerBackend) labels(spec Spec) map[string]string {
	labels := map[string]string{
		dockerManagedLabel: "spawn-controller",
		"hermes-hub.key":   spec.Key,
		"hermes-hub.org":   spec.Org,
		"hermes-hub.owner": spec.Owner,
		"hermes-hub.space": spec.Space,
		"hermes-hub.agent": spec.Agent,
	}
	for k, v := range d.cfg.Labels {
		labels[k] = v
	}
	return labels
}

// checkOCIRuntime fails closed when the spec asks for a sandbox the daemon
// does not offer (moved from v0 sandbox.go checkContainerRuntime).
func (d *dockerBackend) checkOCIRuntime(ctx context.Context, runtime string) error {
	body, err := d.command(ctx, nil, "info", "--format", "{{json .Runtimes}}")
	var runtimes map[string]json.RawMessage
	if err != nil || len(body) > 64*1024 || json.Unmarshal(body, &runtimes) != nil {
		return errors.New("container runtime availability unavailable")
	}
	if _, ok := runtimes[runtime]; !ok {
		return fmt.Errorf("container runtime %s is not installed on the Docker daemon", runtime)
	}
	return nil
}

// ensureNetwork creates the internal per-space edge network when missing;
// internal keeps it off the host's resolver and gateway (X1 default-deny).
func (d *dockerBackend) ensureNetwork(ctx context.Context, name string, labels map[string]string) error {
	if _, err := d.command(ctx, nil, "network", "inspect", "--format", "{{.Id}}", name); err == nil {
		return nil
	}
	args := []string{"network", "create", "--internal"}
	for k, v := range labels {
		args = append(args, "--label", k+"="+v)
	}
	args = append(args, name)
	if out, err := d.command(ctx, nil, args...); err != nil {
		return fmt.Errorf("create network %s: %w: %s", name, err, firstLine(out))
	}
	return nil
}

func (d *dockerBackend) Start(ctx context.Context, spec Spec) (Runtime, error) {
	if spec.Key == "" || spec.Image == "" {
		return Runtime{}, errors.New("backend: spec key and image are required")
	}
	if err := validateSpec(spec); err != nil {
		return Runtime{}, err
	}
	// Trust guard before any daemon call or bind lookup (X6).
	if !d.cfg.Trusted && spec.OCIRuntime != "runsc" {
		return Runtime{}, fmt.Errorf("%w: docker backend requires runsc", ErrUntrusted)
	}
	if spec.OCIRuntime != "" && spec.OCIRuntime != "runc" {
		if err := d.checkOCIRuntime(ctx, spec.OCIRuntime); err != nil {
			return Runtime{}, err
		}
	}
	for _, m := range spec.Mounts {
		if _, err := os.Stat(m.Source); err != nil {
			return Runtime{}, fmt.Errorf("backend: mount source %s: %w", m.Source, err)
		}
	}
	name := d.container(spec.Key)
	labels := d.labels(spec)
	networks := make([]string, 0, len(spec.Edges))
	for _, edge := range spec.Edges {
		network := DockerNetworkName(d.cfg.NetworkPrefix, spec.Space, edge)
		if err := d.ensureNetwork(ctx, network, labels); err != nil {
			return Runtime{}, err
		}
		networks = append(networks, network)
	}
	// Hardening is backend policy the spec cannot weaken (v0 managedRunArgs):
	// read-only root, init, non-root uid, no caps, no new privileges, /tmp
	// scratch on tmpfs.
	args := []string{"run", "-d", "--name", name, "--restart", "no",
		"--log-driver", "local", "--log-opt", "max-size=10m", "--log-opt", "max-file=3",
		"--read-only", "--init", "--user", fmt.Sprintf("10001:%d", max(0, os.Getgid())),
		"--cap-drop", "ALL", "--security-opt", "no-new-privileges:true",
		"--tmpfs", "/tmp:uid=10001,gid=10001,mode=1777"}
	if len(networks) == 0 {
		args = append(args, "--network", "none")
	} else {
		args = append(args, "--network", networks[0])
	}
	if spec.Limits.Memory != "" {
		args = append(args, "--memory", spec.Limits.Memory)
	}
	if spec.Limits.CPUs != "" {
		args = append(args, "--cpus", spec.Limits.CPUs)
	}
	if spec.Limits.PIDs > 0 {
		args = append(args, "--pids-limit", strconv.Itoa(spec.Limits.PIDs))
	}
	if spec.OCIRuntime != "" {
		args = append(args, "--runtime", spec.OCIRuntime)
	}
	for _, m := range spec.Mounts {
		value := "type=bind,src=" + m.Source + ",dst=" + m.Target
		if m.ReadOnly {
			value += ",readonly"
		}
		args = append(args, "--mount", value)
	}
	for _, entry := range spec.Env {
		envName, _, ok := strings.Cut(entry, "=")
		if !ok || envName == "" {
			return Runtime{}, fmt.Errorf("backend: malformed env entry %q", entry)
		}
		args = append(args, "--env", envName)
	}
	for k, v := range labels {
		args = append(args, "--label", k+"="+v)
	}
	args = append(args, spec.Image)
	args = append(args, spec.Command...)
	if out, err := d.command(ctx, spec.Env, args...); err != nil {
		return Runtime{}, fmt.Errorf("backend: start %s: %w: %s", spec.Key, err, firstLine(out))
	}
	// A run argv attaches one network; the rest ride network connect (X1:
	// only declared edges open).
	for _, network := range networks[min(1, len(networks)):] {
		if out, err := d.command(ctx, nil, "network", "connect", network, name); err != nil {
			_, _ = d.command(ctx, nil, "rm", "-f", name)
			return Runtime{}, fmt.Errorf("backend: attach %s: %w: %s", network, err, firstLine(out))
		}
	}
	return d.Inspect(ctx, spec.Key)
}

func (d *dockerBackend) Stop(ctx context.Context, id string) error {
	out, err := d.command(ctx, nil, "rm", "-f", d.container(id))
	if err != nil {
		if isMissingContainer(out) {
			return nil
		}
		return fmt.Errorf("backend: stop %s: %w: %s", id, err, firstLine(out))
	}
	return nil
}

func (d *dockerBackend) Inspect(ctx context.Context, id string) (Runtime, error) {
	out, err := d.command(ctx, nil, "inspect", d.container(id))
	if err != nil {
		if isMissingContainer(out) {
			return Runtime{}, fmt.Errorf("%w: %s", ErrNotFound, id)
		}
		return Runtime{}, fmt.Errorf("backend: inspect %s: %w: %s", id, err, firstLine(out))
	}
	var docs []struct {
		State struct {
			Status string
		}
		Config struct {
			Labels map[string]string
		}
	}
	if err := json.Unmarshal(out, &docs); err != nil || len(docs) != 1 {
		return Runtime{}, fmt.Errorf("backend: inspect %s: unreadable state", id)
	}
	labels := docs[0].Config.Labels
	return Runtime{
		ID:    id,
		Space: labels["hermes-hub.space"],
		Agent: labels["hermes-hub.agent"],
		State: docs[0].State.Status,
	}, nil
}

func (d *dockerBackend) List(ctx context.Context) ([]Runtime, error) {
	out, err := d.command(ctx, nil, "ps", "-a", "--filter", "label="+dockerManagedLabel, "--format", "{{json .}}")
	if err != nil {
		// A daemon error is not an empty daemon: callers must not mark
		// runtimes missing from a list they never got.
		return nil, fmt.Errorf("backend: list: %w: %s", err, firstLine(out))
	}
	var list []Runtime
	for _, line := range strings.Split(string(out), "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		var row struct {
			Names  string
			State  string
			Labels string
		}
		if err := json.Unmarshal([]byte(line), &row); err != nil {
			continue
		}
		rt := Runtime{ID: strings.TrimPrefix(row.Names, d.cfg.ContainerPrefix), State: row.State}
		for _, pair := range strings.Split(row.Labels, ",") {
			k, v, _ := strings.Cut(pair, "=")
			switch k {
			case "hermes-hub.space":
				rt.Space = v
			case "hermes-hub.agent":
				rt.Agent = v
			}
		}
		list = append(list, rt)
	}
	return list, nil
}

// isMissingContainer matches the daemon's "no such object/container" reply;
// casing varies between CLI and daemon versions.
func isMissingContainer(out []byte) bool {
	return strings.Contains(strings.ToLower(string(out)), "no such")
}

func firstLine(out []byte) string {
	s := strings.TrimSpace(string(out))
	if i := strings.IndexByte(s, '\n'); i >= 0 {
		s = s[:i]
	}
	if len(s) > 300 {
		s = s[:300]
	}
	return s
}
