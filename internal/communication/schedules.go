package communication

import (
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"time"
	"unicode"

	"github.com/letya999/hermes-hub/internal/envstore"
	"github.com/letya999/hermes-hub/internal/identity"
)

const defaultCatchUp = time.Hour

// WatchProbe is a bounded deterministic probe: one http(s) GET; the compared
// value is the first capture group of Regex, or the whole bounded body.
type WatchProbe struct {
	Kind  string `json:"kind,omitempty"`
	URL   string `json:"url"`
	Regex string `json:"regex,omitempty"`
	// Tool names one projected read ToolHub tool when kind=mcp_tool; Args is
	// its verbatim argument object (bounded JSON). RequeueTool reserves the
	// event-subscription requeue hook for stage 5.
	Tool        string         `json:"tool,omitempty"`
	Args        map[string]any `json:"args,omitempty"`
	RequeueTool string         `json:"requeue_tool,omitempty"`
}

type Schedule struct {
	Creation       *ScheduleCreation `json:"creation,omitempty"`
	ScheduleID     string            `json:"schedule_id"`
	Revision       uint64            `json:"revision"`
	Envelope       identity.Envelope `json:"envelope"`
	OrganizationID string            `json:"organization_id"`
	UserID         string            `json:"user_id"`
	ActorID        string            `json:"actor_id"`
	ScopeID        string            `json:"scope_id"`
	Channel        string            `json:"channel"`
	ChatID         int64             `json:"chat_id,omitempty"`
	// TaskID/TopicID snapshot the task the routine was created in: occurrence
	// jobs rebind their conversation and deliver back to the same topic.
	TaskID       string `json:"task_id,omitempty"`
	TopicID      int64  `json:"topic_id,omitempty"`
	SlackChannel string `json:"slack_channel,omitempty"`
	SlackThread  string `json:"slack_thread,omitempty"`
	Timezone     string `json:"timezone"`
	Expression   string `json:"expression"`
	JobKind      string `json:"job_kind"`
	// Probe is the deterministic source spec for watcher/check schedules:
	// an http(s) GET plus an optional regex whose first capture group is the
	// compared value. It never carries credentials.
	Probe             *WatchProbe `json:"probe,omitempty"`
	Expect            string      `json:"expect,omitempty"`
	InitialProbeValue *string     `json:"initial_probe_value,omitempty"`
	NotifyMode        string      `json:"notify_mode,omitempty"`
	SourceIDs         []string    `json:"source_ids,omitempty"`
	SnoozedUntil      time.Time   `json:"snoozed_until,omitempty"`
	Input             string      `json:"input"`
	Enabled           bool        `json:"enabled"`
	Paused            bool        `json:"paused"`
	NextDue           time.Time   `json:"next_due"`
	// LastFailureSHA256 dedupes owner-facing error deliveries: a routine that
	// fails every tick with the same normalized error notifies once, and a
	// success clears it so the next failure notifies again.
	LastFailureSHA256 string        `json:"last_failure_sha256,omitempty"`
	CatchUpWindow     time.Duration `json:"catch_up_window_ns"`
	// MaxRetries bounds probe-error retries for check occurrences (SPEC-0054
	// R1.1): 0 means the default 3, values above 8 are rejected.
	MaxRetries int `json:"max_retries,omitempty"`
	// QuietFrom/QuietTo bound an owner-timezone HH:MM window in which
	// owner-facing deliveries are deferred to the window end (SPEC-0054 R2.1);
	// both-or-neither, overnight wraps.
	QuietFrom string `json:"quiet_from,omitempty"`
	QuietTo   string `json:"quiet_to,omitempty"`
	// DailyLimit caps owner-facing notifications per owner-timezone day
	// (SPEC-0054 R2.2): 0 is unlimited, 32 the bound.
	DailyLimit int `json:"daily_limit,omitempty"`
	// Sink routes event-subscription output: "notify" fans events out to the
	// outbox (default), "agent" bundles them into one untrusted-evidence job.
	Sink          string    `json:"sink,omitempty"`
	NativeCronRef string    `json:"native_cron_ref,omitempty"`
	CreatedAt     time.Time `json:"created_at"`
	UpdatedAt     time.Time `json:"updated_at"`
}

