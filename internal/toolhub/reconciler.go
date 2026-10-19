package toolhub

import (
	"context"

	"github.com/letya999/hermes-hub/internal/toolhub/api"
)

// Reconciler compares desired store state to labelled live resources.
// ScanOnce is the single convergence pass: it purges orphans whose owner
// connection is gone and never touches foreign or unlabelled objects.
type Reconciler struct {
	Store     *Store
	Scan      func(ctx context.Context, selector map[string]string) ([]api.LabeledResource, error)
	Purge     func(ctx context.Context, kind, name string) error
	CredAlive func(ctx context.Context, refID string) (bool, error)
	Resolve   func(ctx context.Context, connection api.Connection) (string, error)
	Surface   func(ctx context.Context, connection api.Connection) ([]api.ToolSpec, error)
	Notify    func(ctx context.Context, principal, kind string, body map[string]any) error
	MaxScan   int
}

func (r *Reconciler) ScanOnce(ctx context.Context) error {
	objects, err := r.Scan(ctx, map[string]string{api.LabelConnection: ""})
	if err != nil {
		return err
	}
	for scanned, object := range objects {
		if r.MaxScan > 0 && scanned >= r.MaxScan {
			break
		}
		if r.Store.HasConnection(object.Labels[api.LabelConnection]) {
			continue
		}
		if err := r.Purge(ctx, object.Kind, object.Name); err != nil {
			return err
		}
	}
	if r.CredAlive == nil {
		return nil
	}
	for _, connection := range r.Store.Connections() {
		alive, err := r.CredAlive(ctx, connection.CredentialRefID)
		if err != nil {
			return err
		}
		if alive {
			continue
		}
		if err := r.Store.MarkDegraded(connection.ConnectionID); err != nil {
			return err
		}
		if err := r.Notify(ctx, connection.Owner.ID, "reauth", map[string]any{"connection": connection.ConnectionID}); err != nil {
			return err
		}
	}
	if r.Resolve != nil {
		for _, connection := range r.Store.Connections() {
			pinned := connection.Metadata[api.LabelRecipe]
			if pinned == "" {
				continue
			}
			resolved, err := r.Resolve(ctx, connection)
			if err != nil {
				return err
			}
			if resolved == pinned {
				continue
			}
			if err := r.Store.MarkDegraded(connection.ConnectionID); err != nil {
				return err
			}
			if err := r.Notify(ctx, connection.Owner.ID, "drift", map[string]any{"connection": connection.ConnectionID}); err != nil {
				return err
			}
		}
	}
	return r.checkSurfaces(ctx)
}

// checkSurfaces compares each live connection's pinned tool digest with the
// surface its workload reports now; a rug-pull drift degrades the
// connection and notifies the owner — writes stay hidden until a human
// confirms the new surface.
func (r *Reconciler) checkSurfaces(ctx context.Context) error {
	if r.Surface == nil {
		return nil
	}
	for _, connection := range r.Store.Connections() {
		if connection.Status != api.ActiveStatus {
			continue
		}
		pinned := ""
		for _, binding := range r.Store.bindings {
			if binding.ConnectionID == connection.ConnectionID && binding.Status == api.ActiveStatus && binding.ToolDigest != "" {
				pinned = binding.ToolDigest
				break
			}
		}
		if pinned == "" {
			continue
		}
		surface, err := r.Surface(ctx, connection)
		if err != nil {
			return err
		}
		if api.ToolSurfaceDigest(surface) == pinned {
			continue
		}
		if err := r.Store.MarkDegraded(connection.ConnectionID); err != nil {
			return err
		}
		if err := r.Notify(ctx, connection.Owner.ID, "drift", map[string]any{"connection": connection.ConnectionID}); err != nil {
			return err
		}
	}
	return nil
}
