package supervisor

import "github.com/letya999/hermes-hub/internal/stack"

// The image-pin types and resolution seam moved to internal/stack
// (runtime_image.go, CHG-0336 S7c): spawn-controller resolves the managed
// workload image through the same code. These aliases keep the existing
// supervisor-side readers — image pins CLI, devcheck retention, script and
// kubernetes callers — compiling against the moved implementation.
type (
	RolloutPin  = stack.RolloutPin
	CanaryPin   = stack.CanaryPin
	RolloutEnv  = stack.RolloutEnv
	RolloutPins = stack.RolloutPins
)

var (
	ReadRolloutPins     = stack.ReadRolloutPins
	WriteRolloutPins    = stack.WriteRolloutPins
	resolveRuntimeImage = stack.ResolveRuntimeImage
	rolloutPinsPath     = stack.RolloutPinsPath
	devImage            = stack.DevImage
	devRuntimeImage     = stack.DevRuntimeImage
	devScriptImage      = stack.DevScriptImage
)

// imageSourceOf resolves the winning source for the runtime record; a
// resolution failure (e.g. a forged dev pin) leaves the field empty — the
// spawn side reports the same error when the claim plans the workload.
func imageSourceOf(binding Binding, env, flag string) string {
	_, source, err := stack.ResolveRuntimeImage(binding.ContextRoot, binding.UserID, binding.ContextID, env, flag)
	if err != nil {
		return ""
	}
	return source
}
