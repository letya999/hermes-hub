// Package backend owns compute for space workloads: the Spec every launch
// backend applies or refuses, plus the providers (process, docker) and the
// shared conformance suite in internal/backend/backendtest.
package backend

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/letya999/hermes-hub/shared/proctree"
)

// Runtime is one space workload managed by the controller. Name is the
// backend's own workload handle (docker: container name) — sidecar
// endpoints are derived from it. HostPort is the loopback-published
// control endpoint the managed-runtime relay chose, when the spec asked
// for one.
type Runtime struct {
	ID       string `json:"id"`
	Space    string `json:"space"`
	Agent    string `json:"agent"`
	State    string `json:"state"`
	Address  string `json:"address"`
	Name     string `json:"name,omitempty"`
	HostPort int    `json:"host_port,omitempty"`
}

// Mount is one resolved bind: a host source mapped to a target inside the
// workload. ReadOnly forbids writes through the bind.
type Mount struct {
	Source   string
	Target   string
	ReadOnly bool
}

// Limits caps the workload's resource envelope; zero fields are unset.
type Limits struct {
	Memory     string
	MemorySwap string
	CPUs       string
	PIDs       int
}

// Tmpfs is one scratch tmpfs mount inside the workload (docker --tmpfs
// target:opts); workers declare /tmp, /work and the package-install homes
// this way instead of through bind Mounts.
type Tmpfs struct {
	Target  string
	Options string
}

// SidecarFile is one payload delivered into a sidecar filesystem. ViaExec
// writes it through the sidecar's cellinit after start — the only channel
// for secret material, it never touches argv or the controller disk;
// otherwise it is docker-cp'd into the created container before start.
type SidecarFile struct {
	Target  string
	Content []byte
	ViaExec bool
}

// SidecarSpec is one companion container on the worker's own internal
// network: same hardening and labels as the worker, created and removed
// with it (moved from v0 cell_net.go cellSquid/cellCredProxy). Bridge
// attaches the sidecar to the docker bridge so it can egress; WaitPort
// probes the sidecar's procfs for the port before Start returns.
// Volumes are docker volumes (Source is a logical name the backend
// namespaces per worker); Mounts are binds.
type SidecarSpec struct {
	Name       string
	Image      string
	User       string
	Entrypoint string
	Command    []string
	Env        []string
	Mounts     []Mount
	Volumes    []Mount
	Tmpfs      []Tmpfs
	Bridge     bool
	Files      []SidecarFile
	WaitPort   int
	Limits     Limits
}

// Spec is a fully resolved workload spec: spawn-controller owns all policy,
// the backend applies the Spec or refuses what it cannot enforce. Env
// entries are NAME=value and never carry secrets; Edges are the named
// networks the workload may reach (empty means none).
type Spec struct {
	Key        string
	Org        string
	Owner      string
	Space      string
	Agent      string
	Image      string
	Command    []string
	Mounts     []Mount
	Env        []string
	Edges      []string
	Limits     Limits
	OCIRuntime string
	// Worker fields (S6): a cell overrides the image's user/entrypoint,
	// declares scratch tmpfs and read-only image mounts (toolboxes), may
	// pin a seccomp profile and own companion sidecars.
	User           string
	Entrypoint     string
	Workdir        string
	Tmpfs          []Tmpfs
	ImageMounts    []Mount
	SeccompProfile string
	Sidecars       []SidecarSpec
	// Labels are extra workload labels the caller stamps on top of the
	// backend's ownership set (e.g. a cell's binding identity).
	Labels map[string]string
	// Managed-runtime fields (CHG-0336 S7c): the v0 managed spawn
	// contract. Networks are literal docker network names the workload
	// joins — existing, Compose-owned, never edge-derived or created by
	// the backend. EnvFiles are --env-file paths the daemon reads —
	// their secrets never enter argv or the spec. Port is the workload's
	// service port the Relay forwards to; the backend picks the loopback
	// host port and reports it on Runtime.HostPort. RelayHosts are the
	// reviewed relay aliases the backend resolves on Networks[0] and pins
	// as --add-host entries (v0 sandboxRelayHosts). A spec carrying none
	// of these is a plain worker/runtime claim.
	Networks   []string
	EnvFiles   []string
	Port       int
	Relay      *RelaySpec
	RelayHosts []string
}

