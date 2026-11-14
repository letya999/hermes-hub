package toolhub

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"slices"
	"sort"
	"strings"

	brokerv1 "github.com/letya999/credential-broker/api/v1"
	brokerclient "github.com/letya999/credential-broker/client"

	"github.com/letya999/hermes-hub/internal/credstore"
	"github.com/letya999/hermes-hub/internal/identity"
	"github.com/letya999/hermes-hub/internal/toolhub/api"
)

// removeLadder is the ordered finalizer set for a convergent remove. Steps
// are idempotent; a retry resumes at the first non-done entry.
var removeLadder = []string{
	api.FinalizerWorkloads,
	api.FinalizerVolumes,
	api.FinalizerNetwork,
	api.FinalizerBrokerCredential,
	api.FinalizerCiphertext,
	api.FinalizerWorkspace,
	api.FinalizerImageRef,
	api.FinalizerVerifyZero,
}

func newRemoveLadder() []api.Finalizer {
	ladder := make([]api.Finalizer, 0, len(removeLadder))
	for _, name := range removeLadder {
		ladder = append(ladder, api.Finalizer{Name: name, State: api.FinalizerPending})
	}
	return ladder
}

func (c *ControlPlane) confirmedRemoveInProgress(onboarding api.Onboarding) bool {
	if onboarding.CleanupIncomplete() {
		return true
	}
	// A revoked record runs its removal ladder in place on gone — the stage
	// never enters failed, so pending finalizers are the only in-flight mark.
	if onboarding.Stage == api.StageGone && slices.ContainsFunc(onboarding.Finalizers, func(f api.Finalizer) bool { return f.State != api.FinalizerDone }) {
		return true
	}
	connection, ok := c.Store.connectionFor(onboarding.ConnectionID)
	return ok && connection.Status == api.DeletingStatus
}

// ResumeRemovals retries only removals that already passed the human
// confirmation gate; a pending nonce alone never authorizes deletion.
func (c *ControlPlane) ResumeRemovals(ctx context.Context) error {
	c.Store.mu.RLock()
	all := make([]api.Onboarding, 0, len(c.Store.onboardings))
	for _, onboarding := range c.Store.onboardings {
		all = append(all, onboarding)
	}
	c.Store.mu.RUnlock()
	var failures []error
	for _, onboarding := range all {
		if !c.confirmedRemoveInProgress(onboarding) {
			continue
		}
		auth := identity.Envelope{Schema: identity.Schema, PrincipalID: onboarding.PrincipalID,
			ExternalIdentityID: onboarding.PrincipalID, ContextID: onboarding.ContextID,
			RuntimeID: onboarding.RuntimeID, PolicyVersion: onboarding.PolicyVersion,
			ConversationID: "reconciler", DeliveryTargetID: "reconciler"}
		if _, err := c.remove(ctx, auth, map[string]any{"onboarding_id": onboarding.OnboardingID}); err != nil {
			failures = append(failures, err)
		}
	}
	return errors.Join(failures...)
}

// labelFootprint counts the Docker objects carrying the connection's
// ownership labels at remove start; the receipt reports what was purged.
func (c *ControlPlane) labelFootprint(ctx context.Context, onboarding api.Onboarding) map[string]int {
	if c.LabelQuery == nil || onboarding.ConnectionID == "" {
		return nil
	}
	found, err := c.LabelQuery(ctx, map[string]string{api.LabelConnection: onboarding.ConnectionID})
	if err != nil {
		return nil
	}
	footprint := map[string]int{}
	for kind, names := range found {
		if kind == "image" { // Recipe images are shared and retained by remove.
			continue
		}
		if len(names) > 0 {
			footprint[kind] = len(names)
		}
	}
	return footprint
}

