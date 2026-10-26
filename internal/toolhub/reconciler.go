package toolhub

import (
	"context"
	"errors"
	"time"

	"github.com/letya999/hermes-hub/internal/toolhub/api"
)

// Reconciler compares desired store state to labelled live resources.
// ScanOnce is the single convergence pass: it purges orphans whose owner
// connection is gone and never touches foreign or unlabelled objects.
type Reconciler struct {
	Store     *Store
	Scan      func(ctx context.Context, selector map[string]string) ([]api.LabeledResource, error)
	Purge     func(ctx context.Context, kind, name string, selector map[string]string) error
	CredAlive func(ctx context.Context, refID string) (bool, error)
	Resolve   func(ctx context.Context, connection api.Connection) (string, error)
	Surface   func(ctx context.Context, connection api.Connection) ([]api.ToolSpec, error)
	Notify    func(ctx context.Context, principal, kind string, body map[string]any) error
	MaxScan   int
	// CredResolve re-resolves the connection's credential-recipe digest
	// (provider/endpoint/client claims); CredPurge deletes the stored
	// credential material for the given reference. Drift on these moves the
	// connection to reauth — stored tokens are purged, never silently
	// re-minted against a changed provider.
	CredResolve func(ctx context.Context, connection api.Connection) (string, error)
	CredPurge   func(ctx context.Context, connection api.Connection, ref api.CredentialReference) error
}

func (r *Reconciler) ScanOnce(ctx context.Context) error {
	var failures []error
	if r.Scan != nil && r.Purge != nil {
		objects, err := r.Scan(ctx, map[string]string{api.LabelConnection: ""})
		if err != nil {
			failures = append(failures, err)
			objects = nil
		}
		for scanned, object := range objects {
			if r.MaxScan > 0 && scanned >= r.MaxScan {
				break
			}
			connectionID := object.Labels[api.LabelConnection]
			if connectionID == "" || r.Store.HasConnection(connectionID) {
				continue
			}
			// A shared workload outlives the connection that spawned it: it
			// is purged only when no active binding still consumes its
			// definition — other members' demand keeps the process owned.
			if r.Store.SharedBindingConsumed(object.Labels[api.LabelBinding]) {
				continue
			}
			if err := r.Purge(ctx, object.Kind, object.Name, object.Labels); err != nil {
				failures = append(failures, err)
			}
		}
	}
	if r.CredAlive == nil {
		return errors.Join(failures...)
	}
	failed := make(map[string]bool)
	for _, connection := range r.Store.Connections() {
		if connection.Status != api.ActiveStatus && connection.Status != api.DegradedStatus {
			continue
		}
		if connection.CredentialRefID == "" {
			continue
		}
		if !connection.CredentialRetryAt.IsZero() && time.Now().Before(connection.CredentialRetryAt) {
			failed[connection.ConnectionID] = true
			continue
		}
		alive, err := r.CredAlive(ctx, connection.CredentialRefID)
		if err != nil {
			failed[connection.ConnectionID] = true
			failures = append(failures, err)
			cause := "credential_check_unavailable"
			if errors.Is(err, api.ErrCredentialVisibilityUnknown) {
				cause = "credential_visibility_unknown"
			}
			if _, recordErr := r.Store.observeCredentialCheckIfCurrent(connection, "unknown", cause, time.Now()); recordErr != nil {
				failures = append(failures, recordErr)
			}
			continue
		}
		if alive {
			if _, recordErr := r.Store.observeCredentialCheckIfCurrent(connection, "active", "", time.Now()); recordErr != nil {
				failures = append(failures, recordErr)
			}
			continue
		}
		failed[connection.ConnectionID] = true
		if _, recordErr := r.Store.observeCredentialCheckIfCurrent(connection, "inactive", "credential_inactive", time.Now()); recordErr != nil {
			failures = append(failures, recordErr)
			continue
		}
		if connection.Status == api.DegradedStatus {
			continue
		}
		applied, err := r.Store.markDegradedIfCurrent(connection.ConnectionID, &connection, "", "")
		if err != nil {
			failures = append(failures, err)
			continue
		}
		if !applied {
			continue
		}
		if err := r.Notify(ctx, connection.Owner.ID, "reauth", map[string]any{"connection": connection.ConnectionID}); err != nil {
			failures = append(failures, err)
		}
	}
	if r.CredResolve != nil {
		for _, connection := range r.Store.Connections() {
			if failed[connection.ConnectionID] {
				continue
			}
			if connection.Status != api.ActiveStatus && connection.Status != api.ReauthStatus {
				continue
			}
			pinned := connection.Metadata[api.LabelCredentialRecipe]
			if pinned == "" {
				continue
			}
			if !connection.CredRecipeRetryAt.IsZero() && time.Now().Before(connection.CredRecipeRetryAt) {
				failed[connection.ConnectionID] = true
				continue
			}
			resolved, err := r.CredResolve(ctx, connection)
			if err != nil {
				failed[connection.ConnectionID] = true
				failures = append(failures, err)
				if _, recordErr := r.Store.deferCheckIfCurrent(connection, "credential_recipe", time.Now()); recordErr != nil {
					failures = append(failures, recordErr)
				}
				continue
			}
			applied, recordErr := r.Store.clearCheckRetryIfCurrent(connection, "credential_recipe")
			if recordErr != nil {
				failures = append(failures, recordErr)
				continue
			}
			if !applied {
				continue
			}
			if resolved == pinned {
				continue
			}
			// Fail closed first: the observed digest is pinned before reauth
			// stops serving, and both precede the purge — a failure can never
			// leave a stale credential live or the drift unrecorded.
			if connection.Status != api.ReauthStatus {
				applied, err := r.Store.markReauthIfCurrent(connection.ConnectionID, &connection, resolved)
				if err != nil {
					failures = append(failures, err)
					continue
				}
				if !applied {
					continue
				}
			} else {
				applied, err := r.Store.observeDriftIfCurrent(connection.ConnectionID, &connection, api.LabelAcceptCredential, resolved)
				if err != nil {
					failures = append(failures, err)
					continue
				}
				if !applied {
					continue
				}
			}
			if r.CredPurge != nil && connection.CredentialRefID != "" {
				if ref, ok := r.Store.credentialFor(connection.CredentialRefID); ok {
					if err := r.CredPurge(ctx, connection, ref); err != nil {
						failures = append(failures, err)
						continue
					}
				}
			}
			if err := r.Notify(ctx, connection.Owner.ID, "reauth", map[string]any{"connection": connection.ConnectionID}); err != nil {
				failures = append(failures, err)
			}
		}
	}
	if r.Resolve != nil {
		for _, connection := range r.Store.Connections() {
			if connection.Status != api.ActiveStatus && connection.Status != api.DegradedStatus {
				continue
			}
			if failed[connection.ConnectionID] {
				continue
			}
			if !connection.RecipeRetryAt.IsZero() && time.Now().Before(connection.RecipeRetryAt) {
				failed[connection.ConnectionID] = true
				continue
			}
			pinned := connection.Metadata[api.LabelRecipe]
			if pinned == "" {
				continue
			}
			resolved, err := r.Resolve(ctx, connection)
			if err != nil {
				failed[connection.ConnectionID] = true
				failures = append(failures, err)
				if _, recordErr := r.Store.deferCheckIfCurrent(connection, "recipe", time.Now()); recordErr != nil {
					failures = append(failures, recordErr)
				}
				continue
			}
			applied, recordErr := r.Store.clearCheckRetryIfCurrent(connection, "recipe")
			if recordErr != nil {
				failures = append(failures, recordErr)
				continue
			}
			if !applied {
				continue
			}
			if resolved == pinned {
				continue
			}
			applied, err = r.Store.markDegradedIfCurrent(connection.ConnectionID, &connection, api.LabelAcceptRecipe, resolved)
			if err != nil {
				failures = append(failures, err)
				continue
			}
			if !applied {
				continue
			}
			if err := r.Notify(ctx, connection.Owner.ID, "drift", map[string]any{"connection": connection.ConnectionID}); err != nil {
				failures = append(failures, err)
			}
		}
	}
	return errors.Join(append(failures, r.checkSurfaces(ctx, failed))...)
}

