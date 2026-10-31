# SPEC-0080: Standing assignments (durable intent → plan → specialist wakes)

Frozen: 2026-10-27. Implements GitHub issue #324 (absorbs closed #333–#335).
Decision: [ADR-0082](../../docs/adr/ADR-0082-standing-assignments.md).

## Requirements

### Stage 1 — assignment plane (CHG-0178)

- R1.1: An `Assignment` record (`spool/assignments/<id>.json`) carries
  `assignment_id` (`asg-<16 hex>`), `revision`, the verified owner
  `envelope`, verbatim `intent` text (≤4 KiB), `intent_hash`
  (sha256(principal‖context‖intent) for create-idempotency), `title`,
  `state` (`draft|active|waiting|paused|completed|cancelled|failed`),
  `plan` + `applied_rev`, `grants`, `checkpoint` counters, `blocked`
  reason, timestamps. Records load under the spool lock; a record whose
  envelope or id mismatches its path-side keying is invalid, never
  coerced.
- R1.2: `state` semantics: `draft` = created, not applied;
  `active` = applied, ≥1 link live, none blocked; `waiting` = applied but
  ≥1 link blocked/degraded/missing — `blocked` carries the reason;
  `paused` = all owned artifacts paused; `completed` = owner-marked done or
  explicit `complete`; `cancelled` = terminal, artifacts removed;
  `failed` = apply could not converge (partial receipts stay durable).
- R1.3: `plan` = `{revision, sources[]≤8, specialist?}`; each source is
  `{name, schedule: RoutineDraft}` validated by the existing draft
  validator plus: job_kind ∈ {watcher,check,event}, deterministic ids
  `asg-<aid>-<name>`, `assignment_id`/`assignment_rev` stamped, agent
  sinks carry `wake_prompt` (≤2 KiB). `specialist` = `{instructions ≤2 KiB,
  reply: auto|approval}`.
- R1.4: `apply` reconciles plan→schedules: sources missing or drifted
  (intent hash differs) are created/replaced via the existing
  creation-replay path; owned schedules the plan no longer names are
  deleted. Every source has a durable applied receipt; an interrupted or
  repeated apply is a no-op where receipts match and resumes where they do
  not. Failure mid-plan leaves state `failed`, applied receipts durable.
- R1.5: `pause` pauses every owned schedule; `resume` unpauses and
  reschedules; `cancel` pauses, deletes owned artifacts, removes
  acquisition memberships and writes the `cancelled` tombstone;
  `complete` stops sources but keeps the record. All are
  `expected_revision`-CASable and owner-envelope-gated.
- R1.6: Wake budget: `grants.max_wakes_per_day` (default 48, bound 240)
  counts agent-sink job admissions per assignment per owner-day; exceeding
  settles the occurrence `capped` and surfaces on the status card.
- R1.7: Fencing at effect points: probe settle, event fan-out, job claim
  and delivery claim re-check that `schedule.assignment_id` still resolves
  to a runnable assignment (`active|waiting`); paused/cancelled/failed ⇒
  the occurrence cancels, the job drops terminal-failed with reason
  `assignment_not_runnable`, the staged delivery is dropped. Queued work
  from before a cancel never executes.
- R1.8: An assignment whose linked schedule is deleted out-of-band flips
  to `waiting` with `blocked: artifact_missing:<name>` on the next
  reconcile; recovery requires explicit apply.
- R1.9: Intent is append-only: `intent`/`intent_hash` are set at create and
  immutable; a new intent is a new assignment. Grants may narrow via the
  API; widening is possible only through the chat-command authorize path
  (R2.4). Any mutation by a caller whose envelope does not fully match
  `assignment.envelope` fails with the same not-found refusal class as a
  missing record.

### Stage 2 — control surface (CHG-0180)

