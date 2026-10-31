---
description: Standing assignments are durable owner-intent records that reconcile to owned routine artifacts; source pulls share bounded acquisitions inside a verified boundary; deterministic filters gate every model wake.
last_verified: 2026-10-27
---
# ADR-0082: Standing assignments — intent record, reconcile-to-routines, shared acquisition, filtered wakes

Status: Accepted 2026-10-27. Implements GitHub milestone 23 issues #324 and
#332 (#333–#336 were closed as consolidation-only; their scope lives here).
Specs: [SPEC-0080](../../specs/active/SPEC-0080-standing-assignments.md),
[SPEC-0081](../../specs/active/SPEC-0081-shared-acquisition.md).
Evidence: `.work/in-progress/CHG-0178-assignment-plane/research.md`.

## Context

Routines already give the owner durable reminders, watchers, checks, event
subscriptions, quiet windows, daily caps, idempotent occurrence enqueue,
revision fencing and an outbox that re-verifies audience before send. What is
missing for "watch X and tell me when Y happens" as a *standing* instruction:

- there is no durable record of the owner's intent that outlives the chat —
  a watcher is an artifact, not an assignment; grouping, lifecycle, retained
  resources and blocking reasons have no single card;
- two subscriptions polling the same URL/tool each pay a full fetch; HTTP
  pulls ignore ETag/Last-Modified and Retry-After; connector cursors and
  their expiry/gap state are not recorded;
- event sinks fan out everything that passes extraction — there is no
  deterministic per-event keep/drop/urgent gate before the model sees it;
- push sources cannot reach the hub — intake is pull-only;
- cancel/pause fences the schedule, but a wake job or delivery queued for a
  since-cancelled assignment still runs — nothing re-validates the
  assignment at the effect point.

Surveyed harnesses (iva-agent, OpenDots, paperclip, goclaw, nanoclaw,
osinara, pi-mono, open-duet) converge on one shape: a durable intent/claim
record, deterministic cheap checks that gate expensive model wakes, a claim
taken *before* the wake, and re-validation *at* the effect point. They also
share the same weaknesses this design avoids: process-memory schedulers
(open-dot), JSON state without fencing (iva tick race windows), heavyweight
durable task graphs (pi-mono) or an always-running heartbeat loop
(goclaw-nextlevelbuilder) that we deliberately do not adopt.

## Decision

- **D1**: An `Assignment` is a durable control-plane record
  (`spool/assignments/<id>.json`, engine-owned, same spool lock). It carries
  the verified owner envelope, the verbatim intent text, a state machine
  (`draft|active|waiting|paused|completed|cancelled|failed`), the current
  plan revision, narrowed grants, durable checkpoints/counters and the
  blocking reason. It is not a process, a queue or a second scheduler; it
  binds identity, never a Hermes session.

- **D2**: A plan materializes into ordinary schedules (watcher, check,
  event, hook). Each source name maps to a deterministic schedule id
  (`asg-<aid>-<name>`); owned artifacts carry `assignment_id` +
  `assignment_rev`. `apply` is a reconcile: create what is missing and
  matching, replace what drifted, delete what the plan no longer names —
  idempotent through the existing creation-replay intent hash plus a
  durable per-source applied receipt. A re-apply after interruption resumes
  instead of duplicating. `cancel` pauses then deletes every owned artifact
  and leaves a tombstone record so retained resources stay inspectable.

- **D3**: A specialist is a plan descriptor (`instructions`, reply policy,
  tool hints), not a process. A source that decides an event is potentially
  relevant wakes it through the existing `notify_mode=agent` /
  `sink=agent` job path: the wake prompt rides the schedule, the evidence
  arrives flagged untrusted, the job enters the same bounded, deduplicated,
  restart-safe queue as everything else. One descriptor serves all sources
  and any number of wakes.

- **D4**: Every effect point re-validates. Probe settle, event fan-out,
  job claim and delivery claim check (a) the occurrence/mapping currency
  and envelope as today, and (b) — new — the owning assignment still being
  runnable (`active`/`waiting`) and inside its wake/notice budget. Pause or
  cancel therefore fences work already queued, running or staged for send;
  stale `expected_revision` writes fail; an agent caller can never widen
  grants because grant widening exists only on the verified chat-command
  path (the tool surface has no such verb).

