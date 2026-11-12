package toolhub

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/letya999/hermes-hub/internal/identity"
	"github.com/letya999/hermes-hub/internal/toolhub/api"
)

// InvokeConnector runs one connector_* tool through the shared facade.
func (c *ControlPlane) InvokeConnector(ctx context.Context, auth identity.Envelope, tool string, args map[string]any) (api.OperationResult, error) {
	if err := api.RejectAuthorityArguments(args); err != nil {
		return api.OperationResult{}, err
	}
	selectedTool := tool
	// r5 argument normalization: the catalog parameter is `name` where the
	// facade's internal vocabulary is `target`; connector_edit multiplexes
	// the five lifecycle verbs through its `action` enum.
	if api.ArgString(args, "target") == "" {
		if name := api.ArgString(args, "name"); name != "" {
			args["target"] = name
		}
	}
	if tool == "connector_edit" {
		switch api.ArgString(args, "action") {
		case "enable":
			tool = "connector_enable"
		case "disable":
			tool = "connector_disable"
		case "revoke":
			tool = "connector_revoke"
		case "allow_writes":
			tool = "connector_allow_writes"
		case "reauth":
			tool = "connector_reauth"
		case "resend":
			tool = "connector_resend"
		case "remove":
			tool = "connector_remove"
		default:
			return api.OperationResult{State: api.OpStateFailed, Code: api.OpCodeNotPermitted, Summary: "connector_edit action must be enable|disable|revoke|allow_writes|reauth|resend|remove"}, nil
		}
	}
	authorityTool := tool
	if tool == "connector_resend" {
		authorityTool = "connector_reauth" // same protected-link authority; no new grant shape
	}
	if tool == "connector_revoke" {
		authorityTool = "connector_remove"
	}
	if err := c.Store.RequireConnectorTool(auth, selectedTool, authorityTool, args); err != nil {
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
		target, operationID := api.ArgString(args, "target"), api.ArgString(args, "operation_id")
		if operationID == "" && strings.HasPrefix(target, "op-") {
			operationID, target = target, ""
		}
		result, err = c.connectorStatus(ctx, auth, target, operationID)
	case "connector_install":
		result, err = c.connectorInstall(ctx, auth, args)
	case "connector_enable":
		result, err = c.connectorPhase(ctx, auth, args, api.OpIntentEnable)
	case "connector_disable":
		result, err = c.connectorPhase(ctx, auth, args, api.OpIntentDisable)
	case "connector_revoke":
		result, err = c.connectorPhase(ctx, auth, args, api.OpIntentRevoke)
	case "connector_remove":
		result, err = c.connectorRemove(ctx, auth, args)
	case "connector_reauth":
		result, err = c.connectorReauth(ctx, auth, args)
	case "connector_resend":
		result, err = c.connectorResend(ctx, auth, args)
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
	var ambiguous *ErrAmbiguousTarget
	if errors.As(err, &ambiguous) {
		return api.OperationResult{State: api.OpStateFailed, Code: api.OpCodeAmbiguousTarget, Candidates: ambiguous.Candidates, Summary: "choose an exact connector id"}, nil
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
		if o.PrincipalID != auth.PrincipalID || o.ContextID != auth.ContextID || o.RuntimeID != auth.RuntimeID {
			continue
		}
		if o.Phase == api.PhaseRemoved || o.Phase == api.PhaseRevoked || o.SupersededBy != "" {
			continue
		}
		if o.PolicyVersion != auth.PolicyVersion {
			if _, live := c.Store.connections[o.ConnectionID]; live {
				continue
			}
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
			ObservedAt: o.ObservedAt, ObservationCode: o.ObservationCode,
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

// connectorSearch resolves exact prepared names locally; broader queries use
// the discovery pipeline with selectable registry candidates.
func (c *ControlPlane) connectorSearch(ctx context.Context, auth identity.Envelope, query, kind string) (api.OperationResult, error) {
	query = strings.ToLower(strings.TrimSpace(query))
	if query == "" {
		return api.OperationResult{}, fmt.Errorf("%w: search query required", api.ErrInvalid)
	}
	if entries, err := api.PreparedCatalog(); err == nil {
		for _, entry := range entries {
			if query == strings.ToLower(entry.ID) || query == strings.ToLower(entry.Name) {
				return api.OperationResult{State: api.OpStateDone, Candidates: []api.OperationCandidate{{ID: entry.ID, Source: entry.Source.Repository, Kind: "prepared"}}, Summary: "1 match(es)"}, nil
			}
		}
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
			onboarding, err := c.reconcileConnectorOnboarding(ctx, auth, op.OnboardingID)
			if err != nil && !errors.Is(err, api.ErrNotFound) {
				return api.OperationResult{}, err
			}
			if err == nil {
				result.NextAction = api.ArgString(c.statusBody(onboarding, false), "next_action")
				result.ObservedAt, result.ObservationCode = onboarding.ObservedAt, onboarding.ObservationCode
				if op.Intent == api.OpIntentInstall || op.Intent == api.OpIntentReauth {
					result.State = connectorOnboardingOperationState(onboarding)
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
				c.observeNoticeDelivery(ctx, auth, onboarding, &result)
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
			matches := make([]api.OperationCandidate, 0, 1)
			for _, candidate := range inventory.Candidates {
				if candidate.ConnectionID != "" && (candidate.ConnectionID == target || strings.EqualFold(candidate.DefinitionID, target)) {
					matches = append(matches, candidate)
				}
			}
			if len(matches) == 0 {
				entries, catalogErr := api.PreparedCatalog()
				if catalogErr == nil {
					for _, entry := range entries {
						if !strings.EqualFold(entry.ID, target) && !strings.EqualFold(entry.Name, target) {
							continue
						}
						for _, candidate := range inventory.Candidates {
							if candidate.ConnectionID != "" && strings.EqualFold(candidate.DefinitionID, sourceBasename(entry.Source.Repository)) {
								matches = append(matches, candidate)
							}
						}
					}
				}
			}
			if len(matches) == 1 {
				candidate := matches[0]
				return api.OperationResult{
					State: api.OpStateDone, Candidates: matches,
					Summary: candidate.DefinitionID + " " + string(candidate.ConnectionStatus),
				}, nil
			}
			if len(matches) > 1 {
				return api.OperationResult{State: api.OpStateFailed, Code: api.OpCodeAmbiguousTarget, Candidates: matches, Summary: "ambiguous connector"}, nil
			}
			if onboarding, lookupErr := c.resolveOnboarding(auth, map[string]any{"selector": target}); lookupErr == nil {
				return api.OperationResult{State: connectorOnboardingOperationState(onboarding), Summary: onboarding.DefinitionID + " " + onboarding.Phase}, nil
			}
			for _, candidate := range inventory.Candidates {
				if candidate.ID == target && candidate.ConnectionID == "" {
					return api.OperationResult{State: connectorOnboardingState(candidate.OnboardingPhase), Candidates: []api.OperationCandidate{candidate}, Summary: candidate.DefinitionID + " " + candidate.OnboardingPhase}, nil
				}
			}
		}
		return api.OperationResult{}, err
	}
	onboarding, err := c.reconcileConnectorOnboarding(ctx, auth, resolved.OnboardingID)
	if err != nil {
		return api.OperationResult{}, err
	}
	state := connectorOnboardingOperationState(onboarding)
	phase := onboarding.Phase
	if connection, ok := c.Store.connectionFor(onboarding.ConnectionID); ok && connection.Status == api.DegradedStatus {
		state, phase = api.OpStateAwaitingUser, string(connection.Status)
	}

	result := api.OperationResult{State: state, Summary: onboarding.DefinitionID + " " + phase, NextAction: api.ArgString(c.statusBody(onboarding, false), "next_action"), ObservedAt: onboarding.ObservedAt, ObservationCode: onboarding.ObservationCode}
	c.observeNoticeDelivery(ctx, auth, onboarding, &result)
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

func (c *ControlPlane) observeNoticeDelivery(ctx context.Context, auth identity.Envelope, onboarding api.Onboarding, result *api.OperationResult) {
	if onboarding.NoticeVersion == 0 {
		return
	}
	if onboarding.NoticeQueuedVersion < onboarding.NoticeVersion {
		result.DeliveryStatus = api.PrepareDeliveryNotQueued
		return
	}
	result.LinkSent = true // Accepted by the gateway; delivery is reported separately.
	result.DeliveryStatus = api.ReadPrepareDeliveryStatus(ctx, auth, onboarding.OnboardingID, onboarding.NoticeVersion)
	switch result.DeliveryStatus {
	case api.PrepareDeliveryUncertain:
		result.Code = api.OpCodeDeliveryUnavailable
		result.NextAction = "resend"
	case api.PrepareDeliveryUnknown:
		result.Code = api.OpCodeDeliveryUnavailable
		result.NextAction = "retry_status"
	}
}

func (c *ControlPlane) reconcileConnectorOnboarding(ctx context.Context, auth identity.Envelope, id string) (api.Onboarding, error) {
	return c.Store.OnboardingFor(auth, id)
}

// connectorResend posts the existing protected setup link as a fresh channel
// notice. It never creates a second onboarding or asks for credential values.
func (c *ControlPlane) connectorResend(ctx context.Context, auth identity.Envelope, args map[string]any) (api.OperationResult, error) {
	target := strings.TrimSpace(api.ArgString(args, "target"))

	onboardingID := ""
	if strings.HasPrefix(target, "op-") {
		op, err := c.Store.OperationFor(auth, target)
		if err != nil {
			return api.OperationResult{}, err
		}
		onboardingID = op.OnboardingID
	} else {
		exact, err := c.Store.OnboardingFor(auth, target)
		if err != nil {
			return api.OperationResult{}, err
		}
		onboardingID = exact.OnboardingID
	}
	onboarding, err := c.reconcileConnectorOnboarding(ctx, auth, onboardingID)
	if err != nil {
		return api.OperationResult{}, err
	}
	previousNoticeVersion := onboarding.NoticeVersion
	if onboarding.Phase == api.PhaseEnabled && onboarding.ConnectionID != "" {
		connection, ok := c.Store.connectionFor(onboarding.ConnectionID)
		if ok && connection.Status == api.ActiveStatus {
			definition, err := c.Store.definitionForConnection(connection)
			if err == nil && definition.Execution.EgressClass == api.EgressWeb {
				nonce, _, err := c.Store.MintViewerLink(auth, connection.ConnectionID, onboarding.OnboardingID, c.now(), c.ttl())
				if err != nil {
					return api.OperationResult{}, err
				}
				queued := api.DeliverViewerLinkWithDelivery(ctx, auth, connection.ConnectionID, api.ViewerLinkURL(connection.ConnectionID, nonce))
				result := api.OperationResult{State: api.OpStateDone, LinkSent: queued, Summary: onboarding.DefinitionID}
				if !queued {
					result.State, result.Code = api.OpStateFailed, api.OpCodeDeliveryUnavailable
				}
				return result, nil
			}
		}
	}
	ctx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()
	if onboarding.ConfirmationUsed {
		return api.OperationResult{}, api.ErrConflict
	}
	if onboarding.Phase == api.PhaseAwaitingCreds && onboarding.BrokerRequestID != "" {
		if err := c.reconcileBrokerReady(ctx, auth, &onboarding); err != nil {
			return api.OperationResult{}, err
		}
		onboarding, err = c.Store.OnboardingFor(auth, onboarding.OnboardingID)
		if err != nil {
			return api.OperationResult{}, err
		}
		if onboarding.Phase == api.PhaseVerifying || onboarding.Phase == api.PhaseRetryScheduled {
			return api.OperationResult{State: api.OpStateRunning, Code: onboarding.ErrorCode, Summary: onboarding.DefinitionID, ObservedAt: onboarding.ObservedAt}, nil
		}
	}
	if onboarding.Phase == api.PhaseAwaitingCreds {
		if err := c.regenerateBrokerRequest(ctx, auth, &onboarding); err != nil {
			return api.OperationResult{}, err
		}
		if err := c.refreshCredentialForm(ctx, auth, &onboarding); err != nil {
			return api.OperationResult{}, err
		}
	}
	if onboarding.Phase == api.PhaseAwaitingOAuth {
		definition, err := c.definitionOf(onboarding)
		if err != nil {
			return api.OperationResult{}, err
		}
		if _, err := c.ensurePreparedOAuth(ctx, auth, onboarding, definition, false); err != nil {
			return api.OperationResult{}, err
		}
		onboarding, err = c.Store.OnboardingFor(auth, onboarding.OnboardingID)
		if err != nil {
			return api.OperationResult{}, err
		}
	}
	if err := c.refreshConfirmation(ctx, auth, &onboarding); err != nil {
		return api.OperationResult{}, err
	}
	state := connectorOnboardingOperationState(onboarding)
	if state != api.OpStateAwaitingUser && state != api.OpStateAwaitingConfirm {
		return api.OperationResult{State: api.OpStateFailed, Code: api.OpCodeNotPermitted, Summary: "no pending setup link"}, nil
	}
	if onboarding.NoticeVersion > previousNoticeVersion && onboarding.NoticeQueuedVersion == onboarding.NoticeVersion {
		return api.OperationResult{State: state, LinkSent: true, Summary: onboarding.DefinitionID}, nil
	}
	if err := c.Store.RequestOnboardingNotice(auth, onboarding.OnboardingID); err != nil {
		return api.OperationResult{}, err
	}
	if queued := c.deliverOnboardingNotice(ctx, auth, onboarding.OnboardingID); !queued {
		return api.OperationResult{State: api.OpStateFailed, Code: api.OpCodeDeliveryUnavailable, Summary: "setup link could not be queued"}, nil
	}
	return api.OperationResult{State: state, Summary: "setup link queued for " + onboarding.OnboardingID, LinkSent: true}, nil
}

func connectorOnboardingState(phase string) string {
	switch phase {
	case api.PhasePreparing, api.PhaseReview, api.PhaseVerifying, api.PhaseRetryScheduled, api.PhaseConfirmed:
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

func connectorOnboardingOperationState(o api.Onboarding) string {
	if o.Phase == api.PhaseAwaitingConfirm && o.ConfirmationUsed {
		return api.OpStateRunning
	}
	return connectorOnboardingState(o.Phase)
}
