package api

import (
	task "github.com/letya999/hermes-hub/contracts/task/v1"
	"github.com/letya999/hermes-hub/internal/memory"
)

// RunContext remains the compatibility name of the task contract DTO.
type RunContext = task.RunRecord

// RunKind is the admission-relevant type of a recorded run.
type RunKind string

const (
	// RunKindPrivate is an interactive owner turn in private scope.
	RunKindPrivate RunKind = "private"
	// RunKindGroup is any shared/group-audience run.
	RunKindGroup RunKind = "group"
	// RunKindBackground covers autonomous and system runs (goal, cron,
	// protected forms) — only verified owner assignments may use owner memory.
	RunKindBackground RunKind = "background"
)

// Kind classifies the recorded run: the scope fixes the audience first —
// unknown scopes resolve to group so a crafted scope can never mint private
// context — and the trigger then separates the interactive owner turn from
// background/system work.
func Kind(r RunContext) RunKind {
	if memory.AudienceOf(r.ScopeID) != memory.AudiencePrivate {
		return RunKindGroup
	}
	if r.Trigger != "message" {
		return RunKindBackground
	}
	return RunKindPrivate
}

// MayUseOwnerMemory classifies the audience and purpose. Lifecycle, identity,
// selection, grants and enablement are checked separately at every call.
func MayUseOwnerMemory(r RunContext) bool {
	return !r.Sensitive && (Kind(r) == RunKindPrivate ||
		Kind(r) == RunKindBackground && r.Assignment && r.UserID != "" && r.UserID == r.Envelope.PrincipalID)
}
