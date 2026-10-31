package skillstore

import (
	"path/filepath"
	"time"

	"github.com/letya999/hermes-hub/internal/audit"
)

// audit appends a metadata-only event to the per-store ledger at
// .state/audit.jsonl (R4.6). Skill content never reaches an event; the
// ledger is a trail, not the security boundary, so write failures do not
// fail the store operation.
func (s *Store) audit(outcome, name string) {
	ledger, err := audit.Open(filepath.Join(s.root, stateDir, "audit.jsonl"))
	if err != nil {
		return
	}
	_ = ledger.Append(audit.Event{
		Schema:      audit.Schema,
		EventID:     "ev-" + randSuffix(),
		At:          time.Now().UTC(),
		Kind:        "skill",
		PrincipalID: string(s.scope),
		Outcome:     outcome,
		Name:        name,
	})
}
