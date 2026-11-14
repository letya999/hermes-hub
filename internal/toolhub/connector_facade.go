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
	// facade's internal vocabulary is `target`; `id` and `onboarding_id`
	// are accepted aliases — models address records by the IDs tools return.
	// connector_edit multiplexes the five lifecycle verbs through its
	// `action` enum.
	if api.ArgString(args, "target") == "" {
		for _, key := range []string{"name", "id", "onboarding_id"} {
			if value := api.ArgString(args, key); value != "" {
				args["target"] = value
				break
			}
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
			return api.OperationResult{State: api.ModelStateBroken, Code: api.OpCodeNotPermitted, Summary: "connector_edit action must be enable|disable|revoke|allow_writes|reauth|resend|remove"}, nil
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
		return api.OperationResult{State: api.ModelStateBroken, Code: api.OpCodeNotPermitted, Summary: "unknown connector tool"}, nil
	}
	// A member's lifecycle verbs on a team-owned connection resolve to the
	// typed team denial — report it as the stable not_permitted code.
	if errors.Is(err, ErrTeamConnectionTarget) {
		return api.OperationResult{State: api.ModelStateBroken, Code: api.OpCodeNotPermitted, Summary: tool}, nil
	}
	var ambiguous *ErrAmbiguousTarget
	if errors.As(err, &ambiguous) {
		return api.OperationResult{State: api.ModelStateBroken, Code: api.OpCodeAmbiguousTarget, Candidates: ambiguous.Candidates, Summary: "choose an exact connector id"}, nil
	}
	return result, err
}

// connectorDomainResult folds typed domain failures into failed
// OperationResults. On the wire a completed round trip keeps the client
// circuit breaker closed while a raw protocol error strikes it — three in a
// row read as backend unreachability and mask the real domain cause. Both
// MCP surfaces (direct tools and the agent-tools dispatch) map through here.
func connectorDomainResult(err error) (api.OperationResult, bool) {
	var ambiguous *ErrAmbiguousTarget
	switch {
	case errors.As(err, &ambiguous):
		return api.OperationResult{State: api.ModelStateBroken, Code: api.OpCodeAmbiguousTarget, Summary: "ambiguous connector", Candidates: ambiguous.Candidates}, true
	case errors.Is(err, api.ErrNotFound):
		return api.OperationResult{State: api.ModelStateBroken, Code: api.OpCodeNotFound, Summary: err.Error()}, true
	case errors.Is(err, api.ErrInvalid) || errors.Is(err, api.ErrUnauthorized) || errors.Is(err, api.ErrRevoked) || errors.Is(err, api.ErrConflict) || errors.Is(err, api.ErrStale):
		return api.OperationResult{State: api.ModelStateBroken, Code: api.OpCodeNotPermitted, Summary: err.Error()}, true
	}
	return api.OperationResult{}, false
}

// resolveLifecycleTarget resolves a manage-verb target. A record that exists
// but predates the caller's policy epoch is an explicit dead state — not a
// not-found — reported as the stable stale_policy result with removal as the
// next action, so the model takes the cleanup path instead of retrying
// reauth or diagnosing a phantom outage.
func (c *ControlPlane) resolveLifecycleTarget(auth identity.Envelope, target string) (ConnectorTarget, *api.OperationResult, error) {
	resolved, err := c.Store.ResolveConnectorTarget(auth, target)
	if err == nil || !errors.Is(err, api.ErrNotFound) {
		return resolved, nil, err
	}
	if !c.Store.ownsConnectorTarget(auth, target) {
		return ConnectorTarget{}, nil, err
	}
	return ConnectorTarget{}, &api.OperationResult{
		State: api.ModelStateBroken, Code: api.OpCodeStalePolicy, YouMust: api.YouMustRemove,
		Summary: target + " predates the current policy epoch — remove and reinstall",
	}, nil
}

