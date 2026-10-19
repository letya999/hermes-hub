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
		result, err = c.connectorSearch(ctx, api.ArgString(args, "query"), api.ArgString(args, "kind"))
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
	for _, o := range c.Store.onboardings {
		if o.PrincipalID != auth.PrincipalID || o.ContextID != auth.ContextID || o.RuntimeID != auth.RuntimeID || o.PolicyVersion != auth.PolicyVersion {
			continue
		}
		candidates = append(candidates, api.OperationCandidate{ID: o.OnboardingID, Source: o.SourceURL, Kind: o.Mode})
	}
	for _, connection := range c.Store.connections {
		if connection.Owner.Type != api.TeamOwner {
			continue
		}
		team, ok := c.Store.teams[connection.Owner.ID]
		if !ok || !team.IsMember(auth.PrincipalID) {
			continue
		}
		candidates = append(candidates, api.OperationCandidate{ID: connection.ConnectionID, Source: connection.DefinitionID, Badge: "team"})
	}
	c.Store.mu.RUnlock()
	return api.OperationResult{
		State: api.OpStateDone, Candidates: candidates,
		Summary: fmt.Sprintf("%d connector(s)", len(candidates)),
	}, nil
}

// connectorSearch queries the reviewed prepared catalog.
func (c *ControlPlane) connectorSearch(ctx context.Context, query, kind string) (api.OperationResult, error) {
	query = strings.ToLower(strings.TrimSpace(query))
	if query == "" {
		return api.OperationResult{}, fmt.Errorf("%w: search query required", api.ErrInvalid)
	}
	entries, err := api.PreparedCatalog()
	if err != nil {
		return api.OperationResult{}, err
	}
	var candidates []api.OperationCandidate
	for _, entry := range entries {
		if strings.Contains(strings.ToLower(entry.ID+" "+strings.Join(entry.Aliases, " ")+" "+entry.Name), query) {
			candidates = append(candidates, api.OperationCandidate{ID: entry.ID, Source: entry.Source.Repository, Kind: "mcp"})
		}
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
				result.Finalizers = onboarding.Finalizers
				result.Receipt = onboarding.RemoveReceipt
			}
		}
		return result, nil
	}
	resolved, err := c.Store.ResolveConnectorTarget(auth, target)
	if err != nil {
		return api.OperationResult{}, err
	}
	onboarding, err := c.Store.OnboardingFor(auth, resolved.OnboardingID)
	if err != nil {
		return api.OperationResult{}, err
	}
	state := api.OpStateDone
	if onboarding.Phase == api.PhaseAwaitingCreds {
		state = api.OpStateAwaitingUser
	}
	// Browser connectors mint the owner viewer link on status: the nonce
	// travels only on the channel notice, never inside the tool result.
	if resolved.ConnectionID != "" {
		if connection, ok := c.Store.connectionFor(resolved.ConnectionID); ok && connection.Status == api.ActiveStatus {
			if definition, derr := c.Store.definitionByID(connection.DefinitionID); derr == nil && definition.Execution.EgressClass == api.EgressWeb {
				if nonce, _, merr := c.Store.MintViewerLink(auth, connection.ConnectionID, resolved.OnboardingID, c.now(), c.ttl()); merr == nil {
					api.DeliverViewerLink(ctx, auth, connection.ConnectionID, api.ViewerLinkURL(connection.ConnectionID, nonce))
				}
			}
		}
	}
	return api.OperationResult{State: state, Summary: onboarding.DefinitionID + " " + onboarding.Phase}, nil
}

func connectorToolContract(name string) (string, map[string]any) {
	return "connector tool " + name, map[string]any{"type": "object", "additionalProperties": true}
}
