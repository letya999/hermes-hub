package supervisor

import (
	"net/http"
	"path/filepath"

	"github.com/letya999/hermes-hub/internal/passportview"
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
	space := filepath.Join(m.cfg.SpacesRoot, principal)
	reference, _, err := passportview.CurrentReferenceGenerated(filepath.Join(space, "generated"))
	return err == nil && reference == digest
}
