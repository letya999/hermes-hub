package communication

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"os"
	"path/filepath"
)

// prepareOrigin remembers which channel conversation initiated an onboarding:
// the outcome callback authenticates by principal alone, so the origin is
// captured from the active message job while it still runs and reused once
// that job has finished.
type prepareOrigin struct {
	ConversationID   string `json:"conversation_id"`
	DeliveryTargetID string `json:"delivery_target_id,omitempty"`
	ChatID           int64  `json:"chat_id"`
	TopicID          int64  `json:"topic_id,omitempty"`
}

func (s *Spool) prepareOriginPath(onboardingID string) string {
	h := sha256.Sum256([]byte("prepare-origin\x00" + onboardingID))
	return filepath.Join(s.root, "prepares", hex.EncodeToString(h[:])+".json")
}

func (s *Spool) putPrepareOrigin(onboardingID string, origin prepareOrigin) error {
	path := s.prepareOriginPath(onboardingID)
	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		return err
	}
	return atomicJSON(path, origin)
}

func (s *Spool) loadPrepareOrigin(onboardingID string) (prepareOrigin, bool) {
	body, err := os.ReadFile(s.prepareOriginPath(onboardingID))
	if err != nil {
		return prepareOrigin{}, false
	}
	var origin prepareOrigin
	if err := json.Unmarshal(body, &origin); err != nil || origin.ConversationID == "" {
		return prepareOrigin{}, false
	}
	return origin, true
}

// newestActiveMessageMapping returns the principal's most recently accepted
// non-terminal message job: while its agent is mid-run, that is the job a
// just-arrived prepare callback belongs to.
func (s *Spool) newestActiveMessageMapping(principal string) (JobMapping, bool) {
	ids, err := s.activeMappingIDs()
	if err != nil {
		return JobMapping{}, false
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	var best JobMapping
	for _, id := range ids {
		mapping, err := s.loadMappingLocked(id)
		if err != nil || mapping.PrincipalID != principal || mapping.Trigger != "message" || terminalStatus(mapping.Status) || mapping.CancelRequested {
			continue
		}
		if mapping.CreatedAt.After(best.CreatedAt) {
			best = mapping
		}
	}
	return best, best.JobID != ""
}
