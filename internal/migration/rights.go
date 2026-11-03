package migration

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/letya999/hermes-hub/internal/catalog"
	"github.com/letya999/hermes-hub/internal/passport"
	"github.com/letya999/hermes-hub/internal/stack"
	"github.com/letya999/hermes-hub/internal/toolhub"
)

// DeriveSpaceRights rewrites the space's managed rights state from the
// published grants revision (SPEC-0092 R2.1): capability profiles get the
// flattened allow as their Rights ceiling, and the governance ledger gains
// platform-issued grants for exactly those rows — grants the revision
// dropped are revoked in place. ToolHub records are always derived here,
// never authored by hand. Spaces without a toolhub store have no managed
// principal and skip derivation entirely.
//
// Secondary spaces share the infra space's control plane: profiles and the
// rights ledger live in the infra sibling's store + governance document —
// the same files the shared ToolHub serves — so derivation resolves through
// InfraSpaceDir, exactly like the governance read path.
func DeriveSpaceRights(dir, user, environment string, rev passport.Revision, docs catalog.Docs, now time.Time) error {
	root := filepath.Join(stack.InfraSpaceDir(dir), "runtime", "toolhub")
	storePath := filepath.Join(root, "store.json")
	if _, err := os.Stat(filepath.Join(root, "store.db")); err == nil {
		// SPEC-0090 R5.1: the SQLite substrate is authoritative once the
		// verified store.db exists and the legacy store.json was retired.
		if _, err := os.Stat(storePath); os.IsNotExist(err) {
			storePath = filepath.Join(root, "store.db")
		}
	}
	store, err := toolhub.Load(storePath)
	if errors.Is(err, os.ErrNotExist) {
		return nil // unmanaged space: no principal to bind
	}
	if err != nil {
		return fmt.Errorf("rights derivation: %w", err)
	}
	allow, err := rev.GrantAllow()
	if err != nil {
		return fmt.Errorf("rights derivation: %w", err)
	}
	settings, err := stack.ReadEnvironment(dir, environment)
	if err != nil {
		return fmt.Errorf("rights derivation: %w", err)
	}
	gov := toolhub.NewGovernanceStore(toolhub.GovernanceStorePath(storePath))
	return toolhub.DeriveRights(store, gov, docs.Rights, user, allow, environment, stack.PolicyVersion(settings), now)
}
