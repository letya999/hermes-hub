package toolhub

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
)

var (
	ErrOutputLimit = errors.New("bounded CLI output limit exceeded")
	ErrIsolation   = errors.New("bounded CLI isolation is unavailable")
)

// IsolationCheck is supplied by the container/controller boundary. Host
// exec alone cannot enforce network or filesystem isolation, so a missing
// check fails closed instead of creating a false sandbox.
type IsolationCheck func(ExecutionPolicy, string, string) error

type CLIRunner struct {
	Root               string
	AllowedExecutables map[string]bool
	AllowedEnvironment map[string]bool
	Isolation          IsolationCheck
}

func (r CLIRunner) Call(ctx context.Context, effective EffectiveBinding, tool ToolSpec, arguments map[string]any) (BackendResult, error) {
	return r.Run(ctx, effective.Definition, tool, arguments, nil, "")
}

func (r CLIRunner) CallEnv(ctx context.Context, effective EffectiveBinding, tool ToolSpec, arguments map[string]any, environment map[string]string) (BackendResult, error) {
	return r.Run(ctx, effective.Definition, tool, arguments, environment, "")
}

func (r CLIRunner) Run(ctx context.Context, definition ToolDefinition, tool ToolSpec, arguments map[string]any, environment map[string]string, cwd string) (BackendResult, error) {
	if definition.Transport != BoundedCLI {
		return BackendResult{}, fmt.Errorf("%w: CLI transport required", ErrInvalid)
	}
	if err := definition.Validate(); err != nil {
		return BackendResult{}, err
	}
	if !r.AllowedExecutables[definition.Source.Command] {
		return BackendResult{}, fmt.Errorf("%w: executable is not allowlisted", ErrUnauthorized)
	}
	if r.Isolation == nil {
		return BackendResult{}, ErrIsolation
	}
	root, err := filepath.Abs(r.Root)
	if err != nil || root == "" {
		return BackendResult{}, fmt.Errorf("%w: CLI root", ErrInvalid)
	}
	if cwd == "" {
		cwd = root
	}
	working, err := filepath.Abs(cwd)
	if err != nil || !containedPath(root, working) {
		return BackendResult{}, fmt.Errorf("%w: CLI cwd outside root", ErrUnauthorized)
	}
	if err := noSymlinkPath(root); err != nil {
		return BackendResult{}, err
	}
	if err := noSymlinkPath(working); err != nil {
		return BackendResult{}, err
	}
	info, err := os.Stat(working)
	if err != nil || !info.IsDir() {
		return BackendResult{}, fmt.Errorf("%w: CLI cwd", ErrInvalid)
	}
	for key := range environment {
		value := environment[key]
		if !credentialPattern.MatchString(key) || !cliEnvAllowed(r.AllowedEnvironment, key) || len(value) > 16384 || strings.ContainsAny(value, "\x00\r\n") {
			return BackendResult{}, fmt.Errorf("%w: environment key %q", ErrUnauthorized, key)
		}
	}
	if err := r.Isolation(definition.Execution, root, working); err != nil {
		return BackendResult{}, fmt.Errorf("%w: %v", ErrIsolation, err)
	}
	argv, err := cliArguments(definition.Source.Args, tool, arguments)
	if err != nil {
		return BackendResult{}, err
	}
	cmd := exec.Command(definition.Source.Command, argv...) // #nosec G204 -- command and argv are manifest/typed allowlisted values; no shell is used.
	cmd.Dir = working
	cmd.Env = sortedEnvironment(environment)
	configureProcess(cmd)
	output := &boundedOutput{limit: definition.Execution.OutputBytes, exceeded: make(chan struct{})}
	cmd.Stdout = output
	cmd.Stderr = output
	if err := cmd.Start(); err != nil {
		return BackendResult{}, err
	}
	waitCh := make(chan error, 1)
	go func() { waitCh <- cmd.Wait() }()
	stopWatch := make(chan struct{})
	go func() {
		select {
		case <-output.exceeded:
			terminateProcess(cmd)
		case <-stopWatch:
		}
	}()
	var waitErr error
	select {
	case waitErr = <-waitCh:
	case <-ctx.Done():
		terminateProcess(cmd)
		<-waitCh
		close(stopWatch)
		return BackendResult{}, ctx.Err()
	}
	close(stopWatch)
	if output.overflowed() {
		return BackendResult{}, ErrOutputLimit
	}
	if waitErr != nil {
		return BackendResult{Text: redactOutput(output.String(), environment), IsError: true}, nil
	}
	return BackendResult{Text: redactOutput(output.String(), environment)}, nil
}

