package communication

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"time"
	"unicode/utf8"
)

// Goal is the durable per-conversation goal-mode record (SPEC-0074). It binds
// (principal, conversation) — never a Hermes session id — so session rotation
// cannot orphan it. For the native engine it mirrors the upstream record's
// verdicts; the authoritative upstream row stays in the runtime's session DB.
type Goal struct {
	Schema          int       `json:"schema"`
	TaskID          string    `json:"task_id"`
	PrincipalID     string    `json:"principal_id"`
	ConversationID  string    `json:"conversation_id"`
	Text            string    `json:"goal_text"`
	Engine          string    `json:"engine"`
	Status          string    `json:"status"` // active|paused|done|blocked
	TurnsUsed       int       `json:"turns_used"`
	MaxTurns        int       `json:"max_turns"`
	LastVerdict     string    `json:"last_verdict,omitempty"`
	LastReason      string    `json:"last_reason,omitempty"`
	ConsecParse     int       `json:"consec_parse_failures,omitempty"`
	ConsecTransport int       `json:"consec_transport_failures,omitempty"`
	Epoch           int64     `json:"epoch"`
	CreatedAt       time.Time `json:"created_at"`
	UpdatedAt       time.Time `json:"updated_at"`
}

const (
	goalTextLimit   = 4096
	goalReasonLimit = 480
)

func (s *Spool) goalPath(principal, conversationID string) string {
	h := sha256.Sum256([]byte(principal + "\x00" + conversationID))
	return filepath.Join(s.root, "goals", hex.EncodeToString(h[:])+".json")
}

func (s *Spool) loadGoalLocked(principal, conversationID string) (Goal, error) {
	b, err := os.ReadFile(s.goalPath(principal, conversationID))
	if err != nil {
		return Goal{}, err
	}
	var goal Goal
	if err := json.Unmarshal(b, &goal); err != nil || goal.ConversationID != conversationID || goal.PrincipalID != principal {
		return Goal{}, errors.New("invalid goal record")
	}
	return goal, nil
}

// GoalForTask returns the caller's durable goal for one conversation; absent
// is not an error. The key is the conversation id (unique per chat+task) —
// taskID alone would collide because the synthetic home task is "default" in
// every chat (SPEC-0074 R9).
func (s *Spool) GoalForTask(principal, conversationID string) (Goal, bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	goal, err := s.loadGoalLocked(principal, conversationID)
	if errors.Is(err, os.ErrNotExist) {
		return Goal{}, false, nil
	}
	if err != nil {
		return Goal{}, false, err
	}
	return goal, true, nil
}

// SaveGoal persists a goal record; CreatedAt is stamped on first write.
func (s *Spool) SaveGoal(goal Goal) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.saveGoalLocked(goal)
}

func (s *Spool) saveGoalLocked(goal Goal) error {
	now := time.Now().UTC()
	if goal.CreatedAt.IsZero() {
		goal.CreatedAt = now
	}
	goal.Text = boundGoalField(goal.Text, goalTextLimit)
	goal.LastReason = boundGoalField(goal.LastReason, goalReasonLimit)
	goal.Schema = 1
	goal.UpdatedAt = now
	return atomicJSON(s.goalPath(goal.PrincipalID, goal.ConversationID), goal)
}

// boundGoalField truncates to limit bytes on a rune boundary — a byte-level
// cut leaves a split sequence that encoding/json would silently rewrite as
// U+FFFD, growing the stored field past the bound.
func boundGoalField(s string, limit int) string {
	if len(s) <= limit {
		return s
	}
	s = s[:limit]
	for len(s) > 0 && !utf8.ValidString(s) {
		s = s[:len(s)-1]
	}
	return s
}

// ClearGoal removes the durable record so a cleared goal cannot resume.
func (s *Spool) ClearGoal(principal, conversationID string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := os.Remove(s.goalPath(principal, conversationID)); err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	return nil
}

// parseGoalArgs translates the channel grammar: bare and exact control verbs
// dispatch; anything else is goal text — except the out-of-scope upstream
// verbs, which refuse by name.
func parseGoalArgs(text string) (verb, arg string) {
	fields := strings.Fields(text)
	if len(fields) < 2 {
		return "status", ""
	}
	rest := strings.TrimSpace(strings.TrimPrefix(text, fields[0]))
	switch strings.ToLower(rest) {
	case "status":
		return "status", ""
	case "pause":
		return "pause", ""
	case "resume":
		return "resume", ""
	case "clear":
		return "clear", ""
	}
	if goalUnsupportedVerbs[strings.ToLower(fields[1])] {
		return "unsupported", strings.ToLower(fields[1])
	}
	return "set", rest
}

// goalUnsupportedVerbs are upstream goal verbs deliberately out of scope for
// v1 (SPEC-0074): they get a named refusal instead of silently becoming goal
// text — a mistyped `/goal gate add …` must not spawn a runaway objective.
var goalUnsupportedVerbs = map[string]bool{
	"draft": true, "gate": true, "wait": true, "subgoal": true,
	"show": true, "done": true, "stop": true,
}
