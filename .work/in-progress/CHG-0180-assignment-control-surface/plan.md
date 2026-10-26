# CHG-0180: assignment control surface (M23 stage 3)

Chain: `docs/adr/ADR-0082-standing-assignments.md` D8–D9 ->
`specs/active/SPEC-0080-standing-assignments.md` R2 -> this plan ->
`state.yaml`.
Previous stage: `CHG-0178` (plane), `CHG-0179` (pipeline). Next: `CHG-0181`.

## Goal

Owner and agent can create, inspect, steer and cancel standing assignments
from chat: the agent plans/applies via tools over a REST surface; the owner
holds the exclusive grant-widening verb; status cards report retained
resources, blocking reasons and durable counters.

## Read before coding

1. `internal/communication/http.go` (mux, auth, error shape),
   `internal/communication/routine_management.go` (create/get/list/pause
   handlers, card builders).
2. `internal/communication/gateway.go` `dispatchChannelCommand`
   (`/routines` handler as the pattern), `internal/communication/telegram.go`
   `SetMyCommands` list.
3. `internal/agenttools/routines.go` (thin client pattern, descriptions),
   `internal/agenttools/tools.go`/`tools_api.go` (registration).
4. `internal/routines/assignments.go` (CHG-0178 API).

## Preconditions and rules

- Agent tools get exactly the REST verbs in R2.1 — no authorize verb.
- Chat `authorize` is the only grant-widening path.
- Every mutation owner-envelope-gated + `expected_revision` CAS.

## Slices

### S3.1 REST surface + status card (R2.1, R2.2, R2.5)

Anchors: new `internal/communication/assignments.go`;
`internal/communication/http.go` route table;
`internal/routines/assignments.go` (`AssignmentCard` builder).

Change:
1. Routes: `POST /v1/assignments` (create, `apply` flag),
   `POST /v1/assignments/plan` (dry-run: validate plan, resolve channel
   routes, lint probes; returns per-source verdicts; never mutates),
   `GET /v1/assignments` (caller-scoped list, paging like routines),
   `GET /v1/assignments/{id}` (card per R2.2),
   `POST /v1/assignments/{id}/plan` (CAS update; grants widen ⇒ 403),
   `POST /v1/assignments/{id}/apply|pause|resume|cancel|complete`.
2. Card aggregates link state via `LoadScheduleLocked` +
   `EventStateOf`/`LoadWatchStateLocked` + assignment counters.
3. Blocking/waiting notices via existing owner-notice dedup seam.

Tests:
- API round-trip create→apply→get→pause→resume→cancel;
- dry-run verdicts (one good http source + one broken script source);
- foreign-envelope ⇒ 404-class on every route;
- grant-widen POST /plan ⇒ refused.

### S3.2 Agent tools (R2.3, R2.6)

Anchors: `internal/agenttools/routines.go` (mirror);
new `internal/agenttools/assignments.go`;
`internal/agenttools/tools.go` registration + descriptions.

Change:
1. `assignment_create|plan|apply|list|get|update|pause|resume|cancel|complete`
   → REST mapping; `expected_revision` required on mutations; `update`
   = plan replace (grants may only narrow).
2. Descriptions teach the flow: `assignment_plan` first (dry-run
   verdicts), then create with apply; plan changes need
   `expected_revision`; cancelled is terminal.
3. Input fields: `id`, `title`, `intent`, `plan` (JSON string),
   `expected_revision`, paging `limit/cursor`.

Tests:
- `TestAssignmentToolsMapOps` (verb→route/method table)
- `TestAssignmentToolRequiresRevision`
- `TestAssignmentToolNoAuthorize` — no input field maps to authorize.

### S3.3 Chat commands + authorize (R2.4, R2.5)

Anchors: `internal/communication/gateway.go` `dispatchChannelCommand`;
`internal/communication/assignments.go` command helpers;
`internal/communication/telegram.go` `SetMyCommands`.

Change:
1. `/assignments` — owner list: id, title, state, blocked, next due.
2. `/assignment <id> status|pause|resume|cancel|complete` — verified
   sender envelope; replies via `queueDeliveryChannel`.
3. `/assignment <id> authorize` — sets `grants_authorized_rev =
   plan.revision`; required before a later plan update may widen grants;
   only reachable here (REST rejects the field outright).
4. Register `/assignments` in `SetMyCommands`.

Tests:
- `TestAssignmentCommandsLifecycle`
- `TestAssignmentAuthorizeChatOnly`
- `TestPrivateNotificationAudience` (A1.7: private-target assignment can
  never emit to group target — normalize/deliver path denies).

## Red-team checklist

- Agent POST with `authorize:true` ⇒ field rejected/ignored, grant
  unchanged.
- Status card for foreign id ⇒ not-found refusal (no existence leak).
- `/assignment` typed in a group by a non-owner ⇒ envelope mismatch ⇒
  refused.
- Cancel then immediately re-apply ⇒ cancelled is terminal; new
  assignment required (intent hash still dedupes to the cancelled one →
  returns it with state, caller must create with changed title/intent
  marker or explicit `force:true` — documented refusal, no silent revive).

## Acceptance

SPEC-0080 A1.4–A1.7 + A2.1–A2.2; `just check`.

## Out of scope

- Live runs (CHG-0181), probe internals (CHG-0179), domain logic
  (CHG-0178).

## Done checklist

- [ ] Tests red then green; `just check`.
- [ ] state.yaml updated; commit `CHG-0180`.
