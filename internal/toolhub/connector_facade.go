package toolhub

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/letya999/hermes-hub/internal/identity"
	"github.com/letya999/hermes-hub/internal/toolhub/api"
)

// InvokeConnector runs one connector_* tool through the shared facade.
func (c *ControlPlane) InvokeConnector(ctx context.Context, auth identity.Envelope, tool string, args map[string]any) (api.OperationResult, error) {
	if err := api.RejectAuthorityArguments(args); err != nil {
		return api.OperationResult{}, err
	}
	if err := c.Store.RequireConnectorTool(auth, tool); err != nil {
		return api.OperationResult{}, err
	}
	var result api.OperationResult
	var err error
	switch tool {
	case "connector_list":
		result, err = c.connectorList(auth)
	case "connector_search":
		result, err = c.connectorSearch(ctx, auth, api.ArgString(args, "query"), api.ArgString(args, "kind"))
	case "connector_status":
		result, err = c.connectorStatus(ctx, auth, api.ArgString(args, "target"), api.ArgString(args, "operation_id"))
	case "connector_install":
		result, err = c.connectorInstall(ctx, auth, args)
	case "connector_enable":
		result, err = c.connectorPhase(ctx, auth, args, api.OpIntentEnable)
	case "connector_disable":
		result, err = c.connectorPhase(ctx, auth, args, api.OpIntentDisable)
	case "connector_remove":
		result, err = c.connectorRemove(ctx, auth, args)
	case "connector_reauth":
		result, err = c.connectorReauth(ctx, auth, args)
	case "connector_allow_writes":
		result, err = c.connectorAllowWrites(ctx, auth, args)
	default:
		return api.OperationResult{State: api.OpStateFailed, Code: api.OpCodeNotPermitted, Summary: "unknown connector tool"}, nil
	}
	// A member's lifecycle verbs on a team-owned connection resolve to the
	// typed team denial — report it as the stable not_permitted code.
	if errors.Is(err, ErrTeamConnectionTarget) {
		return api.OperationResult{State: api.OpStateFailed, Code: api.OpCodeNotPermitted, Summary: tool}, nil
	}
	return result, err
}

