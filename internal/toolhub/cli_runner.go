package toolhub

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"sync"
)

var (
	ErrOutputLimit = errors.New("bounded CLI output limit exceeded")
	ErrIsolation   = errors.New("bounded CLI isolation is unavailable")
)

// CLIRunner submits bounded-cli calls to the controller's /cli-exec: the
// workload runs inside a sibling sandbox cell, never inside this process.
// The runner only resolves the workspace, argv and credential delivery; all
// enforcement decisions are the controller's.
type CLIRunner struct {
	Root               string
	AllowedEnvironment map[string]bool
	// Exec posts the exec request to the controller. Nil fails closed: no
	// controller, no execution.
	Exec CLIExecFunc
}

// CLIRunnerFromEnv builds the shipped runner: Root from HUB_STATE and the
// execution channel from the shared controller endpoint. No channel means
// every call denies.
func CLIRunnerFromEnv() (CLIRunner, error) {
	execFn, err := CLIExecFromEnv()
	if err != nil {
		return CLIRunner{}, err
	}
	return CLIRunner{Root: envOr("HUB_STATE", "/state"), Exec: execFn}, nil
}

// CLIAllowlistFromEnv parses HUB_CLI_ALLOWLIST: a comma-separated list of
// bare command names an owner spec may register. The list gates
// registration; the controller's own approval lists are the execution
// boundary, so the rendered value mirrors cli.UserCommands.
func CLIAllowlistFromEnv() map[string]bool {
	raw := strings.TrimSpace(os.Getenv("HUB_CLI_ALLOWLIST"))
	if raw == "" {
		return nil
	}
	allowed := map[string]bool{}
	for _, entry := range strings.Split(raw, ",") {
		entry = strings.TrimSpace(entry)
		if entry == "" || !commandPattern.MatchString(entry) || strings.Contains(entry, "/") {
			continue
		}
		allowed[entry] = true
	}
	if len(allowed) == 0 {
		return nil
	}
	return allowed
}

func (r CLIRunner) Call(ctx context.Context, effective EffectiveBinding, tool ToolSpec, arguments map[string]any) (BackendResult, error) {
	return r.CallEnv(ctx, effective, tool, arguments, nil)
}