type ScheduleCreation struct {
	RequestedExpression string    `json:"requested_expression"`
	ResolvedDueAt       time.Time `json:"resolved_due_at"`
	CreatedAt           time.Time `json:"created_at"`
	IntentSHA256        string    `json:"intent_sha256"`
}

var ErrScheduleConflict = errors.New("schedule id conflicts with an existing or retired intent")
var errScheduleExists = errors.New("schedule already exists or is unreadable")

func scheduleIntent(in Schedule) string {
	in.Creation = nil
	in.Revision, in.Enabled, in.Paused = 0, false, false
	in.NextDue, in.CreatedAt, in.UpdatedAt, in.SnoozedUntil = time.Time{}, time.Time{}, time.Time{}, time.Time{}
	in.InitialProbeValue, in.NativeCronRef, in.LastFailureSHA256 = nil, "", ""
	if in.JobKind == "" {
		in.JobKind = "agent"
	}
	if in.CatchUpWindow <= 0 {
		in.CatchUpWindow = defaultCatchUp
	}
	b, _ := json.Marshal(in)
	return fmt.Sprintf("%x", sha256.Sum256(b))
}

// A replay returns current state, never the enabled state at creation.
// Retired IDs are never replayed or reactivated; their tombstones conflict.
// Foreign-owner collisions and unreadable or legacy records fail closed
// without exposing a receipt or the conflict kind.
func (s *Spool) creationReplayLocked(in Schedule, caller identity.Envelope) (Schedule, bool, error) {
	item, err := s.loadScheduleLocked(in.ScheduleID)
	retired := false
	if os.IsNotExist(err) {
		b, readErr := os.ReadFile(s.cancelPath(in.ScheduleID))
		if os.IsNotExist(readErr) {
			return Schedule{}, false, nil
		}
		if readErr != nil || json.Unmarshal(b, &item) != nil {
			return Schedule{}, true, errScheduleExists
		}
		retired = true
	} else if err != nil {
		return Schedule{}, true, errScheduleExists
	}
	if item.Envelope.PrincipalID != caller.PrincipalID || item.Envelope.ContextID != caller.ContextID {
		return Schedule{}, true, errScheduleExists
	}
	if retired || item.Creation == nil || item.Creation.IntentSHA256 != scheduleIntent(in) {
		return Schedule{}, true, ErrScheduleConflict
	}
	return item, true, nil
}

func (s *Spool) CreateSchedule(in Schedule, caller identity.Envelope, nativeCron string) (Schedule, error) {
	return s.createSchedule(in, caller, nativeCron, nil)
}

// ValidateSchedule checks a complete draft without mutating the spool.
func ValidateSchedule(in Schedule, caller identity.Envelope, now time.Time) error {
	in = normalizeScheduleDraft(in)
	var err error
	in, err = resolveRelativeReminder(in, now)
	if err != nil {
		return err
	}
	if err := validateScheduleDraft(in, caller); err != nil {
		return err
	}
	loc, err := time.LoadLocation(in.Timezone)
	if err != nil {
		return errors.New("invalid timezone")
	}
	_, err = nextExpression(in.Expression, loc, now)
	return err
}

func (s *Spool) createSchedule(in Schedule, caller identity.Envelope, nativeCron string, initialValue *string) (Schedule, error) {
	return s.createScheduleAt(in, caller, nativeCron, initialValue, time.Now().UTC())
}