// checkSurfaces compares each live connection's pinned tool digest with the
// surface its workload reports now; a rug-pull drift degrades the
// connection and notifies the owner — writes stay hidden until a human
// confirms the new surface.
func (r *Reconciler) checkSurfaces(ctx context.Context, failed map[string]bool) error {
	if r.Surface == nil {
		return nil
	}
	var failures []error
	for _, connection := range r.Store.Connections() {
		if connection.Status != api.ActiveStatus || failed[connection.ConnectionID] {
			continue
		}
		binding, pinned := r.Store.pinnedBinding(connection.ConnectionID)
		if !pinned {
			continue
		}
		if !connection.SurfaceRetryAt.IsZero() && time.Now().Before(connection.SurfaceRetryAt) {
			continue
		}
		surface, err := r.Surface(ctx, connection)
		if err != nil {
			failures = append(failures, err)
			if _, recordErr := r.Store.deferCheckIfCurrent(connection, "surface", time.Now()); recordErr != nil {
				failures = append(failures, recordErr)
			}
			continue
		}
		if _, recordErr := r.Store.clearCheckRetryIfCurrent(connection, "surface"); recordErr != nil {
			failures = append(failures, recordErr)
			continue
		}
		observed := api.ToolSurfaceDigest(surface)
		if observed == binding.ToolDigest {
			continue
		}
		applied, err := r.Store.markDegradedIfCurrent(connection.ConnectionID, &connection, api.LabelAcceptSurface, observed)
		if err != nil {
			failures = append(failures, err)
			continue
		}
		if !applied {
			continue
		}
		if err := r.Notify(ctx, connection.Owner.ID, "drift", map[string]any{"connection": connection.ConnectionID}); err != nil {
			failures = append(failures, err)
		}
	}
	return errors.Join(failures...)
}
