package main

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/letya999/hermes-hub/internal/toolhub/api"
)

// TerminalStart is sent on stdin to an isolated, networkless agent-image
// workload. It contains no authority fields or Docker host path.
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

type terminalEmitter struct {
	mu  sync.Mutex
	enc *json.Encoder
}

func (e *terminalEmitter) send(frame terminalFrame) {
	e.mu.Lock()
	_ = e.enc.Encode(frame)
	e.mu.Unlock()
}

type terminalOutput struct {
	emitter *terminalEmitter
	action  string
	written int
	mu      sync.Mutex
}

func (w *terminalOutput) Write(p []byte) (int, error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	if remain := (256 << 10) - w.written; remain > 0 {
		part := p[:min(len(p), remain)]
		w.written += len(part)
		w.emitter.send(terminalFrame{Action: w.action, Data: string(part)})
	}
	return len(p), nil
}

// exec-terminal is only invoked inside the disposable Docker workload. EOF
// on the controlling stream kills the whole process group, so a ToolHub crash
// cannot leave a detached command running against stale authority.
func runExecTerminal(ctx context.Context) error {
	reader := bufio.NewReader(io.LimitReader(os.Stdin, api.AgentExecMaxRequest+1))
	line, err := reader.ReadBytes('\n')
	if err != nil || len(line) > api.AgentExecMaxRequest {
		return errors.New("terminal start frame invalid")
	}
	var start terminalStart
	if json.Unmarshal(line, &start) != nil || strings.TrimSpace(start.Command) == "" || len(start.Command) > 8192 || strings.ContainsRune(start.Command, 0) ||
		!api.ValidCapabilityPath(start.Cwd) || len(start.Stdin) > 64<<10 || start.Timeout < 1 || start.Timeout > 600 {
		return errors.New("terminal start invalid")
	}
	shell := start.Shell
	if shell == "" {
		shell = "sh"
	}
	if shell != "sh" && shell != "bash" {
		return errors.New("terminal shell unavailable")
	}
	root := "/scratch/workspace"
	workdir := filepath.Join(root, filepath.FromSlash(start.Cwd))
	if err := os.MkdirAll(workdir, 0o700); err != nil {
		return err
	}
	if err := unpackInputs(start.InputsTar, workdir); err != nil {
		return err
	}
	lease, cancel := context.WithTimeout(ctx, time.Duration(start.Timeout)*time.Second)
	defer cancel()
	command := exec.CommandContext(lease, shell, "-c", start.Command)
	command.SysProcAttr = scratchProcAttr()
	command.Cancel = func() error { return scratchCancel(command) }
	command.WaitDelay = 2 * time.Second
	command.Dir = workdir
	command.Env = []string{"PATH=/usr/local/bin:/usr/bin:/bin", "HOME=/scratch", "TMPDIR=/tmp", "USER=sandbox", "OUTPUTS=/outputs"}
	command.Env = append(command.Env, scratchSkillEnv(start.Env)...)
	emitter := &terminalEmitter{enc: json.NewEncoder(os.Stdout)}
	command.Stdout = &terminalOutput{emitter: emitter, action: "stdout"}
	command.Stderr = &terminalOutput{emitter: emitter, action: "stderr"}
	stdin, err := command.StdinPipe()
	if err != nil {
		return err
	}
	if err := command.Start(); err != nil {
		return err
	}
	emitter.send(terminalFrame{Action: "started"})
	var inputMu sync.Mutex
	go func() {
		if start.Stdin != "" {
			inputMu.Lock()
			_, _ = io.WriteString(stdin, start.Stdin)
			inputMu.Unlock()
		}
		if !start.Background {
			_ = stdin.Close()
			for {
				if _, err := reader.ReadBytes('\n'); err != nil {
					cancel()
					return
				}
			}
		}
		for {
			line, err := reader.ReadBytes('\n')
			if err != nil || len(line) > 64<<10 {
				cancel()
				return
			}
			var frame terminalFrame
			if json.Unmarshal(line, &frame) != nil || frame.Action != "send" || len(frame.Data) > 32<<10 {
				cancel()
				return
			}
			inputMu.Lock()
			_, err = io.WriteString(stdin, frame.Data)
			inputMu.Unlock()
			if err != nil {
				return
			}
		}
	}()
	runErr := command.Wait()
	_ = stdin.Close()
	result := terminalFrame{Action: "done", TreeStopped: command.Process == nil || scratchTreeGone(command.Process.Pid), TimedOut: errors.Is(lease.Err(), context.DeadlineExceeded)}
	if runErr != nil {
		result.ExitCode = 1
		var exit *exec.ExitError
		if errors.As(runErr, &exit) {
			result.ExitCode = exit.ExitCode()
		}
	}
	emitter.send(result)
	return nil
}
