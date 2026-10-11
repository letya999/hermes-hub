package toolhub

// Owner-scoped bounded-cli registration: the model supplies a declarative spec
// through prepare_source's "cli" object; everything else (grant, owner scope,
// credential form, confirmation, lifecycle) is the existing control path. A
// spec is manifest data only — command names resolve against the shipped
// executable allowlist, argv tokens are literal, and no shell, template or
// command string is ever formed.

import (
	"context"
	"fmt"
	"strings"

	"github.com/letya999/hermes-hub/internal/identity"
)

// cliSpecDeniedArgs names flags a registered spec may never carry because the
// allowlisted binary would execute another program or read outside the
// bounded workspace (preprocessors, hook commands, file operands). Keys are
// command names, values flag prefixes.
var cliSpecDeniedArgs = map[string][]string{
	"git": {}, // git is excluded from user commands entirely: .git/hooks in the workspace execute arbitrary code.
	"rg":  {"--pre", "--pre-glob", "--hostname-bin", "--file", "--ignore-file"},
}

// prepareCLI admits an owner-declared bounded-cli definition. The spec is
// validated, compiled into an immutable ToolDefinition and registered as a
// user publication before the normal confirm/enable lifecycle runs; every
// later enable is still bound to the authenticated principal. scope: shared
// promotes the definition to catalog visibility and is gated to operators
// through the install_shared control grant.
func (c *ControlPlane) prepareCLI(ctx context.Context, auth identity.Envelope, raw any, requestKey string) (map[string]any, error) {
	spec, ok := raw.(map[string]any)
	if !ok {
		return nil, fmt.Errorf("%w: cli spec", ErrInvalid)
	}
	shared, err := cliSpecScope(spec)
	if err != nil {
		return nil, err
	}
	if shared {
		if err := c.Store.RequireControlOperation(auth, "install_shared"); err != nil {
			return nil, err
		}
	} else if err := c.Store.RequireSelfInstall(auth); err != nil {
		return nil, err
	}
	// source+binary switch the op into immutable artifact onboarding; the
	// direct-command shape must not mix with them.
	if argString(spec, "source") != "" || argString(spec, "binary") != "" {
		return c.prepareCLIArtifact(ctx, auth, spec, requestKey, shared)
	}
	if argString(spec, "asset") != "" || argString(spec, "digest") != "" || argString(spec, "member") != "" {
		return nil, fmt.Errorf("%w: asset, digest and member apply to github-release sources only", ErrInvalid)
	}
	definition, err := cliDefinitionFromSpec(spec, shared)
	if err != nil {
		return nil, err
	}
	if err := cliRegistrationAllowed(definition.Source.Command); err != nil {
		return nil, err
	}
	if err := c.registerAdmittedDefinition(&definition, auth.PrincipalID); err != nil {
		return nil, err
	}
	if shared {
		if err := c.Store.PromoteToCatalog(definition.DefinitionID, definition.Version, auth.PrincipalID); err != nil {
			return nil, err
		}
	}
	onboarding, err := c.newOnboarding(auth, OnboardingSelfInstall, requestKey, definition, "", "")
	if err != nil {
		return nil, err
	}
	if err := c.ensureBrokerRequest(ctx, auth, &onboarding, definition); err != nil {
		return nil, err
	}
	return c.statusBody(onboarding, false), nil
}

// cliSpecScope admits "owner" (default) or "shared". A shared definition is
// catalog-visible to every principal with catalog access; execution stays a
// per-caller cell with the caller's own workspace.
func cliSpecScope(spec map[string]any) (bool, error) {
	switch argString(spec, "scope") {
	case "", "owner":
		return false, nil
	case "shared":
		return true, nil
	default:
		return false, fmt.Errorf("%w: cli spec scope must be owner or shared", ErrInvalid)
	}
}

// cliSpecWorkload compiles the spec's lifecycle and workspace hints into the
// immutable workload policy. Class is fixed by scope — owner installs are
// per-user, shared installs are shared — and ToolDefinition.Validate polices
// every lifecycle/scope combination from there.
func cliSpecWorkload(spec map[string]any, shared bool) WorkloadPolicy {
	workload := WorkloadPolicy{Class: PerUser, Rationale: "owner-declared bounded CLI registered through the control plane; per-binding workspace"}
	if shared {
		workload.Class = Shared
		workload.Rationale = "operator-shared bounded CLI; catalog-visible, executed in a per-caller cell"
	}
	workload.Lifecycle = argString(spec, "lifecycle")
	workload.Toolbox = argString(spec, "toolbox")
	workload.WorkspaceScope = argString(spec, "workspace_scope")
	workload.WorkspaceAccess = argString(spec, "workspace_access")
	workload.InstallPackages, _ = spec["install_packages"].(bool)
	if stateless, ok := spec["stateless"].(bool); ok && stateless {
		workload.Stateless = true
	}
	return workload
}

