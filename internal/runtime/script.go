package runtime

import "time"

// ScriptRequest uses the ordinary owner envelope without starting Hermes.
type ScriptRequest struct {
	ExecuteRequest
	DueAt time.Time `json:"due_at"`
	// Input is the bounded fetch/extract fragment passed to input() inside
	// the probe script (SPEC-0059).
	Input string `json:"input,omitempty"`
	// MountSpace mounts the owner's spaces/<user>/watch directory read-only
	// at /watch for file-state probes. Never caller-supplied paths.
	MountSpace bool `json:"mount_space,omitempty"`
	// Needs carries hub-prefetched read-tool results into the isolated
	// runtime as the needs() builtin (SPEC-0059 R5.2). Max four entries,
	// each bounded; the container still has no network or tool access.
	Needs []string `json:"needs,omitempty"`
}
