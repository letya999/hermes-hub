package toolhub

import (
	"fmt"
	"github.com/letya999/hermes-hub/internal/identity"
	"github.com/letya999/hermes-hub/internal/toolhub/api"
)

func (s *Store) Definition(id, version string) (api.ToolDefinition, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	definition, ok := s.definitions[definitionKey(id, version)]
	if !ok {
		return api.ToolDefinition{}, fmt.Errorf("%w: definition", api.ErrNotFound)
	}
	return definition, nil
}

func (s *Store) publicationFor(definition api.ToolDefinition) api.DefinitionPublication {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.publicationLocked(definition)
}

func (s *Store) storedPublication(id, version string) (api.DefinitionPublication, bool) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	pub, ok := s.publications[definitionKey(id, version)]
	return pub, ok
}

func (s *Store) userPublicationOwner(id, version string) (string, bool) {
	pub, ok := s.storedPublication(id, version)
	if !ok || pub.Visibility != api.PublicationUser {
		return "", false
	}
	return pub.OwnerPrincipalID, true
}

// credentialOwner names the principal a connection's credential ciphertext
// is stored under.
func (s *Store) credentialOwner(connectionID string) string {
	if connection, ok := s.connectionFor(connectionID); ok {
		return connection.Owner.ID
	}
	return ""
}

// onboardingForConnection finds the onboarding that materialized the given
// connection — the record a reconcile notice re-arms so the channel link
// lands on a live step.
func (s *Store) onboardingForConnection(connectionID string) (api.Onboarding, bool) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	connection, ok := s.connections[connectionID]
	if !ok {
		return api.Onboarding{}, false
	}
	var selected api.Onboarding
	found := false
	for _, onboarding := range s.onboardings {
		if onboarding.ConnectionID != connectionID || onboarding.SupersededBy != "" ||
			onboarding.Phase == api.PhaseRemoved || onboarding.Phase == api.PhaseRevoked {
			continue
		}
		if connection.Owner.Type == api.PrincipalOwner && onboarding.PrincipalID != connection.Owner.ID {
			continue
		}
		if connection.Owner.Type == api.ContextOwner && onboarding.ContextID != connection.Owner.ID {
			continue
		}
		if onboarding.CredentialRefID != "" && onboarding.CredentialRefID != connection.CredentialRefID {
			continue
		}
		current := onboarding.CredentialRefID != ""
		selectedCurrent := selected.CredentialRefID != ""
		newer := onboarding.CreatedAt.After(selected.CreatedAt) ||
			(onboarding.CreatedAt.Equal(selected.CreatedAt) && onboarding.OnboardingID > selected.OnboardingID)
		if !found || (current && !selectedCurrent) || (current == selectedCurrent && newer) {
			selected, found = onboarding, true
		}
	}
	return cloneOnboarding(selected), found
}

func (s *Store) onboarding(id string) (api.Onboarding, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	onboarding, ok := s.onboardings[id]
	if !ok {
		return api.Onboarding{}, fmt.Errorf("%w: onboarding", api.ErrNotFound)
	}
	return cloneOnboarding(onboarding), nil
}

func (s *Store) binding(id string) (api.ToolBinding, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	binding, ok := s.bindings[id]
	if !ok {
		return api.ToolBinding{}, fmt.Errorf("%w: binding", api.ErrNotFound)
	}
	return binding, nil
}

// brokerGrantReusable reports whether the broker grant stored on a reused
// credential still authorizes the binding this enable would run under. A grant
// is minted for the deterministic binding ID — which folds in the definition
// version and credential reference — and pins the admitting policy epoch. A
// grant's recorded binding identity also survives a failed readiness probe,
// before any enabled binding exists. Any version or epoch change requires a
// fresh grant. The epoch recorded at mint time is the source of truth:
// policy migration rewrites the binding
// record, so its version no longer proves the grant accepts the actor.
func (s *Store) brokerGrantReusable(auth identity.Envelope, definition api.ToolDefinition, match ownerConnectionMatch) bool {
	if match.credential.BrokerGrantID == "" || match.credential.BrokerGrantPolicy != auth.PolicyVersion {
		return false
	}
	id := api.DeterministicBindingID(auth.PrincipalID, auth.ContextID, auth.RuntimeID, definition.DefinitionID, definition.Version, match.connection.ConnectionID, match.credential.CredentialRefID)
	s.mu.RLock()
	defer s.mu.RUnlock()
	binding, ok := s.bindings[id]
	if ok && binding.Status == api.RevokedStatus {
		return false
	}
	if match.credential.BrokerGrantBinding != "" {
		return match.credential.BrokerGrantBinding == id
	}
	return ok && binding.Status != api.RevokedStatus && binding.PolicyVersion == auth.PolicyVersion
}

func (s *Store) findBinding(auth identity.Envelope, definition api.ToolDefinition) *api.ToolBinding {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.findBindingLocked(auth, definition)
}

func (s *Store) WorkloadIDsForBinding(bindingID string) []string {
	s.mu.RLock()
	defer s.mu.RUnlock()
	ids := make([]string, 0)
	for _, workload := range s.workloads {
		if workload.BindingID == bindingID {
			ids = append(ids, workload.WorkloadID)
		}
	}
	return ids
}
