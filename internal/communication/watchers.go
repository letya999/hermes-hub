package communication

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"time"
)

// WatchState is the durable baseline a watcher compares against. It lives in
// spool/watchers/<scheduleID>.json and is the only thing the deterministic
// poll needs between ticks — no model sees an unchanged source.
type WatchState struct {
	ScheduleID string    `json:"schedule_id"`
	Baseline   string    `json:"baseline"`
	Value      string    `json:"value"`
	PrevValue  string    `json:"prev_value,omitempty"`
	ChangedAt  time.Time `json:"changed_at"`
}

// DigestState marks when the owner's watches were last aggregated.
type DigestState struct {
	ScheduleID   string    `json:"schedule_id"`
	LastDigestAt time.Time `json:"last_digest_at"`
}

func (s *Spool) digestPath(id string) string {
	return filepath.Join(s.root, "digests", spoolFileID(id)+".json")
}

func (s *Spool) watchPath(id string) string {
	return filepath.Join(s.root, "watchers", spoolFileID(id)+".json")
}

func (s *Spool) loadWatchStateLocked(id string) (WatchState, error) {
	var st WatchState
	b, err := os.ReadFile(s.watchPath(id))
	if os.IsNotExist(err) {
		return WatchState{ScheduleID: id}, nil
	}
	if err != nil {
		return st, err
	}
	if json.Unmarshal(b, &st) != nil {
		return st, errors.New("invalid watch state")
	}
	return st, nil
}

// SeedWatchBaseline records the creation-time baseline a check schedule
// evaluates against — the world as it was when the user set the follow-up.
func (s *Spool) SeedWatchBaseline(id string, value string, now time.Time) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	return atomicJSON(s.watchPath(id), WatchState{ScheduleID: id, Baseline: watchHash(value), Value: value})
}

// watchValue extracts the compared value — the first capture group of the
// compiled regex, or the whole bounded body when no regex is given (hash the
// extracted value, never the raw page).
func watchValue(body []byte, re *regexp.Regexp) string {
	if re == nil {
		return string(body)
	}
	if m := re.FindSubmatch(body); len(m) > 1 {
		return string(m[1])
	}
	return ""
}

func watchHash(value string) string {
	sum := sha256.Sum256([]byte(value))
	return hex.EncodeToString(sum[:])
}

// ProbeDueOccurrences executes the deterministic probe for every occurrence
// the dispatcher moved to "probing", settles it and updates durable watch
// state. Fetches run outside the spool lock; each settle re-locks and
// re-validates the occurrence is still probing (crash-safe: a stale probing
// occurrence is probed again — delivery ids dedupe).
func (s *Spool) ProbeDueOccurrences(now time.Time, fetch func(context.Context, string) ([]byte, error)) error {
	s.mu.Lock()
	pending := []RoutineOccurrence{}
	entries, err := os.ReadDir(filepath.Join(s.root, "occurrences"))
	if err != nil {
		s.mu.Unlock()
		return err
	}
	for _, entry := range entries {
		if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".json") {
			continue
		}
		b, err := os.ReadFile(filepath.Join(s.root, "occurrences", entry.Name()))
		if err != nil {
			s.mu.Unlock()
			return err
		}
		var o RoutineOccurrence
		if json.Unmarshal(b, &o) != nil || o.State != "probing" {
			continue
		}
		pending = append(pending, o)
	}
	s.mu.Unlock()
	for _, o := range pending {
		s.probeOne(o, now, fetch)
	}
	return nil
}

// fetchWatch is the live probe: one GET, caches bypassed, body capped at
// 256 KiB — the deterministic poll never touches the model.
func (g *Gateway) fetchWatch(ctx context.Context, url string) ([]byte, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Cache-Control", "no-cache")
	resp, err := (&http.Client{Timeout: 10 * time.Second}).Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode/100 != 2 {
		return nil, fmt.Errorf("probe status %d", resp.StatusCode)
	}
	return io.ReadAll(io.LimitReader(resp.Body, 256*1024))
}