func (s *Spool) createScheduleAt(in Schedule, caller identity.Envelope, nativeCron string, initialValue *string, now time.Time) (Schedule, error) {
	in = normalizeScheduleDraft(in)
	if err := validateScheduleDraft(in, caller); err != nil {
		return Schedule{}, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if item, found, err := s.creationReplayLocked(in, caller); found || err != nil {
		return item, err
	}
	requested, intent := in.Expression, scheduleIntent(in)
	var err error
	in, err = resolveRelativeReminder(in, now)
	if err != nil {
		return Schedule{}, err
	}
	in.InitialProbeValue = initialValue
	if nativeCron == "unmigrated" {
		return Schedule{}, errors.New("native Hermes cron is still the clock for this context")
	}
	if err := validateScheduleDraft(in, caller); err != nil {
		return Schedule{}, err
	}
	loc, err := time.LoadLocation(in.Timezone)
	if err != nil {
		return Schedule{}, errors.New("invalid timezone")
	}
	next, err := nextExpression(in.Expression, loc, now)
	if err != nil {
		return Schedule{}, err
	}
	if in.CatchUpWindow <= 0 {
		in.CatchUpWindow = defaultCatchUp
	}
	if in.JobKind == "" {
		in.JobKind = "agent"
	}
	in.Revision = 1
	in.Enabled = true
	in.Paused = false
	in.NextDue = next
	in.CreatedAt = now
	in.UpdatedAt = now
	in.Creation = &ScheduleCreation{RequestedExpression: requested, ResolvedDueAt: next, CreatedAt: now, IntentSHA256: intent}
	path := s.schedulePath(in.ScheduleID)
	if _, err := os.Stat(s.cancelPath(in.ScheduleID)); !os.IsNotExist(err) {
		return Schedule{}, errors.New("schedule id was retired; use a new id")
	}
	if _, err := os.Stat(path); err == nil {
		return Schedule{}, errors.New("schedule already exists")
	}
	if err := atomicJSON(path, in); err != nil {
		return Schedule{}, err
	}
	return in, nil
}

func (s *Spool) ListSchedules(caller identity.Envelope) ([]Schedule, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	entries, err := os.ReadDir(filepath.Join(s.root, "schedules"))
	if err != nil {
		return nil, err
	}
	out := []Schedule{}
	for _, entry := range entries {
		if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".json") {
			continue
		}
		b, err := os.ReadFile(filepath.Join(s.root, "schedules", entry.Name()))
		if err != nil {
			return nil, err
		}
		var item Schedule
		if json.Unmarshal(b, &item) != nil {
			return nil, errors.New("invalid schedule")
		}
		if item.Envelope.PrincipalID != caller.PrincipalID || item.Envelope.ContextID != caller.ContextID {
			continue
		}
		out = append(out, item)
	}
	return out, nil
}

func (s *Spool) UpdateSchedule(id string, caller identity.Envelope, expression, input string, enabled *bool) (Schedule, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	item, err := s.loadScheduleLocked(id)
	if err != nil {
		return Schedule{}, err
	}
	if item.Envelope.PrincipalID != caller.PrincipalID || item.Envelope.ContextID != caller.ContextID {
		return Schedule{}, errors.New("schedule not owned")
	}
	if expression != "" {
		item.Expression = expression
	}
	if input != "" {
		item.Input = input
	}
	if enabled != nil {
		item.Enabled = *enabled
		if !*enabled {
			item.Paused = true
		} else {
			item.Paused = false
		}
	}
	if err := validateScheduleDraft(item, caller); err != nil {
		return Schedule{}, err
	}
	loc, err := time.LoadLocation(item.Timezone)
	if err != nil {
		return Schedule{}, err
	}
	next, err := nextExpression(item.Expression, loc, time.Now().UTC())
	if err != nil {
		return Schedule{}, err
	}
	item.Revision++
	item.NextDue = next
	item.UpdatedAt = time.Now().UTC()
	if err := atomicJSON(s.schedulePath(id), item); err != nil {
		return Schedule{}, err
	}
	return item, nil
}

