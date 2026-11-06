package communication

import (
	"time"

	"github.com/letya999/hermes-hub/internal/commodel"
)

// Event is one discrete item extracted from a probe result.
// Requeue carries the connector-native identifier arguments for the requeue
// hook when the event exposes them (telegram bursts carry chat_id and their
// requeue_event contract wants it, not event_id).
type Event = commodel.Event

// One bounded unfinished source batch belongs to its existing cursor record.
// It must drain before polling again, including sources without an ACK API.
// Declared here for the wire contract only: ToolHub owns batch settlement;
// communication only decodes the field inside a routine card.
type sourceBatch struct {
	Version      int                 `json:"version"`
	Binding      string              `json:"binding"`
	Job          Job                 `json:"job"`
	Seed         string              `json:"seed"`
	CreatedAt    time.Time           `json:"created_at"`
	Events       []Event             `json:"events"`
	Verdicts     map[string]string   `json:"verdicts,omitempty"`
	SinkIDs      map[string]string   `json:"sink_ids"`
	Settled      map[string]bool     `json:"settled"`
	Jobs         map[string]Job      `json:"jobs,omitempty"`
	Deliveries   map[string]Delivery `json:"deliveries,omitempty"`
	CursorBefore string              `json:"cursor_before,omitempty"`
	CursorAfter  string              `json:"cursor_after,omitempty"`
	SourceBefore map[string]string   `json:"source_before,omitempty"`
	SourceAfter  map[string]string   `json:"source_after,omitempty"`
	SourceEvents map[string][]string `json:"source_events"`
	SourceErrors map[string]string   `json:"source_errors,omitempty"`
}

// EventState mirrors the owner-side durable seen-set of an event
// subscription so the routine card stays decodable on the RPC wire.
// ToolHub owns the record; communication never writes it.
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
