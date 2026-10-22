package routines

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/letya999/hermes-hub/internal/commodel"
	"github.com/letya999/hermes-hub/internal/identity"
)

var ErrScheduleConflict = errors.New("schedule id conflicts with an existing or retired intent")
var errScheduleExists = errors.New("schedule already exists or is unreadable")

// ErrRoutineRevision rejects a mutation whose expected revision is stale —
// the caller re-reads current state before retrying.
var ErrRoutineRevision = errors.New("routine revision changed; read current state before retrying")

// ErrRoutineSnoozeNotLater rejects a snooze that does not push the next run
// further out.
var ErrRoutineSnoozeNotLater = errors.New("snooze must be later than the next run")

// EffectiveNextDue is the soonest instant a schedule may fire: its next slot
// pushed past an active snooze window.
func EffectiveNextDue(item commodel.Schedule) time.Time {
	if !item.NextDue.IsZero() && item.NextDue.Before(item.SnoozedUntil) {
		return item.SnoozedUntil
	}
	return item.NextDue
}

// A replay returns current state, never the enabled state at creation.
// Retired IDs are never replayed or reactivated; their tombstones conflict.
// Foreign-owner collisions and unreadable or legacy records fail closed
// without exposing a receipt or the conflict kind.
func (e *Engine) CreationReplayLocked(in commodel.Schedule, caller identity.Envelope) (commodel.Schedule, bool, error) {
	item, err := e.LoadScheduleLocked(in.ScheduleID)
	retired := false
	if os.IsNotExist(err) {
		b, readErr := os.ReadFile(e.CancelPath(in.ScheduleID))
		if os.IsNotExist(readErr) {
			return commodel.Schedule{}, false, nil
		}
		if readErr != nil || json.Unmarshal(b, &item) != nil {
			return commodel.Schedule{}, true, errScheduleExists
		}
		retired = true
	} else if err != nil {
		return commodel.Schedule{}, true, errScheduleExists
	}
	if item.Envelope.PrincipalID != caller.PrincipalID || item.Envelope.ContextID != caller.ContextID {
		return commodel.Schedule{}, true, errScheduleExists
	}
	if retired || item.Creation == nil || item.Creation.IntentSHA256 != ScheduleIntent(in) {
		return commodel.Schedule{}, true, ErrScheduleConflict
	}
	return item, true, nil
}

func (e *Engine) CreateSchedule(in commodel.Schedule, caller identity.Envelope, nativeCron string) (commodel.Schedule, error) {
	return e.createSchedule(in, caller, nativeCron, nil)
}

func (e *Engine) createSchedule(in commodel.Schedule, caller identity.Envelope, nativeCron string, initialValue *string) (commodel.Schedule, error) {
	return e.CreateScheduleAt(in, caller, nativeCron, initialValue, time.Now().UTC())
}