func (s *Spool) PauseSchedule(id string, caller identity.Envelope) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	item, err := s.loadScheduleLocked(id)
	if err != nil {
		return err
	}
	if item.Envelope.PrincipalID != caller.PrincipalID || item.Envelope.ContextID != caller.ContextID {
		return errors.New("schedule not owned")
	}
	item.Paused = true
	item.Revision++
	item.UpdatedAt = time.Now().UTC()
	return atomicJSON(s.schedulePath(id), item)
}

func (s *Spool) DeleteSchedule(id string, caller identity.Envelope) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	item, err := s.loadScheduleLocked(id)
	if err != nil {
		return err
	}
	if item.Envelope.PrincipalID != caller.PrincipalID || item.Envelope.ContextID != caller.ContextID {
		return errors.New("schedule not owned")
	}
	item.Enabled, item.Paused = false, true
	item.NextDue = time.Time{}
	item.UpdatedAt = time.Now().UTC()
	if err := atomicJSON(s.cancelPath(id), item); err != nil {
		return err
	}
	return os.Remove(s.schedulePath(id))
}

func (s *Spool) SnoozeSchedule(id string, caller identity.Envelope, until time.Time) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	item, err := s.loadScheduleLocked(id)
	if err != nil {
		return err
	}
	if item.Envelope.PrincipalID != caller.PrincipalID || item.Envelope.ContextID != caller.ContextID {
		return errors.New("schedule not owned")
	}
	if !until.After(time.Now().UTC()) {
		return errors.New("snooze must be in the future")
	}
	item.SnoozedUntil = until.UTC()
	item.Revision++
	item.UpdatedAt = time.Now().UTC()
	return atomicJSON(s.schedulePath(id), item)
}

func (s *Spool) cancelPath(id string) string {
	return filepath.Join(s.root, "schedules", spoolFileID(id)+".deleted")
}

// RecordRoutineFailure stores the schedule's last failure digest and reports
// whether this failure should notify the owner: only a digest that differs
// from the stored one (or follows a success) is fresh. Schedules without a
// durable record (externally owned clocks) cannot dedupe and always notify.
func (s *Spool) RecordRoutineFailure(scheduleID, digest string) (bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	item, err := s.loadScheduleLocked(scheduleID)
	if os.IsNotExist(err) {
		return true, nil
	}
	if err != nil {
		return true, err
	}
	if digest != "" && item.LastFailureSHA256 == digest {
		return false, nil
	}
	item.LastFailureSHA256 = digest
	item.UpdatedAt = time.Now().UTC()
	return true, atomicJSON(s.schedulePath(scheduleID), item)
}

// ClearRoutineFailure drops the stored failure digest after a success so the
// next failure notifies again. Externally owned schedules are a no-op.
func (s *Spool) ClearRoutineFailure(scheduleID string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	item, err := s.loadScheduleLocked(scheduleID)
	if os.IsNotExist(err) {
		return nil
	}
	if err != nil {
		return err
	}
	if item.LastFailureSHA256 == "" {
		return nil
	}
	item.LastFailureSHA256 = ""
	item.UpdatedAt = time.Now().UTC()
	return atomicJSON(s.schedulePath(scheduleID), item)
}

func (s *Spool) occurrenceCurrentLocked(o RoutineOccurrence) bool {
	if _, err := os.Stat(s.cancelPath(o.ScheduleID)); !os.IsNotExist(err) {
		return false
	}
	schedule, err := s.loadScheduleLocked(o.ScheduleID)
	if os.IsNotExist(err) {
		return true // PutOccurrence also accepts externally owned recurrence clocks.
	}
	return err == nil && schedule.Revision == o.Revision && schedule.Envelope == o.Job.Envelope
}