func (s *Spool) probeOne(o RoutineOccurrence, now time.Time, fetch func(context.Context, string) ([]byte, error)) {
	s.mu.Lock()
	schedule, err := s.loadScheduleLocked(o.ScheduleID)
	s.mu.Unlock()
	if err != nil {
		return
	}
	var re *regexp.Regexp
	if schedule.Probe.Regex != "" {
		re, _ = regexp.Compile(schedule.Probe.Regex)
	}
	body, fetchErr := fetch(context.Background(), schedule.Probe.URL)
	if fetchErr != nil {
		s.settleProbe(o, schedule, "", "", now, fetchErr)
		return
	}
	value := watchValue(body, re)
	s.settleProbe(o, schedule, value, watchHash(value), now, nil)
}

// settleProbe persists the tick outcome: silent baseline seed, no_change,
// changed + one delivery, or failed — and lands the occurrence in a terminal
// state. The watch state write and the occurrence write share the lock.
func (s *Spool) settleProbe(o RoutineOccurrence, schedule Schedule, value, hash string, now time.Time, probeErr error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	path := filepath.Join(s.root, "occurrences", occurrenceID(o)+".json")
	b, err := os.ReadFile(path)
	if err != nil {
		return
	}
	var live RoutineOccurrence
	if json.Unmarshal(b, &live) != nil || live.State != "probing" {
		return
	}
	if probeErr != nil {
		live.State = "failed"
		_ = atomicJSON(path, live)
		return
	}
	st, err := s.loadWatchStateLocked(live.ScheduleID)
	if err != nil {
		return
	}
	if live.JobKind == "check" {
		met := st.Baseline != "" && ((hash != st.Baseline && schedule.Expect == "changed") || (hash == st.Baseline && schedule.Expect == "unchanged"))
		if !met {
			live.State = "skipped"
			_ = atomicJSON(path, live)
			return
		}
		d, err := normalizeDelivery(Delivery{ID: "check-" + live.Job.ID, Channel: live.Job.Channel, ConversationID: live.Job.ConversationID, DeliveryTargetID: live.Job.DeliveryTargetID, ChatID: live.Job.ChatID, TaskID: live.Job.TaskID, TopicID: live.Job.TopicID, SlackChannel: live.Job.SlackChannel, SlackThread: live.Job.SlackThread, Text: live.Job.Text, CreatedAt: now.UTC()})
		if err != nil {
			return
		}
		if err = s.enqueueDeliveryLocked(d); err != nil {
			return
		}
		live.State = "dispatched"
		_ = atomicJSON(path, live)
		return
	}
	if st.Baseline == "" {
		st.Baseline = hash
		st.Value = value
		live.State = "dispatched"
	} else if hash == st.Baseline {
		live.State = "no_change"
	} else {
		st.PrevValue = st.Value
		st.Value = value
		st.Baseline = hash
		st.ChangedAt = now.UTC()
		d, err := normalizeDelivery(Delivery{ID: "watch-" + live.Job.ID, Channel: live.Job.Channel, ConversationID: live.Job.ConversationID, DeliveryTargetID: live.Job.DeliveryTargetID, ChatID: live.Job.ChatID, TaskID: live.Job.TaskID, TopicID: live.Job.TopicID, SlackChannel: live.Job.SlackChannel, SlackThread: live.Job.SlackThread, Text: fmt.Sprintf("%s\nwas: %s\nnow: %s", live.Job.Text, st.PrevValue, st.Value), CreatedAt: now.UTC()})
		if err != nil {
			return
		}
		if err = s.enqueueDeliveryLocked(d); err != nil {
			return
		}
		live.State = "dispatched"
	}
	_ = atomicJSON(s.watchPath(live.ScheduleID), st)
	_ = atomicJSON(path, live)
}
