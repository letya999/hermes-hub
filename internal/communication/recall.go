package communication

import (
	"time"

	"github.com/letya999/hermes-hub/internal/memory"
	memorysearch "github.com/letya999/hermes-hub/internal/memory/search"
)

// RecallForRun pins the admitted-run recall block on the durable mapping —
// the same discipline as CoreForRun: a retried job reuses its snapshot while
// a later vault edit applies to the next run only. Audience comes from the
// job's routing scope, so a group run can only ever assemble its own slice.
func (s *Spool) RecallForRun(job Job, vaultDir string) (string, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	mapping, err := s.loadMappingLocked(job.ID)
	if err != nil {
		return "", err
	}
	if mapping.RecallSnapshotted {
		return mapping.RecallSnapshot, nil
	}
	idx, _, err := memorysearch.NewIndexer(vaultDir).Ensure()
	if err != nil {
		return "", err
	}
	block := memorysearch.Recall(idx, job.Text, string(memory.AudienceOf(job.ScopeID)), time.Now())
	mapping.RecallSnapshot, mapping.RecallSnapshotted = block.Text, true
	if err := s.writeMappingLocked(mapping); err != nil {
		return "", err
	}
	return block.Text, nil
}
