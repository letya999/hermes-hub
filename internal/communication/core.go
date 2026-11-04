package communication

import (
	"github.com/letya999/hermes-hub/internal/memory"
	"github.com/letya999/hermes-hub/internal/vault"
)

// CoreForRun pins the admitted-run CORE snapshot on the durable mapping —
// the same discipline as StyleForRun: a queued or retried job reuses its
// snapshot while a later CORE edit applies to the next run only. Audience is
// enforced at the seam: only private-audience scopes ever receive CORE
// bytes; every other scope returns empty without pinning.
func (s *Spool) CoreForRun(job Job, vaultDir string) (string, error) {
	if memory.AudienceOf(job.ScopeID) != memory.AudiencePrivate {
		return "", nil
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	mapping, err := s.loadMappingLocked(job.ID)
	if err != nil {
		return "", err
	}
	if mapping.CoreSnapshotted {
		return mapping.CoreSnapshot, nil
	}
	block := vault.CoreBlock(vaultDir)
	mapping.CoreSnapshot, mapping.CoreSnapshotted = block, true
	if err := s.writeMappingLocked(mapping); err != nil {
		return "", err
	}
	return block, nil
}
