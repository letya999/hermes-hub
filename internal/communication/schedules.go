package communication

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/letya999/hermes-hub/internal/envstore"
	"github.com/letya999/hermes-hub/internal/identity"
)

const defaultCatchUp = time.Hour

type Schedule struct {
	ScheduleID     string            `json:"schedule_id"`
	Revision       uint64            `json:"revision"`
	Envelope       identity.Envelope `json:"envelope"`
	OrganizationID string            `json:"organization_id"`
	UserID         string            `json:"user_id"`
	ActorID        string            `json:"actor_id"`
	ScopeID        string            `json:"scope_id"`
	Channel        string            `json:"channel"`
	ChatID         int64             `json:"chat_id,omitempty"`
	SlackChannel   string            `json:"slack_channel,omitempty"`
	SlackThread    string            `json:"slack_thread,omitempty"`
	Timezone       string            `json:"timezone"`
	Expression     string            `json:"expression"`
	JobKind        string            `json:"job_kind"`
	Input          string            `json:"input"`
	Enabled        bool              `json:"enabled"`
	Paused         bool              `json:"paused"`
	NextDue        time.Time         `json:"next_due"`
	CatchUpWindow  time.Duration     `json:"catch_up_window_ns"`
	NativeCronRef  string            `json:"native_cron_ref,omitempty"`
	CreatedAt      time.Time         `json:"created_at"`
	UpdatedAt      time.Time         `json:"updated_at"`
}

func (s *Spool) CreateSchedule(in Schedule, caller identity.Envelope, nativeCron string) (Schedule, error) {
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
	now := time.Now().UTC()
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
	s.mu.Lock()
	defer s.mu.Unlock()
	path := s.schedulePath(in.ScheduleID)
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
	return os.Remove(s.schedulePath(id))
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
		if !item.Enabled || item.Paused || item.NextDue.IsZero() || now.Before(item.NextDue) {
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
		job := Job{Envelope: item.Envelope, OrganizationID: item.OrganizationID, UserID: item.UserID, ActorID: item.ActorID, ScopeID: item.ScopeID, Channel: item.Channel, ChatID: item.ChatID, SlackChannel: item.SlackChannel, SlackThread: item.SlackThread, Text: item.Input, Trigger: "cron"}
		occ := RoutineOccurrence{ScheduleID: item.ScheduleID, Revision: item.Revision, DueAt: due, Job: job}
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
	next, err := nextExpression(item.Expression, loc, due.Add(time.Minute))
	if err != nil {
		return err
	}
	item.NextDue = next
	// Catch-up is at most one missed occurrence: skip any extra due times in-window.
	for !item.NextDue.After(now) {
		skipped, skipErr := nextExpression(item.Expression, loc, item.NextDue.Add(time.Minute))
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

func validateScheduleDraft(in Schedule, caller identity.Envelope) error {
	if len(in.ScheduleID) > 80 || !spoolIDPattern.MatchString(in.ScheduleID) || caller != in.Envelope || caller.Validate(in.UserID, in.Envelope.ContextID, in.Envelope.RuntimeID, in.Envelope.PolicyVersion) != nil {
		return errors.New("invalid schedule ownership")
	}
	if in.JobKind != "" && in.JobKind != "agent" && in.JobKind != "script" {
		return errors.New("invalid job kind")
	}
	if strings.TrimSpace(in.Input) == "" || len(in.Input) > 64*1024 || envstore.LooksLikeEnv(in.Input) {
		return errors.New("invalid routine input")
	}
	if in.Channel != "telegram_bot" && in.Channel != "slack_app" {
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
		if minute[t.Minute()] && hour[t.Hour()] && month[int(t.Month())] && (dom[t.Day()] || fields[2] == "*") && (dow[int(t.Weekday())] || fields[4] == "*") {
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
