package api

import (
	"bufio"
	"bytes"
	"context"
	"crypto/rand"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"os/exec"
	"strings"
	"sync"
	"time"
)

type terminalStart struct {
	Command    string            `json:"command"`
	Cwd        string            `json:"cwd,omitempty"`
	Shell      string            `json:"shell,omitempty"`
	Stdin      string            `json:"stdin,omitempty"`
	Timeout    int               `json:"timeout,omitempty"`
	Background bool              `json:"background,omitempty"`
	InputsTar  string            `json:"inputs_tar_base64,omitempty"`
	Env        map[string]string `json:"env,omitempty"`
}

type terminalFrame struct {
	Action      string `json:"action"`
	Data        string `json:"data,omitempty"`
	ExitCode    int    `json:"exit_code,omitempty"`
	TimedOut    bool   `json:"timed_out,omitempty"`
	TreeStopped bool   `json:"tree_stopped,omitempty"`
}

type terminalJob struct {
	id, name, owner, contextID, runtimeID, bindingID string
	effective                                        EffectiveBinding
	cancel                                           context.CancelFunc
	stdin                                            io.WriteCloser
	ready                                            chan struct{}
	done                                             chan struct{}
	mu                                               sync.Mutex
	writeMu                                          sync.Mutex
	state                                            string
	stdout, stderr                                   string
	exitCode                                         int
	timedOut, treeStopped                            bool
	err                                              error
}

func (j *terminalJob) snapshot() map[string]any {
	j.mu.Lock()
	defer j.mu.Unlock()
	result := map[string]any{"job_id": j.id, "state": j.state, "stdout": j.stdout, "stderr": j.stderr}
	if j.state != "running" && j.state != "starting" {
		result["exit_code"], result["timed_out"], result["tree_stopped"] = j.exitCode, j.timedOut, j.treeStopped
	}
	return result
}

type terminalExecutor struct {
	docker  []string
	resolve func(EffectiveBinding) (string, error)
	mu      sync.Mutex
	jobs    map[string]*terminalJob
}

// DockerTerminalExec runs commands in bounded agent-image containers without
// a network, control-plane mounts or a host workspace bind. The admitted
// workspace subtree is copied through exec-pack; commands may write only in
// their private scratch. Background handles belong to this executor process.
func DockerTerminalExec(dockerArgv []string, resolve func(EffectiveBinding) (string, error)) ScratchExecFunc {
	if len(dockerArgv) == 0 {
		dockerArgv = []string{"docker"}
	}
	e := &terminalExecutor{docker: dockerArgv, resolve: resolve, jobs: map[string]*terminalJob{}}
	return e.call
}

func (e *terminalExecutor) dockerCall(ctx context.Context, stdin io.Reader, args ...string) ([]byte, []byte, error) {
	if scope := CurrentDockerScope(); scope != nil {
		if err := scope.GuardDocker(ctx, args); err != nil {
			return nil, nil, err
		}
	}
	cmd := exec.CommandContext(ctx, e.docker[0], append(e.docker[1:], args...)...)
	cmd.Stdin = stdin
	var out, errb bytes.Buffer
	cmd.Stdout = &out
	cmd.Stderr = &errb
	err := cmd.Run()
	return out.Bytes(), errb.Bytes(), err
}

func (e *terminalExecutor) call(ctx context.Context, effective EffectiveBinding, request AgentExecRequest, fence ScratchFenceFunc) (AgentExecResult, error) {
	if fence == nil || e.resolve == nil {
		return AgentExecResult{}, ErrIsolation
	}
	if err := fence(ctx, effective); err != nil {
		return AgentExecResult{}, err
	}
	switch request.Tool {
	case "terminal_exec":
		return e.start(ctx, effective, request, fence)
	case "terminal_job":
		return e.control(ctx, effective, request, fence)
	default:
		return AgentExecResult{}, ErrInvalid
	}
}