func (r CLIRunner) CallEnv(ctx context.Context, effective EffectiveBinding, tool ToolSpec, arguments map[string]any, environment map[string]string) (BackendResult, error) {
	definition := effective.Definition
	if definition.Transport != BoundedCLI {
		return BackendResult{}, fmt.Errorf("%w: CLI transport required", ErrInvalid)
	}
	if err := definition.Validate(); err != nil {
		return BackendResult{}, err
	}
	if r.Exec == nil {
		return BackendResult{}, ErrIsolation
	}
	jobID := injectJobID(ctx, effective)
	w := definition.Workload
	scope := w.WorkspaceScope
	if scope == "" {
		if w.Class == Shared {
			scope = "none"
		} else {
			scope = "binding"
		}
	}
	cellWorkspace := cliWorkspace{Scope: scope, Access: w.WorkspaceAccess}
	if scope == "binding" {
		// The binding workspace is opened here so the controller gets the
		// canonical path; the cell binds it — this process never execs in it.
		workspace, err := OpenWorkloadWorkspace(r.Root, effective, jobID)
		if err != nil {
			return BackendResult{}, err
		}
		if workspace.Cleanup != nil {
			defer workspace.Cleanup()
		}
		if workspace.Path == "" {
			return BackendResult{}, fmt.Errorf("%w: binding workspace unavailable", ErrIsolation)
		}
		cellWorkspace.Path = workspace.Path
	}
	argv, err := cliArguments(definition.Source.Args, tool, arguments)
	if err != nil {
		return BackendResult{}, err
	}
	env := make(map[string]string, len(environment)+len(definition.RuntimeEnvironment))
	for key, value := range definition.RuntimeEnvironment {
		env[key] = value
	}
	for key, value := range environment {
		env[key] = value
	}
	brokeredInputs := map[string]CredentialInput{}
	for _, input := range definition.Credentials {
		if input.Delivery == "brokered" {
			brokeredInputs[input.Name] = input
		}
	}
	var envPairs []string
	var brokered []cliBrokeredCred
	for key, value := range env {
		if !credentialPattern.MatchString(key) || !cliEnvAllowed(r.AllowedEnvironment, key) || len(value) > 16384 || strings.ContainsAny(value, "\x00\r\n") {
			return BackendResult{}, fmt.Errorf("%w: environment key %q", ErrUnauthorized, key)
		}
		if input, ok := brokeredInputs[key]; ok {
			// Brokered delivery: the cell sees a cred-proxy URL; the secret
			// travels only inside the request body to the controller.
			brokered = append(brokered, cliBrokeredCred{EnvName: key, Host: input.Target, Prefix: input.Prefix, Value: value})
			continue
		}
		envPairs = append(envPairs, key+"="+value)
	}
	sort.Strings(envPairs)
	request := cliExecRequest{
		Plan: controllerPlan{
			WorkloadID:        effective.WorkloadID,
			DefinitionID:      definition.DefinitionID,
			DefinitionVersion: definition.Version,
			Image:             definition.Source.Image,
			Digest:            definition.Source.Digest,
			Execution:         definition.Execution,
			Command:           definition.Source.Command,
		},
		Command:         definition.Source.Command,
		Args:            argv,
		Env:             envPairs,
		Principal:       effective.Binding.PrincipalID,
		ContextID:       effective.Binding.ContextID,
		BindingID:       effective.Binding.ToolBindingID,
		JobID:           jobID,
		Lifecycle:       w.Lifecycle,
		ToolboxID:       w.Toolbox,
		Stateless:       w.Stateless,
		InstallPackages: w.InstallPackages,
		Workspace:       cellWorkspace,
		Brokered:        brokered,
	}
	response, err := r.Exec(ctx, request)
	if err != nil {
		if errors.Is(err, ErrOutputLimit) {
			return BackendResult{}, err
		}
		if ctxErr := ctx.Err(); ctxErr != nil && errors.Is(err, ctxErr) {
			return BackendResult{}, ctxErr
		}
		return BackendResult{}, fmt.Errorf("%w: %v", ErrIsolation, err)
	}
	if err := response.Receipt.validate(effective); err != nil {
		return BackendResult{}, fmt.Errorf("%w: %v", ErrIsolation, err)
	}
	if response.Error == "output-limit" {
		return BackendResult{}, ErrOutputLimit
	}
	if response.Error != "" {
		return BackendResult{}, fmt.Errorf("%w: cell reported %s", ErrIsolation, response.Error)
	}
	result := BackendResult{Text: redactOutput(response.Output, env)}
	if response.ExitCode != 0 {
		result.IsError = true
	}
	if response.Receipt.WorkloadID != "" {
		result.Receipt = response.Receipt.WorkloadID + "@" + strings.Join(response.Receipt.Isolation, ",") + "@cell=" + response.Receipt.CellID + ",rt=" + response.Receipt.Runtime
	}
	return result, nil
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
		if argument.Pattern != "" {
			matched, err := regexp.MatchString(argument.Pattern, encoded)
			if err != nil || !matched {
				return nil, fmt.Errorf("%w: CLI argument %q rejected by pattern", ErrUnauthorized, argument.Name)
			}
		}
		if argument.Flag == "" {
			// Positional operands are appended as-is; the mandatory pattern
			// is the injection guard, and a leading dash is refused outright.
			if strings.HasPrefix(encoded, "-") {
				return nil, fmt.Errorf("%w: CLI argument %q cannot look like a flag", ErrUnauthorized, argument.Name)
			}
			args = append(args, encoded)
		} else if argument.Type == "boolean" {
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