// cliRegistrationAllowed requires the declared command name to be admitted
// by the operator allowlist — the same names the controller's UserCommands
// admit at /cli-exec. Artifact binaries are guest paths inside pinned images
// and onboard through prepareCLIArtifact, not this gate.
func cliRegistrationAllowed(command string) error {
	allowed := CLIAllowlistFromEnv()
	if len(allowed) == 0 {
		return fmt.Errorf("%w: no bounded-cli executables are configured", ErrIsolation)
	}
	if allowed[command] {
		return nil
	}
	return fmt.Errorf("%w: executable %q is not allowlisted", ErrUnauthorized, command)
}

var cliSpecKeys = map[string]bool{"install_packages": true, "name": true, "version": true, "command": true, "source": true, "binary": true, "asset": true, "digest": true, "member": true, "base": true, "scope": true, "lifecycle": true, "stateless": true, "toolbox": true, "workspace_scope": true, "workspace_access": true, "args": true, "tools": true, "credentials": true, "runtime_env": true, "egress": true}

// cliDefinitionFromSpec compiles the declarative spec into the immutable
// record. Execution policy and health are fixed here, never model-chosen;
// the caller still sees the result through ToolDefinition.Validate.
func cliDefinitionFromSpec(spec map[string]any, shared bool) (ToolDefinition, error) {
	for key := range spec {
		if !cliSpecKeys[key] {
			return ToolDefinition{}, fmt.Errorf("%w: unknown cli spec field %q", ErrInvalid, key)
		}
	}
	name := argString(spec, "name")
	if !identity.ValidID(name) {
		return ToolDefinition{}, fmt.Errorf("%w: cli name", ErrInvalid)
	}
	version := argString(spec, "version")
	if version == "" {
		version = "1.0.0"
	}
	command := argString(spec, "command")
	if command == "" {
		return ToolDefinition{}, fmt.Errorf("%w: cli command", ErrInvalid)
	}
	fixed, err := strictStringList(spec["args"], "args")
	if err != nil {
		return ToolDefinition{}, err
	}
	egress, err := strictStringList(spec["egress"], "egress")
	if err != nil {
		return ToolDefinition{}, err
	}
	if len(egress) == 0 {
		egress = []string{"127.0.0.1"}
	}
	runtimeEnv, err := cliSpecEnvironment(spec["runtime_env"])
	if err != nil {
		return ToolDefinition{}, err
	}
	credentials, err := strictStringList(spec["credentials"], "credentials")
	if err != nil {
		return ToolDefinition{}, err
	}
	inputs := make([]CredentialInput, 0, len(credentials))
	for _, credential := range credentials {
		inputs = append(inputs, CredentialInput{Name: credential, Required: true})
	}
	tools, err := cliSpecTools(spec["tools"])
	if err != nil {
		return ToolDefinition{}, err
	}
	if err := cliSpecExecDenied(command, fixed, tools); err != nil {
		return ToolDefinition{}, err
	}
	definition := ToolDefinition{
		Schema: SchemaVersion, DefinitionID: name, Version: version, Transport: BoundedCLI,
		Source:             DefinitionSource{Command: command, Args: fixed},
		RuntimeEnvironment: runtimeEnv,
		Credentials:        inputs,
		Tools:              tools,
		Workload:           cliSpecWorkload(spec, shared),
		Execution:          ExecutionPolicy{TimeoutSeconds: 60, OutputBytes: 262144, CPUMillis: 2000, MemoryMiB: 2048, MaxPIDs: 128, Egress: egress},
		Health:             HealthProbe{Kind: "exec", Value: command, TimeoutSeconds: 5},
	}
	if err := definition.Validate(); err != nil {
		return ToolDefinition{}, err
	}
	return definition, nil
}

// strictStringList takes an argv-shaped list verbatim: entries are literal
// tokens, never trimmed, never silently dropped, and bounded per token.
func strictStringList(raw any, field string) ([]string, error) {
	if raw == nil {
		return nil, nil
	}
	var items []any
	switch typed := raw.(type) {
	case []string:
		for _, item := range typed {
			items = append(items, item)
		}
	case []any:
		items = typed
	default:
		return nil, fmt.Errorf("%w: cli spec %s must be a list", ErrInvalid, field)
	}
	if len(items) > 64 {
		return nil, fmt.Errorf("%w: cli spec %s is too long", ErrInvalid, field)
	}
	out := make([]string, 0, len(items))
	for _, item := range items {
		token, ok := item.(string)
		if !ok || token == "" || len(token) > 256 || strings.ContainsAny(token, "\x00\r\n") {
			return nil, fmt.Errorf("%w: cli spec %s token", ErrInvalid, field)
		}
		out = append(out, token)
	}
	return out, nil
}

