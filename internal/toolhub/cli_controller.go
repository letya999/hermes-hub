package toolhub

// The bounded-cli admission path moved off the executor container: /cli-exec
// creates a sibling sandbox cell per call (or reclaims a canary-verified warm
// one) and the receipt attests that cell — its mounts, network and cgroup
// profile re-inspected before it ever runs the tool. Nothing CLI-shaped runs
// inside the ToolHub container anymore.

import (
	"fmt"
	"path/filepath"
	"strings"

	"github.com/letya999/hermes-hub/internal/identity"
)

var cliIsolationScopes = []string{"filesystem", "network", "pid"}

// planApproved reports whether the exec request matches one operator
// approval shape: an exact Approved tuple, a catalog command on the
// UserCommands list, or a digest-pinned artifact plan — owner artifacts are
// registered through the immutable build pipeline, so the reviewed image
// manifest digest plus an absolute guest command is the attested tuple.
func planApproved(cfg *CLIControllerConfig, plan controllerPlan, command string) bool {
	for _, approval := range cfg.Approved {
		if approval.DefinitionID == plan.DefinitionID && approval.DefinitionVersion == plan.DefinitionVersion && approval.Command == command && approval.Image == plan.Image && approval.Digest == plan.Digest && reflectExecution(approval.Execution, plan.Execution) {
			return true
		}
	}
	if plan.Image != "" {
		return digestPattern.MatchString(plan.Digest) && strings.HasPrefix(command, "/") && validCommand(command)
	}
	return contains(cfg.UserCommands, command)
}

// validateCLIConfig runs at controller construction: the tools image and
// cellinit helper are the spine every cell needs, and the ceiling must parse
// before any plan compares against it.
func validateCLIConfig(cfg *CLIControllerConfig) error {
	if cfg == nil {
		return nil
	}
	if err := validCellImageRef(cfg.ToolsImage); err != nil {
		return fmt.Errorf("%w: cli tools image", ErrInvalid)
	}
	if cfg.CellInit == "" || !filepath.IsAbs(cfg.CellInit) || noSymlinkPath(cfg.CellInit) != nil {
		return fmt.Errorf("%w: cellinit path", ErrInvalid)
	}
	if cfg.ProxyImage != "" {
		if err := validCellImageRef(cfg.ProxyImage); err != nil {
			return fmt.Errorf("%w: cli proxy image", ErrInvalid)
		}
	}
	if cfg.Runtime != "" && cfg.Runtime != "runc" && cfg.Runtime != "runsc" && cfg.Runtime != "kata" {
		return fmt.Errorf("%w: unknown cli runtime %q", ErrInvalid, cfg.Runtime)
	}
	if cfg.SpacesRoot != "" && (!filepath.IsAbs(cfg.SpacesRoot) || noSymlinkPath(cfg.SpacesRoot) != nil) {
		return fmt.Errorf("%w: cli spaces root", ErrInvalid)
	}
	if cfg.Environment != "" && !identity.ValidID(cfg.Environment) {
		return fmt.Errorf("%w: cli environment %q", ErrInvalid, cfg.Environment)
	}
	if len(cfg.AllowedEgress) == 0 {
		return fmt.Errorf("%w: CLI egress allowlist", ErrInvalid)
	}
	for _, host := range cfg.AllowedEgress {
		if !hostPattern.MatchString(strings.ToLower(host)) && host != "*" {
			return fmt.Errorf("%w: CLI egress allowlist host %q", ErrInvalid, host)
		}
	}
	if err := cfg.Ceiling.validate(PerUser); err != nil {
		return err
	}
	if len(cfg.Approved) > 64 || len(cfg.UserCommands) > 64 || (len(cfg.Approved) == 0 && len(cfg.UserCommands) == 0) {
		return fmt.Errorf("%w: CLI approval list", ErrInvalid)
	}
	for _, approval := range cfg.Approved {
		if !identity.ValidID(approval.DefinitionID) || !versionPattern.MatchString(approval.DefinitionVersion) {
			return fmt.Errorf("%w: CLI approval identity", ErrInvalid)
		}
		commandOK := commandPattern.MatchString(approval.Command) && !strings.ContainsAny(approval.Command, "/")
		if approval.Image != "" {
			if !digestPattern.MatchString(approval.Digest) || !strings.HasPrefix(approval.Command, "/") || !validCommand(approval.Command) {
				return fmt.Errorf("%w: CLI artifact approval needs image digest and absolute command", ErrInvalid)
			}
		} else if !commandOK {
			return fmt.Errorf("%w: CLI approval command %q", ErrInvalid, approval.Command)
		}
		if err := approval.Execution.validate(PerUser); err != nil {
			return err
		}
		if !executionWithin(approval.Execution, cfg.Ceiling) {
			return fmt.Errorf("%w: CLI approval exceeds ceiling", ErrInvalid)
		}
	}
	for _, command := range cfg.UserCommands {
		if !commandPattern.MatchString(command) || strings.Contains(command, "/") {
			return fmt.Errorf("%w: user CLI command %q", ErrInvalid, command)
		}
	}
	if len(cfg.Toolboxes) > 16 {
		return fmt.Errorf("%w: too many CLI toolboxes", ErrInvalid)
	}
	for name, members := range cfg.Toolboxes {
		if !identity.ValidID(name) || len(members) == 0 || len(members) > 32 {
			return fmt.Errorf("%w: CLI toolbox %q", ErrInvalid, name)
		}
		for _, ref := range members {
			if err := validPinnedImageRef(ref); err != nil {
				return fmt.Errorf("%w: CLI toolbox %q member", ErrInvalid, name)
			}
		}
	}
	if cfg.PoolSize < 0 || cfg.PoolSize > 16 || cfg.PoolClaimWaitSeconds < 0 || cfg.PoolClaimWaitSeconds > 300 || cfg.WarmMaxAgeSeconds < 0 || cfg.WarmMaxAgeSeconds > 7*24*3600 || cfg.WarmMaxReuse < 0 || cfg.WarmMaxReuse > 1<<20 {
		return fmt.Errorf("%w: CLI pool/warm bounds", ErrInvalid)
	}
	return nil
}

// executionWithin reports whether every numeric bound of a dynamic plan fits
// the configured ceiling; mounts and forwards already failed closed earlier.
func executionWithin(plan, ceiling ExecutionPolicy) bool {
	return plan.TimeoutSeconds <= ceiling.TimeoutSeconds && plan.OutputBytes <= ceiling.OutputBytes && plan.CPUMillis <= ceiling.CPUMillis && plan.MemoryMiB <= ceiling.MemoryMiB && plan.MaxPIDs <= ceiling.MaxPIDs && len(plan.Mounts) == 0 && len(plan.TCPForwards) == 0
}
