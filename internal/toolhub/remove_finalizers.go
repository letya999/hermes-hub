package toolhub

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"strings"

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
		return c.revokeBrokerCredential(onboarding)
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
		if onboarding.BindingID == "" {
			return nil
		}
		if binding, err := c.Store.binding(onboarding.BindingID); err == nil {
			definition, _ := c.definitionOf(onboarding)
			effective := api.EffectiveBinding{Binding: binding, Definition: definition}
			if binding.ConnectionID != "" {
				auth := identity.Envelope{Schema: identity.Schema, PrincipalID: onboarding.PrincipalID, ContextID: onboarding.ContextID}
				if conn, _, err := c.Store.OwnedConnection(auth, binding.ConnectionID); err == nil {
					effective.Connection = &conn
				}
			}
			if err := api.RemoveBindingWorkspace(c.WorkloadRoot, effective); err != nil {
				return err
			}
		}
		return c.Store.RemoveBindingRecords(onboarding.BindingID)
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
		if err := c.LabelPurge(ctx, kind, name); err != nil {
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