func (e *Engine) CreateScheduleAt(in commodel.Schedule, caller identity.Envelope, nativeCron string, initialValue *string, now time.Time, routingUser ...commodel.User) (commodel.Schedule, error) {
	in = NormalizeScheduleDraft(in)
	if err := ValidateScheduleDraft(in, caller, e.deps.Hooks); err != nil {
		return commodel.Schedule{}, err
	}
	e.mu.Lock()
	defer e.mu.Unlock()
	if len(routingUser) > 0 && (in.OriginJobID != "" || in.DestinationTaskID != "") {
		var err error
		in, err = e.BindRoutineDestinationLocked(in, caller, routingUser[0])
		if err != nil {
			return commodel.Schedule{}, err
		}
		caller = in.Envelope
	}
	in.OriginJobID, in.DestinationTaskID = "", ""
	if item, found, err := e.CreationReplayLocked(in, caller); found || err != nil {
		return item, err
	}
	requested, intent := in.Expression, ScheduleIntent(in)
	var err error
	in, err = ResolveRelativeReminder(in, now)
	if err != nil {
		return commodel.Schedule{}, err
	}
	in.InitialProbeValue = initialValue
	if nativeCron == "unmigrated" {
		return commodel.Schedule{}, errors.New("native Hermes cron is still the clock for this context")
	}
	if err := ValidateScheduleDraft(in, caller, e.deps.Hooks); err != nil {
		return commodel.Schedule{}, err
	}
	loc, err := time.LoadLocation(in.Timezone)
	if err != nil {
		return commodel.Schedule{}, errors.New("invalid timezone")
	}
	if strings.HasPrefix(strings.TrimSpace(in.Expression), "every:") && in.IntervalAnchor.IsZero() {
		in.IntervalAnchor = now
	}
	next, err := NextSchedule(in, loc, now)
	if err != nil {
		return commodel.Schedule{}, err
	}
	if in.CatchUpWindow <= 0 {
		in.CatchUpWindow = DefaultCatchUp
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
	in.Creation = &commodel.ScheduleCreation{RequestedExpression: requested, ResolvedDueAt: next, CreatedAt: now, IntentSHA256: intent}
	path := e.SchedulePath(in.ScheduleID)
	if _, err := os.Stat(e.CancelPath(in.ScheduleID)); !os.IsNotExist(err) {
		return commodel.Schedule{}, errors.New("schedule id was retired; use a new id")
	}
	if _, err := os.Stat(path); err == nil {
		return commodel.Schedule{}, errors.New("schedule already exists")
	}
	if err := e.atomicJSON(path, in); err != nil {
		return commodel.Schedule{}, err
	}
	return in, nil
}

func (e *Engine) ListSchedules(caller identity.Envelope) ([]commodel.Schedule, error) {
	e.mu.Lock()
	defer e.mu.Unlock()
	entries, err := os.ReadDir(filepath.Join(e.root, "schedules"))
	if err != nil {
		return nil, err
	}
	out := []commodel.Schedule{}
	for _, entry := range entries {
		if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".json") {
			continue
		}
		b, err := os.ReadFile(filepath.Join(e.root, "schedules", entry.Name()))
		if err != nil {
			return nil, err
		}
		var item commodel.Schedule
		if json.Unmarshal(b, &item) != nil {
			return nil, errors.New("invalid schedule")
		}
		if item.Envelope.PrincipalID != caller.PrincipalID || item.Envelope.ContextID != caller.ContextID {
			continue
		}
		if item.JobKind == "reminder" && item.LastOccurrenceID != "" {
			item.LastOccurrenceState, item.LastDeliveryState = e.ReminderStateLocked(item)
		}
		out = append(out, item)
		out[len(out)-1].NextDue = EffectiveNextDue(item)
	}
	return out, nil
}

