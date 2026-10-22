package toolhub

import (
	"context"
	"fmt"
	"net/http"
	"net/url"

	brokerclient "github.com/letya999/credential-broker/client"
	"github.com/letya999/hermes-hub/internal/identity"

	"github.com/letya999/hermes-hub/internal/toolhub/api"
)

// NewReconciler binds the reconciler's seams to this control plane's real
// store, Docker label ops, broker client and channel notifier. Seams whose
// backend is unconfigured stay nil and their checks skip.
func (c *ControlPlane) NewReconciler() *Reconciler {
	r := &Reconciler{Store: c.Store}
	r.CredAlive = func(ctx context.Context, refID string) (bool, error) {
		ref, ok := c.Store.credentialFor(refID)
		if ok && ref.Backend == "credential-broker" {
			control, err := c.reconcileBrokerClient(ref.ConnectionID)
			if err != nil {
				return false, err
			}
			gone, err := brokerCredentialGone(ctx, control, ref.Locator)
			return !gone && err == nil, err
		}
		if c.Secrets == nil {
			return true, nil
		}
		if !ok {
			return false, nil
		}
		_, err := c.Secrets.Info(ref.Locator, c.Store.credentialOwner(ref.ConnectionID))
		return err == nil, nil
	}
	r.CredPurge = func(ctx context.Context, _ api.Connection, ref api.CredentialReference) error {
		if ref.Backend == "credential-broker" {
			control, err := c.reconcileBrokerClient(ref.ConnectionID)
			if err != nil {
				return err
			}
			err = control.Do(ctx, http.MethodDelete, "/v1/credentials/"+url.PathEscape(ref.Locator), nil, nil)
			if brokerclient.IsNotFound(err) {
				return nil
			}
			return err
		}
		if c.Secrets == nil || ref.Locator == "" {
			return nil
		}
		return c.Secrets.Delete(ref.Locator, c.Store.credentialOwner(ref.ConnectionID))
	}
	if c.SurfaceProbe != nil {
		r.Surface = func(ctx context.Context, connection api.Connection) ([]api.ToolSpec, error) {
			definition, err := c.Store.definitionByID(connection.DefinitionID)
			if err != nil {
				return nil, err
			}
			return c.SurfaceProbe(ctx, connection, definition)
		}
	}
	r.CredResolve = func(_ context.Context, connection api.Connection) (string, error) {
		resolution, err := c.Store.Recipe(connection.Metadata[api.LabelRecipe])
		if err != nil {
			return "", nil
		}
		return api.CredentialRecipeDigest(resolution)
	}
	r.Resolve = func(_ context.Context, connection api.Connection) (string, error) {
		resolution, err := c.Store.Recipe(connection.Metadata[api.LabelRecipe])
		if err != nil {
			return "", nil
		}
		return api.ResolutionDigest(resolution)
	}
	r.Notify = func(_ context.Context, _, kind string, body map[string]any) error {
		connectionID, _ := body["connection"].(string)
		onboarding, ok := c.Store.onboardingForConnection(connectionID)
		if !ok {
			return nil
		}
		switch kind {
		case "reauth":
			onboarding.Phase = api.PhaseAwaitingCreds
			onboarding.FormNonce = api.RandomNonce()
			onboarding.FormExpires = c.now().Add(c.ttl())
		case "drift":
			onboarding.Phase = api.PhaseAwaitingConfirm
			onboarding.ConfirmationNonce = api.RandomNonce()
			onboarding.ConfirmationExpires = c.now().Add(c.ttl())
			onboarding.ConfirmationUsed = false
		default:
			return nil
		}
		c.armWizard(&onboarding)
		onboarding.Revision++
		return c.Store.PutOnboarding(onboarding)
	}
	r.Scan = c.LabelScan
	r.Purge = c.LabelPurge
	return r
}

func (c *ControlPlane) reconcileBrokerClient(connectionID string) (*brokerclient.Client, error) {
	if c.Broker == nil || !c.Broker.Enabled() {
		return nil, fmt.Errorf("%w: credential broker unavailable", api.ErrIsolation)
	}
	onboarding, ok := c.Store.onboardingForConnection(connectionID)
	if !ok {
		return nil, fmt.Errorf("%w: credential owner context unavailable", api.ErrIsolation)
	}
	auth := identity.Envelope{Schema: identity.Schema, PrincipalID: onboarding.PrincipalID,
		ExternalIdentityID: onboarding.PrincipalID, ContextID: onboarding.ContextID,
		RuntimeID: onboarding.RuntimeID, ConversationID: onboarding.ContextID,
		DeliveryTargetID: onboarding.ContextID, PolicyVersion: onboarding.PolicyVersion}
	return c.brokerControl(auth, c.onboardingShared(onboarding))
}
