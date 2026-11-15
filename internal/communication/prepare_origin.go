package communication

import (
	"encoding/json"
	"path"
	"path/filepath"
	"strings"
	"time"
)

// prepareOrigin is the resolved channel conversation a notice returns to. It
// is derived fresh on every send — never stored: a persisted route outlives
// the session it names and strands notices on a dead channel.
type prepareOrigin struct {
	ConversationID   string
	DeliveryTargetID string
	Channel          string
	ChatID           int64
	TopicID          int64
}

// prepareOriginMaxAge bounds how far back an initiator may be trusted: an
// onboarding created days ago cannot prove which session is live today.
const prepareOriginMaxAge = 24 * time.Hour

// prepareMessageID returns the Telegram message that already carries this
// onboarding's status, so later notices edit it instead of stacking new
// messages. The anchor is the earliest sent prepare delivery; pending records
// have no receipt yet and failed ones are excluded.
func (s *Spool) prepareMessageID(onboardingID string) int {
	prefix := "prepare-" + onboardingID + "-"
	best := 0
	var bestTime time.Time
	for _, dir := range []string{"sending", "done"} {
		entries, err := s.fsys.ReadDir(filepath.Join(s.root, "outbox", dir))
		if err != nil {
			continue
		}
		for _, name := range entries {
			if !strings.HasPrefix(name, prefix) {
				continue
			}
			b, err := s.fsys.ReadFile(filepath.Join(s.root, "outbox", dir, name))
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
		if err != nil || mapping.PrincipalID != principal || mapping.Trigger != "message" || mapping.Status != "running" || mapping.CancelRequested {
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
	entries, err := s.fsys.ReadDir(filepath.Join(s.root, "mappings"))
	if err != nil {
		return JobMapping{}, false
	}
	var candidates []JobMapping
	for _, name := range entries {
		if !strings.HasSuffix(name, ".json") {
			continue
		}
		body, err := s.fsys.ReadFile(filepath.Join(s.root, "mappings", name))
		if err != nil {
			continue
		}
		var mapping JobMapping
		if json.Unmarshal(body, &mapping) != nil || mapping.PrincipalID != principal || mapping.Trigger != "message" || mapping.CancelRequested {
			continue
		}
		if mapping.CreatedAt.Before(onboardingCreated.Add(-5*time.Minute)) || mapping.CreatedAt.After(onboardingCreated.Add(2*time.Second)) {
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
