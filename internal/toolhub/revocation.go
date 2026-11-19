package toolhub

import (
	"crypto/subtle"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	brokerv1 "github.com/letya999/credential-broker/api/v1"
	"github.com/letya999/hermes-hub/internal/identity"
	"github.com/letya999/hermes-hub/internal/toolhub/api"
)

// PrincipalRevocation is the bounded result of one authority-surface stage.
type PrincipalRevocation struct {
	Profiles    int `json:"profiles,omitempty"`
	Grants      int `json:"grants,omitempty"`
	Bindings    int `json:"bindings,omitempty"`
	Connections int `json:"connections,omitempty"`
	Schedules   int `json:"schedules,omitempty"`
}

// RevokePrincipalAuthority revokes capability profiles and tool grants owned by
// one principal/context. Revocation deliberately bypasses admission re-checks:
// a disabled policy must not block removal of the authority it issued.
func (s *Store) RevokePrincipalAuthority(principal, contextID string, now time.Time) (PrincipalRevocation, error) {
	if !identity.ValidID(principal) || !identity.ValidID(contextID) {
		return PrincipalRevocation{}, api.ErrInvalid
	}
	if err := s.Reload(); err != nil {
		return PrincipalRevocation{}, err
	}
	now = now.UTC()
	var report PrincipalRevocation
	s.mu.Lock()
	for id, profile := range s.capabilityProfiles {
		if profile.PrincipalID != principal || profile.ContextID != contextID || profile.Status == api.RevokedStatus {
			continue
		}
		old := profile
		profile.Status = api.RevokedStatus
		profile.Revision++
		profile.IssuedBy = "platform"
		profile.IssuedAt = now
		profile.Reason = "principal revocation"
		profile.Confirmation = nil
		if err := api.Confirm(&profile, "platform", now); err != nil {
			s.mu.Unlock()
			return report, err
		}
		if err := profile.Validate(); err != nil {
			s.mu.Unlock()
			return report, err
		}
		before := len(s.capabilityChanges)
		s.capabilityProfiles[id] = profile
		s.capabilityChanges = append(s.capabilityChanges, api.CapabilityChange{Profile: &profile})
		if s.path != "" {
			if err := s.saveLocked(s.path); err != nil {
				s.capabilityChanges = s.capabilityChanges[:before]
				s.capabilityProfiles[id] = old
				s.mu.Unlock()
				return report, err
			}
		}
		report.Profiles++
	}
	for id, grant := range s.grants {
		if grant.PrincipalID != principal || grant.Status == api.RevokedStatus {
			continue
		}
		old := grant
		grant.Status = api.RevokedStatus
		grant.Revision++
		grant.IssuedBy = "platform"
		grant.Confirmation = nil
		if err := api.Confirm(&grant, "platform", now); err != nil {
			s.mu.Unlock()
			return report, err
		}
		if err := grant.Validate(); err != nil {
			s.mu.Unlock()
			return report, err
		}
		before := len(s.capabilityChanges)
		s.capabilityChanges = append(s.capabilityChanges, api.CapabilityChange{Grant: &grant})
		s.grants[id] = grant
		if s.path != "" {
			if err := s.saveLocked(s.path); err != nil {
				s.capabilityChanges = s.capabilityChanges[:before]
				s.grants[id] = old
				s.mu.Unlock()
				return report, err
			}
		}
		report.Grants++
	}
	s.mu.Unlock()
	return report, nil
}

// RevokePrincipalConnectors revokes the principal's bindings and only those
// connections that are personally owned by that principal. Shared team and
// context-owned connections remain authoritative for surviving members.
func (s *Store) RevokePrincipalConnectors(principal, contextID string) (PrincipalRevocation, error) {
	if !identity.ValidID(principal) || !identity.ValidID(contextID) {
		return PrincipalRevocation{}, api.ErrInvalid
	}
	if err := s.Reload(); err != nil {
		return PrincipalRevocation{}, err
	}
	var report PrincipalRevocation
	bindings := map[string]struct{}{}
	connections := map[string]struct{}{}
	s.mu.RLock()
	for id, binding := range s.bindings {
		if binding.PrincipalID == principal && binding.ContextID == contextID && binding.Status != api.RevokedStatus {
			bindings[id] = struct{}{}
		}
	}
	for id, connection := range s.connections {
		if connection.Owner.Type == api.PrincipalOwner && connection.Owner.ID == principal && connection.Status != api.RevokedStatus {
			connections[id] = struct{}{}
		}
	}
	s.mu.RUnlock()
	for id := range bindings {
		if err := s.SetBindingStatus(id, api.RevokedStatus); err != nil && !errors.Is(err, api.ErrNotFound) {
			return report, err
		}
		report.Bindings++
	}
	for id := range connections {
		if err := s.SetConnectionStatus(id, api.RevokedStatus); err != nil && !errors.Is(err, api.ErrNotFound) {
			return report, err
		}
		report.Connections++
	}
	return report, nil
}