// Managed reports whether the spec is a managed-runtime claim — the v0
// spawn contract rather than a plain worker/runtime workload.
func (s Spec) Managed() bool {
	return s.Relay != nil || len(s.Networks) > 0 || len(s.EnvFiles) > 0
}

// RelaySpec is the managed runtime's control relay (v0 spawnControlRelay):
// a dual-homed companion on the workload's first literal network plus the
// control network, publishing its relay port on the host loopback — the
// only inbound route to an internal-network runtime. The backend names it
// <workload>-ctl and targets http://<workload>:Spec.Port.
type RelaySpec struct {
	Image    string
	Control  string // control network name (created as a plain bridge)
	MountDir string // optional identity bind for the relay's own host files
	Limits   Limits
}

// ExecSpec is one bounded command inside a running workload: argv never
// passes through a shell, env values travel through the exec's own
// environment (only names reach argv), Dir stays inside the workload and
// Stdin/Timeout/MaxOutput bound the call (S6).
type ExecSpec struct {
	Argv      []string
	Env       []string
	Dir       string
	Stdin     []byte
	Timeout   time.Duration
	MaxOutput int64
	// Sidecar names the companion the exec targets; "" is the workload.
	Sidecar string
}

// ExecResult reports one exec's outcome: the exit code, both streams
// separately, and whether either was cut at MaxOutput.
type ExecResult struct {
	ExitCode        int
	Stdout          []byte
	Stderr          []byte
	StdoutTruncated bool
	StderrTruncated bool
}

// ErrNotFound is the typed Inspect/Stop miss S4 maps to `missing`.
var ErrNotFound = errors.New("backend: runtime not found")

// ErrUntrusted marks refusals the installation trust flag demands (X6).
var ErrUntrusted = errors.New("backend: refused by installation trust policy")

// ErrSpec refuses a Spec a backend cannot apply without ambiguity: mount
// sources and targets travel inside the docker --mount CSV grammar, where
// ',' reads as another option, '=' splits a value and '"' or a newline
// breaks the flag — carrying one would inject options the caller never
// declared (mount_option_injection_refused).
var ErrSpec = errors.New("backend: spec refused")

