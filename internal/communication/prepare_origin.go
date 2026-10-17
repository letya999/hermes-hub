package communication

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"os"
	"path"
	"path/filepath"
	"strings"
	"time"
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

// prepareMessageID returns the Telegram message that already carries this
// onboarding's status, so later notices edit it instead of stacking new
// messages. The anchor is the earliest sent prepare delivery; pending records
// have no receipt yet and failed ones are excluded.
func (s *Spool) prepareMessageID(onboardingID string) int {
	prefix := "prepare-" + onboardingID + "-"
	best := 0
	var bestTime time.Time
	for _, dir := range []string{"sending", "done"} {
		entries, err := os.ReadDir(filepath.Join(s.root, "outbox", dir))
		if err != nil {
			continue
		}
		for _, entry := range entries {
			if entry.IsDir() || !strings.HasPrefix(entry.Name(), prefix) {
				continue
			}
			b, err := os.ReadFile(filepath.Join(s.root, "outbox", dir, entry.Name()))
			if err != nil {
				continue
			}
			var d Delivery
			if json.Unmarshal(b, &d) != nil || d.TelegramMessageID <= 0 {
				continue
			}
			if best == 0 || d.CreatedAt.Before(bestTime) {
				best, bestTime = d.TelegramMessageID, d.CreatedAt
			}
		}
	}
	return best
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

// initiatingMessageMapping finds the message job that issued the prepare call.
// The call happens inside the job, so only jobs accepted no later than the
// onboarding's creation instant qualify — but concurrent sessions can overlap
// inside that window, so several candidates are disambiguated by content: the
// initiator's stored result names the source it installed (repo token), while
// an unrelated session's job does not.
func (s *Spool) initiatingMessageMapping(principal string, onboardingCreated time.Time, repository string) (JobMapping, bool) {
	entries, err := os.ReadDir(filepath.Join(s.root, "mappings"))
	if err != nil {
		return JobMapping{}, false
	}
	var candidates []JobMapping
	for _, entry := range entries {
		if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".json") {
			continue
		}
		body, err := os.ReadFile(filepath.Join(s.root, "mappings", entry.Name()))
		if err != nil {
			continue
		}
		var mapping JobMapping
		if json.Unmarshal(body, &mapping) != nil || mapping.PrincipalID != principal || mapping.Trigger != "message" || mapping.CancelRequested {
			continue
		}
		if mapping.CreatedAt.After(onboardingCreated.Add(2 * time.Second)) {
			continue
		}
		candidates = append(candidates, mapping)
	}
	if len(candidates) == 0 {
		return JobMapping{}, false
	}
	if token := repoToken(repository); token != "" && len(candidates) > 1 {
		var named []JobMapping
		for _, mapping := range candidates {
			if strings.Contains(strings.ToLower(mapping.Result), token) {
				named = append(named, mapping)
			}
		}
		if len(named) > 0 {
			candidates = named
		}
	}
	best := candidates[0]
	for _, mapping := range candidates[1:] {
		if mapping.CreatedAt.After(best.CreatedAt) {
			best = mapping
		}
	}
	return best, true
}

// repoToken reduces a source URL to the name an agent report is most likely to
// quote back: the basename without the -mcp/mcp- wrapper ("gitlab-mcp" →
// "gitlab", "ru-marketplace-mcp" → "ru-marketplace").
func repoToken(repository string) string {
	base := strings.ToLower(path.Base(strings.TrimSuffix(strings.TrimSpace(repository), "/")))
	base = strings.TrimSuffix(base, "-mcp")
	base = strings.TrimPrefix(base, "mcp-")
	if len(base) < 3 {
		return ""
	}
	return base
}