- R2.1: REST under the existing control API auth:
  `POST /v1/assignments` (create; `apply:true` provisions inline),
  `POST /v1/assignments/plan` (dry-run validation: draft validation +
  channel/route resolution + per-source probe lint; never mutates),
  `GET /v1/assignments`, `GET /v1/assignments/{id}` (status card),
  `POST /v1/assignments/{id}/plan` (CAS plan update; grant widening
  refused), `POST /v1/assignments/{id}/apply`, `/pause`, `/resume`,
  `/cancel`, `/complete`.
- R2.2: The status card exposes: state, blocking reason, plan/applied revs,
  per-source `{name, schedule_id, kind, last_state, next_due, last_error,
  degraded/gap}`, retained artifacts, counters `{wakes_today, llm_calls,
  notices_today, fetches, shared_fetches, last_wake_latency_ms}`.
- R2.3: Agent tools `assignment_create`, `assignment_plan`, `assignment_apply`,
  `assignment_list`, `assignment_get`, `assignment_update`, `assignment_pause`,
  `assignment_resume`, `assignment_cancel`, `assignment_complete` — thin
  clients over R2.1 with the existing principal/auth headers; mutations
  require `expected_revision`. No tool can set `authorize` or widen grants.
- R2.4: Chat commands: `/assignments` (list) and
  `/assignment <id> status|pause|resume|cancel|complete|authorize` on the
  verified command path; `authorize` exists only here and records
  `grants_authorized_rev` — the only route able to raise grant ceilings.
- R2.5: Owner-facing notices about assignment blocking/waiting ride the
  existing deduplicated outbox path; a private-only assignment can never
  fall back to a group target (audience revalidation already in the
  outbox stays authoritative).
- R2.6: Slack slice wiring: a plan may name `reply_channel` slack targets
  for its specialist; urgent verdicts (SPEC-0081 R2.x) route to the
  owner envelope's private channel only. Communication-channel access
  alone grants no Slack data access — sources still need their own
  connector/tool grants.

### Stage 3 — live acceptance (CHG-0181)

- R3.1: Live scenario executes end-to-end on the dev stack: create via
  agent tool (chat-driven), interrupt mid-apply (process kill), restart →
  apply resumes idempotently, plan update changes a condition, pause →
  quiet, resume → catches up, cancel mid-processing → no stale delivery.
- R3.2: Where a real Slack workspace + connector scopes are configured,
  the ten-contact slice runs against the real API; otherwise the slice is
  verified against the signed `/v1/slack/events` intake + recorded
  PostMessage boundary, and the gap is reported honestly.
- R3.3: Evidence is receipts (job/mapping/outbox states, counters), never
  raw user content; artifacts live under `.work/tmp/` only.

## Acceptance

- A1.1 `TestAssignmentRestartResume`: apply killed mid-way ⇒ restart ⇒
  second apply converges with zero duplicate artifacts.
- A1.2 `TestAssignmentPlanIdempotent`: apply twice ⇒ identical schedule
  set, identical applied receipts, no extra files.
- A1.3 `TestAssignmentCancelFencesLateWake`: occurrence probing + wake job
  queued ⇒ cancel ⇒ job claim drops `assignment_not_runnable`, deliveries
  dropped, artifacts gone.
- A1.4 `TestAssignmentIntentOwnerOnly`: foreign-envelope read/mutation on
  every verb ⇒ not-found refusal; intent field immutable via API.
- A1.5 `TestAssignmentGrantWidenDenied`: plan update widening grants via
  API ⇒ refused; chat `authorize` then update ⇒ allowed.
- A1.6 `TestAssignmentWakeBudget`: cap 1/day ⇒ second agent wake settles
  `capped`; card reflects counters.
- A1.7 `TestPrivateNotificationAudience`: assignment with private delivery
  + group fallback attempt ⇒ denied, never delivered to the group.
- A2.1 API round-trip incl. dry-run report of one good + one broken source.
- A2.2 `TestSlackHumanReplyWins`: evidence bundle + specialist instruction
  contract verified; dedup + bot-loop drop on signed intake.
- A3.1 Live acceptance log: restart/cancel/dedup/privacy receipts in
  CHG-0181 `state.yaml`.