// validateSpec refuses the parts of a Spec that cannot cross into a
// backend argv unambiguously. It runs at the top of every Start, before
// trust guards, disk writes or daemon calls.
func validateSpec(spec Spec) error {
	check := func(what, source, target string) error {
		if strings.ContainsAny(source, ",=\"\n\r") || strings.ContainsAny(target, ",=\"\n\r") {
			return fmt.Errorf("%w: %s %q -> %q carries option-breaking characters", ErrSpec, what, source, target)
		}
		return nil
	}
	for _, m := range spec.Mounts {
		if err := check("mount", m.Source, m.Target); err != nil {
			return err
		}
	}
	for _, m := range spec.ImageMounts {
		if err := check("image mount", m.Source, m.Target); err != nil {
			return err
		}
	}
	for _, t := range spec.Tmpfs {
		if strings.ContainsAny(t.Target, ":,\"\n\r") || strings.ContainsAny(t.Options, "\"\n\r") {
			return fmt.Errorf("%w: tmpfs %q carries option-breaking characters", ErrSpec, t.Target)
		}
	}
	for _, f := range spec.EnvFiles {
		if f == "" || strings.ContainsAny(f, "\"\n\r") {
			return fmt.Errorf("%w: env file %q carries option-breaking characters", ErrSpec, f)
		}
	}
	for _, n := range spec.Networks {
		if n == "" || strings.ContainsAny(n, ",=\"\n\r") {
			return fmt.Errorf("%w: network %q carries option-breaking characters", ErrSpec, n)
		}
	}
	for _, h := range spec.RelayHosts {
		if !relayNamePattern.MatchString(h) {
			return fmt.Errorf("%w: relay host %q", ErrSpec, h)
		}
	}
	if spec.Relay != nil {
		if spec.Relay.Control == "" || strings.ContainsAny(spec.Relay.Control, ",=\"\n\r") {
			return fmt.Errorf("%w: relay control network %q", ErrSpec, spec.Relay.Control)
		}
		if spec.Relay.MountDir != "" && strings.ContainsAny(spec.Relay.MountDir, ":\"\n\r") {
			return fmt.Errorf("%w: relay mount %q carries option-breaking characters", ErrSpec, spec.Relay.MountDir)
		}
		if spec.Port <= 0 || spec.Port > 65535 {
			return fmt.Errorf("%w: relay needs the workload port", ErrSpec)
		}
	}
	for _, s := range spec.Sidecars {
		if !sidecarNamePattern.MatchString(s.Name) {
			return fmt.Errorf("%w: sidecar name %q", ErrSpec, s.Name)
		}
		for _, m := range s.Mounts {
			if err := check("sidecar mount", m.Source, m.Target); err != nil {
				return err
			}
		}
		for _, m := range s.Volumes {
			if err := check("sidecar volume", m.Source, m.Target); err != nil {
				return err
			}
		}
		for _, t := range s.Tmpfs {
			if strings.ContainsAny(t.Target, ":,\"\n\r") || strings.ContainsAny(t.Options, "\"\n\r") {
				return fmt.Errorf("%w: sidecar tmpfs %q carries option-breaking characters", ErrSpec, t.Target)
			}
		}
		for _, f := range s.Files {
			if !filepath.IsAbs(f.Target) || strings.ContainsAny(f.Target, ":\"\n\r") {
				return fmt.Errorf("%w: sidecar file target %q", ErrSpec, f.Target)
			}
		}
	}
	return nil
}

var sidecarNamePattern = regexp.MustCompile(`^[a-z][a-z0-9-]{0,31}$`)

// relayNamePattern bounds the relay aliases the managed spec asks the
// backend to resolve — the same charset docker's own aliases use.
var relayNamePattern = regexp.MustCompile(`^[a-z][a-z0-9._-]{0,63}$`)

// Backend owns compute for space workloads: it starts, inspects, lists and
// stops runtime instances; admission and passports stay elsewhere. List's
// error is the difference between "nothing is running" and "the daemon did
// not answer" — a reconciler must never read the latter as the former.
// Exec runs one bounded command inside a started workload; VerifyWorker
// re-inspects a live worker against the spec that built it — the reuse
// guard's proof the cell still carries every isolation claim.
type Backend interface {
	Start(context.Context, Spec) (Runtime, error)
	Stop(context.Context, string) error
	Inspect(context.Context, string) (Runtime, error)
	List(context.Context) ([]Runtime, error)
	Exec(context.Context, string, ExecSpec) (ExecResult, error)
	VerifyWorker(context.Context, string, Spec) error
}

// NewProcess returns the process backend: it starts service binaries from
// binDir as local child processes under this process's group. env names the
// operator variables a child may inherit; token is the shared service
// credential the child must serve and callers must present. An untrusted
// installation is refused outright (X6): process mode offers no isolation.
func NewProcess(binDir, dataDir string, env []string, token string, trusted bool, ctx context.Context, group *proctree.Group) Backend {
	return &processBackend{
		binDir:   binDir,
		dataDir:  dataDir,
		env:      env,
		token:    token,
		trusted:  trusted,
		ctx:      ctx,
		group:    group,
		children: map[string]*childRuntime{},
	}
}