func (e *Engine) UpdateSchedule(id string, caller identity.Envelope, expression, input string, enabled *bool) (commodel.Schedule, error) {
	e.mu.Lock()
	defer e.mu.Unlock()
	item, err := e.LoadScheduleLocked(id)
	if err != nil {
		return commodel.Schedule{}, err
	}
	if item.Envelope.PrincipalID != caller.PrincipalID || item.Envelope.ContextID != caller.ContextID {
		return commodel.Schedule{}, errors.New("schedule not owned")
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
	if err := ValidateScheduleDraft(item, caller, e.deps.Hooks); err != nil {
		return commodel.Schedule{}, err
	}
	loc, err := time.LoadLocation(item.Timezone)
	if err != nil {
		return commodel.Schedule{}, err
	}
	next, err := NextSchedule(item, loc, time.Now().UTC())
	if err != nil {
		return commodel.Schedule{}, err
	}
	item.Revision++
	item.NextDue = next
	item.UpdatedAt = time.Now().UTC()
	if err := e.atomicJSON(e.SchedulePath(id), item); err != nil {
		return commodel.Schedule{}, err
	}
	return item, nil
}

func (e *Engine) PauseSchedule(id string, caller identity.Envelope, expected ...uint64) error {
	e.mu.Lock()
	defer e.mu.Unlock()
	item, err := e.LoadScheduleLocked(id)
	if err != nil {
		return err
	}
	if item.Envelope.PrincipalID != caller.PrincipalID || item.Envelope.ContextID != caller.ContextID {
		return errors.New("schedule not owned")
	}
	item.Paused = true
	if len(expected) > 0 && expected[0] != item.Revision {
		return ErrRoutineRevision
	}
	item.Revision++
	item.UpdatedAt = time.Now().UTC()
	return e.atomicJSON(e.SchedulePath(id), item)
}

func (e *Engine) DeleteSchedule(id string, caller identity.Envelope, expected ...uint64) error {
	e.mu.Lock()
	defer e.mu.Unlock()
	item, err := e.LoadScheduleLocked(id)
	if err != nil {
		return err
	}
	if item.Envelope.PrincipalID != caller.PrincipalID || item.Envelope.ContextID != caller.ContextID {
		return errors.New("schedule not owned")
	}
	item.Enabled, item.Paused = false, true
	if len(expected) > 0 && expected[0] != item.Revision {
		return ErrRoutineRevision
	}
	item.Revision++
	item.NextDue = time.Time{}
	item.UpdatedAt = time.Now().UTC()
	if err := e.atomicJSON(e.CancelPath(id), item); err != nil {
		return err
	}
	return os.Remove(e.SchedulePath(id))
}

func (e *Engine) SnoozeSchedule(id string, caller identity.Envelope, until time.Time, expected ...uint64) error {
	e.mu.Lock()
	defer e.mu.Unlock()
	item, err := e.LoadScheduleLocked(id)
	if err != nil {
		return err
	}
	if item.Envelope.PrincipalID != caller.PrincipalID || item.Envelope.ContextID != caller.ContextID {
		return errors.New("schedule not owned")
	}
	if !until.After(time.Now().UTC()) {
		return errors.New("snooze must be in the future")
	}
	if len(expected) > 0 && expected[0] != item.Revision {
		return ErrRoutineRevision
	}
	if !item.NextDue.IsZero() && !until.After(EffectiveNextDue(item)) {
		return ErrRoutineSnoozeNotLater
	}
	item.SnoozedUntil = until.UTC()
	if strings.HasPrefix(strings.TrimSpace(item.Expression), "once:") {
		item.Expression = "once:" + until.UTC().Format(time.RFC3339Nano)
		item.NextDue = until.UTC()
		item.Enabled, item.Paused = true, false
	} else if item.NextDue.Before(until) {
		item.NextDue = until.UTC()
	}
	item.Revision++
	item.UpdatedAt = time.Now().UTC()
	return e.atomicJSON(e.SchedulePath(id), item)
}

func (e *Engine) CancelPath(id string) string {
	return filepath.Join(e.root, "schedules", commodel.FileID(id)+".deleted")
}

// RecordRoutineFailure stores the schedule's last failure digest and reports
// whether this failure should notify the owner: only a digest that differs
// from the stored one (or follows a success) is fresh. Schedules without a
// durable record (externally owned clocks) cannot dedupe and always notify.
func (e *Engine) RecordRoutineFailure(scheduleID, digest string) (bool, error) {
	e.mu.Lock()
	defer e.mu.Unlock()
	item, err := e.LoadScheduleLocked(scheduleID)
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
	return true, e.atomicJSON(e.SchedulePath(scheduleID), item)
}

// ClearRoutineFailure drops the stored failure digest after a success so the
// next failure notifies again. Externally owned schedules are a no-op.
func (e *Engine) ClearRoutineFailure(scheduleID string) error {
	e.mu.Lock()
	defer e.mu.Unlock()
	item, err := e.LoadScheduleLocked(scheduleID)
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
	return e.atomicJSON(e.SchedulePath(scheduleID), item)
}

func (e *Engine) OccurrenceCurrentLocked(o commodel.RoutineOccurrence) bool {
	if _, err := os.Stat(e.CancelPath(o.ScheduleID)); !os.IsNotExist(err) {
		return false
	}
	schedule, err := e.LoadScheduleLocked(o.ScheduleID)
	if os.IsNotExist(err) {
		return true // PutOccurrence also accepts externally owned recurrence clocks.
	}
	return err == nil && schedule.Revision == o.Revision && schedule.Envelope.Equal(o.Job.Envelope)
}

func (e *Engine) TickSchedules(now time.Time, nativeCron string, authorize func(commodel.Job) error) error {
	if nativeCron == "unmigrated" {
		return nil
	}
	e.mu.Lock()
	defer e.mu.Unlock()
	entries, err := os.ReadDir(filepath.Join(e.root, "schedules"))
	if err != nil {
		return err
	}
	// One unreadable record must not starve the rest of the queue: it moves to
	// schedules/quarantine and the tick continues, reporting the failure after
	// every other schedule had its pass.
	var errs []error
	for _, entry := range entries {
		if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".json") {
			continue
		}
		item, err := e.LoadScheduleLocked(strings.TrimSuffix(entry.Name(), ".json"))
		if err != nil {
			if qerr := e.quarantineScheduleLocked(filepath.Join(e.root, "schedules", entry.Name())); qerr != nil {
				return qerr
			}
			errs = append(errs, fmt.Errorf("schedule %s: %w", strings.TrimSuffix(entry.Name(), ".json"), err))
			continue
		}
		if item.BlockedReason != "" || now.Before(item.SnoozedUntil) || !item.Enabled || item.Paused || item.NextDue.IsZero() || now.Before(item.NextDue) || item.Delivery == "stream" {
			continue
		}
		loc, err := time.LoadLocation(item.Timezone)
		if err != nil {
			continue
		}
		due := item.NextDue
		if due.Before(item.SnoozedUntil) {
			due = item.SnoozedUntil
		}
		if item.LastOccurrenceID != "" {
			if !commodel.IDPattern.MatchString(item.LastOccurrenceID) || len(item.LastOccurrenceID) > 160 {
				if err := e.BlockScheduleLocked(&item, "invalid last occurrence reference", now); err != nil {
					return err
				}
				continue
			}
			lastPath := filepath.Join(e.root, "occurrences", item.LastOccurrenceID+".json")
			body, err := os.ReadFile(lastPath)
			if err == nil {
				var last commodel.RoutineOccurrence
				if json.Unmarshal(body, &last) != nil || last.Job.ID != item.LastOccurrenceID {
					if err := e.quarantineOccurrenceLocked(lastPath); err != nil {
						return err
					}
					if err := e.settleScheduleLocked(&item, loc, due, now); err != nil {
						return err
					}
					continue
				}
				outstanding := last.State == "pending" || last.State == "probing" || last.State == "scripting"
				if last.JobKind == "reminder" && last.State == "pending" {
					window := last.CatchUpWindow
					if window <= 0 {
						window = DefaultCatchUp
					}
					outstanding = now.Sub(last.DueAt) <= window
				}
				if last.State == "dispatched" && last.JobKind == "agent" {
					mapping, mappingErr := e.deps.LoadMapping(last.Job.ID)
					if mappingErr != nil && !os.IsNotExist(mappingErr) {
						return mappingErr
					}
					outstanding = mappingErr == nil && !commodel.TerminalStatus(mapping.Status)
					if os.IsNotExist(mappingErr) {
						for _, dir := range []string{"pending", "running"} {
							_, statErr := os.Stat(filepath.Join(e.root, dir, commodel.FileID(last.Job.ID)+".json"))
							if statErr == nil {
								outstanding = true
							}
							if statErr != nil && !os.IsNotExist(statErr) {
								return statErr
							}
						}
					}
				}
				if last.Revision == item.Revision && outstanding {
					if err := e.settleScheduleLocked(&item, loc, due, now); err != nil {
						return err
					}
					continue
				}
			} else if !os.IsNotExist(err) {
				return err
			}
		}
		window := item.CatchUpWindow
		if window <= 0 {
			window = DefaultCatchUp
		}
		job := commodel.Job{Envelope: item.Envelope, OrganizationID: item.OrganizationID, UserID: item.UserID, ActorID: item.ActorID, ScopeID: item.ScopeID, Channel: item.Channel, ChatID: item.ChatID, TaskID: item.TaskID, TopicID: item.TopicID, SlackChannel: item.SlackChannel, SlackThread: item.SlackThread, Text: item.Input, Trigger: "cron"}
		occ := commodel.RoutineOccurrence{ScheduleID: item.ScheduleID, Revision: item.Revision, DueAt: due, JobKind: item.JobKind, Job: job, CatchUpWindow: window}
		if err := ValidateOccurrence(occ, item.Envelope, e.deps.ChannelFor); err != nil {
			continue
		}
		if authorize != nil && authorize(occ.Job) != nil {
			if err := e.BlockScheduleLocked(&item, "owner authorization failed", now); err != nil {
				return err
			}
			continue
		}
		occ.DueAt = due.UTC()
		occ.State = "pending"
		if now.Sub(due) > window {
			occ.State = "missed"
		}
		occ.Job.ID = occurrenceID(occ)
		occ.Job.IdempotencyKey = occ.Job.ID
		occ.Job.Trigger = "cron"
		occ.Job.CreatedAt = due.UTC()
		occPath := filepath.Join(e.root, "occurrences", occ.Job.ID+".json")
		if _, err := os.Stat(occPath); err == nil {
			item.LastOccurrenceID = occ.Job.ID
			if err := e.settleScheduleLocked(&item, loc, due, now); err != nil {
				return err
			}
			continue
		}
		found := false
		for _, dir := range []string{"receipts", "archive", "quarantine"} {
			_, statErr := os.Stat(filepath.Join(e.root, "occurrences", dir, occ.Job.ID+".json"))
			if statErr == nil {
				found = true
				break
			}
			if !os.IsNotExist(statErr) {
				return statErr
			}
		}
		if !found {
			if err := e.atomicJSON(occPath, occ); err != nil {
				return err
			}
		}
		item.LastOccurrenceID = occ.Job.ID
		if err := e.settleScheduleLocked(&item, loc, due, now); err != nil {
			return err
		}
	}
	return errors.Join(errs...)
}