func (s *Spool) TickSchedules(now time.Time, nativeCron string, authorize func(Job) error) error {
	if nativeCron == "unmigrated" {
		return nil
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	entries, err := os.ReadDir(filepath.Join(s.root, "schedules"))
	if err != nil {
		return err
	}
	for _, entry := range entries {
		if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".json") {
			continue
		}
		item, err := s.loadScheduleLocked(strings.TrimSuffix(entry.Name(), ".json"))
		if err != nil {
			return err
		}
		if now.Before(item.SnoozedUntil) || !item.Enabled || item.Paused || item.NextDue.IsZero() || now.Before(item.NextDue) {
			continue
		}
		loc, err := time.LoadLocation(item.Timezone)
		if err != nil {
			continue
		}
		due := item.NextDue
		window := item.CatchUpWindow
		if window <= 0 {
			window = defaultCatchUp
		}
		if now.Sub(due) > window {
			if err := s.settleScheduleLocked(&item, loc, due, now); err != nil {
				return err
			}
			continue
		}
		job := Job{Envelope: item.Envelope, OrganizationID: item.OrganizationID, UserID: item.UserID, ActorID: item.ActorID, ScopeID: item.ScopeID, Channel: item.Channel, ChatID: item.ChatID, TaskID: item.TaskID, TopicID: item.TopicID, SlackChannel: item.SlackChannel, SlackThread: item.SlackThread, Text: item.Input, Trigger: "cron"}
		occ := RoutineOccurrence{ScheduleID: item.ScheduleID, Revision: item.Revision, DueAt: due, JobKind: item.JobKind, Job: job}
		if err := validateOccurrence(occ, item.Envelope); err != nil {
			continue
		}
		if authorize != nil && authorize(occ.Job) != nil {
			continue
		}
		occ.DueAt = due.UTC()
		occ.State = "pending"
		occ.Job.ID = occurrenceID(occ)
		occ.Job.IdempotencyKey = occ.Job.ID
		occ.Job.Trigger = "cron"
		occ.Job.CreatedAt = due.UTC()
		occPath := filepath.Join(s.root, "occurrences", occ.Job.ID+".json")
		if _, err := os.Stat(occPath); err == nil {
			if err := s.settleScheduleLocked(&item, loc, due, now); err != nil {
				return err
			}
			continue
		}
		if err := atomicJSON(occPath, occ); err != nil {
			return err
		}
		if err := s.settleScheduleLocked(&item, loc, due, now); err != nil {
			return err
		}
	}
	return nil
}

func (s *Spool) settleScheduleLocked(item *Schedule, loc *time.Location, due, now time.Time) error {
	if strings.HasPrefix(strings.TrimSpace(item.Expression), "once:") {
		item.Enabled = false
		item.Paused = true
		item.NextDue = time.Time{}
		item.UpdatedAt = now
		return atomicJSON(s.schedulePath(item.ScheduleID), item)
	}
	next, err := nextExpression(item.Expression, loc, due)
	if err != nil {
		return err
	}
	item.NextDue = next
	// Catch-up is at most one missed occurrence: skip any extra due times in-window.
	for !item.NextDue.After(now) {
		skipped, skipErr := nextExpression(item.Expression, loc, item.NextDue)
		if skipErr != nil || !skipped.After(item.NextDue) {
			break
		}
		item.NextDue = skipped
	}
	item.UpdatedAt = now
	return atomicJSON(s.schedulePath(item.ScheduleID), item)
}

func (s *Spool) schedulePath(id string) string {
	return filepath.Join(s.root, "schedules", spoolFileID(id)+".json")
}

func (s *Spool) loadScheduleLocked(id string) (Schedule, error) {
	b, err := os.ReadFile(s.schedulePath(id))
	if err != nil {
		return Schedule{}, err
	}
	var item Schedule
	if json.Unmarshal(b, &item) != nil || item.ScheduleID == "" {
		return Schedule{}, errors.New("invalid schedule")
	}
	return item, nil
}

