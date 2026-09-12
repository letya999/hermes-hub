package communication

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// RequeueObservations retries observation only for durably admitted runs.
// Unknown admission stays uncertain; replaying its input could repeat an action.
func (s *Spool) RequeueObservations(now time.Time) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	entries, err := os.ReadDir(filepath.Join(s.root, "failed"))
	if err != nil {
		return err
	}
	for _, entry := range entries {
		if !strings.HasSuffix(entry.Name(), ".json") {
			continue
		}
		from := filepath.Join(s.root, "failed", entry.Name())
		b, err := os.ReadFile(from)
		var job Job
		if err != nil || json.Unmarshal(b, &job) != nil {
			return errors.New("invalid recovery job")
		}
		mapping, err := s.loadMappingLocked(job.ID)
		if errors.Is(err, os.ErrNotExist) {
			continue
		}
		if err != nil {
			return err
		}
		if mapping.Status != "uncertain" || mapping.RunID == "" || mapping.SessionID == "" || mapping.RuntimeGeneration == "" || now.Before(mapping.NextObservationAt) {
			continue
		}
		if mapping.Fingerprint != jobFingerprint(job) || mapping.JobID != job.ID || entry.Name() != spoolFileID(job.ID)+".json" {
			return errors.New("recovery job binding mismatch")
		}
		if mapping.ObservationAttempts < 6 {
			mapping.ObservationAttempts++
		}
		delay := 5 * time.Second
		for attempt := uint64(1); attempt < mapping.ObservationAttempts; attempt++ {
			delay *= 2
			if delay >= 30*time.Second {
				delay = 30 * time.Second
				break
			}
		}
		mapping.NextObservationAt = now.Add(delay)
		if err = s.writeMappingLocked(mapping); err != nil {
			return err
		}
		if err = os.Rename(from, filepath.Join(s.root, "pending", entry.Name())); err != nil {
			return err
		}
	}
	return nil
}
