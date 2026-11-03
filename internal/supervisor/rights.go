package supervisor

import (
	"fmt"
	"log"
	"net/http"
	"os"
	"strings"

	"github.com/letya999/hermes-hub/internal/toolhub/api"
)

// rightsGate is the supervisor-side PEP (SPEC-0092 R2.2): every
// runtime-facing route that mutates or inspects the caller's own runtime
// decides agents.* against the deployment's ToolHub first. A %s slot in
// the URL pattern carries the asserted principal for per-space toolhub
// addressing; a slotless pattern names the shared control plane whose
// profile store bounds which principals may be asserted — a supervisor
// shared across spaces can never mint cross-space authority either way.
//
// Empty HUB_TOOLHUB_URL_PATTERN leaves the legacy posture: deployments
// without toolhub addressing keep admitting on the shared bearer alone.
func (m *Manager) rightsGate(w http.ResponseWriter, r *http.Request, principal, capability, operation, scope string) bool {
	pattern := strings.TrimSpace(os.Getenv("HUB_TOOLHUB_URL_PATTERN"))
	if pattern == "" {
		return true
	}
	url := pattern
	if strings.Contains(pattern, "%s") {
		url = fmt.Sprintf(pattern, principal)
	}
	allow, err := api.DecideClient{
		URL:         url,
		Token:       m.cfg.RuntimeAuth,
		Principal:   principal,
		Environment: m.cfg.Environment,
	}.GrantsRight(r.Context(), capability, operation, scope)
	if err != nil {
		log.Printf("supervisor rights decide principal=%s right=%s.%s outcome=error err=%v", principal, capability, operation, err)
	}
	if !allow {
		writeJSON(w, http.StatusForbidden, map[string]string{"error": "right denied"})
		return false
	}
	return true
}
