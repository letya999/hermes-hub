package api

import (
	"archive/tar"
	"bytes"
	"context"
	"crypto/rand"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/letya999/hermes-hub/internal/skillstore"
)

// ScratchPending quarantines bindings whose last sandbox stop was never
// confirmed. A stored entry means an executor may still be live: no
// replacement lease may issue for that binding until the container is
// confirmed gone. Entries clear on a failed docker inspect.
var ScratchPending sync.Map

// ScratchLeasePath mirrors ScratchPending to disk so a ToolHub restart still
// quarantines a binding whose executor was never confirmed gone. Without the
// durable file a restart could drop the quarantine while the workload lived.
var (
	ScratchLeasePath string
	ScratchLeaseMu   sync.Mutex
)

// SetScratchLeasePath enables durable executor leases: existing entries are
// loaded into the quarantine map and every later mutation is persisted
// atomically alongside the file. A corrupt file fails closed — the path is
// not enabled and the in-memory quarantine keeps working.
func SetScratchLeasePath(path string) error {
	ScratchLeaseMu.Lock()
	defer ScratchLeaseMu.Unlock()
	body, err := os.ReadFile(path)
	switch {
	case errors.Is(err, os.ErrNotExist):
	case err != nil:
		return err
	default:
		var leases map[string]string
		if err := json.Unmarshal(body, &leases); err != nil {
			return fmt.Errorf("scratch lease file: %w", err)
		}
		for binding, name := range leases {
			ScratchPending.Store(binding, name)
		}
	}
	ScratchLeasePath = path
	return nil
}

// scratchLeaseSave persists the quarantine map; enabled only after
// SetScratchLeasePath succeeded. Write-then-rename keeps a torn write from
// dropping every pending lease.
func scratchLeaseSave() {
	if ScratchLeasePath == "" {
		return
	}
	leases := map[string]string{}
	ScratchPending.Range(func(k, v any) bool {
		leases[k.(string)] = v.(string)
		return true
	})
	body, err := json.Marshal(leases)
	if err != nil {
		return
	}
	tmp := ScratchLeasePath + ".tmp"
	if os.WriteFile(tmp, body, 0o600) == nil {
		_ = os.Rename(tmp, ScratchLeasePath)
	}
}

func scratchLeaseSet(binding, name string) {
	ScratchLeaseMu.Lock()
	defer ScratchLeaseMu.Unlock()
	ScratchPending.Store(binding, name)
	scratchLeaseSave()
}

func scratchLeaseClear(binding string) {
	ScratchLeaseMu.Lock()
	defer ScratchLeaseMu.Unlock()
	ScratchPending.Delete(binding)
	scratchLeaseSave()
}

// Scratch-pack protocol: the runtime-side `hubctl exec-pack` streams a bounded
// tar of exactly the admitted path scope; the sandbox-side `hubctl
// exec-scratch` runs the script with private scratch and returns a bounded
// result plus an export tar. Neither binary accepts authority fields.
type ScratchPackRequest struct {
	Path   string            `json:"path"`
	Scopes []CapabilityScope `json:"scopes,omitempty"`
}

type ScratchExecRequest struct {
	Command        string `json:"command"`
	TimeoutSeconds int    `json:"timeout_seconds,omitempty"`
	InputsTar      string `json:"inputs_tar_base64,omitempty"`
	// Env carries the one-call skill credential lease (R8.3): the control
	// side fills it from the (principal, skill) store and the workload
	// appends validated entries to its sanitized env. Never argv, never
	// container config — stdin only.
	Env map[string]string `json:"env,omitempty"`
}

// ScratchExecResult is the sandbox runner's bounded response. ExportTar holds
// files the script wrote under /outputs; the control side applies them under
// the admitted prefix through create-only file writes — never deletes, never
// overwrites.
type ScratchExecResult struct {
	ExitCode  int    `json:"exit_code"`
	Stdout    string `json:"stdout,omitempty"`
	Stderr    string `json:"stderr,omitempty"`
	ExportTar string `json:"export_tar_base64,omitempty"`
	Stopped   bool   `json:"stopped"`
	// TreeStopped confirms the workload's process group is fully gone; a
	// false value means orphaned children may still have run until the
	// container removal killed them — reported, never hidden.
	TreeStopped bool `json:"tree_stopped,omitempty"`
}

