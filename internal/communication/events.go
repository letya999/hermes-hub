package communication

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/letya999/hermes-hub/internal/commodel"
)

// Event is one discrete item extracted from a probe result.
// Requeue carries the connector-native identifier arguments for the requeue
// hook when the event exposes them (telegram bursts carry chat_id and their
// requeue_event contract wants it, not event_id).
type Event = commodel.Event

// EventState is the durable seen-set of an event subscription
// (spool/event-state/<scheduleID>.json) — replay across a restart cannot
// double-deliver because delivery ids and the seen-set are both durable.
type EventState struct {
	PendingBatch  *sourceBatch `json:"pending_batch,omitempty"`
	ProbeRevision uint64       `json:"probe_revision,omitempty"`
	ScheduleID    string       `json:"schedule_id"`
	Seen          []string     `json:"seen,omitempty"`
	Degraded      bool         `json:"degraded,omitempty"`
	// Overflow counts events that could not be admitted to the sink; they
	// stay unseen and are requeued to the connector by id only.
	Overflow int `json:"overflow,omitempty"`
	// LastSuccess/LastError feed the operator-facing /watches listing.
	LastSuccess time.Time `json:"last_success,omitempty"`
	LastError   string    `json:"last_error,omitempty"`
	// NotifyDay/NotifyCount apply the schedule's owner-timezone daily cap to
	// event fan-out (SPEC-0054 R2.2).
	NotifyDay   string `json:"notify_day,omitempty"`
	NotifyCount int    `json:"notify_count,omitempty"`
	// Cursor is the source-declared resume token for the next poll
	// (SPEC-0081 R3.1); GapCount/LastGapAt make seen-set eviction visible —
	// the subscription can no longer prove dedup for the evicted range.
	Cursor    string    `json:"cursor,omitempty"`
	GapCount  int       `json:"gap_count,omitempty"`
	LastGapAt time.Time `json:"last_gap_at,omitempty"`
	// SourceCursors/SourceErrors carry the same contract per fan-out input
	// of a multi-source subscription, keyed by the source label — one dead
	// connector must not reset healthy siblings' cursors or health.
	SourceCursors map[string]string `json:"source_cursors,omitempty"`
	SourceErrors  map[string]string `json:"source_errors,omitempty"`
}

func (s *Spool) eventPath(id string) string {
	return filepath.Join(s.root, "event-state", spoolFileID(id)+".json")
}

// EventStateOf reads one subscription's durable state for the operator
// surface (/watches); a missing file is an empty state, not an error.
func (s *Spool) EventStateOf(id string) (EventState, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.loadEventStateLocked(id)
}

func (s *Spool) loadEventStateLocked(id string) (EventState, error) {
	var st EventState
	var probeRevision uint64
	if schedule, err := s.loadScheduleLocked(id); err == nil {
		probeRevision = schedule.ProbeRevision
	}
	b, err := s.readEventRecordLocked(s.eventPath(id))
	if os.IsNotExist(err) {
		if _, markerErr := s.fsys.Lstat(s.eventPath(id) + ".initialized"); markerErr == nil {
			return EventState{ScheduleID: id, ProbeRevision: probeRevision, Degraded: true, GapCount: 1, LastGapAt: time.Now().UTC(), LastError: "source cursor missing; recovery required"}, errors.New("source cursor missing; recovery required")
		} else if !os.IsNotExist(markerErr) {
			return st, markerErr
		}
		return EventState{ScheduleID: id, ProbeRevision: probeRevision}, nil
	}
	if err != nil {
		return st, err
	}
	if strictEventJSON(b, &st) != nil || st.ScheduleID != id {
		return st, errors.New("invalid event state")
	}
	if st.ProbeRevision != probeRevision {
		if st.PendingBatch != nil {
			return st, errors.New("source batch probe revision changed; recovery required")
		}
		return EventState{ScheduleID: id, ProbeRevision: probeRevision, GapCount: st.GapCount, LastGapAt: st.LastGapAt}, nil
	}
	return st, nil
}

func (st EventState) seen(id string) bool {
	for _, s := range st.Seen {
		if s == id {
			return true
		}
	}
	return false
}

const eventSeenCap = 512

func (st *EventState) markSeen(id string, now time.Time) {
	st.Seen = append(st.Seen, id)
	if len(st.Seen) > eventSeenCap {
		dropped := len(st.Seen) - eventSeenCap
		st.Seen = st.Seen[dropped:]
		// The evicted ids can no longer prove dedup: surface the loss as a
		// durable gap instead of silently weakening the subscription
		// (SPEC-0081 R3.2).
		st.GapCount += dropped
		st.LastGapAt = now.UTC()
	}
}