func (e *terminalExecutor) start(ctx context.Context, effective EffectiveBinding, request AgentExecRequest, fence ScratchFenceFunc) (AgentExecResult, error) {
	args := request.Arguments
	command, _ := args["cmd"].(string)
	cwd, _ := args["cwd"].(string)
	path, _ := args["path"].(string)
	shell, _ := args["shell"].(string)
	initial, _ := args["stdin"].(string)
	background, _ := args["background"].(bool)
	if strings.TrimSpace(command) == "" || len(command) > 8192 || strings.ContainsRune(command, 0) ||
		!ValidCapabilityPath(cwd) || !ValidCapabilityPath(path) || len(initial) > 64<<10 ||
		(shell != "" && shell != "sh" && shell != "bash") {
		return AgentExecResult{}, ErrInvalid
	}
	timeout := request.TimeoutSeconds
	if timeout <= 0 || timeout > 600 {
		timeout = 300
	}
	if requested, ok := args["timeout"].(float64); ok {
		if requested < 1 || requested > float64(timeout) || requested != float64(int(requested)) {
			return AgentExecResult{}, ErrInvalid
		}
		timeout = int(requested)
	}
	selected := cwd
	if selected == "" {
		selected = path
	}
	for _, scope := range request.Scopes {
		if scope.PathArgument != "path" || scope.PathPrefix == "" {
			continue
		}
		if path == "" || path != scope.PathPrefix && !strings.HasPrefix(path, scope.PathPrefix+"/") ||
			selected != path && !strings.HasPrefix(selected, path+"/") {
			return AgentExecResult{}, fmt.Errorf("%w: terminal path outside admitted scope", ErrInvalid)
		}
	}
	runtime, err := e.resolve(effective)
	if err != nil {
		return AgentExecResult{}, err
	}
	if scope := CurrentDockerScope(); scope != nil && !scope.AdmitSupervisedRuntime(ctx, runtime) {
		return AgentExecResult{}, ErrIsolation
	}
	imageBytes, _, err := e.dockerCall(ctx, nil, "inspect", "-f", "{{.Config.Image}}", runtime)
	if err != nil {
		return AgentExecResult{}, ErrIsolation
	}
	image := strings.TrimSpace(string(imageBytes))
	if image == "" || strings.ContainsAny(image, "\x00\r\n") {
		return AgentExecResult{}, ErrIsolation
	}
	var pack string
	if selected != "" {
		body, _ := json.Marshal(ScratchPackRequest{Path: selected, Scopes: request.Scopes})
		input, _, err := e.dockerCall(ctx, bytes.NewReader(body), "exec", "-i", runtime, "hubctl", "exec-pack")
		if err != nil || len(input) > scratchPackLimit {
			return AgentExecResult{}, ErrIsolation
		}
		pack = base64.StdEncoding.EncodeToString(input)
	}
	if err := fence(ctx, effective); err != nil {
		return AgentExecResult{}, err
	}
	random := make([]byte, 12)
	if _, err := rand.Read(random); err != nil {
		return AgentExecResult{}, err
	}
	id := "terminal-" + hex.EncodeToString(random)
	job := &terminalJob{id: id, name: "hermes-" + id, owner: effective.Binding.PrincipalID,
		contextID: effective.Binding.ContextID, runtimeID: effective.Binding.RuntimeID,
		bindingID: effective.Binding.ToolBindingID, effective: effective, ready: make(chan struct{}), done: make(chan struct{}), state: "starting"}
	e.mu.Lock()
	count := 0
	for existingID, existing := range e.jobs {
		select {
		case <-existing.done:
			delete(e.jobs, existingID) // bounded retained results; active work is never evicted
			continue
		default:
		}
		if existing.owner == job.owner {
			count++
		}
	}
	if count >= 8 || len(e.jobs) >= 64 {
		e.mu.Unlock()
		return AgentExecResult{}, fmt.Errorf("%w: terminal job limit", ErrIsolation)
	}
	e.jobs[id] = job
	e.mu.Unlock()
	start := terminalStart{Command: command, Cwd: selected, Shell: shell, Stdin: initial,
		Timeout: timeout, Background: background, InputsTar: pack, Env: skillLeaseEnv(ctx, effective, request)}
	jobCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), time.Duration(timeout+15)*time.Second)
	job.cancel = cancel
	go e.run(jobCtx, job, image, start)
	go func() {
		ticker := time.NewTicker(time.Second)
		defer ticker.Stop()
		for {
			select {
			case <-job.done:
				return
			case <-ticker.C:
				if fence(context.Background(), effective) != nil {
					job.cancel()
					return
				}
			}
		}
	}()
	select {
	case <-job.ready:
		job.mu.Lock()
		startErr := job.err
		job.mu.Unlock()
		if startErr != nil {
			return AgentExecResult{}, startErr
		}
	case <-ctx.Done():
		job.cancel()
		return AgentExecResult{}, ctx.Err()
	}
	if background {
		return AgentExecResult{Result: map[string]any{"job_id": id, "state": "running"}}, nil
	}
	select {
	case <-job.done:
	case <-ctx.Done():
		job.cancel()
		<-job.done
	}
	e.mu.Lock()
	delete(e.jobs, id)
	e.mu.Unlock()
	if err := fence(context.Background(), effective); err != nil {
		return AgentExecResult{}, err
	}
	return AgentExecResult{Result: job.snapshot()}, nil
}

