package commodel

import (
	"time"

	"github.com/letya999/hermes-hub/internal/identity"
)

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
	Extract     []ExtractStep  `json:"extract,omitempty"`
	Script      string         `json:"script,omitempty"`
	// Recipe names a builtin probe preset expanded at create time; the
	// snapshot below is what actually runs, so catalog edits never silently
	// change existing schedules (SPEC-0059).
	Recipe        string `json:"recipe,omitempty"`
	RecipeVersion int    `json:"recipe_version,omitempty"`
	// Needs declares read-tool calls the hub prefetches under the owner's
	// grants before running a script/command probe; results reach the
	// networkless script through the needs() builtin (SPEC-0059 R5.2).
	Needs []ProbeNeed `json:"needs,omitempty"`
	// Service names a registered internal endpoint for kind=service probes
	// (SPEC-0059 R5.5); the URL comes from operator config, never the draft.
	Service string `json:"service,omitempty"`
	// Steps/WaitFor are the declarative browser pipeline for kind=render
	// probes: replayed by the external render service before the extract
	// chain sees the DOM (SPEC-0059 R5.4).
	Steps   []RenderStep `json:"steps,omitempty"`
	WaitFor string       `json:"wait_for,omitempty"`
	// Filter is the deterministic per-event gate for event subscriptions
	// (SPEC-0081 R4.1): an isolated script reading the event JSON and
	// emitting keep|drop|urgent. Dropped events are marked seen without
	// fan-out; urgent events bypass quiet windows and daily caps.
	Filter string `json:"filter,omitempty"`
}

// ProbeNeed is one hub-prefetched read-tool call for a script probe.
type ProbeNeed struct {
	Tool string         `json:"tool"`
	Args map[string]any `json:"args,omitempty"`
}

// RenderStep is one declarative browser action the render service replays
// before extraction; Evaluate runs page JS and stays bounded like a probe
// script.
type RenderStep struct {
	Action   string `json:"action"`
	Selector string `json:"selector,omitempty"`
	Value    string `json:"value,omitempty"`
}

// ExtractStep is one step in the deterministic extract chain applied to a
// probe's fetched document before comparison.
type ExtractStep struct {
	Kind     string `json:"kind"`
	Selector string `json:"selector,omitempty"`
	Pattern  string `json:"pattern,omitempty"`
	Path     string `json:"path,omitempty"`
}

type RoutineTiming struct {
	Windows  []RoutineWindow `json:"windows,omitempty"`
	At       []string        `json:"at,omitempty"`
	Weekdays []int           `json:"weekdays,omitempty"`
	StartAt  time.Time       `json:"start_at,omitempty"`
	EndAt    time.Time       `json:"end_at,omitempty"`
}

type RoutineWindow struct {
	From string `json:"from"`
	To   string `json:"to"`
}