const (
	scratchPackLimit    = 8 << 20
	scratchResultLimit  = 8 << 20
	scratchExportMaxNum = 64
	scratchExportMaxOne = 2 << 20
	scratchMaxName      = "hermes-scratch-"
)

// ScratchFenceFunc re-verifies admitted authority at apply/export time; the
// endpoint wires it to the store's revision check.
type ScratchFenceFunc func(context.Context, EffectiveBinding) error

// ScratchExecFunc runs one Sandboxed tool call. The fence argument is the
// endpoint-wired reverify hook; nil means "no authority to export".
type ScratchExecFunc func(ctx context.Context, effective EffectiveBinding, request AgentExecRequest, fence ScratchFenceFunc) (AgentExecResult, error)

// DockerScratchExec runs one sandboxed call as a disposable container. The
// agent runtime only ever sees the read pack channel; the workload gets
// neither the runtime mounts, the control plane, nor a network. Stop is
// recorded only after the container is confirmed gone — an unconfirmed
// executor reports pending, never success.
func DockerScratchExec(dockerArgv []string, container func(EffectiveBinding) (string, error), apply AgentExecFunc) ScratchExecFunc {
	if len(dockerArgv) == 0 {
		dockerArgv = []string{"docker"}
	}
	run := func(ctx context.Context, stdin io.Reader, args ...string) (stdout, stderr []byte, err error) {
		if scope := CurrentDockerScope(); scope != nil {
			if gerr := scope.GuardDocker(ctx, args); gerr != nil {
				return nil, nil, gerr
			}
		}
		cmd := exec.CommandContext(ctx, dockerArgv[0], append(dockerArgv[1:], args...)...)
		cmd.Stdin = stdin
		var out, errb bytes.Buffer
		cmd.Stdout = &out
		cmd.Stderr = &errb
		err = cmd.Run()
		return out.Bytes(), errb.Bytes(), err
	}
	return func(ctx context.Context, effective EffectiveBinding, request AgentExecRequest, fence ScratchFenceFunc) (AgentExecResult, error) {
		// Cleanup and quarantine checks run detached from the caller's ctx:
		// a cancelled admission must still be able to verify and remove a
		// possibly-live executor instead of leaving it orphaned.
		cleanCtx, cleanCancel := context.WithTimeout(context.WithoutCancel(ctx), 30*time.Second)
		defer cleanCancel()
		if name, quarantined := ScratchPending.Load(effective.Binding.ToolBindingID); quarantined {
			if _, _, err := run(cleanCtx, nil, "inspect", name.(string)); err == nil {
				return AgentExecResult{}, fmt.Errorf("%w: sandbox executor %s still live; lease quarantined", ErrIsolation, name)
			}
			scratchLeaseClear(effective.Binding.ToolBindingID)
		}
		runtime, err := container(effective)
		if err != nil {
			return AgentExecResult{}, err
		}
		if scope := CurrentDockerScope(); scope != nil && !scope.AdmitSupervisedRuntime(ctx, runtime) {
			return AgentExecResult{}, fmt.Errorf("%w: sandbox exec on foreign container %q denied", ErrIsolation, runtime)
		}
		command, _ := request.Arguments["command"].(string)
		relPath, _ := request.Arguments["path"].(string)
		if strings.TrimSpace(command) == "" || len(command) > 8192 || strings.ContainsRune(command, 0) {
			return AgentExecResult{}, fmt.Errorf("%w: sandboxed command", ErrInvalid)
		}
		if !ValidCapabilityPath(relPath) {
			return AgentExecResult{}, fmt.Errorf("%w: sandboxed path", ErrInvalid)
		}
		scope := ""
		for _, candidate := range request.Scopes {
			if candidate.PathArgument == "path" {
				scope = candidate.PathPrefix
				break
			}
		}
		if scope != "" && relPath != scope && !strings.HasPrefix(relPath, scope+"/") {
			return AgentExecResult{}, fmt.Errorf("%w: sandboxed path %q outside scope %q", ErrInvalid, relPath, scope)
		}
		out, errb, err := run(ctx, nil, "inspect", "-f", "{{.Config.Image}}", runtime)
		if err != nil {
			return AgentExecResult{}, fmt.Errorf("runtime image lookup failed: %s", ShortErr(errb))
		}
		image := strings.TrimSpace(string(out))
		if image == "" || strings.ContainsAny(image, "\x00\r\n") {
			return AgentExecResult{}, fmt.Errorf("%w: runtime image identity", ErrIsolation)
		}
		var inputsB64 string
		if relPath != "" {
			pack, _ := json.Marshal(ScratchPackRequest{Path: relPath, Scopes: request.Scopes})
			tarred, errb, err := run(ctx, bytes.NewReader(pack), "exec", "-i", runtime, "hubctl", "exec-pack")
			if err != nil {
				return AgentExecResult{}, fmt.Errorf("input materialization failed: %s", ShortErr(errb))
			}
			inputsB64 = base64.StdEncoding.EncodeToString(tarred)
		}
		nameSuffix := make([]byte, 8)
		if _, err := rand.Read(nameSuffix); err != nil {
			return AgentExecResult{}, err
		}
		name := scratchMaxName + hex.EncodeToString(nameSuffix)
		timeout := request.TimeoutSeconds
		if timeout <= 0 || timeout > 600 {
			timeout = 300
		}
		lease, cancel := context.WithTimeout(ctx, time.Duration(timeout+15)*time.Second)
		defer cancel()
		body, _ := json.Marshal(ScratchExecRequest{Command: command, TimeoutSeconds: timeout, InputsTar: inputsB64, Env: skillLeaseEnv(ctx, effective, request)})
		args := []string{"run", "--name", name, "--network", "none", "--read-only",
			"--cap-drop", "ALL", "--security-opt", "no-new-privileges:true", "--user", "10001:10001",
			"--tmpfs", "/scratch:rw,exec,size=64m", "--tmpfs", "/outputs:rw,size=64m", "--tmpfs", "/tmp:rw,size=16m",
			"--memory", "256m", "--cpus", "0.5", "--pids-limit", "64", "--stop-timeout", "2"}
		if scope := CurrentDockerScope(); scope != nil {
			args = append(args, "--label", "hermes-hub.scope="+scope.Project)
		}
		args = append(args, "-i", "--entrypoint", "hubctl", image, "exec-scratch")
		stdout, stderr, runErr := run(lease, bytes.NewReader(body), args...)
		stopped, stopErr := confirmStopped(cleanCtx, run, name)
		if !stopped {
			// Do not report a possibly-live executor as finished: the lease is
			// over, the container may still act and no replacement may start
			// for this binding until the executor is confirmed gone.
			scratchLeaseSet(effective.Binding.ToolBindingID, name)
			if stopErr != nil {
				return AgentExecResult{}, fmt.Errorf("%w: sandbox stop unconfirmed: %s", ErrIsolation, stopErr)
			}
			return AgentExecResult{}, fmt.Errorf("%w: sandbox executor stop unconfirmed", ErrIsolation)
		}
		if runErr != nil {
			return AgentExecResult{}, fmt.Errorf("sandbox execution failed: %s", ShortErr(stderr))
		}
		var result ScratchExecResult
		if err := json.NewDecoder(io.LimitReader(bytes.NewReader(stdout), scratchResultLimit)).Decode(&result); err != nil {
			return AgentExecResult{}, fmt.Errorf("sandbox result unparseable: %w", err)
		}
		applied, skipped, applyErr := ApplyExports(ctx, apply, fence, effective, request, relPath, result.ExportTar)
		summary := map[string]any{
			"exit_code": result.ExitCode, "stdout": result.Stdout, "stderr": result.Stderr,
			"applied": applied, "skipped": skipped, "stopped": true, "path": relPath, "scope": scope,
			"tree_stopped": result.TreeStopped,
		}
		if applyErr != nil {
			summary["export_error"] = applyErr.Error()
		}
		return AgentExecResult{Result: summary}, nil
	}
}