// processBackend starts service binaries from the controller's own binary
// directory as local child processes. env names the operator variables the
// child may inherit; token is the shared service credential the child must
// serve and callers must present; group binds every child to this process.
type processBackend struct {
	binDir   string
	dataDir  string
	env      []string
	token    string
	trusted  bool
	ctx      context.Context
	group    *proctree.Group
	mu       sync.Mutex
	children map[string]*childRuntime
}

type childRuntime struct {
	rt   Runtime
	cmd  *exec.Cmd
	done chan struct{}
}

// envFor derives the child's environment: no operator secrets beyond the
// declared pass-through names, plus computed wiring.
func (b *processBackend) envFor(spec Spec, port int) []string {
	child := filepath.Join(b.dataDir, "runtimes", spec.Key)
	env := []string{
		"HUB_TRUSTED=true", "HUB_SERVICE=harness-runtime",
		"HUB_LISTEN=127.0.0.1:" + strconv.Itoa(port),
		"HUB_DATA_DIR=" + child,
		"HUB_DATABASE_URL=sqlite://" + filepath.ToSlash(filepath.Join(child, "harness-runtime.db")),
		"HUB_SERVICE_TOKEN=" + b.token,
	}
	for _, name := range b.env {
		if value, ok := os.LookupEnv(name); ok {
			env = append(env, name+"="+value)
		}
	}
	for _, file := range spec.EnvFiles {
		lines, err := envFileLines(file)
		if err != nil {
			continue // an unreadable env file must not block the child's own wiring
		}
		env = append(env, lines...)
	}
	return append(env, spec.Env...)
}

// envFileLines parses one KEY=VALUE env file for the process child — the
// same files docker's --env-file reads. Comments and blanks are skipped;
// the file is read under the controller's own filesystem, never the spec's.
func envFileLines(path string) ([]string, error) {
	body, err := os.ReadFile(path) // #nosec G304 -- spec-declared env file
	if err != nil {
		return nil, err
	}
	var out []string
	for _, line := range strings.Split(string(body), "\n") {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "#") || !strings.Contains(line, "=") {
			continue
		}
		out = append(out, line)
	}
	return out, nil
}

