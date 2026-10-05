package toolhub

import (
	"bufio"
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os/exec"
	"strings"
	"sync"
	"sync/atomic"
)

// AgentExecRequest is the one-shot contract between the ToolHub gateway and
// the private executor inside the owning runtime. The admitted capability
// scopes travel verbatim: the executor enforces them as os.Root boundaries
// and can never widen a grant.
type AgentExecRequest struct {
	Tool           string            `json:"tool"`
	Arguments      map[string]any    `json:"arguments"`
	Scopes         []CapabilityScope `json:"scopes,omitempty"`
	TimeoutSeconds int               `json:"timeout_seconds,omitempty"`
}

// AgentExecResult is the executor's bounded response. Error carries a
// tool-level failure (denied scope, unknown tool); transport failures return
// an error instead.
type AgentExecResult struct {
	Result any    `json:"result,omitempty"`
	Error  string `json:"error,omitempty"`
}

// AgentExecFunc is the private executor channel. Host entrypoints wire it to
// a transport the agent cannot reach (docker exec today); it stays nil in
// tests and unconfigured deployments, where dispatch fails closed.
type AgentExecFunc func(ctx context.Context, effective EffectiveBinding, request AgentExecRequest) (AgentExecResult, error)

// AgentExecBackend serves the in-process agent-tools executor: the hub-owned
// file, document, image, artifact, service, routine, provider and SSH tools
// that run inside the owning runtime's workspace island.
type AgentExecBackend struct {
	Exec AgentExecFunc
	// Scratch serves Sandboxed tools through the disposable-workload channel;
	// nil fails closed like a missing Exec.
	Scratch ScratchExecFunc
	// Fence is the apply-time authority recheck the endpoint wires to the
	// store's revision check; exports fail closed without it.
	Fence ScratchFenceFunc
}

func (b AgentExecBackend) Call(ctx context.Context, effective EffectiveBinding, tool ToolSpec, arguments map[string]any) (BackendResult, error) {
	return b.CallEnv(ctx, effective, tool, arguments, nil)
}

func (b AgentExecBackend) CallEnv(ctx context.Context, effective EffectiveBinding, tool ToolSpec, arguments map[string]any, _ map[string]string) (BackendResult, error) {
	if effective.Definition.Transport != AgentTools {
		return BackendResult{}, fmt.Errorf("%w: agent executor received %s transport", ErrInvalid, effective.Definition.Transport)
	}
	if !tool.Sandboxed && b.Exec == nil || tool.Sandboxed && b.Scratch == nil {
		return BackendResult{}, ErrIsolation
	}
	var out AgentExecResult
	var err error
	if tool.Sandboxed {
		out, err = b.Scratch(ctx, effective, AgentExecRequest{
			Tool:           tool.Name,
			Arguments:      arguments,
			Scopes:         effective.CapabilityScopes,
			TimeoutSeconds: effective.Definition.Execution.TimeoutSeconds,
		}, b.Fence)
	} else {
		out, err = b.Exec(ctx, effective, AgentExecRequest{
			Tool:           tool.Name,
			Arguments:      arguments,
			Scopes:         effective.CapabilityScopes,
			TimeoutSeconds: effective.Definition.Execution.TimeoutSeconds,
		})
	}
	if err != nil {
		return BackendResult{}, fmt.Errorf("%w: agent executor channel: %v", ErrIsolation, err)
	}
	if out.Error != "" {
		return BackendResult{IsError: true, Text: out.Error}, nil
	}
	return BackendResult{Structured: out.Result}, nil
}

const agentExecMaxResponse = 4 << 20

// AgentExecFrame carries one call on the persistent tools-daemon channel. The
// request fields are the same verbatim admitted contract the one-shot
// tools-exec consumes; id correlates multiplexed replies. A frame with Cancel
// set carries no request: it asks the daemon to stop the in-flight call with
// the same id so a cancelled admission does not keep running unobserved.
type AgentExecFrame struct {
	ID     uint64 `json:"id"`
	Cancel bool   `json:"cancel,omitempty"`
	AgentExecRequest
}