func cliSpecEnvironment(raw any) (map[string]string, error) {
	if raw == nil {
		return nil, nil
	}
	values, ok := raw.(map[string]any)
	if !ok || len(values) > 32 {
		return nil, fmt.Errorf("%w: cli spec runtime_env", ErrInvalid)
	}
	env := make(map[string]string, len(values))
	for key, value := range values {
		text, ok := value.(string)
		if !ok || !credentialPattern.MatchString(key) || text == "" || len(text) > 16384 || strings.ContainsAny(text, "\x00\r\n") || strings.Contains(text, "${") {
			return nil, fmt.Errorf("%w: cli spec runtime env %q", ErrInvalid, key)
		}
		env[key] = text
	}
	return env, nil
}

func cliSpecTools(raw any) ([]ToolSpec, error) {
	list, ok := raw.([]any)
	if !ok || len(list) == 0 || len(list) > 16 {
		return nil, fmt.Errorf("%w: cli spec tools", ErrInvalid)
	}
	tools := make([]ToolSpec, 0, len(list))
	for _, item := range list {
		entry, ok := item.(map[string]any)
		if !ok {
			return nil, fmt.Errorf("%w: cli spec tool", ErrInvalid)
		}
		for key := range entry {
			if key != "name" && key != "description" && key != "effect" && key != "arguments" {
				return nil, fmt.Errorf("%w: unknown cli spec tool field %q", ErrInvalid, key)
			}
		}
		effect := ReadEffect
		switch argString(entry, "effect") {
		case "", "read":
		case "write":
			effect = WriteEffect
		default:
			return nil, fmt.Errorf("%w: cli spec tool effect", ErrInvalid)
		}
		arguments, err := cliSpecArguments(entry["arguments"])
		if err != nil {
			return nil, err
		}
		tools = append(tools, ToolSpec{
			Name: argString(entry, "name"), Description: argString(entry, "description"),
			Effect: effect, Arguments: arguments,
		})
	}
	return tools, nil
}

func cliSpecArguments(raw any) ([]CLIArgument, error) {
	if raw == nil {
		return nil, nil
	}
	list, ok := raw.([]any)
	if !ok || len(list) > 32 {
		return nil, fmt.Errorf("%w: cli spec tool arguments", ErrInvalid)
	}
	arguments := make([]CLIArgument, 0, len(list))
	for _, item := range list {
		entry, ok := item.(map[string]any)
		if !ok {
			return nil, fmt.Errorf("%w: cli spec argument", ErrInvalid)
		}
		for key := range entry {
			if key != "name" && key != "flag" && key != "type" && key != "required" && key != "pattern" {
				return nil, fmt.Errorf("%w: unknown cli spec argument field %q", ErrInvalid, key)
			}
		}
		kind := argString(entry, "type")
		if kind == "" {
			kind = "string"
		}
		required, _ := entry["required"].(bool)
		arguments = append(arguments, CLIArgument{
			Name: argString(entry, "name"), Flag: argString(entry, "flag"), Type: kind, Required: required,
			Pattern: argString(entry, "pattern"),
		})
	}
	return arguments, nil
}

// cliSpecExecDenied rejects spec argv that turns an allowlisted binary into a
// program runner (rg --pre executes a user-supplied path per file). The check
// covers fixed tokens and declared argument flags alike.
func cliSpecExecDenied(command string, fixed []string, tools []ToolSpec) error {
	denied, known := cliSpecDeniedArgs[command]
	if !known {
		return nil
	}
	if len(denied) == 0 {
		return fmt.Errorf("%w: %q may not be registered: it executes hook scripts from writable data", ErrUnauthorized, command)
	}
	match := func(token string) bool {
		for _, flag := range denied {
			if token == flag || strings.HasPrefix(token, flag+"=") {
				return true
			}
		}
		return false
	}
	for _, token := range fixed {
		// Owner spec argv is long-option shaped only: a bare token is a
		// positional operand (for rg a literal path, which escapes the
		// contained workspace), and short-option clusters cannot be
		// deny-checked reliably. Operands arrive through typed arguments.
		if !strings.HasPrefix(token, "--") || len(token) < 3 {
			return fmt.Errorf("%w: cli spec argv %q is not a long option; operands come from typed arguments", ErrUnauthorized, token)
		}
		if match(token) {
			return fmt.Errorf("%w: cli spec argv %q executes a program or reads outside the workspace", ErrUnauthorized, token)
		}
	}
	for _, tool := range tools {
		for _, argument := range tool.Arguments {
			if match(argument.Flag) {
				return fmt.Errorf("%w: cli spec flag %q executes a program", ErrUnauthorized, argument.Flag)
			}
		}
	}
	return nil
}
