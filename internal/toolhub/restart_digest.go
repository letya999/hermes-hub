package toolhub

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"slices"
	"strings"

	"github.com/letya999/hermes-hub/internal/identity"
	"github.com/letya999/hermes-hub/internal/toolhub/api"
)

func (s *Store) semanticRestartDigestLocked(principal, contextID, runtimeID string) string {
	type managedToolSurface struct {
		Tool   api.ProjectedTool
		Scopes []api.CapabilityScope
		Limits api.CapabilityLimits
	}
	type managedSurface struct {
		ID, Environment, PolicyVersion, Connectors string
		Generation                                 uint64
		SelfInstall                                bool
		Controls                                   []string
		Tools                                      []managedToolSurface
	}
	managed := []managedSurface{}
	for _, profile := range s.capabilityProfiles {
		if profile.PrincipalID == principal && profile.ContextID == contextID && profile.RuntimeID == runtimeID {
			auth := identity.Envelope{Schema: identity.Schema, ExternalIdentityID: principal, ConversationID: contextID, DeliveryTargetID: contextID, PrincipalID: principal, ContextID: contextID, RuntimeID: runtimeID, PolicyVersion: profile.PolicyVersion, CapabilityProfile: profile.ProfileID, Environment: profile.Environment, Generation: profile.Generation}
			tools, err := s.managedToolsLocked(auth)
			if err != nil {
				return ""
			}
			item := managedSurface{ID: profile.ProfileID, Environment: profile.Environment, PolicyVersion: profile.PolicyVersion, Connectors: profile.Connectors, Generation: profile.Generation, SelfInstall: profile.SelfInstall, Controls: slices.Clone(profile.ControlOperations)}
			slices.Sort(item.Controls)
			for _, tool := range tools {
				_, effective, err := s.managedToolLocked(auth, tool.Name, nil, true)
				if err != nil {
					return ""
				}
				item.Tools = append(item.Tools, managedToolSurface{Tool: tool, Scopes: effective.CapabilityScopes, Limits: api.CapabilityLimits{OutputBytes: effective.Definition.Execution.OutputBytes, TimeoutSeconds: effective.Definition.Execution.TimeoutSeconds}})
			}
			managed = append(managed, item)
		}
	}
	if len(managed) > 0 {
		slices.SortFunc(managed, func(a, b managedSurface) int { return strings.Compare(a.ID, b.ID) })
		body, _ := json.Marshal(managed)
		sum := sha256.Sum256(body)
		return "sha256:" + hex.EncodeToString(sum[:])
	}
	type bindingSurface struct {
		ID                 string
		PolicyVersion      string
		ToolDigest         string
		ConnectionRevision uint64
		ConnectionOwner    api.OwnerRef
		ConnectionCurrent  uint64
		ConnectionRef      string
		ConnectionStatus   api.Status
		TeamMember         bool
		TeamWrites         bool
		CredentialRefID    string
		CredentialRevision uint64
		CredentialCurrent  uint64
		CredentialStatus   api.Status
		Tools              []api.ToolSpec
	}
	type grantSurface struct {
		ID, Kind, DefinitionID, Version, Operation, Status string
	}
	bindings := make([]bindingSurface, 0)
	for _, binding := range s.bindings {
		if binding.PrincipalID != principal || binding.ContextID != contextID || binding.RuntimeID != runtimeID || binding.Status != api.ActiveStatus {
			continue
		}
		definition, ok := s.definitions[definitionKey(binding.DefinitionID, binding.DefinitionVersion)]
		if !ok {
			return ""
		}
		item := bindingSurface{ID: binding.ToolBindingID, PolicyVersion: binding.PolicyVersion, ToolDigest: binding.ToolDigest, ConnectionRevision: binding.ConnectionRevision, CredentialRefID: binding.CredentialRefID, CredentialRevision: binding.CredentialRevision, Tools: definition.ProjectedToolSpecs(binding)}
		if binding.ConnectionID != "" {
			connection, ok := s.connections[binding.ConnectionID]
			if !ok {
				return ""
			}
			if connection.Owner.Type == api.TeamOwner {
				team, ok := s.teams[connection.Owner.ID]
				if !ok {
					return ""
				}
				item.TeamMember, item.TeamWrites = team.IsMember(principal), team.CanWrite(principal)
			}
			item.ConnectionStatus = connection.Status
			item.ConnectionOwner = connection.Owner
			item.ConnectionCurrent = connection.Revision
			item.ConnectionRef = connection.CredentialRefID
		}
		if binding.CredentialRefID != "" {
			credential, ok := s.credentials[binding.CredentialRefID]
			if !ok {
				return ""
			}
			item.CredentialStatus = credential.Status
			item.CredentialCurrent = credential.Revision
		}
		bindings = append(bindings, item)
	}
	slices.SortFunc(bindings, func(a, b bindingSurface) int { return strings.Compare(a.ID, b.ID) })
	grants := make([]grantSurface, 0)
	for _, grant := range s.grants {
		if grant.PrincipalID == principal {
			grants = append(grants, grantSurface{grant.GrantID, string(grant.Kind), grant.DefinitionID, grant.DefinitionVersion, grant.Operation, string(grant.Status)})
		}
	}
	slices.SortFunc(grants, func(a, b grantSurface) int { return strings.Compare(a.ID, b.ID) })
	body, _ := json.Marshal(struct {
		Bindings []bindingSurface
		Grants   []grantSurface
	}{bindings, grants})
	sum := sha256.Sum256(body)
	return "sha256:" + hex.EncodeToString(sum[:])
}
