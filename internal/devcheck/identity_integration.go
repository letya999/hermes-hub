//go:build integration

package devcheck

import (
	"context"
	"path/filepath"

	"github.com/letya999/hermes-hub/internal/commodel"
	"github.com/letya999/hermes-hub/internal/identityhub"
)

// Integration gates own isolated synthetic roots; they provision the same
// registry authority as control instead of enabling a production fallback.
func integrationIdentity(ctx context.Context, root string, users []commodel.User) (*identityhub.Registry, error) {
	path, err := identityhub.StorePath(filepath.Join(root, ".control"), "prod")
	if err != nil {
		return nil, err
	}
	r, err := identityhub.Open(path)
	if err != nil {
		return nil, err
	}
	if len(users) > 0 {
		if _, err := r.ImportLegacy(ctx, "integration", "personal", users); err != nil {
			_ = r.Close()
			return nil, err
		}
	}
	return r, nil
}
