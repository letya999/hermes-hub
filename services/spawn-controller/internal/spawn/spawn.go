// Package spawn owns spawn-controller.db: the embedded schema, the
// PassportRevision/Runtime/Worker transition tables and the lease API.
// The HTTP path does not use it yet — wiring lands in S4.
package spawn

import (
	"context"

	"github.com/letya999/hermes-hub/services/spawn-controller/internal/spawn/migrations"
	"github.com/letya999/hermes-hub/shared/store"
)

// Open opens the store at url and applies the service schema.
func Open(ctx context.Context, url string) (*store.Store, error) {
	return store.Open(ctx, url, migrations.FS)
}