// authError reports whether a probe failure looks like connector auth
// (401/403, unauthorized, forbidden) — the durable signal the owner must
// reconnect the source.
func authError(err error) bool {
	m := strings.ToLower(err.Error())
	return strings.Contains(m, "401") || strings.Contains(m, "403") ||
		strings.Contains(m, "unauthorized") || strings.Contains(m, "forbidden")
}

// extractEvents turns one probe result into discrete events. A tool result
// carrying {events:[...]} or a bare array yields one event per item — the id
// is the item's declared identity else a stable content hash. Any other
// shape, including a plain http/github value, is exactly one event keyed by
// the new value hash.
func extractEvents(probe *WatchProbe, body []byte) ([]Event, error) {
	if probe == nil {
		return nil, errors.New("probe required")
	}
	if probe.Kind != "mcp_tool" {
		return []Event{{ID: watchHash(string(body)), Text: boundedWatchText(string(body))}}, nil
	}
	var envelope struct {
		Events   []json.RawMessage `json:"events"`
		Results  []json.RawMessage `json:"results"`
		Messages []json.RawMessage `json:"messages"`
		Event    *bool             `json:"event"`
	}
	var items []json.RawMessage
	if err := json.Unmarshal(body, &envelope); err == nil && envelope.Event != nil && !*envelope.Event {
		return nil, nil // wait-tool timeout sentinel: not an event
	} else if err == nil && envelope.Events != nil {
		items = envelope.Events
	} else if err == nil && envelope.Results != nil {
		items = envelope.Results
	} else if err == nil && envelope.Messages != nil {
		items = envelope.Messages
	} else {
		var array []json.RawMessage
		if err := json.Unmarshal(body, &array); err == nil {
			items = array
		} else {
			items = []json.RawMessage{json.RawMessage(body)}
		}
	}
	events := make([]Event, 0, len(items))
	for _, raw := range items {
		if len(raw) > 64*1024 {
			return nil, errors.New("event exceeds filter input limit")
		}
		var item map[string]any
		var id, text string
		var requeue map[string]any
		if json.Unmarshal(raw, &item) == nil {
			for _, key := range []string{"id", "event_id", "message_id"} {
				switch v := item[key].(type) {
				case string:
					if v != "" {
						id = v
					}
				case float64:
					// Connector-native ids are often numeric (telegram
					// message ids); coerce them into the durable dedup id.
					id = strconv.FormatFloat(v, 'f', -1, 64)
				}
				if id != "" {
					break
				}
			}
			for _, key := range []string{"text", "title", "message"} {
				if v, ok := item[key].(string); ok && v != "" {
					text = v
					break
				}
			}
			if text == "" {
				// Handle-style events (settled bursts) carry routing fields
				// rather than prose — the compact record keeps chat_id for
				// the sink while boundedWatchText still bounds it.
				var compact bytes.Buffer
				if json.Compact(&compact, raw) == nil {
					text = compact.String()
				}
			}
			if chatID, ok := item["chat_id"]; ok && chatID != nil {
				requeue = map[string]any{"chat_id": chatID}
			}
		} else {
			var scalar string
			if json.Unmarshal(raw, &scalar) == nil {
				text = scalar
			}
		}
		if id == "" {
			id = watchHash(string(raw))
		}
		rawText := ""
		if item != nil {
			var compact bytes.Buffer
			if json.Compact(&compact, raw) == nil {
				rawText = compact.String()
			}
		}
		events = append(events, Event{ID: id, Text: boundedWatchText(text), Raw: rawText, Requeue: requeue})
	}
	return events, nil
}

// filterInput is what the keep|drop|urgent script reads via input(): the
// compacted event JSON when the source item decoded as an object, else the
// plain text — connector fields stay visible to scope filters.
func filterInput(ev Event) string {
	if ev.Raw != "" {
		return ev.Raw
	}
	return ev.Text
}