func (e *terminalExecutor) run(ctx context.Context, job *terminalJob, image string, start terminalStart) {
	defer close(job.done)
	defer job.cancel()
	defer func() {
		select {
		case <-job.ready:
		default:
			close(job.ready)
		}
	}()
	args := []string{"run", "--rm", "-i", "--name", job.name, "--network", "none", "--read-only",
		"--cap-drop", "ALL", "--security-opt", "no-new-privileges:true", "--user", "10001:10001",
		"--tmpfs", "/scratch:rw,exec,size=64m", "--tmpfs", "/outputs:rw,size=64m", "--tmpfs", "/tmp:rw,size=16m",
		"--memory", "256m", "--cpus", "0.5", "--pids-limit", "64", "--stop-timeout", "2"}
	if scope := CurrentDockerScope(); scope != nil {
		args = append(args, "--label", "hermes-hub.scope="+scope.Project)
	}
	args = append(args, "--entrypoint", "hubctl", image, "exec-terminal")
	if scope := CurrentDockerScope(); scope != nil && scope.GuardDocker(ctx, args) != nil {
		job.fail(ErrIsolation)
		return
	}
	cmd := exec.CommandContext(ctx, e.docker[0], append(e.docker[1:], args...)...)
	stdin, err := cmd.StdinPipe()
	if err != nil {
		job.fail(err)
		return
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		job.fail(err)
		return
	}
	var stderr bytes.Buffer
	cmd.Stderr = &BoundedWriter{W: &stderr, Max: 4096}
	if err := cmd.Start(); err != nil {
		job.fail(err)
		return
	}
	job.stdin = stdin
	body, _ := json.Marshal(start)
	job.writeMu.Lock()
	_, err = stdin.Write(append(body, '\n'))
	job.writeMu.Unlock()
	if err != nil {
		job.fail(err)
		job.cancel()
		_ = cmd.Wait()
		e.cleanup(job)
		return
	}
	reader := bufio.NewReaderSize(stdout, 64*1024)
	ready := false
	for {
		line, readErr := reader.ReadBytes('\n')
		if len(line) > 512<<10 {
			job.fail(ErrIsolation)
			job.cancel()
			break
		}
		if len(line) > 0 {
			var frame terminalFrame
			if json.Unmarshal(line, &frame) != nil {
				job.fail(ErrIsolation)
				job.cancel()
				break
			}
			job.mu.Lock()
			switch frame.Action {
			case "started":
				job.state = "running"
				if !ready {
					close(job.ready)
					ready = true
				}
			case "stdout":
				if len(job.stdout)+len(frame.Data) <= 256<<10 {
					job.stdout += frame.Data
				}
			case "stderr":
				if len(job.stderr)+len(frame.Data) <= 256<<10 {
					job.stderr += frame.Data
				}
			case "done":
				job.state, job.exitCode, job.timedOut, job.treeStopped = "done", frame.ExitCode, frame.TimedOut, frame.TreeStopped
			default:
				job.err = ErrIsolation
				job.cancel()
			}
			job.mu.Unlock()
		}
		if readErr != nil {
			break
		}
	}
	_ = stdin.Close()
	waitErr := cmd.Wait()
	if waitErr != nil {
		job.mu.Lock()
		if job.state != "done" {
			job.state = "failed"
			job.err = ErrIsolation
		}
		job.mu.Unlock()
	}
	if err := e.cleanup(job); err != nil {
		job.fail(err)
	} else {
		job.mu.Lock()
		if ctx.Err() == context.Canceled && job.state == "failed" {
			job.state, job.exitCode = "done", -1
		}
		if job.state == "done" {
			job.treeStopped = true // Docker removal is the final process boundary.
		}
		job.mu.Unlock()
	}
	job.mu.Lock()
	if job.state == "starting" || job.state == "running" {
		job.state = "failed"
		job.err = ErrIsolation
	}
	job.mu.Unlock()
}