// connectorList enumerates the caller's live connectors plus every team
// connection whose stored team record lists the caller as an active member —
// those carry the "team" badge because members use but never own them.
// Owned records from an earlier policy epoch stay visible — marked
// stale_policy — because removal is the one transition still legal on them.
func (c *ControlPlane) connectorList(auth identity.Envelope) (api.OperationResult, error) {
	c.Store.mu.RLock()
	var candidates []api.OperationCandidate
	represented := map[string]bool{}
	selected := map[string]api.Onboarding{}
	for _, o := range c.Store.onboardings {
		if o.PrincipalID != auth.PrincipalID || o.ContextID != auth.ContextID || o.RuntimeID != auth.RuntimeID {
			continue
		}
		if o.Stage == api.StageGone || o.SupersededBy != "" {
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
		disabled := o.Stage == api.StageLive && c.Store.bindings[o.BindingID].Status == api.DisabledStatus
		candidate := api.OperationCandidate{
			ID: o.OnboardingID, Source: o.SourceURL, Kind: o.Mode,
			DefinitionID: o.DefinitionID,
			State:        o.ModelState(disabled), YouMust: o.YouMust(c.now()),
			ObservedAt: o.ObservedAt, ObservationCode: o.ObservationCode,
		}
		if o.PolicyVersion != auth.PolicyVersion {
			candidate.Badge = api.OpCodeStalePolicy
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
		State: api.ModelStateDone, Candidates: candidates,
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
				return api.OperationResult{State: api.ModelStateDone, Candidates: []api.OperationCandidate{{ID: entry.ID, Source: entry.Source.Repository, Kind: "prepared"}}, Summary: "1 match(es)"}, nil
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
	return api.OperationResult{State: api.ModelStateDone, Candidates: candidates, Summary: fmt.Sprintf("%d match(es)", len(candidates))}, nil
}

// connectorStatus reports one operation or resolves a target to its live
// connector state. Operation ids reach the journal directly.
func (c *ControlPlane) connectorStatus(ctx context.Context, auth identity.Envelope, target, operationID string) (api.OperationResult, error) {
	if operationID != "" {
		op, err := c.Store.OperationFor(auth, operationID)
		if err != nil {
			return api.OperationResult{}, err
		}
		result := api.OperationResult{OperationID: op.OperationID, State: connectorModelStateFromOp(op.State), Code: op.ErrorCode, Summary: op.Intent + " " + op.Target}
		if op.OnboardingID != "" {
			onboarding, err := c.reconcileConnectorOnboarding(ctx, auth, op.OnboardingID)
			if err != nil && !errors.Is(err, api.ErrNotFound) {
				return api.OperationResult{}, err
			}
			if err == nil {
				result.YouMust = onboarding.YouMust(c.now())
				result.ObservedAt, result.ObservationCode = onboarding.ObservedAt, onboarding.ObservationCode
				if op.Intent == api.OpIntentInstall || op.Intent == api.OpIntentReauth {
					result.State = onboarding.ModelState(c.Store.bindingStatus(onboarding.BindingID) == api.DisabledStatus)
					if connection, ok := c.Store.connectionFor(onboarding.ConnectionID); ok && connection.Status == api.DegradedStatus {
						result.State = api.ModelStateBroken
						result.Summary += " degraded"
					}
					if onboarding.Stage == api.StageFailed {
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
				if onboarding.Removed() && op.State == api.OpStateAwaitingConfirm {
					result.State = api.ModelStateDone
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
					State: api.ModelStateDone, Candidates: matches,
					Summary: candidate.DefinitionID + " " + string(candidate.ConnectionStatus),
				}, nil
			}
			if len(matches) > 1 {
				return api.OperationResult{State: api.ModelStateBroken, Code: api.OpCodeAmbiguousTarget, Candidates: matches, Summary: "ambiguous connector"}, nil
			}
			if onboarding, lookupErr := c.resolveOnboarding(auth, map[string]any{"selector": target}); lookupErr == nil {
				return api.OperationResult{State: onboarding.ModelState(c.Store.bindingStatus(onboarding.BindingID) == api.DisabledStatus), YouMust: onboarding.YouMust(c.now()), Summary: onboarding.DefinitionID + " " + onboarding.NoticePhase()}, nil
			}
			for _, candidate := range inventory.Candidates {
				if candidate.ID == target && candidate.ConnectionID == "" {
					return api.OperationResult{State: candidate.State, Candidates: []api.OperationCandidate{candidate}, Summary: candidate.DefinitionID + " " + candidate.State}, nil
				}
			}
			if c.Store.ownsConnectorTarget(auth, target) {
				return api.OperationResult{State: api.ModelStateBroken, Code: api.OpCodeStalePolicy, YouMust: api.YouMustRemove,
					Summary: target + " predates the current policy epoch — remove and reinstall"}, nil
			}
		}
		return api.OperationResult{}, err
	}
	onboarding, err := c.reconcileConnectorOnboarding(ctx, auth, resolved.OnboardingID)
	if err != nil {
		return api.OperationResult{}, err
	}
	state := onboarding.ModelState(c.Store.bindingStatus(onboarding.BindingID) == api.DisabledStatus)
	phase := onboarding.NoticePhase()
	if connection, ok := c.Store.connectionFor(onboarding.ConnectionID); ok && connection.Status == api.DegradedStatus {
		state, phase = api.ModelStateBroken, string(connection.Status)
	}

	result := api.OperationResult{State: state, Summary: onboarding.DefinitionID + " " + phase, YouMust: onboarding.YouMust(c.now()), ObservedAt: onboarding.ObservedAt, ObservationCode: onboarding.ObservationCode}
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

// observeNoticeDelivery folds the channel's durable outcome into the result:
// the record only knows a notice was queued; sent and failed come from the
// channel's persisted read — with its name, so the model can say where.
func (c *ControlPlane) observeNoticeDelivery(ctx context.Context, auth identity.Envelope, onboarding api.Onboarding, result *api.OperationResult) {
	if onboarding.NoticeVersion == 0 {
		return
	}
	if onboarding.NoticeQueuedVersion < onboarding.NoticeVersion {
		result.Delivery = api.DeliveryNotSent
		return
	}
	status, channel := api.ReadPrepareDeliveryStatus(ctx, auth, onboarding.OnboardingID, onboarding.NoticeVersion)
	result.Delivery = api.ContractDelivery(status)
	result.Channel = channel
	switch status {
	case api.PrepareDeliveryUncertain:
		result.Code = api.OpCodeDeliveryUnavailable
		result.YouMust = api.YouMustRetry
	case api.PrepareDeliveryUnknown:
		result.Code = api.OpCodeDeliveryUnavailable
		result.YouMust = api.YouMustWait
	}
}

// connectorModelStateFromOp maps the internal journal vocabulary onto the
// model's — Operation.State is not model-facing, only its projection is.
func connectorModelStateFromOp(state string) string {
	switch state {
	case api.OpStateQueued, api.OpStateRunning:
		return api.ModelStateWorking
	case api.OpStateAwaitingUser, api.OpStateAwaitingConfirm:
		return api.ModelStateWaitingYou
	case api.OpStateFailed:
		return api.ModelStateBroken
	default:
		return api.ModelStateDone
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
		resolved, stale, err := c.resolveLifecycleTarget(auth, target)
		if stale != nil {
			return *stale, nil
		}
		if err != nil {
			return api.OperationResult{}, err
		}
		onboardingID = resolved.OnboardingID
	}
	onboarding, err := c.reconcileConnectorOnboarding(ctx, auth, onboardingID)
	if err != nil {
		return api.OperationResult{}, err
	}
	previousNoticeVersion := onboarding.NoticeVersion
	if onboarding.Stage == api.StageLive && onboarding.ConnectionID != "" {
		connection, ok := c.Store.connectionFor(onboarding.ConnectionID)
		if ok && connection.Status == api.ActiveStatus {
			definition, err := c.Store.definitionForConnection(connection)
			if err == nil && definition.Execution.EgressClass == api.EgressWeb {
				nonce, _, err := c.Store.MintViewerLink(auth, connection.ConnectionID, onboarding.OnboardingID, c.now(), c.ttl())
				if err != nil {
					return api.OperationResult{}, err
				}
				queued := api.DeliverViewerLinkWithDelivery(ctx, auth, connection.ConnectionID, api.ViewerLinkURL(connection.ConnectionID, nonce))
				result := api.OperationResult{State: api.ModelStateReady, Summary: onboarding.DefinitionID}
				if queued {
					result.Delivery = api.DeliveryQueued
				} else {
					result.State, result.Code, result.Delivery = api.ModelStateBroken, api.OpCodeDeliveryUnavailable, api.DeliveryNotSent
				}
				return result, nil
			}
		}
	}
	ctx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()
	if !onboarding.Removed() && onboarding.GateKind() == api.GateRemove {
		// An armed owner-confirmed removal is the pending action — resending
		// the setup link would arm the opposite transition. Re-queue the live
		// remove notice; once expired, bare remove re-mints nonce and link.
		if !onboarding.GateExpiry(api.GateRemove).IsZero() && c.now().Before(onboarding.GateExpiry(api.GateRemove)) {
			if err := c.Store.RequestOnboardingNotice(auth, onboarding.OnboardingID); err != nil {
				return api.OperationResult{}, err
			}
			queued := c.deliverOnboardingNotice(ctx, auth, onboarding.OnboardingID)
			result := api.OperationResult{State: api.ModelStateWaitingYou, YouMust: api.YouMustClickLink, Summary: "remove " + onboarding.DefinitionID}
			if queued {
				result.Delivery = api.DeliveryQueued
				c.observeNoticeDelivery(ctx, auth, onboarding, &result)
			} else {
				result.Code, result.Delivery = api.OpCodeDeliveryUnavailable, api.DeliveryNotSent
			}
			return result, nil
		}
		if _, err := c.remove(ctx, auth, map[string]any{"onboarding_id": onboarding.OnboardingID}); err != nil {
			return api.OperationResult{}, err
		}
		result := api.OperationResult{State: api.ModelStateWaitingYou, YouMust: api.YouMustClickLink, Delivery: api.DeliveryQueued, Summary: "remove " + onboarding.DefinitionID}
		if fresh, err := c.Store.OnboardingFor(auth, onboarding.OnboardingID); err == nil {
			c.observeNoticeDelivery(ctx, auth, fresh, &result)
		}
		return result, nil
	}
	if onboarding.ConfirmationUsed {
		return api.OperationResult{}, api.ErrConflict
	}
	if onboarding.Awaiting(api.GateCredentials) && onboarding.BrokerRequestID != "" {
		if err := c.reconcileBrokerReady(ctx, auth, &onboarding); err != nil {
			return api.OperationResult{}, err
		}
		onboarding, err = c.Store.OnboardingFor(auth, onboarding.OnboardingID)
		if err != nil {
			return api.OperationResult{}, err
		}
		if onboarding.Stage == api.StagePending && onboarding.Gate == nil {
			return api.OperationResult{State: api.ModelStateWorking, Code: onboarding.ErrorCode, Summary: onboarding.DefinitionID, ObservedAt: onboarding.ObservedAt}, nil
		}
	}
	if onboarding.Awaiting(api.GateCredentials) {
		if err := c.regenerateBrokerRequest(ctx, auth, &onboarding); err != nil {
			return api.OperationResult{}, err
		}
		if err := c.refreshCredentialForm(ctx, auth, &onboarding); err != nil {
			return api.OperationResult{}, err
		}
	}
	if onboarding.Awaiting(api.GateOAuth) {
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
	state := onboarding.ModelState(false)
	if state != api.ModelStateWaitingYou {
		return api.OperationResult{State: api.ModelStateBroken, Code: api.OpCodeNotPermitted, Summary: "no pending setup link"}, nil
	}
	if onboarding.NoticeVersion > previousNoticeVersion && onboarding.NoticeQueuedVersion == onboarding.NoticeVersion {
		result := api.OperationResult{State: state, YouMust: api.YouMustClickLink, Delivery: api.DeliveryQueued, Summary: onboarding.DefinitionID}
		c.observeNoticeDelivery(ctx, auth, onboarding, &result)
		return result, nil
	}
	if err := c.Store.RequestOnboardingNotice(auth, onboarding.OnboardingID); err != nil {
		return api.OperationResult{}, err
	}
	if queued := c.deliverOnboardingNotice(ctx, auth, onboarding.OnboardingID); !queued {
		return api.OperationResult{State: api.ModelStateBroken, Code: api.OpCodeDeliveryUnavailable, Delivery: api.DeliveryNotSent, Summary: "setup link could not be queued"}, nil
	}
	if fresh, err := c.Store.OnboardingFor(auth, onboarding.OnboardingID); err == nil {
		onboarding = fresh
	}
	result := api.OperationResult{State: state, YouMust: api.YouMustClickLink, Delivery: api.DeliveryQueued, Summary: "setup link queued for " + onboarding.OnboardingID}
	c.observeNoticeDelivery(ctx, auth, onboarding, &result)
	return result, nil
}

// The model contract is derived by api.Onboarding.ModelState/YouMust
// (model_contract.go). The journal keeps its own internal vocabulary — this
// maps the canonical record onto it for install-progress bookkeeping.
func connectorOpStateFromOnboarding(o api.Onboarding) string {
	switch {
	case o.Awaiting(api.GateCredentials), o.Awaiting(api.GateOAuth):
		return api.OpStateAwaitingUser
	case o.Awaiting(api.GateConfirmSetup), o.GateKind() == api.GateRemove:
		return api.OpStateAwaitingConfirm
	case o.Stage == api.StageFailed:
		return api.OpStateFailed
	case o.Stage == api.StageLive, o.Stage == api.StageGone:
		return api.OpStateDone
	default:
		return api.OpStateRunning
	}
}

// connectorToolContract mirrors the authored connector contract from the
// agent-tools catalog (internal/agenttools capability.go + arg_docs.go):
// the direct surface must name the same parameters the catalog projects —
// a bare object schema leaves the model guessing argument names.
func connectorToolContract(name string) (string, map[string]any) {
	str := func(desc string) map[string]any { return map[string]any{"type": "string", "description": desc} }
	switch name {
	case "connector_search":
		return "Search the connector catalog by name and kind; returns connector ids and availability.", map[string]any{
			"type": "object", "additionalProperties": true,
			"properties": map[string]any{
				"query": str("Name or keyword to find in the connector catalogs."),
				"kind":  map[string]any{"type": "string", "enum": []string{"prepared", "blocked", "metadata-only", "requires-preflight", "registry-source", "github-source"}},
				"limit": map[string]any{"type": "integer", "description": "Maximum candidates to return."},
			},
		}
	case "connector_status":
		return "Read connector state by exact onboarding or operation ID; the result carries the contract fields state, you_must and delivery+channel — sent means the named channel confirmed the send, uncertain means unconfirmed, never a delivered message. Status does not contact providers, renew links or send messages.", map[string]any{
			"type": "object", "required": []string{"name"}, "additionalProperties": true,
			"properties": map[string]any{
				"name":    str("Exact onboarding or operation ID for a pending install; connector name or connection ID otherwise."),
				"id":      str("Alias of name — accepted when a returned record ID is addressed directly."),
				"verbose": map[string]any{"type": "boolean", "description": "Include diagnostics in the report."},
			},
		}
	case "connector_install":
		return "Install a connector candidate from connector_search; returns a protected credential form when required — never accepts secrets in arguments.", map[string]any{
			"type": "object", "required": []string{"name"}, "additionalProperties": true,
			"properties": map[string]any{
				"name":    str("Candidate id or name from connector_search."),
				"id":      str("Alias of name — accepted when a search candidate ID is addressed directly."),
				"version": str("Version or digest to pin, when the candidate offers choices."),
			},
		}
	case "connector_edit":
		return "Change a connector's lifecycle: enable; disable use without deleting credentials; revoke authorization; allow_writes; reauth to open protected credential entry even while setup awaits confirmation; resend the current setup notice or enabled browser viewer link by exact onboarding or operation ID; remove an owned connection or stale onboarding after protected owner confirmation. Never request credentials in chat.", map[string]any{
			"type": "object", "required": []string{"name", "action"}, "additionalProperties": true,
			"properties": map[string]any{
				"name":   str("Connector name or exact owned connection/onboarding ID; resend requires an exact onboarding or operation ID."),
				"id":     str("Alias of name — accepted when a returned record ID is addressed directly."),
				"action": map[string]any{"type": "string", "enum": []string{"enable", "disable", "revoke", "allow_writes", "reauth", "resend", "remove"}, "description": "disable stops use while retaining credentials; revoke cuts authorization; reauth opens protected credential entry including unfinished setups; resend repeats the current pending action, not credential entry after submission; remove requires protected owner confirmation. Secrets never enter chat."},
			},
		}
	default: // connector_list
		return "List owned connections and onboarding records, including stale owner cleanup targets, with their recorded status.", map[string]any{"type": "object", "additionalProperties": true}
	}
}