// skillLeaseEnv resolves the one-call credential lease for a `skill` argument
// (R8.3): the effective index names the credentials an eligible skill
// declares; the owner store's .state holds the values. Only the intersection
// travels on the stdin request — a missing store, unknown or ineligible
// skill, malformed name or empty lease degrades to no env, never an error.
func skillLeaseEnv(ctx context.Context, effective EffectiveBinding, request AgentExecRequest) map[string]string {
	skill, _ := request.Arguments["skill"].(string)
	principalID := effective.Binding.PrincipalID
	// Per-principal mounts win when configured; the single legacy mount is
	// the owner's view (multi-principal sibling isolation mirrors the
	// communication gateway's /skills-effective-by contract).
	indexDir, storeDir := os.Getenv("HUB_SKILLS_EFFECTIVE"), os.Getenv("HUB_SKILLS_STORE")
	if root := os.Getenv("HUB_SKILLS_BY_PRINCIPAL"); root != "" {
		indexDir = filepath.Join(root, principalID)
	}
	if root := os.Getenv("HUB_SKILLS_STORE_BY"); root != "" {
		storeDir = filepath.Join(root, principalID)
	}
	if !skillstore.ValidName(skill) || storeDir == "" || indexDir == "" {
		return nil
	}
	body, err := os.ReadFile(filepath.Join(indexDir, "index.json")) // #nosec G304 G703 -- operator-mounted effective view, fixed filename
	if err != nil {
		return nil
	}
	var idx struct {
		Skills []struct {
			Name     string `json:"name"`
			Eligible bool   `json:"eligible"`
			Requires struct {
				Credentials []struct {
					Name string `json:"name"`
					Ref  string `json:"ref"`
				} `json:"credentials"`
			} `json:"requires"`
		} `json:"skills"`
	}
	if json.Unmarshal(body, &idx) != nil {
		return nil
	}
	declared := map[string]bool{}
	refs := map[string]string{}
	for _, e := range idx.Skills {
		if e.Name != skill || !e.Eligible {
			continue
		}
		for _, c := range e.Requires.Credentials {
			declared[c.Name] = true
			if c.Ref != "" {
				refs[c.Name] = c.Ref
			}
		}
	}
	if len(declared) == 0 {
		return nil
	}
	env := map[string]string{}
	// Resolve refs in the caller's running runtime; ToolHub's local state may
	// belong to a different principal.
	if len(refs) > 0 {
		if selfEnv := runtimeSkillRefs(ctx, effective.Binding, refs); len(selfEnv) > 0 {
			for name, ref := range refs {
				if selfEnv[ref] != "" {
					env[name] = selfEnv[ref]
				}
			}
		}
	}
	// Stored per-skill values fill last — an explicit owner value always
	// wins over a ref fallback.
	if stored, err := skillstore.ReadCredentials(storeDir, skill); err == nil {
		for name, value := range stored {
			if declared[name] {
				env[name] = value
			}
		}
	}
	if len(env) == 0 {
		return nil
	}
	return env
}