// revokePrincipalOAuth delegates the OAuth/grant stage to the Broker through
// ToolHub's configured control signer. The actor remains the exact target
// principal; context_manager only lets the bulk call cover shared-context
// credentials that the same principal can already manage.
func (g *Gateway) revokePrincipalOAuth(r *http.Request, auth identity.Envelope) (PrincipalRevocation, error) {
	if g.Control == nil || g.Control.Broker == nil || !g.Control.Broker.Enabled() {
		return PrincipalRevocation{}, fmt.Errorf("%w: credential broker unavailable", api.ErrIsolation)
	}
	control, err := g.Control.Broker.NewContextManaged(auth, "broker:control")
	if err != nil {
		return PrincipalRevocation{}, err
	}
	out, err := control.RevokePrincipalGrants(r.Context(), brokerv1.PrincipalRevocationRequest{PrincipalID: auth.PrincipalID, ContextID: auth.ContextID})
	if err != nil {
		return PrincipalRevocation{}, err
	}
	return PrincipalRevocation{Grants: out.Grants}, nil
}

// servePrincipalRevocation is the control-plane revocation boundary. The
// service bearer selects the enrolled target through X-Hub-Principal; the body
// must pin that same principal/context so a header cannot widen the scope.
func (g *Gateway) servePrincipalRevocation(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	if r.Header.Get("Origin") != "" {
		http.Error(w, "browser origins forbidden", http.StatusForbidden)
		return
	}
	value := strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer ")
	principal := strings.TrimSpace(r.Header.Get("X-Hub-Principal"))
	if g.ControlToken == "" || subtle.ConstantTimeCompare([]byte(value), []byte(g.ControlToken)) != 1 || !identity.ValidID(principal) {
		http.Error(w, "unauthorized", http.StatusUnauthorized)
		return
	}
	var body struct {
		PrincipalID string `json:"principal_id"`
		ContextID   string `json:"context_id"`
		Stage       string `json:"stage"`
	}
	d := json.NewDecoder(http.MaxBytesReader(w, r.Body, 64<<10))
	d.DisallowUnknownFields()
	if err := d.Decode(&body); err != nil || d.Decode(new(any)) != io.EOF || body.PrincipalID != principal || !identity.ValidID(body.ContextID) || body.Stage != "toolhub" && body.Stage != "connectors" && body.Stage != "oauth" && body.Stage != "cron" {
		http.Error(w, "invalid revocation request", http.StatusBadRequest)
		return
	}
	g.mu.Lock()
	var auth identity.Envelope
	enrolled := false
	for _, candidate := range g.Tokens {
		if candidate.PrincipalID == principal && candidate.ContextID == body.ContextID {
			auth, enrolled = candidate, true
			break
		}
	}
	g.mu.Unlock()
	if !enrolled {
		http.Error(w, "unauthorized", http.StatusUnauthorized)
		return
	}
	var report PrincipalRevocation
	var err error
	switch body.Stage {
	case "toolhub":
		report, err = g.Store.RevokePrincipalAuthority(body.PrincipalID, body.ContextID, time.Now().UTC())
	case "connectors":
		report, err = g.Store.RevokePrincipalConnectors(body.PrincipalID, body.ContextID)
	case "oauth":
		report, err = g.revokePrincipalOAuth(r, auth)
	case "cron":
		if g.pauseSchedules != nil {
			report.Schedules, err = g.pauseSchedules(r.Context(), auth)
		}
	}
	if err != nil {
		http.Error(w, "revocation failed", http.StatusInternalServerError)
		return
	}
	if g.Governance != nil && body.Stage == "toolhub" {
		grants, listErr := g.Governance.RightGrants()
		if listErr != nil {
			http.Error(w, "revocation failed", http.StatusInternalServerError)
			return
		}
		for _, grant := range grants {
			if grant.PrincipalID == auth.PrincipalID && grant.Status != api.GrantRevoked {
				if err := g.Governance.RevokeRight(grant.GrantID, "platform", "principal revocation", time.Now().UTC()); err != nil {
					http.Error(w, "revocation failed", http.StatusInternalServerError)
					return
				}
				report.Grants++
			}
		}
	}
	if body.Stage == "toolhub" || body.Stage == "connectors" {
		if err := g.RefreshProjection(); err != nil {
			http.Error(w, "revocation failed", http.StatusInternalServerError)
			return
		}
	}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(report)
}
