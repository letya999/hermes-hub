package runtime

import "time"

// ScriptRequest uses the ordinary owner envelope without starting Hermes.
type ScriptRequest struct {
	ExecuteRequest
	DueAt time.Time `json:"due_at"`
	// Input is the bounded fetch/extract fragment passed to input() inside
	// the probe script (SPEC-0057).
	Input string `json:"input,omitempty"`
	// MountSpace mounts the owner's spaces/<user>/watch directory read-only
	// at /watch for file-state probes. Never caller-supplied paths.
	MountSpace bool `json:"mount_space,omitempty"`
}