type Schedule struct {
	OriginJobID       string              `json:"origin_job_id,omitempty"`
	DestinationTaskID string              `json:"destination_task_id,omitempty"`
	Creation          *ScheduleCreation   `json:"creation,omitempty"`
	ScheduleID        string              `json:"schedule_id"`
	Revision          uint64              `json:"revision"`
	Envelope          identity.Envelope   `json:"envelope"`
	PriorEnvelopes    []identity.Envelope `json:"prior_envelopes,omitempty"`
	OrganizationID    string              `json:"organization_id"`
	UserID            string              `json:"user_id"`
	ActorID           string              `json:"actor_id"`
	ScopeID           string              `json:"scope_id"`
	Channel           string              `json:"channel"`
	ChatID            int64               `json:"chat_id,omitempty"`
	// TaskID/TopicID snapshot the task the routine was created in: occurrence
	// jobs rebind their conversation and deliver back to the same topic.
	TaskID         string         `json:"task_id,omitempty"`
	TopicID        int64          `json:"topic_id,omitempty"`
	SlackChannel   string         `json:"slack_channel,omitempty"`
	SlackThread    string         `json:"slack_thread,omitempty"`
	Timezone       string         `json:"timezone"`
	Expression     string         `json:"expression"`
	Timing         *RoutineTiming `json:"timing,omitempty"`
	IntervalAnchor time.Time      `json:"interval_anchor,omitempty"`
	JobKind        string         `json:"job_kind"`
	// Probe is the deterministic source spec for watcher/check schedules:
	// an http(s) GET plus an optional regex whose first capture group is the
	// compared value. It never carries credentials.
	Probe               *WatchProbe `json:"probe,omitempty"`
	Expect              string      `json:"expect,omitempty"`
	InitialProbeValue   *string     `json:"initial_probe_value,omitempty"`
	ProbeRevision       uint64      `json:"probe_revision,omitempty"`
	NotifyMode          string      `json:"notify_mode,omitempty"`
	SourceIDs           []string    `json:"source_ids,omitempty"`
	SnoozedUntil        time.Time   `json:"snoozed_until,omitempty"`
	Input               string      `json:"input"`
	Enabled             bool        `json:"enabled"`
	Paused              bool        `json:"paused"`
	NextDue             time.Time   `json:"next_due"`
	LastOccurrenceID    string      `json:"last_occurrence_id,omitempty"`
	LastOccurrenceState string      `json:"last_occurrence_state,omitempty"`
	LastDeliveryState   string      `json:"last_delivery_state,omitempty"`
	// LastFailureSHA256 dedupes owner-facing error deliveries: a routine that
	// fails every tick with the same normalized error notifies once, and a
	// success clears it so the next failure notifies again.
	LastFailureSHA256 string `json:"last_failure_sha256,omitempty"`
	// BlockedAt/BlockedReason mark a schedule the tick can no longer advance:
	// the owner sees state "blocked" in routine_get and must edit or delete it.
	BlockedAt     time.Time     `json:"blocked_at,omitempty"`
	BlockedReason string        `json:"blocked_reason,omitempty"`
	CatchUpWindow time.Duration `json:"catch_up_window_ns"`
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
	Sink string `json:"sink,omitempty"`
	// DeliverMode selects what a watcher ships on each successful probe tick
	// (SPEC-0059 R6): empty/"notify" delivers only on change (default),
	// "content" delivers the probed value as text every tick, "artifact"
	// stages the value as a file attachment named ArtifactName.
	DeliverMode  string `json:"deliver_mode,omitempty"`
	ArtifactName string `json:"artifact_name,omitempty"`
	// Delivery selects how an event subscription is fed: empty/"poll" fires
	// one occurrence per cron tick, "stream" runs a hub-owned bounded
	// long-poll loop against the probe tool instead (no occurrences).
	Delivery      string `json:"delivery,omitempty"`
	NativeCronRef string `json:"native_cron_ref,omitempty"`
	// AssignmentID marks a schedule provisioned by an assignment apply;
	// WakePrompt prefixes agent-sink evidence jobs with the specialist's
	// instructions (SPEC-0080). Both are engine-stamped, never draft-set.
	AssignmentID string    `json:"assignment_id,omitempty"`
	WakePrompt   string    `json:"wake_prompt,omitempty"`
	CreatedAt    time.Time `json:"created_at"`
	UpdatedAt    time.Time `json:"updated_at"`
}