// connectorList enumerates the caller's live connectors plus every team
// connection whose stored team record lists the caller as an active member —
// those carry the "team" badge because members use but never own them.
func (c *ControlPlane) connectorList(auth identity.Envelope) (api.OperationResult, error) {
	c.Store.mu.RLock()
	var candidates []api.OperationCandidate
	represented := map[string]bool{}
	selected := map[string]api.Onboarding{}
	for _, o := range c.Store.onboardings {
		if o.PrincipalID != auth.PrincipalID || o.ContextID != auth.ContextID || o.RuntimeID != auth.RuntimeID || o.PolicyVersion != auth.PolicyVersion {
			continue
		}
		if o.Phase == api.PhaseRemoved || o.Phase == api.PhaseRevoked || o.SupersededBy != "" {
			continue
		}
		key := "onboarding:" + o.OnboardingID
		if connection, ok := c.Store.connections[o.ConnectionID]; ok && connection.Owner.Matches(auth) {
			if o.CredentialRefID != "" && o.CredentialRefID != connection.CredentialRefID {
				continue
			}
			key = "connection:" + connection.ConnectionID
		}
		previous, found := selected[key]
		current, previousCurrent := o.CredentialRefID != "", previous.CredentialRefID != ""
		newer := o.CreatedAt.After(previous.CreatedAt) || (o.CreatedAt.Equal(previous.CreatedAt) && o.OnboardingID > previous.OnboardingID)
		if !found || (current && !previousCurrent) || (current == previousCurrent && newer) {
			selected[key] = o
		}
	}
	for _, o := range selected {
		candidate := api.OperationCandidate{
			ID: o.OnboardingID, Source: o.SourceURL, Kind: o.Mode,
			DefinitionID: o.DefinitionID, OnboardingPhase: o.Phase,
		}
		if definition, ok := c.Store.definitions[definitionKey(o.DefinitionID, o.DefinitionVersion)]; ok {
			candidate.Transport = string(definition.Transport)
		}
		if connection, ok := c.Store.connections[o.ConnectionID]; ok && connection.Owner.Matches(auth) {
			candidate.ConnectionID = connection.ConnectionID
			candidate.ConnectionStatus = connection.Status
			represented[connection.ConnectionID] = true
		}
		candidates = append(candidates, candidate)
	}
	for _, connection := range c.Store.connections {
		if connection.Owner.Matches(auth) && !represented[connection.ConnectionID] {
			candidates = append(candidates, api.OperationCandidate{
				ID: connection.ConnectionID, ConnectionID: connection.ConnectionID,
				DefinitionID: connection.DefinitionID, ConnectionStatus: connection.Status,
			})
		}
		if connection.Owner.Type != api.TeamOwner {
			continue
		}
		team, ok := c.Store.teams[connection.Owner.ID]
		if !ok || !team.IsMember(auth.PrincipalID) {
			continue
		}
		candidates = append(candidates, api.OperationCandidate{
			ID: connection.ConnectionID, Source: connection.DefinitionID, Badge: "team",
			ConnectionID: connection.ConnectionID, DefinitionID: connection.DefinitionID,
			ConnectionStatus: connection.Status,
		})
	}
	c.Store.mu.RUnlock()
	for i := range candidates {
		if candidates[i].ConnectionID == "" {
			continue
		}
		connection, ok := c.Store.connectionFor(candidates[i].ConnectionID)
		if !ok {
			continue
		}
		candidates[i].Owner = &connection.Owner
		candidates[i].CredentialVisibility = connection.CredentialVisibility
		candidates[i].CredentialCheckedAt = connection.CredentialCheckedAt
		candidates[i].HealthCause = connection.HealthCause
		if candidates[i].Transport != "" {
			continue
		}
		if definition, err := c.Store.definitionForConnection(connection); err == nil {
			candidates[i].Transport = string(definition.Transport)
		}
	}
	return api.OperationResult{
		State: api.OpStateDone, Candidates: candidates,
		Summary: fmt.Sprintf("%d connector(s)", len(candidates)),
	}, nil
}

// connectorSearch runs the same discovery pipeline as discover: prepared
// entries plus configured registries, with selectable candidate ids minted
// so connector_install can take the choice directly.
func (c *ControlPlane) connectorSearch(ctx context.Context, auth identity.Envelope, query, kind string) (api.OperationResult, error) {
	query = strings.ToLower(strings.TrimSpace(query))
	if query == "" {
		return api.OperationResult{}, fmt.Errorf("%w: search query required", api.ErrInvalid)
	}
	body, err := c.discover(ctx, auth, query)
	if err != nil {
		return api.OperationResult{}, err
	}
	found, _ := body["candidates"].([]api.DiscoveryCandidate)
	candidates := make([]api.OperationCandidate, 0, len(found))
	for _, candidate := range found {
		id := candidate.PreparedID
		if id == "" {
			id = candidate.ID
		}
		candidates = append(candidates, api.OperationCandidate{ID: id, Source: candidate.Source.Repository, Kind: candidate.Status})
	}
	return api.OperationResult{State: api.OpStateDone, Candidates: candidates, Summary: fmt.Sprintf("%d match(es)", len(candidates))}, nil
}