func (c *ControlPlane) runRemoveFinalizer(ctx context.Context, onboarding api.Onboarding, name string) error {
	// A member of a shared connection owns only its binding: the connection,
	// its ciphertext and the shared workload are owner-only.
	shared := c.Store.sharedForeignConnection(onboarding.PrincipalID, onboarding.ConnectionID)
	switch name {
	case api.FinalizerWorkloads:
		if shared {
			return nil
		}
		if err := c.releaseBindingWorkloads(onboarding, true); err != nil {
			return err
		}
		return c.purgeLabelled(ctx, onboarding, "container")
	case api.FinalizerVolumes:
		if shared {
			return nil
		}
		return c.purgeLabelled(ctx, onboarding, "volume")
	case api.FinalizerNetwork:
		if shared {
			return nil
		}
		return c.purgeLabelled(ctx, onboarding, "network")
	case api.FinalizerBrokerCredential:
		if shared {
			return nil
		}
		return c.deleteBrokerCredentials(ctx, onboarding)
	case api.FinalizerCiphertext:
		if shared {
			return nil
		}
		if err := c.revokeLocalCredentialStatus(onboarding); err != nil {
			return err
		}
		// Remove deletes the owner's ciphertext, not just revokes it — a
		// revoked marker still leaves the secret recoverable.
		if c.Secrets != nil && onboarding.Locator != "" {
			owner := onboarding.PrincipalID
			if onboarding.CredentialOwner != "" {
				owner = onboarding.CredentialOwner
			}
			if owner == onboarding.PrincipalID {
				if err := c.Secrets.Delete(onboarding.Locator, owner); err != nil && !errors.Is(err, credstore.ErrNotFound) {
					return err
				}
			}
		}
		return nil
	case api.FinalizerWorkspace:
		return c.removeOwnedWorkspace(onboarding, shared)
	case api.FinalizerImageRef:
		// Images are shared per recipe digest — removing one connector never
		// deletes them; the store keeps no per-connection image record.
		return nil
	case api.FinalizerVerifyZero:
		if c.LabelQuery == nil || onboarding.ConnectionID == "" {
			return nil
		}
		found, err := c.LabelQuery(ctx, map[string]string{api.LabelConnection: onboarding.ConnectionID})
		if err != nil {
			return err
		}
		var leftovers []string
		for kind, names := range found {
			if kind == "image" {
				continue
			}
			for _, n := range names {
				leftovers = append(leftovers, kind+" "+n)
			}
		}
		if len(leftovers) > 0 {
			sort.Strings(leftovers)
			return fmt.Errorf("%w: %s", api.ErrStale, strings.Join(leftovers, ", "))
		}
		return nil
	}
	return nil
}

func (c *ControlPlane) removeOwnedWorkspace(onboarding api.Onboarding, shared bool) error {
	bindingIDs := []string{}
	if shared || onboarding.ConnectionID == "" {
		if onboarding.BindingID != "" {
			bindingIDs = append(bindingIDs, onboarding.BindingID)
		}
	} else {
		c.Store.mu.RLock()
		for id, binding := range c.Store.bindings {
			if binding.ConnectionID == onboarding.ConnectionID {
				bindingIDs = append(bindingIDs, id)
			}
		}
		c.Store.mu.RUnlock()
		sort.Strings(bindingIDs)
	}
	for _, id := range bindingIDs {
		binding, err := c.Store.binding(id)
		if errors.Is(err, api.ErrNotFound) {
			continue
		}
		if err != nil {
			return err
		}
		definition, err := c.Store.Definition(binding.DefinitionID, binding.DefinitionVersion)
		if err != nil {
			definition = api.ToolDefinition{DefinitionID: binding.DefinitionID}
			definition.Workload.Class = binding.WorkloadClass
		}
		effective := api.EffectiveBinding{Binding: binding, Definition: definition}
		if connection, ok := c.Store.connectionFor(binding.ConnectionID); ok {
			effective.Connection = &connection
		}
		if err := api.RemoveBindingWorkspace(c.WorkloadRoot, effective); err != nil {
			return err
		}
		if c.CredentialStagingRoot != "" && c.CredentialStagingRoot != c.WorkloadRoot {
			if err := api.RemoveBindingWorkspace(c.CredentialStagingRoot, effective); err != nil {
				return err
			}
		}
		if err := c.Store.RemoveBindingRecords(id); err != nil {
			return err
		}
	}
	if shared || onboarding.ConnectionID == "" {
		return nil
	}
	auth := identity.Envelope{Schema: identity.Schema, PrincipalID: onboarding.PrincipalID,
		ExternalIdentityID: onboarding.PrincipalID, ContextID: onboarding.ContextID,
		RuntimeID: onboarding.RuntimeID, PolicyVersion: onboarding.PolicyVersion,
		ConversationID: onboarding.PrincipalID, DeliveryTargetID: onboarding.PrincipalID}
	return c.Store.RemoveOwnedConnectionRecords(auth, onboarding.ConnectionID)
}