func runtimeSkillRefs(ctx context.Context, binding ToolBinding, refs map[string]string) map[string]string {
	base := strings.TrimRight(os.Getenv("HUB_RUNTIME_SUPERVISOR_URL"), "/")
	token := os.Getenv("HUB_COMMUNICATION_AUTH")
	if base == "" || token == "" {
		return nil
	}
	names := make([]string, 0, len(refs))
	for _, ref := range refs {
		if !skillstore.ValidCredentialName(ref) {
			return nil
		}
		names = append(names, ref)
	}
	if len(names) > 16 {
		return nil
	}
	body, _ := json.Marshal(map[string]any{"principal_id": binding.PrincipalID, "context_id": binding.ContextID, "runtime_id": binding.RuntimeID, "policy_version": binding.PolicyVersion, "refs": names})
	callCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(callCtx, http.MethodPost, base+"/v1/skill-ref-lease", bytes.NewReader(body)) // #nosec G704 -- operator-configured internal supervisor
	if err != nil {
		return nil
	}
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("Content-Type", "application/json")
	response, err := http.DefaultClient.Do(req) // #nosec G704 -- operator-configured internal supervisor
	if err != nil {
		return nil
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		return nil
	}
	var values map[string]string
	if json.NewDecoder(io.LimitReader(response.Body, 128*1024)).Decode(&values) != nil {
		return nil
	}
	return values
}

