package communication

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"github.com/letya999/hermes-hub/internal/toolhub"
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
	Generation  uint64    `json:"generation,omitempty"`
	ScheduleID  string    `json:"schedule_id"`
	Baseline    string    `json:"baseline"`
	Value       string    `json:"value"`
	PrevValue   string    `json:"prev_value,omitempty"`
	ChangedAt   time.Time `json:"changed_at"`
	LastSuccess time.Time `json:"last_success"`
	LastError   string    `json:"last_error,omitempty"`
	// NotifyDay/NotifyCount implement the owner-timezone daily notification
	// cap (SPEC-0054 R2.2): the day rolls the counter back to zero.
	NotifyDay   string `json:"notify_day,omitempty"`
	NotifyCount int    `json:"notify_count,omitempty"`
}

// DigestState marks when the owner's watches were last aggregated.
type DigestState struct {
	Errors       map[string]string `json:"errors,omitempty"`
	Generations  map[string]uint64 `json:"generations,omitempty"`
	ScheduleID   string            `json:"schedule_id"`
	LastDigestAt time.Time         `json:"last_digest_at"`
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
		if schedule, err := s.loadScheduleLocked(id); err == nil && schedule.InitialProbeValue != nil {
			value := *schedule.InitialProbeValue
			return WatchState{ScheduleID: id, Baseline: watchHash(value), Value: value, LastSuccess: schedule.CreatedAt}, nil
		}
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

// taskProbeValueLocked resolves a task: probe under the schedule owner's
// principal — a foreign or missing id is an error, never a synthetic state.
func (s *Spool) taskProbeValueLocked(principal, taskID string) (string, error) {
	task, err := s.loadTaskLocked(principal, taskID)
	if err != nil {
		return "", err
	}
	if task.Archived {
		return "closed", nil
	}
	return "open", nil
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
	return s.ProbeDueOccurrencesContext(context.Background(), now, nil, fetch)
}

func (s *Spool) ProbeDueOccurrencesContext(ctx context.Context, now time.Time, authorize func(Job) error, fetch func(context.Context, string) ([]byte, error)) error {
	return s.processProactive(ctx, "probing", func(o RoutineOccurrence) { s.probeOne(ctx, o, now, authorize, fetch) })
}

// fetchWatch is the live probe: one GET, caches bypassed, body capped at
// 256 KiB — the deterministic poll never touches the model.
func (g *Gateway) fetchWatch(ctx context.Context, url string) ([]byte, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Cache-Control", "no-cache")
	client := g.watchHTTP
	if client == nil {
		client = toolhub.PublicHTTPClient(10 * time.Second)
		defer client.CloseIdleConnections()
	}
	resp, err := client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode/100 != 2 {
		return nil, fmt.Errorf("probe status %d", resp.StatusCode)
	}
	body, err := io.ReadAll(io.LimitReader(resp.Body, 256*1024+1))
	if len(body) > 256*1024 {
		return nil, errors.New("probe body exceeds size limit")
	}
	return body, err
}

func (s *Spool) probeOne(ctx context.Context, o RoutineOccurrence, now time.Time, authorize func(Job) error, fetch func(context.Context, string) ([]byte, error)) {
	if o.RetryAfter.After(now) {
		return // bounded check retry waiting out its backoff (R1.1)
	}
	s.mu.Lock()
	schedule, err := s.loadScheduleLocked(o.ScheduleID)
	current := s.occurrenceCurrentLocked(o) && (authorize == nil || authorize(o.Job) == nil)
	s.mu.Unlock()
	if !current {
		o.State = "cancelled"
		_ = atomicJSON(filepath.Join(s.root, "occurrences", occurrenceID(o)+".json"), o)
		return
	}
	if err != nil || schedule.Probe == nil {
		return
	}
	receipt := filepath.Join(s.root, "occurrences", occurrenceID(o)+".probe-result")
	if body, err := os.ReadFile(receipt); err == nil {
		var result probeResult
		if json.Unmarshal(body, &result) != nil {
			return
		}
		s.mu.Lock()
		_ = s.applyProbeResultLocked(result)
		s.mu.Unlock()
		return
	} else if !os.IsNotExist(err) {
		return
	}
	var body []byte
	var fetchErr error
	if strings.HasPrefix(schedule.Probe.URL, "task:") {
		// Hub-internal predicate: the compared value is the owner's task
		// state, resolved under the schedule owner's principal (R1.3).
		s.mu.Lock()
		state, taskErr := s.taskProbeValueLocked(o.Job.PrincipalID, strings.TrimPrefix(schedule.Probe.URL, "task:"))
		s.mu.Unlock()
		if taskErr != nil {
			s.settleProbe(o, schedule, "", "", now, taskErr, authorize)
			return
		}
		body = []byte(state)
	} else {
		ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
		defer cancel()
		body, fetchErr = fetch(ctx, schedule.Probe.URL)
	}
	if fetchErr != nil {
		s.settleProbe(o, schedule, "", "", now, fetchErr, authorize)
		return
	}
	value, valueErr := probeValue(schedule.Probe, body)
	if valueErr != nil {
		s.settleProbe(o, schedule, "", "", now, valueErr, authorize)
		return
	}
	s.settleProbe(o, schedule, value, watchHash(value), now, nil, authorize)
}

// settleProbe persists the tick outcome: silent baseline seed, no_change,
// changed + one delivery, or failed — and lands the occurrence in a terminal
// state. The watch state write and the occurrence write share the lock.
type probeResult struct {
	Watch      WatchState        `json:"watch"`
	Occurrence RoutineOccurrence `json:"occurrence"`
	Delivery   *Delivery         `json:"delivery,omitempty"`
}

func (s *Spool) applyProbeResultLocked(result probeResult) error {
	o := result.Occurrence
	if !s.occurrenceCurrentLocked(o) {
		o.State = "cancelled"
		return atomicJSON(filepath.Join(s.root, "occurrences", occurrenceID(o)+".json"), o)
	}
	if result.Delivery != nil {
		if err := s.enqueueDeliveryLocked(*result.Delivery); err != nil {
			return err
		}
	}
	live, err := s.loadWatchStateLocked(o.ScheduleID)
	if err != nil {
		return err
	}
	if live.Generation <= result.Watch.Generation {
		if err := atomicJSON(s.watchPath(o.ScheduleID), result.Watch); err != nil {
			return err
		}
	}
	return atomicJSON(filepath.Join(s.root, "occurrences", occurrenceID(o)+".json"), o)
}

func (s *Spool) settleProbe(o RoutineOccurrence, schedule Schedule, value, hash string, now time.Time, probeErr error, authorize func(Job) error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	path := filepath.Join(s.root, "occurrences", occurrenceID(o)+".json")
	body, err := os.ReadFile(path)
	if err != nil {
		return
	}
	var live RoutineOccurrence
	if json.Unmarshal(body, &live) != nil || live.State != "probing" {
		return
	}
	if !s.occurrenceCurrentLocked(live) || (authorize != nil && authorize(live.Job) != nil) {
		live.State = "cancelled"
		_ = atomicJSON(path, live)
		return
	}
	st, err := s.loadWatchStateLocked(live.ScheduleID)
	if err != nil {
		return
	}
	result := probeResult{Watch: st, Occurrence: live}
	if probeErr != nil {
		st.LastError = "probe failed; baseline unchanged"
		limit := schedule.MaxRetries
		if limit <= 0 {
			limit = 3
		}
		if live.JobKind == "check" && live.RetryCount < limit {
			// unknown != false: stay probing with a retry deadline instead
			// of settling failed on the first probe error (R1.1). DueAt is
			// immutable — it keys the occurrence id. The retry writes go
			// straight to the durable files: a probe-result receipt here
			// would replay forever and block the actual retry.
			live.RetryCount++
			live.RetryAfter = now.Add(time.Duration(live.RetryCount) * time.Minute).UTC()
			_ = atomicJSON(s.watchPath(live.ScheduleID), st)
			_ = atomicJSON(path, live)
			return
		}
		live.State = "failed"
	} else {
		st.LastSuccess, st.LastError = now.UTC(), ""
		notify, text := false, live.Job.Text
		if live.JobKind == "check" {
			notify = (st.Baseline != "" && ((hash != st.Baseline && schedule.Expect == "changed") || (hash == st.Baseline && schedule.Expect == "unchanged"))) || (strings.HasPrefix(schedule.Expect, "equals:") && value == strings.TrimPrefix(schedule.Expect, "equals:"))
			live.State = "skipped"
			if notify {
				live.State = "dispatched"
				// The verdict travels with the delivery: the owner sees which
				// predicate fired and the anchored/current values (R1.2).
				text = fmt.Sprintf("%s\ncheck %s met\nwas: %s\nnow: %s", live.Job.Text, schedule.Expect, boundedWatchText(st.Value), boundedWatchText(value))
			}
		} else if st.Baseline == "" {
			st.Baseline, st.Value = hash, value
			live.State = "dispatched"
		} else if hash == st.Baseline {
			live.State = "no_change"
		} else {
			st.PrevValue, st.Value, st.Baseline = st.Value, value, hash
			st.ChangedAt = now.UTC()
			st.Generation++
			text = fmt.Sprintf("%s\n%s\nwas: %s\nnow: %s", live.Job.Text, schedule.Probe.URL, boundedWatchText(st.PrevValue), boundedWatchText(st.Value))
			notify = schedule.NotifyMode != "digest"
			live.State = "dispatched"
		}
		if notify && schedule.DailyLimit > 0 {
			day := now.UTC().Format("2006-01-02")
			if loc, err := time.LoadLocation(schedule.Timezone); err == nil {
				day = now.In(loc).Format("2006-01-02")
			}
			if st.NotifyDay != day {
				st.NotifyDay, st.NotifyCount = day, 0
			}
			if st.NotifyCount >= schedule.DailyLimit {
				// The baseline already advanced above; the owner sees the
				// occurrence as capped rather than a lost notification.
				live.State = "capped"
				notify = false
			} else {
				st.NotifyCount++
			}
		}
		if notify && schedule.NotifyMode == "agent" {
			// Semantic escalation is opt-in per revision: the change evidence
			// reaches the model flagged untrusted, through the same idempotent
			// job admission the agent kind uses (SPEC-0054 R2.3).
			admitted := live.Job
			admitted.Envelope.ConversationID = routineConversation(live.ScheduleID)
			admitted.Text = "[untrusted watcher evidence] " + text
			if _, err := s.enqueueLocked(admitted); err != nil {
				return
			}
		} else if notify {
			prefix := "watch-"
			if live.JobKind == "check" {
				prefix = "check-"
			}
			d, err := normalizeDelivery(Delivery{ID: prefix + live.Job.ID, OccurrenceID: live.Job.ID, Channel: live.Job.Channel, ConversationID: live.Job.ConversationID, DeliveryTargetID: live.Job.DeliveryTargetID, ChatID: live.Job.ChatID, TaskID: live.Job.TaskID, TopicID: live.Job.TopicID, SlackChannel: live.Job.SlackChannel, SlackThread: live.Job.SlackThread, Text: text, NotBefore: quietDefer(schedule, now), CreatedAt: now.UTC()})
			if err != nil {
				return
			}
			result.Delivery = &d
		}
	}
	result.Watch, result.Occurrence = st, live
	receipt := filepath.Join(s.root, "occurrences", occurrenceID(o)+".probe-result")
	if atomicJSON(receipt, result) != nil {
		return
	}
	_ = s.applyProbeResultLocked(result)
}

// quietDefer reports when a delivery may leave the outbox: zero outside the
// owner's quiet window, the next window end inside it. Overnight windows
// (22:00-07:00) wrap. Bad input defers nothing — validation already rejected
// it, and a corrupt record must not silently silence notifications.
func quietDefer(schedule Schedule, now time.Time) time.Time {
	if schedule.QuietFrom == "" || schedule.QuietTo == "" {
		return time.Time{}
	}
	loc, err := time.LoadLocation(schedule.Timezone)
	if err != nil {
		return time.Time{}
	}
	from, err1 := time.Parse("15:04", schedule.QuietFrom)
	to, err2 := time.Parse("15:04", schedule.QuietTo)
	if err1 != nil || err2 != nil {
		return time.Time{}
	}
	local := now.In(loc)
	start := time.Date(local.Year(), local.Month(), local.Day(), from.Hour(), from.Minute(), 0, 0, loc)
	end := time.Date(local.Year(), local.Month(), local.Day(), to.Hour(), to.Minute(), 0, 0, loc)
	if to.Before(from) || to.Equal(from) {
		// Overnight window: the part after midnight belongs to the day that
		// started at `from` yesterday.
		if local.Before(start) {
			start = start.AddDate(0, 0, -1)
		} else {
			end = end.AddDate(0, 0, 1)
		}
	}
	if local.Before(start) || !local.Before(end) {
		return time.Time{}
	}
	return end.UTC()
}

func boundedWatchText(value string) string {
	runes := []rune(value)
	if len(runes) > 1024 {
		return string(runes[:1024]) + "…"
	}
	return value
}

func probeValue(probe *WatchProbe, body []byte) (string, error) {
	if probe == nil {
		return "", errors.New("probe required")
	}
	if probe.Kind == "github_issue" {
		var issue struct {
			State string `json:"state"`
		}
		if json.Unmarshal(body, &issue) != nil || (issue.State != "open" && issue.State != "closed") {
			return "", errors.New("invalid GitHub issue response")
		}
		return issue.State, nil
	}
	var re *regexp.Regexp
	if probe.Regex != "" {
		var err error
		re, err = regexp.Compile(probe.Regex)
		if err != nil {
			return "", err
		}
		if re.NumSubexp() == 0 || re.FindSubmatch(body) == nil {
			return "", errors.New("probe did not match")
		}
	}
	return watchValue(body, re), nil
}