- **D5**: Physical acquisition is separated from logical subscriptions.
  `spool/acquisitions/<key>.json` is keyed by sha256 over
  (principal, context, runtime, policy) + the canonical probe
  (`kind+url|tool+args`), so sharing is structurally impossible across
  connector/account/security boundaries. A member whose interval hint is
  not yet elapsed reuses the stored bounded snapshot instead of fetching;
  the member that actually fetches stores bounded body, `ETag`,
  `Last-Modified` and fetch metadata. HTTP 304 settles as `no_change`
  without extraction or a model call. `Retry-After` and repeated failures
  raise a durable `backoff_until`; occurrence due times carry deterministic
  jitter. The last member leaving retires the record.

- **D6**: Event subscriptions gain a deterministic filter: `probe.filter`
  is bounded Starlark run per extracted event through the same isolated
  script runtime, returning `keep` | `drop` | `urgent` (+ optional reason).
  `drop` marks the event seen without delivery; `urgent` bypasses the quiet
  defer (not the daily cap); everything is still deduplicated by the
  durable seen-set first. Connector envelopes may carry
  `cursor`/`next_cursor`/`cursor_expired`; cursors persist per subscription
  (`EventState`), expiry marks a visible gap (`gap_since`) and notifies the
  owner once per distinct cause.

- **D7**: `probe.kind=hook` makes an event subscription a push intake: the
  hub mints a 128-bit token at create, stores only its sha-256, and serves
  `POST /v1/hooks/{schedule_id}` gated by constant-time bearer compare,
  bounded body, the same extract→filter→seen-set→fanout pipeline. The token
  is returned once at create; loss means rotate (new token, old dead).

- **D8**: Control surface mirrors the lifecycle: REST `/v1/assignments`
  (create/dry-run/apply/list/get/plan update/pause/resume/cancel/complete)
  for the agent tools; `/assignments` and `/assignment <id> <verb>` chat
  commands for the owner, plus the grant-authorize verb that exists only
  there. Status derives from linked artifacts: per-source state, next due,
  last error/gap, retained resources, durable counters (wakes, LLM calls =
  wakes, notices, shared-fetch savings, last wake latency).

- **D9**: The Slack live slice uses only verified surfaces: a
  `delivery=stream` `mcp_tool` subscription on the owner's Slack connector
  (or the existing signed `/v1/slack/events` intake where configured), a
  filter script enforcing the sender allowlist and bot-loop drop, the
  durable seen-set for dedup, `urgent` verdicts routed by normalized
  delivery to the owner's private Slack channel only — never a group — and
  specialist instructions that re-check the thread (prefetched read tool)
  before any bounded reply, so a human answer wins.

- **D10**: Measurement is durable, not ad hoc: assignment counters record
  wakes/LLM calls/notices and last wake latency (due→enqueue→claim
  timestamps already persist on occurrences and mappings); acquisition
  records count fetches vs shared hits; per-probe tick cost stays O(1)
  file reads. Live acceptance measures the stack (docker stats, log
  counters) rather than fixture math.

## Alternatives considered

- **pi-mono-style durable task graph**: rejected — a second scheduler and
  dependency graph duplicate `spool/occurrences` and buy complexity the
  milestone does not need.
- **goclaw-nextlevelbuilder heartbeats (always-running timer per agent)**:
  rejected — the milestone requires bounded wake-driven work, no resident
  LLM process.
- **DB-backed acquisition pool**: rejected — the spool is the single
  durable substrate of this plane; a database would be an unsolicited
  dependency and a second writer to fence.
- **Letting the agent widen grants via tools with a flag**: rejected — a
  tool parameter is model-controlled; only the verified chat-command path
  (human keystrokes) may authorize an expanded mandate.
- **Assignment owns its own event/delivery queue**: rejected — the outbox
  already deduplicates, re-verifies audience and survives restart.

## Invariants kept

- Hermes stays an unmodified upstream dependency; no new service, database,
  queue or framework is required to use assignments.
- Every model wake is still gated by a deterministic check; unchanged
  source data never calls the LLM; wake evidence is always flagged
  untrusted.
- Owner intent and grants are writable only under the verified owner
  envelope; cross-principal/context access fails closed.
- One spool writer, `atomicJSON` writes, idempotent replay — a restart at
  any point neither duplicates effects nor loses a decided wake.