func resolveRelativeReminder(in Schedule, now time.Time) (Schedule, error) {
	if !strings.HasPrefix(in.Expression, "after:") {
		return in, nil
	}
	delay, err := time.ParseDuration(strings.TrimPrefix(in.Expression, "after:"))
	if in.JobKind != "reminder" || err != nil || delay < time.Second || delay > 8760*time.Hour || delay%time.Second != 0 {
		return Schedule{}, errors.New("after: requires a reminder and an integral delay from 1s to 8760h")
	}
	in.Expression = "once:" + now.Add(delay).UTC().Format(time.RFC3339Nano)
	return in, nil
}

var probeToolPattern = regexp.MustCompile(`^[a-zA-Z0-9][a-zA-Z0-9._-]{0,127}$`)

// validMCPProbe bounds a tool-call probe (SPEC-0054 R3.2): a named projected
// tool and no URL — credentials stay inside the ToolHub workload so the
// probe never carries a source address.
func validMCPProbe(probe *WatchProbe) error {
	if probe == nil || !probeToolPattern.MatchString(probe.Tool) {
		return errors.New("mcp_tool probe needs a projected tool name")
	}
	if probe.URL != "" {
		return errors.New("mcp_tool probe takes no url")
	}
	if probe.RequeueTool != "" && !probeToolPattern.MatchString(probe.RequeueTool) {
		return errors.New("invalid requeue tool name")
	}
	if probe.Args != nil {
		b, err := json.Marshal(probe.Args)
		if err != nil || len(b) > 8*1024 {
			return errors.New("probe args exceed bounds")
		}
	}
	return nil
}

// validTaskProbe reports whether url is a hub-internal task predicate
// (task:<id>) — a verified source the probe resolves without any fetch
// (SPEC-0054 R1.3).
func validTaskProbe(url string) bool {
	id := strings.TrimPrefix(url, "task:")
	return id != url && id != "" && len(id) <= 80 && spoolIDPattern.MatchString(id)
}

func normalizeScheduleDraft(in Schedule) Schedule {
	if in.Probe != nil {
		probe := *in.Probe
		probe.URL = strings.TrimSpace(probe.URL)
		in.Probe = &probe
	}
	return in
}