// AgentExecReply is the daemon's demultiplexed response: id selects the
// pending caller; the embedded result is the bounded executor contract.
type AgentExecReply struct {
	ID uint64 `json:"id"`
	AgentExecResult
}

// agentExecSession is one live tools-daemon: a single `docker exec -i`
// channel into the owning runtime container multiplexing framed calls. When
// the process dies every pending call fails and the pool respawns lazily.
// daemonOutcome distinguishes a demultiplexed reply from a transport failure:
// session death resolves pending calls with an error, never a fake result.
type daemonOutcome struct {
	reply AgentExecReply
	err   error
}

type agentExecSession struct {
	pendingMu sync.Mutex
	pending   map[uint64]chan daemonOutcome
	dead      chan struct{}
	deadErr   error
	seq       atomic.Uint64

	writeMu sync.Mutex
	stdin   io.WriteCloser

	cmd    *exec.Cmd
	stderr *bytes.Buffer
}

func (s *agentExecSession) register(id uint64) (chan daemonOutcome, error) {
	s.pendingMu.Lock()
	defer s.pendingMu.Unlock()
	select {
	case <-s.dead:
		return nil, s.deadErr
	default:
	}
	ch := make(chan daemonOutcome, 1)
	s.pending[id] = ch
	return ch, nil
}

func (s *agentExecSession) unregister(id uint64) {
	s.pendingMu.Lock()
	delete(s.pending, id)
	s.pendingMu.Unlock()
}

// die fails every pending call exactly once and marks the session for lazy
// respawn; the caller never revives a dead channel.
func (s *agentExecSession) die(err error) {
	s.pendingMu.Lock()
	defer s.pendingMu.Unlock()
	select {
	case <-s.dead:
		return
	default:
	}
	if err == nil {
		err = errors.New("tools-daemon stopped")
	}
	s.deadErr = err
	close(s.dead)
	for id, ch := range s.pending {
		ch <- daemonOutcome{err: fmt.Errorf("executor channel: %w", err)}
		delete(s.pending, id)
	}
}

// readReplies demultiplexes daemon reply frames until the stream or the
// daemon itself ends, then sinks the session.
func (s *agentExecSession) readReplies(stdout io.Reader) {
	reader := bufio.NewReaderSize(stdout, 64*1024)
	var readErr error
	for {
		line, err := reader.ReadBytes('\n')
		if len(line) > agentExecMaxResponse+1024 {
			readErr = errors.New("tools-daemon reply exceeds bound")
			break
		}
		if len(line) > 0 {
			var reply AgentExecReply
			if json.Unmarshal(line, &reply) == nil {
				s.pendingMu.Lock()
				if ch, ok := s.pending[reply.ID]; ok {
					ch <- daemonOutcome{reply: reply}
					delete(s.pending, reply.ID)
				}
				s.pendingMu.Unlock()
			}
		}
		if err != nil {
			readErr = err
			break
		}
	}
	waitErr := s.cmd.Wait()
	if readErr == nil || errors.Is(readErr, io.EOF) {
		readErr = waitErr
	}
	detail := strings.TrimSpace(s.stderr.String())
	if len(detail) > 200 {
		detail = detail[:200]
	}
	if detail != "" {
		readErr = fmt.Errorf("%v: %s", readErr, detail)
	}
	s.die(readErr)
}

// agentExecPool owns persistent executor sessions keyed by resolved container
// name. Each owning runtime keeps its own daemon — per-user by construction;
// a resolver that returns a shared container name would share one session
// across bindings, which is the documented group/shared extension point.
type agentExecPool struct {
	docker   []string
	resolve  func(EffectiveBinding) (string, error)
	mu       sync.Mutex
	sessions map[string]*agentExecSession
}