// connectorStatus reports one operation or resolves a target to its live
// connector state. Operation ids reach the journal directly.
func (c *ControlPlane) connectorStatus(ctx context.Context, auth identity.Envelope, target, operationID string) (api.OperationResult, error) {
	if operationID != "" {
		op, err := c.Store.OperationFor(auth, operationID)
		if err != nil {
			return api.OperationResult{}, err
		}
		result := api.OperationResult{OperationID: op.OperationID, State: op.State, Code: op.ErrorCode, Summary: op.Intent + " " + op.Target}
		if op.OnboardingID != "" {
			if onboarding, err := c.Store.OnboardingFor(auth, op.OnboardingID); err == nil {
				if op.Intent == api.OpIntentInstall {
					result.State = connectorOnboardingState(onboarding.Phase)
					if connection, ok := c.Store.connectionFor(onboarding.ConnectionID); ok && connection.Status == api.DegradedStatus {
						result.State = api.OpStateAwaitingUser
						result.Summary += " degraded"
					}
					if onboarding.Phase == api.PhaseFailed {
						result.Code = api.OpCodeProbeFailed
						result.Summary += ": " + onboarding.Error
					}
				}
				result.Finalizers = onboarding.Finalizers
				result.Receipt = onboarding.RemoveReceipt
				// A sibling's completed ladder sweeps this record to removed
				// without running its own finalizers — the connector is gone,
				// so the journaled operation is done regardless of its arm
				// state.
				if onboarding.Phase == api.PhaseRemoved && op.State == api.OpStateAwaitingConfirm {
					result.State = api.OpStateDone
				}
			}
		}
		return result, nil
	}
	resolved, err := c.Store.ResolveConnectorTarget(auth, target)
	if err != nil {
		if errors.Is(err, api.ErrNotFound) || errors.Is(err, ErrTeamConnectionTarget) {
			inventory, listErr := c.connectorList(auth)
			if listErr != nil {
				return api.OperationResult{}, listErr
			}
			for _, candidate := range inventory.Candidates {
				if candidate.ConnectionID != "" && candidate.ConnectionID == target {
					return api.OperationResult{
						State: api.OpStateDone, Candidates: []api.OperationCandidate{candidate},
						Summary: candidate.DefinitionID + " " + string(candidate.ConnectionStatus),
					}, nil
				}
			}
			if onboarding, lookupErr := c.resolveOnboarding(auth, map[string]any{"selector": target}); lookupErr == nil {
				return api.OperationResult{State: connectorOnboardingState(onboarding.Phase), Summary: onboarding.DefinitionID + " " + onboarding.Phase}, nil
			}
		}
		return api.OperationResult{}, err
	}
	onboarding, err := c.Store.OnboardingFor(auth, resolved.OnboardingID)
	if err != nil {
		return api.OperationResult{}, err
	}
	state := connectorOnboardingState(onboarding.Phase)
	phase := onboarding.Phase
	if connection, ok := c.Store.connectionFor(onboarding.ConnectionID); ok && connection.Status == api.DegradedStatus {
		state, phase = api.OpStateAwaitingUser, string(connection.Status)
	}
	// Browser connectors mint the owner viewer link on status: the nonce
	// travels only on the channel notice, never inside the tool result.
	if resolved.ConnectionID != "" {
		if connection, ok := c.Store.connectionFor(resolved.ConnectionID); ok && connection.Status == api.ActiveStatus {
			if definition, derr := c.Store.definitionForConnection(connection); derr == nil && definition.Execution.EgressClass == api.EgressWeb {
				if nonce, _, merr := c.Store.MintViewerLink(auth, connection.ConnectionID, resolved.OnboardingID, c.now(), c.ttl()); merr == nil {
					api.DeliverViewerLink(ctx, auth, connection.ConnectionID, api.ViewerLinkURL(connection.ConnectionID, nonce))
				}
			}
		}
	}
	result := api.OperationResult{State: state, Summary: onboarding.DefinitionID + " " + phase}
	inventory, err := c.connectorList(auth)
	if err != nil {
		return api.OperationResult{}, err
	}
	for _, candidate := range inventory.Candidates {
		if candidate.ID == onboarding.OnboardingID {
			result.Candidates = []api.OperationCandidate{candidate}
			break
		}
	}
	return result, nil
}

func connectorOnboardingState(phase string) string {
	switch phase {
	case api.PhasePreparing:
		return api.OpStateRunning
	case api.PhaseAwaitingCreds, api.PhaseAwaitingOAuth:
		return api.OpStateAwaitingUser
	case api.PhaseAwaitingConfirm:
		return api.OpStateAwaitingConfirm
	case api.PhaseFailed:
		return api.OpStateFailed
	default:
		return api.OpStateDone
	}
}

func connectorToolContract(name string) (string, map[string]any) {
	return "connector tool " + name, map[string]any{"type": "object", "additionalProperties": true}
}
