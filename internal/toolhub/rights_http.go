package toolhub

import (
	"crypto/subtle"
	"encoding/json"
	"net/http"
	"strings"
	"time"
)

// serveRightsDecide is the out-of-process PDP door (SPEC-0092 R2.2): PEPs
// that run outside ToolHub's address space — model-relay, supervisor,
// communication-hub — ask Decide over this route. The principal always
// comes from the bearer envelope; a body-supplied principal is ignored and
// a foreign one denies.
func (g *Gateway) serveRightsDecide(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	if r.Header.Get("Origin") != "" {
		http.Error(w, "browser origins forbidden", http.StatusForbidden)
		return
	}
	bearer := strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer ")
	var request struct {
		Capability  string `json:"capability"`
		Operation   string `json:"operation"`
		Scope       string `json:"scope,omitempty"`
		PrincipalID string `json:"principal_id,omitempty"`
		Environment string `json:"environment,omitempty"`
	}
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<20)).Decode(&request); err != nil {
		http.Error(w, "bad request", http.StatusBadRequest)
		return
	}
	var decision any
	// Control-plane bearer: trusted in-space services (supervisor,
	// model-relay, communication-hub) assert a principal this deployment
	// serves; the ceiling resolves from its active profile. Deployments
	// sharing one ToolHub hold every sibling space's profiles, so the
	// boundary is the store's profile set — a principal with no active
	// profile here is a cross-space assertion, refused outright.
	if g.ControlToken != "" && subtle.ConstantTimeCompare([]byte(bearer), []byte(g.ControlToken)) == 1 {
		if request.PrincipalID == "" || (request.PrincipalID != g.SpacePrincipal && !g.Store.HasActiveProfile(request.PrincipalID)) {
			http.Error(w, `{"allow":false,"reason":"principal outside this space"}`, http.StatusForbidden)
			return
		}
		out, err := g.Store.DecideAsserted(request.PrincipalID, request.Environment, request.Capability, request.Operation, request.Scope, time.Now())
		if err != nil {
			http.Error(w, "decision unavailable", http.StatusServiceUnavailable)
			return
		}
		decision = out
	} else {
		auth, ok := g.connectorAuth(r)
		if !ok {
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}
		// Envelope wins: a body principal that differs from the bearer's is a
		// forgery attempt — deny, never trust it (SPEC-0092 red-team).
		if request.PrincipalID != "" && request.PrincipalID != auth.PrincipalID {
			decision = map[string]any{"allow": false, "reason": "foreign principal"}
		} else {
			out, err := g.Store.Decide(auth, request.Capability, request.Operation, request.Scope, time.Now())
			if err != nil {
				http.Error(w, "decision unavailable", http.StatusServiceUnavailable)
				return
			}
			decision = out
		}
	}
	w.Header().Set("Content-Type", "application/json")
	if err := json.NewEncoder(w).Encode(decision); err != nil {
		return
	}
}