func (c *ControlPlane) deleteBrokerCredentials(ctx context.Context, onboarding api.Onboarding) error {
	if c.Broker == nil || !c.Broker.Enabled() {
		if onboarding.BrokerCredentialID != "" || onboarding.BrokerRequestID != "" {
			return fmt.Errorf("%w: credential broker unavailable", api.ErrCredentialVisibilityUnknown)
		}
		return nil
	}
	auth := identity.Envelope{Schema: identity.Schema, PrincipalID: onboarding.PrincipalID,
		ExternalIdentityID: onboarding.PrincipalID, ContextID: onboarding.ContextID,
		RuntimeID: onboarding.RuntimeID, PolicyVersion: onboarding.PolicyVersion,
		ConversationID: onboarding.PrincipalID, DeliveryTargetID: onboarding.PrincipalID}
	control, err := c.brokerControl(auth, c.onboardingShared(onboarding))
	if err != nil {
		return err
	}
	connectionID := onboarding.ConnectionID
	if onboarding.BrokerRequestID != "" {
		request, err := control.Request(ctx, onboarding.BrokerRequestID)
		if err != nil && !brokerclient.IsNotFound(err) {
			return err
		}
		if err == nil {
			if connectionID != "" && connectionID != request.ConnectionID {
				return fmt.Errorf("%w: broker request connection mismatch", api.ErrCredentialVisibilityUnknown)
			}
			connectionID = request.ConnectionID
			if request.Status != "ready" && request.Status != "canceled" {
				if err := control.Do(ctx, http.MethodPost, "/v1/requests/"+url.PathEscape(request.ID)+"/cancel", nil, nil); err != nil {
					return err
				}
			}
		} else if connectionID == "" {
			return fmt.Errorf("%w: broker request not visible", api.ErrCredentialVisibilityUnknown)
		}
	}
	if connectionID == "" {
		if onboarding.BrokerCredentialID == "" {
			return nil
		}
		var credential brokerv1.Credential
		if err := control.Do(ctx, http.MethodGet, "/v1/credentials/"+url.PathEscape(onboarding.BrokerCredentialID), nil, &credential); err != nil {
			return fmt.Errorf("%w: broker credential not visible: %v", api.ErrCredentialVisibilityUnknown, err)
		}
		connectionID = credential.ConnectionID
	}
	path := "/v1/connections/" + url.PathEscape(connectionID) + "/credentials"
	var credentials []brokerv1.Credential
	if err := control.Do(ctx, http.MethodGet, path, nil, &credentials); err != nil {
		return err
	}
	for _, credential := range credentials {
		if credential.Status == "deleted" {
			continue
		}
		if err := control.Do(ctx, http.MethodDelete, "/v1/credentials/"+url.PathEscape(credential.ID), nil, nil); err != nil {
			return err
		}
	}
	credentials = nil
	if err := control.Do(ctx, http.MethodGet, path, nil, &credentials); err != nil {
		return err
	}
	for _, credential := range credentials {
		if credential.Status != "deleted" {
			return fmt.Errorf("%w: broker credential remains", api.ErrCredentialVisibilityUnknown)
		}
	}
	return nil
}

// purgeLabelled sweeps leftovers of one kind: the controller's release path
// already removes what it created, so the sweep is a no-op unless a labelled
// object survived (crashed remove, external intervention).
func (c *ControlPlane) purgeLabelled(ctx context.Context, onboarding api.Onboarding, kind string) error {
	if c.LabelQuery == nil || c.LabelPurge == nil || onboarding.ConnectionID == "" {
		return nil
	}
	found, err := c.LabelQuery(ctx, map[string]string{api.LabelConnection: onboarding.ConnectionID})
	if err != nil {
		return err
	}
	for _, name := range found[kind] {
		if err := c.LabelPurge(ctx, kind, name, map[string]string{api.LabelConnection: onboarding.ConnectionID}); err != nil {
			return err
		}
	}
	return nil
}

// removeReceipt renders the verified-zero line shown in connector_status and
// the operation summary, e.g. "removed: 1 container, 2 volumes; verified 0
// remaining".
func removeReceipt(footprint map[string]int) string {
	if len(footprint) == 0 {
		return "verified 0 remaining"
	}
	kinds := make([]string, 0, len(footprint))
	for kind := range footprint {
		kinds = append(kinds, kind)
	}
	sort.Strings(kinds)
	parts := make([]string, 0, len(kinds))
	for _, kind := range kinds {
		n := footprint[kind]
		label := kind
		if n != 1 {
			label += "s"
		}
		parts = append(parts, fmt.Sprintf("%d %s", n, label))
	}
	return "removed: " + strings.Join(parts, ", ") + "; verified 0 remaining"
}