func (j *terminalJob) fail(err error) {
	j.mu.Lock()
	j.err, j.state = err, "failed"
	j.mu.Unlock()
}

func (e *terminalExecutor) cleanup(job *terminalJob) error {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	// An absent --rm container has no label for GuardDocker to inspect.
	// Inspect only the internal random name, then enforce ownership before rm.
	inspect := func() (string, error) {
		cmd := exec.CommandContext(ctx, e.docker[0], append(e.docker[1:], "inspect", job.name)...)
		var detail bytes.Buffer
		cmd.Stdout = io.Discard
		cmd.Stderr = &BoundedWriter{W: &detail, Max: 4096}
		err := cmd.Run()
		return detail.String(), err
	}
	if detail, err := inspect(); err != nil {
		if strings.Contains(strings.ToLower(detail), "no such object") || strings.Contains(strings.ToLower(detail), "no such container") {
			return nil
		}
		return ErrIsolation
	}
	if scope := CurrentDockerScope(); scope != nil && !scope.Owns(ctx, "container", job.name) {
		return ErrIsolation
	}
	if _, _, err := e.dockerCall(ctx, nil, "rm", "-f", job.name); err != nil {
		return ErrIsolation
	}
	if detail, err := inspect(); err == nil || !strings.Contains(strings.ToLower(detail), "no such object") && !strings.Contains(strings.ToLower(detail), "no such container") {
		return ErrIsolation
	}
	return nil
}

func (e *terminalExecutor) control(ctx context.Context, effective EffectiveBinding, request AgentExecRequest, fence ScratchFenceFunc) (AgentExecResult, error) {
	action, _ := request.Arguments["action"].(string)
	id, _ := request.Arguments["job"].(string)
	if action == "list" {
		e.mu.Lock()
		rows := []map[string]any{}
		for _, job := range e.jobs {
			if job.owner == effective.Binding.PrincipalID && job.contextID == effective.Binding.ContextID && job.runtimeID == effective.Binding.RuntimeID && job.bindingID == effective.Binding.ToolBindingID {
				rows = append(rows, map[string]any{"job_id": job.id, "state": job.snapshot()["state"]})
			}
		}
		e.mu.Unlock()
		return AgentExecResult{Result: map[string]any{"jobs": rows}}, nil
	}
	if action != "poll" && action != "send" && action != "kill" {
		return AgentExecResult{}, ErrInvalid
	}
	e.mu.Lock()
	job := e.jobs[id]
	e.mu.Unlock()
	if job == nil || job.owner != effective.Binding.PrincipalID || job.contextID != effective.Binding.ContextID ||
		job.runtimeID != effective.Binding.RuntimeID || job.bindingID != effective.Binding.ToolBindingID {
		return AgentExecResult{}, ErrNotFound
	}
	if err := fence(ctx, job.effective); err != nil {
		job.cancel()
		return AgentExecResult{}, err
	}
	switch action {
	case "send":
		data, _ := request.Arguments["data"].(string)
		if len(data) > 32<<10 || data == "" {
			return AgentExecResult{}, ErrInvalid
		}
		job.mu.Lock()
		running := job.state == "running"
		job.mu.Unlock()
		if !running {
			return AgentExecResult{}, ErrInvalid
		}
		body, _ := json.Marshal(terminalFrame{Action: "send", Data: data})
		job.writeMu.Lock()
		_, err := job.stdin.Write(append(body, '\n'))
		job.writeMu.Unlock()
		if err != nil {
			return AgentExecResult{}, ErrIsolation
		}
	case "kill":
		job.cancel()
		select {
		case <-job.done:
		case <-ctx.Done():
			return AgentExecResult{}, ctx.Err()
		}
	}
	if err := fence(ctx, job.effective); err != nil {
		return AgentExecResult{}, err
	}
	return AgentExecResult{Result: job.snapshot()}, nil
}