func cliArguments(fixed []string, tool ToolSpec, values map[string]any) ([]string, error) {
	allowed := map[string]CLIArgument{}
	for _, argument := range tool.Arguments {
		allowed[argument.Name] = argument
	}
	for key := range values {
		if _, ok := allowed[key]; !ok {
			return nil, fmt.Errorf("%w: undeclared CLI argument %q", ErrUnauthorized, key)
		}
	}
	args := append([]string(nil), fixed...)
	for _, argument := range tool.Arguments {
		value, present := values[argument.Name]
		if !present {
			if argument.Required {
				return nil, fmt.Errorf("%w: missing CLI argument %q", ErrInvalid, argument.Name)
			}
			continue
		}
		encoded, err := scalarArgument(argument, value)
		if err != nil {
			return nil, err
		}
		if argument.Type == "boolean" {
			if encoded == "true" {
				args = append(args, argument.Flag)
			} else {
				args = append(args, argument.Flag+"=false")
			}
		} else {
			args = append(args, argument.Flag, encoded)
		}
	}
	if len(args) > 128 {
		return nil, fmt.Errorf("%w: too many CLI arguments", ErrInvalid)
	}
	return args, nil
}

func scalarArgument(argument CLIArgument, value any) (string, error) {
	var encoded string
	switch argument.Type {
	case "string":
		var ok bool
		encoded, ok = value.(string)
		if !ok {
			return "", fmt.Errorf("%w: CLI argument %q must be string", ErrInvalid, argument.Name)
		}
	case "integer":
		switch number := value.(type) {
		case float64:
			if number != float64(int64(number)) {
				return "", fmt.Errorf("%w: CLI integer %q", ErrInvalid, argument.Name)
			}
			encoded = strconv.FormatInt(int64(number), 10)
		case int:
			encoded = strconv.Itoa(number)
		default:
			return "", fmt.Errorf("%w: CLI argument %q must be integer", ErrInvalid, argument.Name)
		}
	case "number":
		number, ok := value.(float64)
		if !ok {
			return "", fmt.Errorf("%w: CLI argument %q must be number", ErrInvalid, argument.Name)
		}
		encoded = strconv.FormatFloat(number, 'g', -1, 64)
	case "boolean":
		boolean, ok := value.(bool)
		if !ok {
			return "", fmt.Errorf("%w: CLI argument %q must be boolean", ErrInvalid, argument.Name)
		}
		encoded = strconv.FormatBool(boolean)
	default:
		return "", fmt.Errorf("%w: CLI argument type", ErrInvalid)
	}
	if encoded == "" || len(encoded) > 256 || strings.ContainsAny(encoded, "\x00\r\n") {
		return "", fmt.Errorf("%w: CLI argument %q is unsafe", ErrInvalid, argument.Name)
	}
	return encoded, nil
}

func cliEnvAllowed(allowlist map[string]bool, key string) bool {
	if allowlist == nil {
		return true
	}
	return allowlist[key]
}

func sortedEnvironment(values map[string]string) []string {
	result := make([]string, 0, len(values))
	for key, value := range values {
		result = append(result, key+"="+value)
	}
	sort.Strings(result)
	return result
}

func redactOutput(output string, environment map[string]string) string {
	for _, value := range environment {
		if value != "" {
			output = strings.ReplaceAll(output, value, "[REDACTED]")
		}
	}
	return output
}

func containedPath(root, path string) bool {
	rel, err := filepath.Rel(root, path)
	return err == nil && rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator)) && !filepath.IsAbs(rel)
}

func noSymlinkPath(path string) error {
	if !filepath.IsAbs(path) {
		return fmt.Errorf("%w: absolute path required", ErrInvalid)
	}
	path = filepath.Clean(path)
	volume := filepath.VolumeName(path)
	current := volume + string(filepath.Separator)
	for _, part := range strings.Split(strings.TrimPrefix(path, current), string(filepath.Separator)) {
		if part == "" {
			continue
		}
		current = filepath.Join(current, part)
		info, err := os.Lstat(current)
		if err != nil {
			return fmt.Errorf("%w: CLI path: %v", ErrInvalid, err)
		}
		if info.Mode()&os.ModeSymlink != 0 {
			return fmt.Errorf("%w: CLI path contains symlink", ErrUnauthorized)
		}
	}
	return nil
}

type boundedOutput struct {
	mu       sync.Mutex
	buffer   bytes.Buffer
	limit    int
	exceeded chan struct{}
	overflow bool
}

func (o *boundedOutput) Write(value []byte) (int, error) {
	o.mu.Lock()
	defer o.mu.Unlock()
	remaining := o.limit - o.buffer.Len()
	if remaining <= 0 {
		if !o.overflow {
			o.overflow = true
			close(o.exceeded)
		}
		return 0, ErrOutputLimit
	}
	if len(value) > remaining {
		_, _ = o.buffer.Write(value[:remaining])
		if !o.overflow {
			o.overflow = true
			close(o.exceeded)
		}
		return remaining, ErrOutputLimit
	}
	return o.buffer.Write(value)
}

func (o *boundedOutput) overflowed() bool {
	o.mu.Lock()
	defer o.mu.Unlock()
	return o.overflow
}

func (o *boundedOutput) String() string {
	o.mu.Lock()
	defer o.mu.Unlock()
	return o.buffer.String()
}