// RoutineDraft is the POST /v1/routines boundary: it carries only the fields
// an owner may set at creation, so lifecycle, identity and routing internals
// (revision, envelope, enabled, snoozed_until, task_id, ...) can never be
// persisted straight from the request body.
type RoutineDraft struct {
	ScheduleID        string         `json:"schedule_id"`
	OriginJobID       string         `json:"origin_job_id,omitempty"`
	DestinationTaskID string         `json:"destination_task_id,omitempty"`
	Channel           string         `json:"channel,omitempty"`
	ChatID            int64          `json:"chat_id,omitempty"`
	TopicID           int64          `json:"topic_id,omitempty"`
	SlackChannel      string         `json:"slack_channel,omitempty"`
	SlackThread       string         `json:"slack_thread,omitempty"`
	Timezone          string         `json:"timezone"`
	Expression        string         `json:"expression"`
	Timing            *RoutineTiming `json:"timing,omitempty"`
	IntervalAnchor    time.Time      `json:"interval_anchor,omitempty"`
	JobKind           string         `json:"job_kind,omitempty"`
	Probe             *WatchProbe    `json:"probe,omitempty"`
	Expect            string         `json:"expect,omitempty"`
	NotifyMode        string         `json:"notify_mode,omitempty"`
	SourceIDs         []string       `json:"source_ids,omitempty"`
	Input             string         `json:"input"`
	CatchUpWindow     time.Duration  `json:"catch_up_window_ns,omitempty"`
	MaxRetries        int            `json:"max_retries,omitempty"`
	QuietFrom         string         `json:"quiet_from,omitempty"`
	QuietTo           string         `json:"quiet_to,omitempty"`
	DailyLimit        int            `json:"daily_limit,omitempty"`
	Sink              string         `json:"sink,omitempty"`
	DeliverMode       string         `json:"deliver_mode,omitempty"`
	ArtifactName      string         `json:"artifact_name,omitempty"`
	Delivery          string         `json:"delivery,omitempty"`
}

func (d RoutineDraft) Schedule() Schedule {
	return Schedule{ScheduleID: d.ScheduleID, OriginJobID: d.OriginJobID, DestinationTaskID: d.DestinationTaskID, Channel: d.Channel, ChatID: d.ChatID, TopicID: d.TopicID, SlackChannel: d.SlackChannel, SlackThread: d.SlackThread, Timezone: d.Timezone, Expression: d.Expression, Timing: d.Timing, IntervalAnchor: d.IntervalAnchor, JobKind: d.JobKind, Probe: d.Probe, Expect: d.Expect, NotifyMode: d.NotifyMode, SourceIDs: d.SourceIDs, Input: d.Input, CatchUpWindow: d.CatchUpWindow, MaxRetries: d.MaxRetries, QuietFrom: d.QuietFrom, QuietTo: d.QuietTo, DailyLimit: d.DailyLimit, Sink: d.Sink, DeliverMode: d.DeliverMode, ArtifactName: d.ArtifactName, Delivery: d.Delivery}
}

type ScheduleCreation struct {
	RequestedExpression string    `json:"requested_expression"`
	ResolvedDueAt       time.Time `json:"resolved_due_at"`
	CreatedAt           time.Time `json:"created_at"`
	IntentSHA256        string    `json:"intent_sha256"`
}

// RoutineOccurrence is the durable handoff from a schedule owner. Recurrence
// calculation belongs to that owner; each occurrence enters the ordinary queue.
type RoutineOccurrence struct {
	ScheduleID string    `json:"schedule_id"`
	Revision   uint64    `json:"revision"`
	DueAt      time.Time `json:"due_at"`
	Job        Job       `json:"job"`
	// JobKind mirrors the schedule kind. "reminder" occurrences deliver their
	// text through the outbox directly — no ordinary job, no runtime, no model.
	JobKind       string        `json:"job_kind,omitempty"`
	CatchUpWindow time.Duration `json:"catch_up_window_ns,omitempty"`
	// RetryCount counts probe-error retries (check kind, SPEC-0054 R1.1) and
	// RetryAfter holds them back until the backoff deadline passes.
	RetryCount int       `json:"retry_count,omitempty"`
	RetryAfter time.Time `json:"retry_after,omitempty"`
	State      string    `json:"state"`
}

// Event is one discrete item extracted from a watch probe result.
type Event struct {
	ID   string
	Text string
	// Requeue carries the connector-native identifier arguments for the
	// requeue hook when the event exposes them.
	Requeue map[string]any
	// Urgent is set by a deterministic filter verdict: the event bypasses
	// quiet windows and daily caps (SPEC-0081 R4.1).
	Urgent bool `json:"urgent,omitempty"`
}