// confirmStopped removes the named executor and verifies the removal: only a
// confirmed-gone container lets the call record execution_stopped. A remove
// failure or a surviving inspect means the executor might still run.
func confirmStopped(ctx context.Context, run func(context.Context, io.Reader, ...string) ([]byte, []byte, error), name string) (bool, error) {
	if _, errb, err := run(ctx, nil, "rm", "-f", name); err != nil {
		return false, fmt.Errorf("remove sandbox: %s", ShortErr(errb))
	}
	if _, _, err := run(ctx, nil, "inspect", name); err == nil {
		return false, nil
	}
	return true, nil
}

// ApplyExports unpacks the export tar and applies each member as a create-only
// file write under <path>/exports/. Deletes are impossible by construction:
// the only primitive used is a revision-guarded write.
func ApplyExports(ctx context.Context, apply AgentExecFunc, fence ScratchFenceFunc, effective EffectiveBinding, request AgentExecRequest, relPath, tarB64 string) (applied, skipped []string, err error) {
	if tarB64 == "" {
		return nil, nil, nil
	}
	if fence == nil {
		return nil, nil, fmt.Errorf("%w: export without authority recheck", ErrIsolation)
	}
	if err := fence(ctx, effective); err != nil {
		return nil, nil, err
	}
	body, err := base64.StdEncoding.DecodeString(tarB64)
	if err != nil || len(body) > scratchPackLimit {
		return nil, nil, fmt.Errorf("%w: export tar", ErrInvalid)
	}
	reader := tar.NewReader(io.LimitReader(bytes.NewReader(body), scratchPackLimit+1))
	seen := 0
	for {
		header, err := reader.Next()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return applied, skipped, fmt.Errorf("export tar: %w", err)
		}
		if seen++; seen > scratchExportMaxNum {
			return applied, skipped, fmt.Errorf("%w: export member count", ErrInvalid)
		}
		name := header.Name
		if header.Typeflag != tar.TypeReg || header.Size > scratchExportMaxOne || !ValidCapabilityPath(name) || name == "" {
			skipped = append(skipped, name)
			continue
		}
		content := make([]byte, header.Size)
		if _, err := io.ReadFull(reader, content); err != nil {
			return applied, skipped, fmt.Errorf("export member %s: %w", name, err)
		}
		target := path.Join(relPath, "exports", name) // #nosec G305 -- member names are validated by ValidCapabilityPath
		out, callErr := apply(ctx, effective, AgentExecRequest{
			Tool:           "file_write",
			Arguments:      map[string]any{"path": target, "export_base64": base64.StdEncoding.EncodeToString(content)},
			Scopes:         request.Scopes,
			TimeoutSeconds: request.TimeoutSeconds,
		})
		if callErr != nil {
			return applied, skipped, callErr
		}
		if out.Error != "" {
			skipped = append(skipped, name)
			continue
		}
		applied = append(applied, target)
	}
	return applied, skipped, nil
}

func ShortErr(stderr []byte) string {
	detail := strings.TrimSpace(string(stderr))
	if len(detail) > 200 {
		detail = detail[:200]
	}
	return detail
}
