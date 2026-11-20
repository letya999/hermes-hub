// Docker launch backend, moved from v0 internal/supervisor/{backend,ensure}.go
// and sandbox.go; the v0 copy is deleted in CHG-0336 S8. Docker CLI via
// os/exec with argv built from the Spec: no shell, no Docker SDK. The worker
// sidecar lifecycle moved here from v0 internal/toolhub/cell_net.go (S6).
package backend

import (
	"bytes"
	"context"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

// DockerConfig wires the docker CLI backend. ContainerPrefix and
// NetworkPrefix namespace everything the backend creates; Labels are added
// to every container and network (tests mark them for label-scoped
// cleanup). Trusted false accepts only runsc specs (X6). StateRoot hosts
// the temp files docker cp stages for sidecar configs; empty means the OS
// temp dir.
type DockerConfig struct {
	Docker          string
	ContainerPrefix string
	NetworkPrefix   string
	Labels          map[string]string
	Trusted         bool
	StateRoot       string
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

// workerNet is the worker's own internal network its sidecars share —
// the moved v0 "hermes-<cell>-net" per-cell topology.
func (d *dockerBackend) workerNet(id string) string {
	return d.cfg.NetworkPrefix + sanitizeName(id) + "-cell"
}

// command runs the docker CLI with a bound. env carries full NAME=value
// pairs into the CLI's own process environment — the only channel spec env
// values travel through, keeping them out of argv.
func (d *dockerBackend) command(ctx context.Context, env []string, args ...string) ([]byte, error) {
	callCtx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	cmd := exec.CommandContext(callCtx, d.cfg.Docker, args...) // #nosec G204 -- configured docker binary, argv built internally
	cmd.Env = dockerCLIEnv(env)
	return cmd.CombinedOutput()
}

// dockerCLIEnv builds the docker CLI child's environment: an allowlist of
// the controller's own variables — the ones the CLI genuinely needs plus
// the operator's DOCKER_* daemon configuration — then the spec env whose
// values the -e NAME mechanism forwards to the container. Everything else
// in os.Environ is dropped, so a stray controller variable or a spec name
// the caller injected can no longer redirect or reconfigure the CLI
// itself (caller-facing DOCKER_* names are refused upstream in
// Workers.Exec and the contract).
func dockerCLIEnv(specEnv []string) []string {
	allow := map[string]bool{
		"PATH": true, "HOME": true, "USERPROFILE": true,
		"HOMEDRIVE": true, "HOMEPATH": true, "APPDATA": true,
		"LOCALAPPDATA": true, "SYSTEMROOT": true, "SYSTEMDRIVE": true,
		"COMSPEC": true, "PATHEXT": true, "TEMP": true, "TMP": true,
		"TMPDIR": true, "LANG": true, "LC_ALL": true,
		"SSL_CERT_FILE": true, "SSL_CERT_DIR": true,
	}
	out := make([]string, 0, len(specEnv)+16)
	for _, entry := range os.Environ() {
		name, _, _ := strings.Cut(entry, "=")
		if allow[name] || strings.HasPrefix(name, "DOCKER_") {
			out = append(out, entry)
		}
	}
	return append(out, specEnv...)
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
	for k, v := range spec.Labels {
		labels[k] = v
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

// resolveImage maps a name:tag (or digest-pinned) ref to the daemon's
// immutable image ID — create-by-ID so a retagged local name cannot swap
// content between resolution and create (moved from v0 cell.go
// resolveCellImage).
func (d *dockerBackend) resolveImage(ctx context.Context, ref string) (string, error) {
	out, err := d.command(ctx, nil, "image", "inspect", ref, "--format", "{{.Id}}")
	id := strings.TrimSpace(string(out))
	if err != nil || !dockerImageID(id) {
		return "", fmt.Errorf("backend: image %q unavailable", ref)
	}
	return id, nil
}

// dockerImageID is the daemon image-id shape (sha256:<64 hex>); containerd
// store daemons may answer a bare 64-hex id, which also passes.
func dockerImageID(id string) bool {
	id = strings.TrimPrefix(id, "sha256:")
	if len(id) != 64 {
		return false
	}
	_, err := hex.DecodeString(id)
	return err == nil
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
	networks := make([]string, 0, len(spec.Edges)+1)
	for _, edge := range spec.Edges {
		network := DockerNetworkName(d.cfg.NetworkPrefix, spec.Space, edge)
		if err := d.ensureNetwork(ctx, network, labels); err != nil {
			return Runtime{}, err
		}
		networks = append(networks, network)
	}
	// A sidecar'd worker owns one internal network the companions share
	// (moved from v0 cell_net.go cellEgress).
	if len(spec.Sidecars) > 0 {
		workerNet := d.workerNet(spec.Key)
		if err := d.ensureNetwork(ctx, workerNet, labels); err != nil {
			return Runtime{}, err
		}
		networks = append(networks, workerNet)
	}
	// Hardening is backend policy the spec cannot weaken (v0 managedRunArgs):
	// read-only root, init, no caps, no new privileges, /tmp scratch on
	// tmpfs. A cell spec overrides the default user/entrypoint/workdir.
	user := spec.User
	if user == "" {
		user = fmt.Sprintf("10001:%d", max(0, os.Getgid()))
	}
	args := []string{"run", "-d", "--name", name, "--restart", "no",
		"--log-driver", "local", "--log-opt", "max-size=10m", "--log-opt", "max-file=3",
		"--read-only", "--init", "--user", user,
		"--cap-drop", "ALL", "--security-opt", "no-new-privileges:true"}
	if spec.SeccompProfile != "" {
		args = append(args, "--security-opt", "seccomp="+spec.SeccompProfile)
	}
	if len(spec.Tmpfs) == 0 {
		args = append(args, "--tmpfs", "/tmp:uid=10001,gid=10001,mode=1777")
	}
	for _, t := range spec.Tmpfs {
		args = append(args, "--tmpfs", t.Target+":"+t.Options)
	}
	if len(networks) == 0 {
		args = append(args, "--network", "none")
	} else {
		args = append(args, "--network", networks[0])
	}
	if spec.Limits.Memory != "" {
		args = append(args, "--memory", spec.Limits.Memory)
	}
	if spec.Limits.MemorySwap != "" {
		args = append(args, "--memory-swap", spec.Limits.MemorySwap)
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
	if spec.Workdir != "" {
		args = append(args, "--workdir", spec.Workdir)
	}
	for _, m := range spec.Mounts {
		value := "type=bind,src=" + m.Source + ",dst=" + m.Target
		if m.ReadOnly {
			value += ",readonly"
		}
		args = append(args, "--mount", value)
	}
	for _, m := range spec.ImageMounts {
		args = append(args, "--mount", "type=image,src="+m.Source+",dst="+m.Target+",readonly")
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
	if spec.Entrypoint != "" {
		args = append(args, "--entrypoint", spec.Entrypoint)
	}
	args = append(args, spec.Image)
	args = append(args, spec.Command...)
	started := false
	defer func() {
		if !started {
			d.cleanup(context.Background(), spec.Key)
		}
	}()
	if out, err := d.command(ctx, spec.Env, args...); err != nil {
		return Runtime{}, fmt.Errorf("backend: start %s: %w: %s", spec.Key, err, firstLine(out))
	}
	// A run argv attaches one network; the rest ride network connect (X1:
	// only declared edges open).
	for _, network := range networks[min(1, len(networks)):] {
		if out, err := d.command(ctx, nil, "network", "connect", network, name); err != nil {
			return Runtime{}, fmt.Errorf("backend: attach %s: %w: %s", network, err, firstLine(out))
		}
	}
	for _, sidecar := range spec.Sidecars {
		if err := d.startSidecar(ctx, spec, sidecar); err != nil {
			return Runtime{}, err
		}
	}
	rt, err := d.Inspect(ctx, spec.Key)
	if err != nil {
		return Runtime{}, err
	}
	started = true
	return rt, nil
}

// startSidecar creates, loads and starts one companion on the worker's own
// network: the moved cellSquid/cellCredProxy lifecycle — volume for config,
// docker cp for plain files, bridge connect for egress, an exec channel for
// secret payloads and a procfs port probe when the sidecar must listen.
func (d *dockerBackend) startSidecar(ctx context.Context, spec Spec, s SidecarSpec) error {
	name := d.container(spec.Key) + "-" + s.Name
	labels := d.labels(spec)
	labels["hermes-hub.sidecar"] = s.Name
	for _, vol := range s.Volumes {
		volName := name + "-" + sanitizeName(vol.Source)
		args := []string{"volume", "create"}
		for k, v := range labels {
			args = append(args, "--label", k+"="+v)
		}
		if out, err := d.command(ctx, nil, append(args, volName)...); err != nil {
			return fmt.Errorf("backend: sidecar %s volume: %w: %s", s.Name, err, firstLine(out))
		}
	}
	image := s.Image
	if !dockerImageID(image) {
		resolved, err := d.resolveImage(ctx, image)
		if err != nil {
			return err
		}
		image = resolved
	}
	user := s.User
	if user == "" {
		user = "10001:10001"
	}
	args := []string{"create", "--name", name, "--network", d.workerNet(spec.Key),
		"--read-only", "--user", user, "--cap-drop", "ALL",
		"--security-opt", "no-new-privileges:true"}
	if spec.SeccompProfile != "" {
		args = append(args, "--security-opt", "seccomp="+spec.SeccompProfile)
	}
	for _, t := range s.Tmpfs {
		args = append(args, "--tmpfs", t.Target+":"+t.Options)
	}
	for _, vol := range s.Volumes {
		args = append(args, "--mount", "type=volume,src="+name+"-"+sanitizeName(vol.Source)+",dst="+vol.Target)
	}
	for _, m := range s.Mounts {
		value := "type=bind,src=" + m.Source + ",dst=" + m.Target
		if m.ReadOnly {
			value += ",readonly"
		}
		args = append(args, "--mount", value)
	}
	for _, entry := range s.Env {
		envName, _, ok := strings.Cut(entry, "=")
		if !ok || envName == "" {
			return fmt.Errorf("backend: malformed sidecar env entry %q", entry)
		}
		args = append(args, "--env", envName)
	}
	for k, v := range labels {
		args = append(args, "--label", k+"="+v)
	}
	if s.Limits.CPUs != "" {
		args = append(args, "--cpus", s.Limits.CPUs)
	}
	if s.Limits.Memory != "" {
		args = append(args, "--memory", s.Limits.Memory)
	}
	if s.Limits.PIDs > 0 {
		args = append(args, "--pids-limit", strconv.Itoa(s.Limits.PIDs))
	}
	if s.Entrypoint != "" {
		args = append(args, "--entrypoint", s.Entrypoint)
	}
	args = append(args, image)
	args = append(args, s.Command...)
	if out, err := d.command(ctx, s.Env, args...); err != nil {
		return fmt.Errorf("backend: sidecar %s create: %w: %s", s.Name, err, firstLine(out))
	}
	// Plain files ride docker cp into the created container; the read-only
	// rootfs still accepts a volume or tmpfs destination (moved from v0
	// cellSquid's squid.conf staging).
	for _, f := range s.Files {
		if f.ViaExec {
			continue
		}
		tmp, err := d.stageFile(f.Content)
		if err != nil {
			return err
		}
		if out, err := d.command(ctx, nil, "cp", tmp, name+":"+f.Target); err != nil {
			os.Remove(tmp)
			return fmt.Errorf("backend: sidecar %s cp %s: %w: %s", s.Name, f.Target, err, firstLine(out))
		}
		os.Remove(tmp)
	}
	if s.Bridge {
		if out, err := d.command(ctx, nil, "network", "connect", "bridge", name); err != nil {
			return fmt.Errorf("backend: sidecar %s bridge: %w: %s", s.Name, err, firstLine(out))
		}
	}
	if out, err := d.command(ctx, nil, "start", name); err != nil {
		return fmt.Errorf("backend: sidecar %s start: %w: %s", s.Name, err, firstLine(out))
	}
	// Secret payloads never touch argv or the controller disk: they stream
	// through the sidecar's cellinit stdin into its tmpfs (moved from v0
	// cellCredProxy's rules write).
	for _, f := range s.Files {
		if !f.ViaExec {
			continue
		}
		cmd := exec.CommandContext(ctx, d.cfg.Docker, "exec", "-i", name, "/cellinit", "write", f.Target) // #nosec G204 -- configured binary, internal argv
		cmd.Stdin = bytes.NewReader(f.Content)
		if out, err := cmd.CombinedOutput(); err != nil {
			return fmt.Errorf("backend: sidecar %s secret file: %w: %s", s.Name, err, firstLine(out))
		}
	}
	if s.WaitPort > 0 {
		if err := d.waitListen(ctx, name, s.WaitPort); err != nil {
			return err
		}
	}
	return nil
}

// stageFile writes one sidecar payload world-readable under the configured
// state root (or OS temp) so docker cp can read it; the caller removes it.
func (d *dockerBackend) stageFile(content []byte) (string, error) {
	dir := d.cfg.StateRoot
	if dir == "" {
		dir = os.TempDir()
	}
	tmp, err := os.CreateTemp(dir, ".sidecar-*")
	if err != nil {
		return "", err
	}
	if _, err := tmp.Write(content); err != nil {
		tmp.Close()
		os.Remove(tmp.Name())
		return "", err
	}
	if err := tmp.Close(); err != nil {
		return "", err
	}
	if err := os.Chmod(tmp.Name(), 0o644); err != nil {
		os.Remove(tmp.Name())
		return "", err
	}
	return tmp.Name(), nil
}

// waitListen probes the sidecar's procfs for its listen port — the moved v0
// waitProxyListen: docker start returns before the daemon finishes init, and
// a worker that connects in that window sees connection refused.
func (d *dockerBackend) waitListen(ctx context.Context, name string, port int) error {
	portHex := fmt.Sprintf(":%04X", port)
	deadline := time.Now().Add(10 * time.Second)
	for {
		if _, err := d.command(ctx, nil, "exec", name, "grep", "-q", portHex, "/proc/net/tcp", "/proc/net/tcp6"); err == nil {
			return nil
		}
		if time.Now().After(deadline) {
			return fmt.Errorf("backend: sidecar %s port %d not listening", name, port)
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(100 * time.Millisecond):
		}
	}
}

// Stop removes the workload plus everything it owned: sidecars answer the
// same key label, the worker's own network is named deterministically and
// its volumes carry the key label (moved from v0 removeCell).
func (d *dockerBackend) Stop(ctx context.Context, id string) error {
	out, err := d.command(ctx, nil, "ps", "-aq", "--filter", "label=hermes-hub.key="+id)
	if err != nil {
		return fmt.Errorf("backend: stop %s: %w: %s", id, err, firstLine(out))
	}
	ids := strings.Fields(string(out))
	if len(ids) > 0 {
		if out, err := d.command(ctx, nil, append([]string{"rm", "-f"}, ids...)...); err != nil && !isMissingContainer(out) {
			return fmt.Errorf("backend: stop %s: %w: %s", id, err, firstLine(out))
		}
	}
	_, _ = d.command(ctx, nil, "network", "rm", d.workerNet(id))
	if out, err := d.command(ctx, nil, "volume", "ls", "-q", "--filter", "label=hermes-hub.key="+id); err == nil {
		if vols := strings.Fields(string(out)); len(vols) > 0 {
			_, _ = d.command(ctx, nil, append([]string{"volume", "rm", "-f"}, vols...)...)
		}
	}
	return nil
}

// cleanup is Stop on the start-failure path: whatever partially exists goes.
func (d *dockerBackend) cleanup(ctx context.Context, id string) {
	_ = d.Stop(ctx, id)
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
		Name:  d.container(id),
	}, nil
}

// execArgs builds the `docker exec` argv: env names ride argv while
// values stay out of it entirely (X4) — the CLI forwards each -e NAME
// from its own process environment, so a value in argv would mean the
// builder itself leaked it.
func (d *dockerBackend) execArgs(id string, spec ExecSpec) ([]string, error) {
	target := d.container(id)
	if spec.Sidecar != "" {
		if !sidecarNamePattern.MatchString(spec.Sidecar) {
			return nil, fmt.Errorf("%w: sidecar name %q", ErrSpec, spec.Sidecar)
		}
		target += "-" + spec.Sidecar
	}
	args := []string{"exec"}
	if spec.Stdin != nil {
		args = append(args, "-i")
	}
	for _, entry := range spec.Env {
		envName, _, ok := strings.Cut(entry, "=")
		if !ok || envName == "" {
			return nil, fmt.Errorf("%w: malformed env entry %q", ErrSpec, entry)
		}
		args = append(args, "-e", envName)
	}
	if spec.Dir != "" {
		args = append(args, "-w", spec.Dir)
	}
	args = append(args, target)
	return append(args, spec.Argv...), nil
}

// Exec runs `docker exec` against the workload or one of its named sidecars:
// argv goes to the container uninterpreted (no shell unless the caller's own
// argv is one), env names ride argv while values travel in the CLI's own
// environment (never argv — X4), and the streams come back separately
// bounded.
func (d *dockerBackend) Exec(ctx context.Context, id string, spec ExecSpec) (ExecResult, error) {
	if len(spec.Argv) == 0 {
		return ExecResult{}, fmt.Errorf("%w: exec argv required", ErrSpec)
	}
	timeout := spec.Timeout
	if timeout <= 0 {
		timeout = 30 * time.Second
	}
	limit := spec.MaxOutput
	if limit <= 0 {
		limit = 1 << 20
	}
	args, err := d.execArgs(id, spec)
	if err != nil {
		return ExecResult{}, err
	}
	callCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	cmd := exec.CommandContext(callCtx, d.cfg.Docker, args...) // #nosec G204 -- configured docker binary, argv built internally
	cmd.Env = dockerCLIEnv(spec.Env)
	if spec.Stdin != nil {
		cmd.Stdin = bytes.NewReader(spec.Stdin)
	}
	stdout := &bounded{limit: limit}
	stderr := &bounded{limit: limit}
	cmd.Stdout, cmd.Stderr = stdout, stderr
	err = cmd.Run()
	if callCtx.Err() != nil {
		return ExecResult{}, fmt.Errorf("exec %s: %w", id, callCtx.Err())
	}
	code := 0
	if err != nil {
		var exitErr *exec.ExitError
		if !errors.As(err, &exitErr) {
			return ExecResult{}, fmt.Errorf("exec %s: %w", id, err)
		}
		code = exitErr.ExitCode()
	}
	return ExecResult{
		ExitCode: code, Stdout: stdout.buf.Bytes(), Stderr: stderr.buf.Bytes(),
		StdoutTruncated: stdout.truncated, StderrTruncated: stderr.truncated,
	}, nil
}

// workerProfile is the inspect shape the reuse guard re-verifies — the
// moved v0 genericContainer fields cellProfileProblem compared.
type workerProfile struct {
	State struct {
		Running bool
	}
	Config struct {
		User   string
		Image  string
		Labels map[string]string
	}
	HostConfig struct {
		Privileged     bool
		PidMode        string
		ReadonlyRootfs bool
		NetworkMode    string
		Runtime        string
		CapAdd         []string
		CapDrop        []string
		SecurityOpt    []string
		Tmpfs          map[string]string
	}
	Mounts []struct {
		Type        string
		Source      string
		Destination string
		RW          bool
	}
}

// VerifyWorker re-inspects the worker and names the first violated
// isolation claim — the moved v0 cellProfileProblem: running state,
// hardening profile, image, user, runtime, network, labels and the exact
// mount table the spec declared (tmpfs declarations count from either
// HostConfig.Tmpfs or .Mounts, matching both docker stores).
func (d *dockerBackend) VerifyWorker(ctx context.Context, id string, spec Spec) error {
	out, err := d.command(ctx, nil, "inspect", d.container(id))
	if err != nil {
		return fmt.Errorf("backend: worker inspect: %w: %s", err, firstLine(out))
	}
	var docs []workerProfile
	if json.Unmarshal(out, &docs) != nil || len(docs) != 1 {
		return errors.New("backend: worker inspect unreadable")
	}
	c := docs[0]
	problem := func(p string) error {
		return fmt.Errorf("backend: worker %s profile: %s", id, p)
	}
	if !c.State.Running {
		return problem("state")
	}
	if c.HostConfig.Privileged {
		return problem("privileged")
	}
	if c.HostConfig.PidMode != "" {
		return problem("host-pid")
	}
	if !c.HostConfig.ReadonlyRootfs {
		return problem("writable-root")
	}
	if len(c.HostConfig.CapAdd) != 0 || len(c.HostConfig.CapDrop) != 1 || c.HostConfig.CapDrop[0] != "ALL" {
		return problem("caps")
	}
	if !containsStr(c.HostConfig.SecurityOpt, "no-new-privileges:true") && !containsStr(c.HostConfig.SecurityOpt, "no-new-privileges=true") {
		return problem("security-opt")
	}
	if spec.SeccompProfile != "" && !seccompMatches(c.HostConfig.SecurityOpt, spec.SeccompProfile) {
		return problem("seccomp")
	}
	image := spec.Image
	if !dockerImageID(image) {
		resolved, err := d.resolveImage(ctx, image)
		if err != nil {
			return problem("image-resolve")
		}
		image = resolved
	}
	if c.Config.Image != image && !strings.EqualFold(c.Config.Image, image) {
		return problem("image")
	}
	user := c.Config.User
	if user == "" || user == "0" || user == "0:0" || user == "root" {
		return problem("root-user")
	}
	if spec.User != "" && user != spec.User {
		return problem("user")
	}
	if spec.OCIRuntime != "" && spec.OCIRuntime != "runc" && c.HostConfig.Runtime != spec.OCIRuntime {
		return problem("runtime")
	}
	// The worker's first network is the create-time --network: its own
	// internal sidecar net, the first declared edge, or none.
	wantNet := "none"
	switch {
	case len(spec.Sidecars) > 0:
		wantNet = d.workerNet(spec.Key)
	case len(spec.Edges) > 0:
		wantNet = DockerNetworkName(d.cfg.NetworkPrefix, spec.Space, spec.Edges[0])
	}
	if c.HostConfig.NetworkMode != wantNet {
		return problem("network")
	}
	labels := c.Config.Labels
	if labels[dockerManagedLabel] != "spawn-controller" || labels["hermes-hub.key"] != spec.Key || labels["hermes-hub.org"] != spec.Org || labels["hermes-hub.owner"] != spec.Owner || labels["hermes-hub.space"] != spec.Space {
		return problem("labels")
	}
	// The mount table must contain nothing the spec did not declare: each
	// bind by source and writability, each tmpfs target, each read-only
	// image mount. Anything else denies — this is what keeps state,
	// credential stores and the docker socket out of a worker.
	wantMounts := map[string]bool{}
	tmpfs := map[string]bool{}
	for _, m := range spec.Mounts {
		wantMounts[m.Target] = true
	}
	for _, m := range spec.ImageMounts {
		wantMounts[m.Target] = true
	}
	for _, t := range spec.Tmpfs {
		wantMounts[t.Target] = true
	}
	seen := map[string]bool{}
	for path := range c.HostConfig.Tmpfs {
		tmpfs[path] = true
	}
	for _, mount := range c.Mounts {
		if !wantMounts[mount.Destination] {
			return problem("mount:" + mount.Destination)
		}
		want := specMountOf(spec, mount.Destination)
		switch {
		case want == nil:
			for _, t := range spec.Tmpfs {
				if t.Target == mount.Destination {
					if mount.Type != "tmpfs" {
						return problem("tmpfs:" + mount.Destination)
					}
					tmpfs[mount.Destination] = true
				}
			}
		case want.kind == "image":
			if mount.Type != "image" || mount.RW {
				return problem("toolset:" + mount.Destination)
			}
		default:
			if mount.Type != "bind" || mount.RW != !want.ro || !samePath(mount.Source, want.source) {
				return problem("bind:" + mount.Destination)
			}
		}
		seen[mount.Destination] = true
	}
	for _, t := range spec.Tmpfs {
		if !tmpfs[t.Target] {
			return problem("tmpfs:" + t.Target)
		}
	}
	for _, m := range spec.Mounts {
		if !seen[m.Target] {
			return problem("mount-missing:" + m.Target)
		}
	}
	for _, m := range spec.ImageMounts {
		if !seen[m.Target] {
			return problem("toolset-missing:" + m.Target)
		}
	}
	return nil
}

// specMountOf returns the declared bind or image mount for a target; a nil
// result means the target was declared as tmpfs.
func specMountOf(spec Spec, target string) *struct {
	source string
	ro     bool
	kind   string
} {
	for _, m := range spec.Mounts {
		if m.Target == target {
			return &struct {
				source string
				ro     bool
				kind   string
			}{source: filepath.Clean(m.Source), ro: m.ReadOnly, kind: "bind"}
		}
	}
	for _, m := range spec.ImageMounts {
		if m.Target == target {
			return &struct {
				source string
				ro     bool
				kind   string
			}{kind: "image"}
		}
	}
	return nil
}

// samePath reports two bind sources equal after cleaning and separator
// normalization — the daemon reports its resolved host path.
func samePath(a, b string) bool {
	clean := func(p string) string { return filepath.ToSlash(filepath.Clean(p)) }
	return strings.EqualFold(clean(a), clean(b))
}

// seccompMatches reports whether the security opts carry the spec's
// seccomp profile — daemon renders both "seccomp=<path>" and the
// "seccomp=<path>" flag form.
func seccompMatches(opts []string, profile string) bool {
	for _, opt := range opts {
		if strings.HasPrefix(opt, "seccomp=") && samePath(strings.TrimPrefix(opt, "seccomp="), profile) {
			return true
		}
	}
	return false
}

func containsStr(list []string, want string) bool {
	for _, v := range list {
		if v == want {
			return true
		}
	}
	return false
}

func (d *dockerBackend) List(ctx context.Context) ([]Runtime, error) {
	out, err := d.command(ctx, nil, "ps", "-a", "--filter", "label="+dockerManagedLabel, "--format", "{{json .}}")
	if err != nil {
		// A daemon error is not an empty daemon: callers must not mark
		// runtimes missing from a list they never got.
		return nil, fmt.Errorf("backend: list: %w: %s", err, firstLine(out))
	}
	return d.parseList(string(out)), nil
}

// parseList keeps only this backend's own containers from the daemon's
// managed-label dump: the label is shared across backends, so a foreign
// prefix is another backend's workload and a sidecar label marks a
// companion that lives and dies with its worker — listing either as an
// orphan would have the reconciler stop a live cell mid-exec.
func (d *dockerBackend) parseList(out string) []Runtime {
	var list []Runtime
	for _, line := range strings.Split(out, "\n") {
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
		if !strings.HasPrefix(row.Names, d.cfg.ContainerPrefix) {
			continue
		}
		rt := Runtime{ID: strings.TrimPrefix(row.Names, d.cfg.ContainerPrefix), State: row.State}
		sidecar := false
		for _, pair := range strings.Split(row.Labels, ",") {
			k, v, _ := strings.Cut(pair, "=")
			switch k {
			case "hermes-hub.sidecar":
				sidecar = true
			case "hermes-hub.space":
				rt.Space = v
			case "hermes-hub.agent":
				rt.Agent = v
			}
		}
		if sidecar {
			continue
		}
		list = append(list, rt)
	}
	return list
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
