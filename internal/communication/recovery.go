package communication

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"time"

	hubruntime "github.com/letya999/hermes-hub/internal/runtime"
)

// RequeueObservations retries observation for uncertain jobs. A durably
// admitted run re-observes by run identity; an admission never confirmed
// (no run binding) is only requeued when reconcileAdmissionUnknown is set —
// that path re-observes through the supervisor's /v1/resume, which replays a
// settled outcome or reports the admission still unknown. Replaying the input
// directly could repeat an action, so the plain-runtime path keeps the old
// run-binding requirement.
func (s *Spool) RequeueObservations(now time.Time, reconcileAdmissionUnknown bool) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	entries, err := os.ReadDir(filepath.Join(s.root, "failed"))
	if err != nil {
		return err
	}
jobs:
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
		if mapping.Status != "uncertain" || now.Before(mapping.NextObservationAt) {
			continue
		}
		admitted := mapping.RunID != "" && mapping.SessionID != "" && mapping.RuntimeGeneration != ""
		if !admitted && !reconcileAdmissionUnknown {
			continue
		}
		if mapping.Fingerprint != jobFingerprint(job) || mapping.JobID != job.ID || entry.Name() != spoolFileID(job.ID)+".json" {
			return errors.New("recovery job binding mismatch")
		}
		// Once the budget notice is durable, further observation cannot
		// notify again and must not consume a worker forever.
		if !now.Before(mapping.UncertainSince.Add(hubruntime.RunBudget())) {
			for _, dir := range []string{"pending", "sending", "done", "failed"} {
				_, err := os.Stat(filepath.Join(s.root, "outbox", dir, spoolFileID("job-"+job.ID+"-uncertain")+".json"))
				if err == nil {
					continue jobs
				}
				if !errors.Is(err, os.ErrNotExist) {
					return err
				}
			}
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