// Start applies the spec's trust boundary first: a process child offers no
// isolation, so untrusted installs, network edges and sandboxed OCI runtimes
// are refused before anything touches disk. Spec mounts are never honoured
// as binds — the child only ever sees its own data directory.
func (b *processBackend) Start(ctx context.Context, spec Spec) (Runtime, error) {
	if err := validateSpec(spec); err != nil {
		return Runtime{}, err
	}
	if !b.trusted {
		return Runtime{}, fmt.Errorf("%w: process backend", ErrUntrusted)
	}
	if len(spec.Edges) > 0 {
		return Runtime{}, errors.New("process backend cannot apply network edges")
	}
	if spec.OCIRuntime != "" && spec.OCIRuntime != "runc" {
		return Runtime{}, fmt.Errorf("process backend cannot apply OCI runtime %s", spec.OCIRuntime)
	}
	// Cell-only fields are isolation features a plain process cannot apply;
	// a spec that asks for them is refused rather than silently weakened.
	if len(spec.Sidecars) > 0 {
		return Runtime{}, fmt.Errorf("%w: process backend cannot run sidecars", ErrSpec)
	}
	if len(spec.Tmpfs) > 0 || len(spec.ImageMounts) > 0 || spec.User != "" || spec.Entrypoint != "" || spec.Workdir != "" || spec.SeccompProfile != "" || spec.Limits.MemorySwap != "" {
		return Runtime{}, fmt.Errorf("%w: process backend cannot apply worker isolation fields", ErrSpec)
	}
	name := spec.Image
	if name == "" {
		name = "harness-runtime"
	}
	path := name
	if !filepath.IsAbs(path) {
		if runtime.GOOS == "windows" && !strings.HasSuffix(name, ".exe") {
			name += ".exe"
		}
		path = filepath.Join(b.binDir, name)
	}
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return Runtime{}, err
	}
	port := listener.Addr().(*net.TCPAddr).Port
	_ = listener.Close()
	if err := os.MkdirAll(filepath.Join(b.dataDir, "runtimes", spec.Key), 0o700); err != nil {
		return Runtime{}, err
	}
	// The child's lifetime binds to the backend, not to the request that
	// triggered the spawn: cancelling one caller must not kill the runtime.
	cmd := exec.CommandContext(b.ctx, path, spec.Command...) // #nosec G204 -- resolved spec image, binary directory or absolute
	proctree.Configure(cmd)
	cmd.Cancel = func() error { return proctree.Interrupt(cmd.Process) }
	cmd.WaitDelay = 10 * time.Second
	cmd.Env = b.envFor(spec, port)
	cmd.Stdout, cmd.Stderr = os.Stdout, os.Stderr
	if err := cmd.Start(); err != nil {
		return Runtime{}, fmt.Errorf("spawn %s: %w", spec.Key, err)
	}
	if err := b.group.Assign(cmd); err != nil {
		_ = cmd.Cancel()
		return Runtime{}, err
	}
	child := &childRuntime{cmd: cmd, done: make(chan struct{})}
	child.rt = Runtime{ID: spec.Key, Space: spec.Space, Agent: spec.Agent, State: "starting", Address: fmt.Sprintf("http://127.0.0.1:%d", port)}
	go func() { _ = cmd.Wait(); close(child.done) }()
	b.mu.Lock()
	b.children[spec.Key] = child
	b.mu.Unlock()
	deadline := time.Now().Add(10 * time.Second)
	for {
		req, _ := http.NewRequestWithContext(ctx, http.MethodGet, child.rt.Address+"/healthz", nil)
		resp, err := http.DefaultClient.Do(req)
		if err == nil {
			_ = resp.Body.Close()
			if resp.StatusCode == http.StatusOK {
				child.rt.State = "running"
				return child.rt, nil
			}
		}
		select {
		case <-child.done:
			b.remove(spec.Key)
			return Runtime{}, errors.New("runtime exited before healthy")
		case <-ctx.Done():
			b.remove(spec.Key)
			_ = b.Stop(context.Background(), spec.Key)
			return Runtime{}, ctx.Err()
		case <-time.After(50 * time.Millisecond):
		}
		if time.Now().After(deadline) {
			_ = cmd.Cancel()
			b.remove(spec.Key)
			return Runtime{}, errors.New("runtime did not become healthy")
		}
	}
}

func (b *processBackend) remove(id string) {
	b.mu.Lock()
	delete(b.children, id)
	b.mu.Unlock()
}

func (b *processBackend) Stop(_ context.Context, id string) error {
	b.mu.Lock()
	child, ok := b.children[id]
	if ok {
		delete(b.children, id)
	}
	b.mu.Unlock()
	if !ok {
		return nil
	}
	select {
	case <-child.done:
	default:
		_ = child.cmd.Cancel()
		<-child.done
	}
	return nil
}

func (b *processBackend) Inspect(_ context.Context, id string) (Runtime, error) {
	b.mu.Lock()
	child, ok := b.children[id]
	b.mu.Unlock()
	if !ok {
		return Runtime{}, fmt.Errorf("%w: %s", ErrNotFound, id)
	}
	rt := child.rt
	select {
	case <-child.done:
		rt.State = "exited"
	default:
	}
	return rt, nil
}

func (b *processBackend) List(context.Context) ([]Runtime, error) {
	b.mu.Lock()
	list := make([]Runtime, 0, len(b.children))
	for _, child := range b.children {
		list = append(list, child.rt)
	}
	b.mu.Unlock()
	for i := range list {
		if rt, err := b.Inspect(context.Background(), list[i].ID); err == nil {
			list[i] = rt
		}
	}
	return list, nil
}