func validateScheduleDraft(in Schedule, caller identity.Envelope) error {
	if in.Probe != nil && strings.ContainsFunc(in.Probe.URL, func(r rune) bool { return unicode.IsControl(r) || unicode.Is(unicode.Cf, r) }) {
		return errors.New("probe URL contains invisible or control characters")
	}
	if len(in.ScheduleID) > 80 || !spoolIDPattern.MatchString(in.ScheduleID) || caller != in.Envelope || caller.Validate(in.UserID, in.Envelope.ContextID, in.Envelope.RuntimeID, in.Envelope.PolicyVersion) != nil {
		return errors.New("invalid schedule ownership")
	}
	if in.JobKind != "" && in.JobKind != "agent" && in.JobKind != "script" && in.JobKind != "reminder" && in.JobKind != "watcher" && in.JobKind != "check" && in.JobKind != "digest" && in.JobKind != "brief" && in.JobKind != "event" {
		return errors.New("invalid job kind")
	}
	if in.JobKind == "check" && in.Expect != "changed" && in.Expect != "unchanged" && (!strings.HasPrefix(in.Expect, "equals:") || len(in.Expect) <= len("equals:") || len(in.Expect) > 1024) {
		return errors.New("invalid check expect")
	}
	if in.JobKind == "brief" && in.Probe != nil {
		return errors.New("a brief aggregates sources and carries no probe")
	}
	if in.MaxRetries != 0 && (in.JobKind != "check" || in.MaxRetries < 0 || in.MaxRetries > 8) {
		return errors.New("max_retries is a check bound between 0 and 8")
	}
	if in.JobKind == "check" {
		if in.Probe == nil {
			return errors.New("check needs a probe")
		}
		if in.Probe.Kind == "mcp_tool" {
			if err := validMCPProbe(in.Probe); err != nil {
				return err
			}
		} else if strings.HasPrefix(in.Probe.URL, "task:") {
			if !validTaskProbe(in.Probe.URL) {
				return errors.New("invalid task probe")
			}
		} else {
			u, err := url.Parse(in.Probe.URL)
			if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" || u.User != nil {
				return errors.New("check probe must be http(s)")
			}
			if in.Probe.Regex != "" {
				if _, err := regexp.Compile(in.Probe.Regex); err != nil {
					return errors.New("check probe regex does not compile")
				}
			}
		}
	}
	if in.JobKind == "watcher" || in.JobKind == "event" {
		if in.Probe == nil {
			return errors.New("watcher needs a probe")
		}
		if in.Probe.Kind == "mcp_tool" {
			if err := validMCPProbe(in.Probe); err != nil {
				return err
			}
		} else {
			u, err := url.Parse(in.Probe.URL)
			if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" || u.User != nil {
				return errors.New("watch probe must be a plain http(s) url")
			}
			if in.Probe.Regex != "" {
				if _, err := regexp.Compile(in.Probe.Regex); err != nil {
					return errors.New("watch probe regex does not compile")
				}
			}
		}
	}
	if len(in.SourceIDs) > 128 || (len(in.SourceIDs) > 0 && in.JobKind != "digest" && in.JobKind != "brief") {
		return errors.New("source_ids require a digest or brief and at most 128 sources")
	}
	seenSources := map[string]bool{}
	for _, id := range in.SourceIDs {
		if !spoolIDPattern.MatchString(id) || len(id) > 80 || seenSources[id] || id == in.ScheduleID {
			return errors.New("invalid digest source_ids")
		}
		seenSources[id] = true
	}
	if in.NotifyMode != "" && in.NotifyMode != "immediate" && in.NotifyMode != "digest" && in.NotifyMode != "agent" {
		return errors.New("invalid notify mode")
	}
	if in.NotifyMode == "agent" && in.JobKind != "watcher" && in.JobKind != "check" {
		return errors.New("agent escalation requires a watcher or check")
	}
	if (in.QuietFrom == "") != (in.QuietTo == "") {
		return errors.New("quiet window needs both bounds")
	}
	if in.QuietFrom != "" {
		from, err1 := time.Parse("15:04", in.QuietFrom)
		to, err2 := time.Parse("15:04", in.QuietTo)
		if err1 != nil || err2 != nil || from.Equal(to) {
			return errors.New("invalid quiet window")
		}
	}
	if in.DailyLimit < 0 || in.DailyLimit > 32 {
		return errors.New("daily_limit must be 0..32")
	}
	if in.Sink != "" && in.Sink != "notify" && in.Sink != "agent" {
		return errors.New("invalid event sink")
	}
	if in.Sink != "" && in.JobKind != "event" {
		return errors.New("sink requires an event schedule")
	}
	if in.Probe != nil {
		if len(in.Probe.URL) > 2048 {
			return errors.New("probe URL exceeds limit")
		}
		if in.Probe.Kind != "" && in.Probe.Kind != "http" && in.Probe.Kind != "github_issue" && in.Probe.Kind != "mcp_tool" {
			return errors.New("invalid probe kind")
		}
		if in.Probe.Regex != "" {
			re, err := regexp.Compile(in.Probe.Regex)
			if err != nil || re.NumSubexp() == 0 {
				return errors.New("probe regex needs a capture group")
			}
		}
		if in.Probe.Kind == "github_issue" {
			u, err := url.Parse(in.Probe.URL)
			if err != nil || u.Scheme != "https" || u.Host != "api.github.com" || u.User != nil || u.RawQuery != "" || u.Fragment != "" || in.Probe.Regex != "" || !regexp.MustCompile(`^/repos/[A-Za-z0-9_.-]+/[A-Za-z0-9_.-]+/issues/[1-9][0-9]*$`).MatchString(u.Path) {
				return errors.New("GitHub issue probe needs its public REST issue URL")
			}
		}
	}
	if in.JobKind == "script" && ScriptSyntaxError(in.Input) != nil {
		return errors.New("script does not compile")
	}
	if strings.TrimSpace(in.Input) == "" || len(in.Input) > 64*1024 || envstore.LooksLikeEnv(in.Input) {
		return errors.New("invalid routine input")
	}
	if in.Channel != "telegram_bot" && in.Channel != "slack_app" && in.Channel != "mock" {
		return errors.New("invalid schedule channel")
	}
	if in.Channel == "slack_app" && in.SlackChannel != "" && (!validSlackIMChannel(in.SlackChannel) || !validSlackThread(in.SlackThread)) {
		return errors.New("invalid schedule channel")
	}
	if in.Timezone == "" {
		return errors.New("timezone required")
	}
	return nil
}