// settleEvents lands one event occurrence: unseen events fan out to the
// sink, the seen-set and the occurrence settle under one lock. It returns
// the events the sink could not admit — the caller best-effort requeues
// those to the connector by native id, outside the spool lock.
func (s *Spool) settleEvents(o RoutineOccurrence, schedule Schedule, body []byte, now time.Time, probeErr error, authorize func(Job) error) ([]Event, error) {
	if handled, err := s.resumeOccurrenceBatch(o, schedule, now, authorize); handled || err != nil {
		return nil, err
	}
	path := filepath.Join(s.root, "occurrences", occurrenceID(o)+".json")
	raw, err := s.fsys.ReadFile(path)
	if err != nil {
		return nil, err
	}
	var live RoutineOccurrence
	if json.Unmarshal(raw, &live) != nil {
		return nil, errors.New("invalid source occurrence")
	}
	if live.State != "probing" {
		return nil, nil
	}
	if !s.rt.OccurrenceCurrent(live) || (authorize != nil && authorize(live.Job) != nil) {
		_, err = s.rt.MutateProbingOccurrence(o, func(current *RoutineOccurrence) error {
			current.State = "cancelled"
			return nil
		})
		return nil, err
	}
	st, err := s.loadEventStateLocked(live.ScheduleID)
	if err != nil {
		return nil, err
	}
	if probeErr != nil {
		st.LastError = boundedWatchText(probeErr.Error())
		if authError(probeErr) {
			st.Degraded = true
		}
		s.mu.Lock()
		stateErr := s.writeEventStateLocked(live.ScheduleID, st)
		s.mu.Unlock()
		_, occErr := s.rt.MutateProbingOccurrence(o, func(current *RoutineOccurrence) error {
			current.State = "failed"
			return nil
		})
		return nil, errors.Join(stateErr, occErr)
	}
	events, err := extractEvents(schedule.Probe, body)
	if err != nil {
		_, occErr := s.rt.MutateProbingOccurrence(o, func(current *RoutineOccurrence) error {
			current.State = "failed"
			return nil
		})
		return nil, errors.Join(err, occErr)
	}
	var env struct {
		Cursor string `json:"next_cursor"`
	}
	cursor := ""
	if json.Unmarshal(body, &env) == nil {
		cursor = env.Cursor
	}
	fresh := []Event{}
	for _, ev := range events {
		if !st.seen(ev.ID) {
			fresh = append(fresh, ev)
		}
	}

	// The deterministic per-event gate (SPEC-0081 R4.1) runs outside the
	// spool lock: the script performs I/O in a disposable runtime and must
	// not starve every other writer while it evaluates each event. A filter
	// that cannot evaluate fails closed — events stay unseen and retry on a
	// later poll.
	var verdicts map[string]string
	var filterErr error
	if schedule.Probe != nil && schedule.Probe.Filter != "" {
		verdicts = make(map[string]string, len(fresh))
		if s.probeScript == nil {
			filterErr = errors.New("event filter unavailable")
		}
		for _, ev := range fresh {
			if filterErr != nil {
				break
			}
			verdict, verr := s.probeScript(context.Background(), live, schedule.Probe.Filter, filterInput(ev), false, nil)
			if verr != nil {
				filterErr = verr
				break
			}
			verdict = strings.TrimSpace(verdict)
			if verdict != "keep" && verdict != "drop" && verdict != "urgent" {
				filterErr = errors.New("invalid event filter verdict")
				break
			}
			verdicts[ev.ID] = verdict
		}
	}

	// The occurrence and the seen-set may have moved while the filter ran;
	// re-validate before admitting anything to the sink.
	raw, err = s.fsys.ReadFile(path)
	if err != nil {
		return nil, err
	}
	if json.Unmarshal(raw, &live) != nil {
		return nil, errors.New("invalid source occurrence")
	}
	if live.State != "probing" {
		return nil, nil
	}
	if !s.rt.OccurrenceCurrent(live) || (authorize != nil && authorize(live.Job) != nil) {
		_, err = s.rt.MutateProbingOccurrence(o, func(current *RoutineOccurrence) error {
			current.State = "cancelled"
			return nil
		})
		return nil, err
	}
	s.mu.Lock()
	st, err = s.loadEventStateLocked(live.ScheduleID)
	if err != nil {
		s.mu.Unlock()
		return nil, err
	}
	if filterErr != nil {
		st.LastError = boundedWatchText(filterErr.Error())
		err = s.writeEventStateLocked(live.ScheduleID, st)
		s.mu.Unlock()
		_, occErr := s.rt.MutateProbingOccurrence(o, func(current *RoutineOccurrence) error {
			current.State = "failed"
			return nil
		})
		return nil, errors.Join(err, occErr)
	}
	err = s.startSourceBatchLocked(&st, schedule, live.Job, events, verdicts, live.Job.ID, cursor, nil, map[string][]string{"": eventIDs(events)}, nil, now)
	s.mu.Unlock()
	if err != nil {
		return nil, err
	}
	overflowed, drained, err := s.applySourceBatch(&st, schedule, now, authorize)
	if err != nil {
		return overflowed, err
	}
	if drained {
		_, err = s.rt.MutateProbingOccurrence(o, func(current *RoutineOccurrence) error {
			current.State = "dispatched"
			return nil
		})
		return overflowed, err
	}
	return overflowed, nil
}