// quarantineScheduleLocked moves an unreadable schedule record out of the hot
// scan so the tick keeps serving every other owner.
func (e *Engine) quarantineScheduleLocked(path string) error {
	dir := filepath.Join(e.root, "schedules", "quarantine")
	if err := os.MkdirAll(dir, 0700); err != nil {
		return err
	}
	return os.Rename(path, filepath.Join(dir, filepath.Base(path)))
}

// blockScheduleLocked marks a schedule the tick can no longer advance: the
// state is visible as "blocked" in routine_get and the owner gets exactly one
// outbox notification per normalized reason, deduped by LastFailureSHA256.
// s.mu must be held.
func (e *Engine) BlockScheduleLocked(item *commodel.Schedule, reason string, now time.Time) error {
	item.BlockedReason = reason
	item.BlockedAt = now.UTC()
	item.UpdatedAt = now.UTC()
	sum := sha256.Sum256([]byte("blocked\x00" + item.ScheduleID + "\x00" + reason))
	digest := hex.EncodeToString(sum[:])
	if item.LastFailureSHA256 != digest {
		item.LastFailureSHA256 = digest
		text := "Рутина «" + item.ScheduleID + "» остановлена: " + reason + ". Откройте её через routine_get, исправьте и сделайте resume — или удалите."
		d := commodel.Delivery{ID: "routine-blocked-" + item.ScheduleID + "-" + digest[:8], IdempotencyKey: "routine-blocked-" + item.ScheduleID + "-" + digest[:8], Channel: item.Channel, ConversationID: item.Envelope.ConversationID, DeliveryTargetID: item.Envelope.DeliveryTargetID, ChatID: item.ChatID, TaskID: item.TaskID, TopicID: item.TopicID, SlackChannel: item.SlackChannel, SlackThread: item.SlackThread, Text: text, CreatedAt: now.UTC()}
		if ch := e.channelFor(item.Channel); ch != nil {
			// The notice goes to the owner's verified home, never to the
			// schedule's own destination — a stale binding is exactly what
			// may have caused the block.
			ch.RouteBlockedNotice(*item, &d)
		}
		d, err := e.normalizeDelivery(d)
		if err == nil {
			if err := e.deps.EnqueueDelivery(d); err != nil {
				return err
			}
			e.deps.Signal()
		}
	}
	return e.atomicJSON(e.SchedulePath(item.ScheduleID), item)
}

