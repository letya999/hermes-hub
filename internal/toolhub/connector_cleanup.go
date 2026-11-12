package toolhub

import (
	"fmt"
	"strings"
	"time"

	"github.com/letya999/hermes-hub/internal/identity"
	"github.com/letya999/hermes-hub/internal/toolhub/api"
)

// claimRemovalTarget gives an owner a current-policy cleanup record for an
// older onboarding or an owned connection whose onboarding was lost. The
// record is only a selector: no authorization is cut until owner confirmation.
func (s *Store) claimRemovalTarget(auth identity.Envelope, target string) (ConnectorTarget, error) {
	if err := auth.Validate(auth.PrincipalID, auth.ContextID, auth.RuntimeID, auth.PolicyVersion); err != nil {
		return ConnectorTarget{}, fmt.Errorf("%w: %v", api.ErrUnauthorized, err)
	}
	target = strings.TrimSpace(target)
	if target == "" {
		return ConnectorTarget{}, api.ErrNotFound
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	owned := func(o api.Onboarding) bool {
		return o.PrincipalID == auth.PrincipalID && o.ContextID == auth.ContextID && o.RuntimeID == auth.RuntimeID && o.Phase != api.PhaseRemoved && o.SupersededBy == ""
	}
	var selected api.Onboarding
	if o, ok := s.onboardings[target]; ok && owned(o) {
		selected = o
	}
	var connection api.Connection
	if selected.ConnectionID != "" {
		connection = s.connections[selected.ConnectionID]
		if connection.ConnectionID != "" && (connection.Owner.Type != api.PrincipalOwner || !connection.Owner.Matches(auth)) {
			return ConnectorTarget{}, api.ErrNotFound
		}
	}
	if selected.OnboardingID == "" {
		if c, ok := s.connections[target]; ok {
			if c.Owner.Type != api.PrincipalOwner || !c.Owner.Matches(auth) {
				return ConnectorTarget{}, api.ErrNotFound
			}
			connection = c
		} else {
			for _, c := range s.connections {
				if c.DefinitionID != target || c.Owner.Type != api.PrincipalOwner || !c.Owner.Matches(auth) || c.Status == api.RevokedStatus {
					continue
				}
				if connection.ConnectionID != "" {
					return ConnectorTarget{}, &ErrAmbiguousTarget{Candidates: []api.OperationCandidate{{ID: connection.ConnectionID}, {ID: c.ConnectionID}}}
				}
				connection = c
			}
		}
	}
	if selected.OnboardingID == "" && connection.ConnectionID != "" {
		for _, o := range s.onboardings {
			if !owned(o) || o.ConnectionID != connection.ConnectionID {
				continue
			}
			bestCurrent := selected.CredentialRefID != "" && selected.CredentialRefID == connection.CredentialRefID
			thisCurrent := o.CredentialRefID != "" && o.CredentialRefID == connection.CredentialRefID
			if selected.OnboardingID == "" || thisCurrent && !bestCurrent || thisCurrent == bestCurrent && (o.CreatedAt.After(selected.CreatedAt) || o.CreatedAt.Equal(selected.CreatedAt) && o.OnboardingID > selected.OnboardingID) {
				selected = o
			}
		}
	}
	if selected.OnboardingID == "" && connection.ConnectionID == "" {
		for _, o := range s.onboardings {
			if !owned(o) || o.ConnectionID != "" || o.DefinitionID != target {
				continue
			}
			if selected.OnboardingID != "" {
				return ConnectorTarget{}, &ErrAmbiguousTarget{Candidates: []api.OperationCandidate{{ID: selected.OnboardingID}, {ID: o.OnboardingID}}}
			}
			selected = o
		}
	}
	if selected.OnboardingID == "" && connection.ConnectionID == "" {
		return ConnectorTarget{}, api.ErrNotFound
	}
	if selected.OnboardingID == "" {
		selected = api.Onboarding{Schema: api.SchemaVersion,
			OnboardingID: api.DeterministicID("onboard", auth.PrincipalID, auth.ContextID, auth.RuntimeID, "remove", connection.ConnectionID),
			PrincipalID:  auth.PrincipalID, ContextID: auth.ContextID, RuntimeID: auth.RuntimeID,
			Mode: api.OnboardingCatalog, Phase: api.PhaseEnabled, DefinitionID: connection.DefinitionID,
			ConnectionID: connection.ConnectionID, Revision: 1, CreatedAt: time.Now().UTC()}
	}
	previous, existed := s.onboardings[selected.OnboardingID]
	selected.PolicyVersion = auth.PolicyVersion
	selected.Initiator = &auth
	selected.FormNonce, selected.ConfirmationNonce, selected.WizardNonce = "", "", ""
	if selected.Phase != api.PhaseCleanupIncomplete && connection.Status != api.DeletingStatus {
		selected.RemoveNonce = ""
		selected.RemoveExpires = time.Time{}
	}
	selected.BrokerAuthorizationURL, selected.ChannelAuthURL = "", ""
	selected.PendingWrites = false
	if connection.ConnectionID != "" {
		selected.ConnectionID = connection.ConnectionID
		selected.DefinitionID = connection.DefinitionID
		selected.CredentialRefID = connection.CredentialRefID
		if ref, ok := s.credentials[connection.CredentialRefID]; ok && ref.ConnectionID == connection.ConnectionID {
			selected.Locator = ref.Locator
			if ref.Backend == "credential-broker" {
				selected.BrokerCredentialID = ref.Locator
			}
		}
		for _, binding := range s.bindings {
			if binding.ConnectionID == connection.ConnectionID && binding.PrincipalID == auth.PrincipalID && binding.ContextID == auth.ContextID && binding.RuntimeID == auth.RuntimeID {
				selected.BindingID = binding.ToolBindingID
				selected.DefinitionVersion = binding.DefinitionVersion
				break
			}
		}
	}
	if existed {
		selected.Revision++
	}
	selected.ActionRevision = selected.Revision
	if err := selected.Validate(); err != nil {
		return ConnectorTarget{}, err
	}
	s.onboardings[selected.OnboardingID] = cloneOnboarding(selected)
	if s.path != "" {
		before := s.diskDigest
		if err := s.saveLocked(s.path); err != nil {
			if s.diskDigest == before {
				if existed {
					s.onboardings[selected.OnboardingID] = previous
				} else {
					delete(s.onboardings, selected.OnboardingID)
				}
			}
			return ConnectorTarget{}, err
		}
	}
	return ConnectorTarget{OnboardingID: selected.OnboardingID, DefinitionID: selected.DefinitionID, ConnectionID: selected.ConnectionID, SourceURL: selected.SourceURL, Kind: selected.Mode}, nil
}