func nextExpression(expr string, loc *time.Location, from time.Time) (time.Time, error) {
	expr = strings.TrimSpace(expr)
	if strings.HasPrefix(expr, "once:") {
		due, err := time.Parse(time.RFC3339, strings.TrimPrefix(expr, "once:"))
		if err != nil || !due.After(from) {
			return time.Time{}, errors.New("invalid once expression")
		}
		return due.UTC(), nil
	}
	return nextCron(expr, loc, from)
}

func nextCron(expr string, loc *time.Location, from time.Time) (time.Time, error) {
	fields := strings.Fields(expr)
	if len(fields) != 5 {
		return time.Time{}, errors.New("cron expression must have 5 fields")
	}
	minute, err1 := parseCronField(fields[0], 0, 59)
	hour, err2 := parseCronField(fields[1], 0, 23)
	dom, err3 := parseCronField(fields[2], 1, 31)
	month, err4 := parseCronField(fields[3], 1, 12)
	dow, err5 := parseCronField(fields[4], 0, 6)
	if err1 != nil || err2 != nil || err3 != nil || err4 != nil || err5 != nil {
		return time.Time{}, errors.New("invalid cron field")
	}
	t := from.In(loc).Truncate(time.Minute).Add(time.Minute)
	limit := t.Add(400 * 24 * time.Hour)
	for !t.After(limit) {
		if minute[t.Minute()] && hour[t.Hour()] && month[int(t.Month())] {
			if fields[2] != "*" && fields[4] != "*" {
				if dom[t.Day()] || dow[int(t.Weekday())] {
					return t.UTC(), nil
				}
			} else if (fields[2] == "*" || dom[t.Day()]) && (fields[4] == "*" || dow[int(t.Weekday())]) {
				return t.UTC(), nil
			}
		}
		t = t.Add(time.Minute)
	}
	return time.Time{}, errors.New("no upcoming cron occurrence")
}

func parseCronField(field string, min, max int) ([]bool, error) {
	ok := make([]bool, max+1)
	if field == "*" {
		for i := min; i <= max; i++ {
			ok[i] = true
		}
		return ok, nil
	}
	for _, part := range strings.Split(field, ",") {
		step := 1
		rangePart := part
		if strings.Contains(part, "/") {
			bits := strings.SplitN(part, "/", 2)
			rangePart = bits[0]
			n, err := strconv.Atoi(bits[1])
			if err != nil || n <= 0 {
				return nil, errors.New("invalid step")
			}
			step = n
		}
		start, end := min, max
		if rangePart != "*" {
			if strings.Contains(rangePart, "-") {
				bits := strings.SplitN(rangePart, "-", 2)
				a, err1 := strconv.Atoi(bits[0])
				b, err2 := strconv.Atoi(bits[1])
				if err1 != nil || err2 != nil || a < min || b > max || a > b {
					return nil, errors.New("invalid range")
				}
				start, end = a, b
			} else {
				a, err := strconv.Atoi(rangePart)
				if err != nil || a < min || a > max {
					return nil, errors.New("invalid value")
				}
				start, end = a, a
			}
		}
		for i := start; i <= end; i += step {
			ok[i] = true
		}
	}
	return ok, nil
}
