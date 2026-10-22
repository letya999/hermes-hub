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

// ListOnboardings returns every onboarding record regardless of principal;
// only the admin diagnostics surface (issue 141) calls it.
func (s *Store) ListOnboardings() ([]api.Onboarding, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	out := make([]api.Onboarding, 0, len(s.onboardings))
	for _, o := range s.onboardings {
		out = append(out, o)
	}
	return out, nil
}

// OnboardingRecord reads one onboarding by id across principals; only the
// admin diagnostics surface (issue 141) calls it.
func (s *Store) OnboardingRecord(id string) (api.Onboarding, error) {
	return s.onboarding(id)
}

func (s *Store) onboarding(id string) (api.Onboarding, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	onboarding, ok := s.onboardings[id]
	if !ok {
		return api.Onboarding{}, fmt.Errorf("%w: onboarding", api.ErrNotFound)
	}
	return onboarding, nil
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
