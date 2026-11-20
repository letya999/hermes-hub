// Package backend owns compute for space workloads: the interface every
// launch backend implements plus the first provider, process.
package backend

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"sync"
	"time"

	"github.com/letya999/hermes-hub/shared/proctree"
)

// Runtime is one space workload managed by the controller.
type Runtime struct {
	ID      string `json:"id"`
	Space   string `json:"space"`
	Agent   string `json:"agent"`
	State   string `json:"state"`
	Address string `json:"address"`
}

// SpawnSpec describes one child runtime to Start.
type SpawnSpec struct {
	Key   string
	Space string
	Agent string
}

// Backend owns compute for space workloads: it starts, inspects, lists and
// stops runtime instances; admission and passports stay elsewhere.
type Backend interface {
	Start(context.Context, SpawnSpec) (Runtime, error)
	Stop(context.Context, string) error
	Inspect(context.Context, string) (Runtime, error)
	List(context.Context) []Runtime
}

// NewProcess returns the process backend: it starts service binaries from
// binDir as local child processes under this process's group. env names the
// operator variables a child may inherit; token is the shared service
// credential the child must serve and callers must present.
func NewProcess(binDir, dataDir string, env []string, token string, ctx context.Context, group *proctree.Group) Backend {
	return &processBackend{
		binDir:   binDir,
		dataDir:  dataDir,
		env:      env,
		token:    token,
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
func (b *processBackend) envFor(spec SpawnSpec, port int) []string {
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
	return env
}

func (b *processBackend) Start(ctx context.Context, spec SpawnSpec) (Runtime, error) {
	name := "harness-runtime"
	if runtime.GOOS == "windows" {
		name += ".exe"
	}
	path := filepath.Join(b.binDir, name)
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
	cmd := exec.CommandContext(b.ctx, path) // #nosec G204 -- own binary directory, fixed service name
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
		return Runtime{}, errors.New("unknown runtime")
	}
	rt := child.rt
	select {
	case <-child.done:
		rt.State = "exited"
	default:
	}
	return rt, nil
}

func (b *processBackend) List(context.Context) []Runtime {
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
	return list
}