// NewAgentExecPool builds a lazily-spawning persistent executor pool; Close
// kills every live session (shutdown and tests only — a dead session respawns
// on the next call anyway).
func NewAgentExecPool(dockerArgv []string, resolve func(EffectiveBinding) (string, error)) *agentExecPool {
	if len(dockerArgv) == 0 {
		dockerArgv = []string{"docker"}
	}
	return &agentExecPool{docker: dockerArgv, resolve: resolve, sessions: map[string]*agentExecSession{}}
}

func (p *agentExecPool) session(container string) (*agentExecSession, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if s, ok := p.sessions[container]; ok {
		select {
		case <-s.dead:
			delete(p.sessions, container)
		default:
			return s, nil
		}
	}
	cmd := exec.Command(p.docker[0], append(p.docker[1:], "exec", "-i", container, "hubctl", "tools-daemon")...)
	s := &agentExecSession{pending: map[uint64]chan daemonOutcome{}, dead: make(chan struct{}), stderr: &bytes.Buffer{}}
	stdin, err := cmd.StdinPipe()
	if err != nil {
		return nil, err
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return nil, err
	}
	cmd.Stderr = &boundedWriter{w: s.stderr, max: 4096}
	if err := cmd.Start(); err != nil {
		return nil, fmt.Errorf("tools-daemon spawn: %w", err)
	}
	s.cmd = cmd
	s.stdin = stdin
	go s.readReplies(stdout)
	p.sessions[container] = s
	return s, nil
}

// boundedWriter caps captured daemon stderr for error detail.
type boundedWriter struct {
	w   *bytes.Buffer
	max int
}

func (b *boundedWriter) Write(p []byte) (int, error) {
	if b.w.Len()+len(p) > b.max {
		p = p[:max(0, b.max-b.w.Len())]
	}
	return b.w.Write(p)
}

func (p *agentExecPool) call(ctx context.Context, effective EffectiveBinding, request AgentExecRequest) (AgentExecResult, error) {
	container, err := p.resolve(effective)
	if err != nil {
		return AgentExecResult{}, err
	}
	s, err := p.session(container)
	if err != nil {
		return AgentExecResult{}, err
	}
	id := s.seq.Add(1)
	replyCh, err := s.register(id)
	if err != nil {
		return AgentExecResult{}, err
	}
	frame, err := json.Marshal(AgentExecFrame{ID: id, AgentExecRequest: request})
	if err != nil {
		s.unregister(id)
		return AgentExecResult{}, err
	}
	s.writeMu.Lock()
	_, err = s.stdin.Write(append(frame, '\n'))
	s.writeMu.Unlock()
	if err != nil {
		s.unregister(id)
		s.die(fmt.Errorf("tools-daemon write: %w", err))
		return AgentExecResult{}, err
	}
	select {
	case outcome := <-replyCh:
		if outcome.err != nil {
			return AgentExecResult{}, outcome.err
		}
		return outcome.reply.AgentExecResult, nil
	case <-ctx.Done():
		// Propagate the cancel into the daemon so the in-flight call stops
		// inside the runtime instead of finishing unobserved. Best effort:
		// a dead session kills the daemon process anyway.
		if frame, err := json.Marshal(AgentExecFrame{ID: id, Cancel: true}); err == nil {
			s.writeMu.Lock()
			_, _ = s.stdin.Write(append(frame, '\n'))
			s.writeMu.Unlock()
		}
		s.unregister(id)
		return AgentExecResult{}, ctx.Err()
	}
}

// Exec adapts the pool to the AgentExecFunc contract.
func (p *agentExecPool) Exec(ctx context.Context, effective EffectiveBinding, request AgentExecRequest) (AgentExecResult, error) {
	return p.call(ctx, effective, request)
}

