package toolhub

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"os/exec"
	"strings"
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
