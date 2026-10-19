package toolhub

import (
	"context"
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
	switch tool {
	case "connector_list":
		return c.connectorList(auth)
	case "connector_search":
		return c.connectorSearch(ctx, api.ArgString(args, "query"), api.ArgString(args, "kind"))
	case "connector_status":
		return c.connectorStatus(auth, api.ArgString(args, "target"), api.ArgString(args, "operation_id"))
	default:
		return api.OperationResult{State: api.OpStateFailed, Code: api.OpCodeNotPermitted, Summary: "unknown connector tool"}, nil
	}
}

// connectorList enumerates the caller's live connectors.
func (c *ControlPlane) connectorList(auth identity.Envelope) (api.OperationResult, error) {
	c.Store.mu.RLock()
	var candidates []api.OperationCandidate
	for _, o := range c.Store.onboardings {
		if o.PrincipalID != auth.PrincipalID || o.ContextID != auth.ContextID || o.RuntimeID != auth.RuntimeID || o.PolicyVersion != auth.PolicyVersion {
			continue
		}
		candidates = append(candidates, api.OperationCandidate{ID: o.OnboardingID, Source: o.SourceURL, Kind: o.Mode})
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
func (c *ControlPlane) connectorStatus(auth identity.Envelope, target, operationID string) (api.OperationResult, error) {
	if operationID != "" {
		op, err := c.Store.OperationFor(auth, operationID)
		if err != nil {
			return api.OperationResult{}, err
		}
		return api.OperationResult{OperationID: op.OperationID, State: op.State, Code: op.ErrorCode, Summary: op.Intent + " " + op.Target}, nil
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
	return api.OperationResult{State: state, Summary: onboarding.DefinitionID + " " + onboarding.Phase}, nil
}

func connectorToolContract(name string) (string, map[string]any) {
	return "connector tool " + name, map[string]any{"type": "object", "additionalProperties": true}
}