// Close kills every live daemon session; used on shutdown and by tests.
func (p *agentExecPool) Close() {
	p.mu.Lock()
	sessions := make([]*agentExecSession, 0, len(p.sessions))
	for _, s := range p.sessions {
		sessions = append(sessions, s)
	}
	p.sessions = map[string]*agentExecSession{}
	p.mu.Unlock()
	for _, s := range sessions {
		_ = s.stdin.Close()
		_ = s.cmd.Process.Kill()
		s.die(errors.New("tools-daemon pool closed"))
	}
}

// DaemonAgentExec serves admitted agent-tools calls over one persistent
// tools-daemon per owning runtime container instead of spawning `docker exec`
// per call. The channel is identical in kind — docker exec never crosses the
// agent network — only its lifetime changes: spawn cost amortizes across
// calls while each frame still carries the verbatim admitted scopes.
func DaemonAgentExec(dockerArgv []string, resolve func(EffectiveBinding) (string, error)) AgentExecFunc {
	return NewAgentExecPool(dockerArgv, resolve).Exec
}

// DockerAgentExec invokes `hubctl tools-exec` inside the owning runtime
// container over the Docker engine API. The channel never crosses the agent
// network and needs no listener, port or token inside the runtime: the agent
// cannot reach the engine socket, so it cannot invoke the executor or forge
// capability scopes.
func DockerAgentExec(dockerArgv []string, container func(EffectiveBinding) (string, error)) AgentExecFunc {
	if len(dockerArgv) == 0 {
		dockerArgv = []string{"docker"}
	}
	return func(ctx context.Context, effective EffectiveBinding, request AgentExecRequest) (AgentExecResult, error) {
		name, err := container(effective)
		if err != nil {
			return AgentExecResult{}, err
		}
		body, err := json.Marshal(request)
		if err != nil {
			return AgentExecResult{}, err
		}
		cmd := exec.CommandContext(ctx, dockerArgv[0], append(dockerArgv[1:], "exec", "-i", name, "hubctl", "tools-exec")...)
		cmd.Stdin = bytes.NewReader(body)
		var stdout, stderr bytes.Buffer
		cmd.Stdout = &stdout
		cmd.Stderr = &stderr
		if err := cmd.Run(); err != nil {
			detail := strings.TrimSpace(stderr.String())
			if len(detail) > 200 {
				detail = detail[:200]
			}
			return AgentExecResult{}, fmt.Errorf("executor failed: %v: %s", err, detail)
		}
		var result AgentExecResult
		if err := json.NewDecoder(io.LimitReader(&stdout, agentExecMaxResponse)).Decode(&result); err != nil {
			return AgentExecResult{}, fmt.Errorf("executor response unparseable: %w", err)
		}
		return result, nil
	}
}

// FixedAgentContainer resolves one explicit runtime container name, rendered
// by the host (HUB_AGENT_EXEC_CONTAINER). A single-name mapping serves the
// owning space only; multi-space deployments resolve per-binding instead.
func FixedAgentContainer(name string) func(EffectiveBinding) (string, error) {
	return func(EffectiveBinding) (string, error) {
		if name == "" {
			return "", fmt.Errorf("%w: agent executor container is not configured", ErrIsolation)
		}
		return name, nil
	}
}

// ManagedAgentContainer derives the supervisor-spawned runtime container for
// the binding's gateway runtime: hermes-context-<sha256(principal\x00context
// \x00"gateway")[:8]>. The supervisor owns this naming contract; if it ever
// changes the executor channel fails closed instead of hitting another
// container.
func ManagedAgentContainer(e EffectiveBinding) (string, error) {
	key := e.Binding.PrincipalID + "\x00" + e.Binding.ContextID + "\x00" + "gateway"
	if e.Binding.PrincipalID == "" || e.Binding.ContextID == "" {
		return "", fmt.Errorf("%w: agent executor binding is incomplete", ErrIsolation)
	}
	sum := sha256.Sum256([]byte(key))
	return "hermes-context-" + hex.EncodeToString(sum[:8]), nil
}