func (e *Engine) settleScheduleLocked(item *commodel.Schedule, loc *time.Location, due, now time.Time) error {
	if strings.HasPrefix(strings.TrimSpace(item.Expression), "once:") {
		item.Enabled = false
		item.Paused = true
		item.NextDue = time.Time{}
		item.UpdatedAt = now
		return e.atomicJSON(e.SchedulePath(item.ScheduleID), item)
	}
	next, err := NextSchedule(*item, loc, now)
	if errors.Is(err, ErrExhausted) {
		item.Enabled, item.Paused, item.NextDue = false, true, time.Time{}
		item.UpdatedAt = now
		return e.atomicJSON(e.SchedulePath(item.ScheduleID), item)
	}
	if err != nil {
		if errors.Is(err, ErrExhausted) {
			item.Enabled = false
			item.NextDue = time.Time{}
			item.UpdatedAt = now
			return e.atomicJSON(e.SchedulePath(item.ScheduleID), item)
		}
		return err
	}
	item.NextDue = next
	item.UpdatedAt = now
	return e.atomicJSON(e.SchedulePath(item.ScheduleID), item)
}

func (e *Engine) SchedulePath(id string) string {
	return filepath.Join(e.root, "schedules", commodel.FileID(id)+".json")
}

func (e *Engine) LoadScheduleLocked(id string) (commodel.Schedule, error) {
	b, err := os.ReadFile(e.SchedulePath(id))
	if err != nil {
		return commodel.Schedule{}, err
	}
	var item commodel.Schedule
	if json.Unmarshal(b, &item) != nil || item.ScheduleID == "" {
		return commodel.Schedule{}, errors.New("invalid schedule")
	}
	return item, nil
}
