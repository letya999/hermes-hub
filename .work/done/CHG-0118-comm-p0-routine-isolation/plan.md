# CHG-0118: Communication plane separation, phase 0 — routine tick isolation and boundary hardening

Chain: approved refactor plan (chat, 2026-02) -> this plan -> `state.yaml`.
Next stages: CHG-0119+ (broker-only credential intake, knowledge move, channel contract).

## Goal

Phase 0 of the communication-hub refactor: fix the defects that corrupt or
silently kill work before any package moves happen. One bad schedule must not
stop the routine tick for every owner; a schedule that can never fire must
become visibly `blocked` with one owner notification instead of silently
spinning; the routine-create HTTP boundary must stop accepting persisted
fields; Slack events must be deduped only after their job is durable; and the
gateway must not hold the credential-store key when the broker owns secrets.

## Read before coding

1. `docs/routines.md`, `docs/adr/ADR-0042-routine-lifecycle.md`,
   `specs/active/SPEC-0056-routine-lifecycle.md`,
   `specs/active/SPEC-0058-routine-destinations.md`
2. `internal/communication/schedules.go` (`TickSchedules`),
   `internal/communication/occurrences.go` (`DispatchDueOccurrences`),
   `internal/communication/http.go` (`handleRoutines`),
   `internal/communication/slack.go`, `internal/communication/config.go`

## Preconditions and rules

- Never reset or commit other tasks' uncommitted work.
- Red test first, then fix. Go stdlib only.
- `just check` before handoff; Docker gates when image code changes.
- Structure stays: no package moves, no new abstractions beyond the listed
  slices. Telegram remains the only production channel; Slack stays as-is.

## Slices

### S1. Schedule-record isolation in TickSchedules

Anchors: `schedules.go` `TickSchedules`, `loadScheduleLocked`,
`occurrences.go` `tickProactiveContext`.

Change:
1. An unreadable schedule record moves to `schedules/quarantine/` and the tick
   continues with the next record instead of returning.
2. Per-schedule failures inside the loop (mapping/state reads, settle writes)
   are collected and returned after the pass; they no longer abort the pass.
3. `tickProactiveContext` runs every stage even when an earlier stage failed:
   durable work is independent across stages.

Tests (red first):
- Corrupt `schedules/bad.json` plus one valid due schedule: tick returns an
  error, the valid schedule still produced its occurrence, the corrupt file
  sits under `schedules/quarantine/`.

### S2. Visible blocked state for dead schedules

Anchors: `schedules.go` `Schedule`, `TickSchedules`, `validateScheduleDraft`,
`routine_management.go` `routineState`, `editRoutine`, `UpdateSchedule`,
`occurrences.go` `DispatchDueOccurrences`.

Change:
1. `Schedule` gains `BlockedAt`/`BlockedReason` (omitempty).
2. `routineState` reports `blocked` when `BlockedReason != ""`; the tick skips
   blocked schedules before any work.
3. The tick blocks a schedule it can never advance: invalid
   `last_occurrence_id`, occurrence that fails tick-time authorization.
   Decision (changed during implementation): `DispatchDueOccurrences` does
   NOT block the schedule — occurrence records can be replayed from stale
   state and a stale record must never poison a healthy routine
   (`TestM10DigestQuietSelectionAndRecovery`). Persistent owner-policy
   denial is caught at occurrence minting inside `TickSchedules`, one stage
   earlier, so the schedule still blocks visibly and notifies once.
4. Blocking enqueues exactly one owner-facing delivery per normalized reason
   through the outbox, deduped by `LastFailureSHA256`.
5. Any successful `editRoutine` (update or resume) clears the block — the
   owner's edit is the fix intent. `snooze`/`pause` deliberately do not
   clear it: they delay but do not repair the cause.

Tests (red first):
- Schedule with invalid `last_occurrence_id` becomes `blocked`, one delivery
  lands in `outbox/pending`, a second tick adds no second delivery, and
  `routine_get` state is `blocked`.
- Disabled owner: occurrence fails authorization, schedule becomes `blocked`.
- After `resume` (editRoutine) the schedule is active and ticks again.

### S3. RoutineDraft boundary DTO

Anchors: `http.go` `handleRoutines`, `schedules.go` `Schedule`.

Change:
1. `POST /v1/routines` decodes a `RoutineDraft` carrying only creation-time
   fields and converts it to `Schedule`; persisted state fields
   (`snoozed_until`, `revision`, `native_cron_ref`, `next_due`, `paused`,
   `enabled`, `task_id`, `topic_id`, `prior_envelopes`, failure/occurrence
   state) can no longer be set from the wire.

Tests (red first):
- POST with `snoozed_until` far future + valid fields: created schedule fires
  on schedule (snooze dropped) or the field is ignored; POST with `task_id`
  outside an owned binding does not bind the routine to that task.

### S4. Credential-store gate

Anchors: `config.go` `ConfigFromEnv`, `gateway.go` `openCredentialSurface`.

Change:
1. When `config.BrokerApprove.Enabled()`, `ConfigFromEnv` clears
   `CredentialStore`/`CredentialKeyFile` even if the env vars are set; the
   gateway never opens the local credential store in broker mode.

Tests (red first):
- Env carries `HUB_CREDENTIAL_STORE`/`HUB_CREDENTIAL_KEY_FILE` plus broker
  vars: `ConfigFromEnv` yields an empty credential surface.

### S5. Slack event dedupe after durable ingest

Anchors: `slack.go` `HandleSlackEvents`, `RememberEvent`.

Change:
1. Split `RememberEvent` into `EventSeen` (stat) and `MarkEvent` (write).
2. The handler checks `EventSeen`, ingests, and only then `MarkEvent`s. An
   ingest failure leaves no marker so Slack's retry can succeed; the
   marker write happens after the job/delivery is durable.
3. Job-level idempotency (`slack:<event_id>`) already protects the markerless
   window.

Tests (red first):
- First delivery of an unmapped event returns 403 and a re-delivery still
  returns 403 (previously the second was deduped to 200 while the job never
  existed).
- A successful event re-delivered returns 200 without a second job.

## Red-team checklist

- A poisoned schedule record must not starve other owners or the same owner's
  other routines.
- A schedule blocked on authorization must not keep spawning occurrences or
  notifying every tick.
- A crafted create body must not inject `snoozed_until`, `revision`,
  `native_cron_ref`, `task_id`/`topic_id` or prior envelopes.
- A Slack retry after an ingest failure must re-process; a retried success
  must not double-enqueue the job.

## Acceptance

- `go test ./internal/communication/...` green including new regressions.
- `just check` green; own Go coverage >= 85%.

## Out of scope

- Package extraction, channel contract, routine-address changes (origin
  binding already shipped in CHG-0114), credential-form removal, knowledge
  move, quota move — later phases.
- Live provider verification (owner-only action).

## Done checklist

- [x] Tests red then green; `go test ./internal/communication` PASS.
- [x] `just check` PASS — coverage 86.35%.
- [x] Red-team recorded in state.yaml.
- [x] Docs updated (`docs/routines.md` blocked state).
- [x] state done; folder moved to `.work/done/`.