// VerifyWorker confirms the process child still runs — a process backend
// offers no container profile to re-check, so a spec carrying worker
// isolation fields is refused the same way Start refuses it.
func (b *processBackend) VerifyWorker(_ context.Context, id string, spec Spec) error {
	if len(spec.Sidecars) > 0 || len(spec.Tmpfs) > 0 || len(spec.ImageMounts) > 0 || spec.User != "" || spec.Entrypoint != "" || spec.Workdir != "" || spec.SeccompProfile != "" {
		return fmt.Errorf("%w: process backend cannot apply worker isolation fields", ErrSpec)
	}
	b.mu.Lock()
	child, ok := b.children[id]
	b.mu.Unlock()
	if !ok {
		return fmt.Errorf("%w: %s", ErrNotFound, id)
	}
	select {
	case <-child.done:
		return fmt.Errorf("%w: %s exited", ErrNotFound, id)
	default:
	}
	return nil
}

// bounded captures one output stream up to MaxOutput; beyond it the stream
// keeps draining (so the child never blocks) while the truncated flag is
// set — the caller learns output was cut without holding unbounded memory.
type bounded struct {
	buf       bytes.Buffer
	limit     int64
	truncated bool
}

func (w *bounded) Write(p []byte) (int, error) {
	room := w.limit - int64(w.buf.Len())
	if room > 0 {
		if int64(len(p)) <= room {
			_, _ = w.buf.Write(p)
		} else {
			_, _ = w.buf.Write(p[:room])
			w.truncated = true
		}
	} else {
		w.truncated = true
	}
	return len(p), nil
}

// Exec runs one command inside the worker's own data directory — the
// trusted-only child S6 grants the process backend. The worker must exist
// (its directory is the proof); argv runs without a shell and Dir may only
// descend into the worker directory, never escape it. Env values travel
// through the child's environment, never its argv.
func (b *processBackend) Exec(ctx context.Context, id string, spec ExecSpec) (ExecResult, error) {
	if !b.trusted {
		return ExecResult{}, fmt.Errorf("%w: process backend", ErrUntrusted)
	}
	if len(spec.Argv) == 0 {
		return ExecResult{}, fmt.Errorf("%w: exec argv required", ErrSpec)
	}
	b.mu.Lock()
	_, ok := b.children[id]
	b.mu.Unlock()
	if !ok {
		return ExecResult{}, fmt.Errorf("%w: %s", ErrNotFound, id)
	}
	if spec.Sidecar != "" {
		return ExecResult{}, fmt.Errorf("%w: process backend has no sidecars", ErrSpec)
	}
	dir := filepath.Join(b.dataDir, "runtimes", id)
	if spec.Dir != "" {
		if filepath.IsAbs(spec.Dir) {
			return ExecResult{}, fmt.Errorf("%w: exec dir must be relative", ErrSpec)
		}
		resolved := filepath.Clean(filepath.Join(dir, spec.Dir))
		if resolved != dir && !strings.HasPrefix(resolved, dir+string(filepath.Separator)) {
			return ExecResult{}, fmt.Errorf("%w: exec dir escapes the worker directory", ErrSpec)
		}
		dir = resolved
	}
	timeout := spec.Timeout
	if timeout <= 0 {
		timeout = 30 * time.Second
	}
	limit := spec.MaxOutput
	if limit <= 0 {
		limit = 1 << 20
	}
	callCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	cmd := exec.CommandContext(callCtx, spec.Argv[0], spec.Argv[1:]...) // #nosec G204 -- trusted exec argv from the worker caller
	proctree.Configure(cmd)
	cmd.Dir = dir
	cmd.Env = append(os.Environ(), spec.Env...)
	if spec.Stdin != nil {
		cmd.Stdin = bytes.NewReader(spec.Stdin)
	}
	stdout := &bounded{limit: limit}
	stderr := &bounded{limit: limit}
	cmd.Stdout, cmd.Stderr = stdout, stderr
	err := cmd.Run()
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
