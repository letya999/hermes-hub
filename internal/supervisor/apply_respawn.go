package supervisor

import (
	"net/http"

	spawnv1 "github.com/letya999/hermes-hub/contracts/spawn/v1"
)

// An operator apply respawns the runtime while the digest it just
// activated is still live — the header binds the request to that state
// (S7b: the apply journal is gone; the effective digest is the anchor).
// The shared bearer is still required by serveHTTP; this narrow check
// binds the respawn to the space's committed pointer.
func (m *Manager) applyRespawnAuthorized(r *http.Request, principal, contextID, runtimeID string) bool {
	digest := r.Header.Get("X-Hub-Apply-Revision")
	if digest == "" || principal != contextID || runtimeID != "" {
		return false
	}
	if m.cfg.Spawn == nil {
		return false
	}
	spec, err := m.cfg.Spawn.Effective(r.Context(), spawnv1.Tenant{Org: "local", Owner: principal}, principal)
	return err == nil && spec.Digest == digest
}
