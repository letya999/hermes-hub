---
description: v1 proactive-engine - listeners and a durable event inbox, one object model (Watch, Routine, Assignment), a seven-stage pipeline, task and progress ledgers per assignment, and an attention inbox with message budgets.
last_verified: 2026-11-19
---
# ADR-0108: Proactive engine - listeners, one object model, ledgers, attention

Status: accepted direction (M40, owner issue #572). Date: 2026-11-19.
Delivery: CHG-0344 (proactive-engine extraction and migration), CHG-0345
(listeners and event inbox), CHG-0346 (pipeline, ledgers, report protocol),
CHG-0347 (attention), CHG-0349 (adaptive planning), CHG-0352 (eval gate).
Supersedes: ADR-0041 (proactive automation on the occurrence pipeline).
Partly supersedes: ADR-0082 (standing assignments: the state machine D1 and
materialization D2 are replaced; acquisition sharing D5 and re-validation D4
are kept), ADR-0075 (goal mode becomes an Assignment with criteria; the
compaction half stays), ADR-0095 (automation ownership moves from ToolHub to
proactive-engine). Prior art: CHG-0328 `research.md`.

## Context

v0 proactive behaviour is spread over five object kinds in four packages:
routines and occurrences (`internal/routines`), ToolHub automation
(dispatch, outbox, assignments, watches, dreaming), commodel routine and
assignment records, communication goal/watcher code and the runtime goal
judge. Each has its own clock or trigger, its own delivery path and its own
notion of "done". Event subscriptions poll connector read tools; nothing
receives pushed events. The owner cannot see why the agent did or did not
message them, and there is no shared budget for interruptions.

## Decisions

1. **proactive-engine owns proactive state.** One control-zone module owns
   listeners, the event inbox, Watch/Routine/Assignment records, their
   ledgers and the attention inbox. It submits all execution to task-engine
   (ADR-0107 D5) and never spawns or calls runtimes itself. Its clock is the
   only proactive clock.
2. **Listeners and a durable event inbox.** A listener is a declared source
   adapter: `poll` (connector read tool or HTTP on an interval, with
   `ETag`/`Last-Modified` reuse from ADR-0082 D5), `push` (signed webhook
   received at hub-edge), or `stream` (bounded long-lived subscription). Every
   event is written to the inbox before any processing, keyed by
   `(listener, source_event_id)` for dedupe, with a per-listener cursor.
   Event content is untrusted data, never instructions (CaMeL-style split:
   triage may read it, it cannot widen grants or choose tools).
3. **Three object kinds, one model.**
   - *Watch* - a deterministic condition over inbox events that produces a
     notice candidate. No model call unless its `escalate` step is set.
   - *Routine* - clock-triggered work (reminder, digest, check, script,
     brief). Deterministic kinds never wake a model (ADR-0107 D5).
   - *Assignment* - a long-running owner intent with acceptance criteria,
     budget (wakes, tokens, wall time) and a deadline. It may own Watches and
     Routines it materializes, and carries the two ledgers (D5).
   All three share owner, agent profile, grants (subset of the profile),
   action mode (ADR-0109), revision fence and lifecycle (D6).
4. **Seven-stage pipeline.** Every proactive unit of work passes the same
   stages; each stage writes its outcome before the next starts:

   | Stage | Does | May call a model |
   |---|---|---|
   | 1 ingest | listener writes the event to the inbox | no |
   | 2 normalize | parse to a typed event, bound size, strip active content | no |
   | 3 dedupe | drop repeats by key and by content digest window | no |
   | 4 match | route to Watches/Assignments whose filters match | no |
   | 5 triage | deterministic filters first, then a bounded model judgment for the remaining candidates | yes (quarantined, read-only) |
   | 6 act | create a task-engine job under the owner's profile, grants and mode | yes (in runtime) |
   | 7 report | write a report record through the report protocol to attention | no |

   A unit can stop at any stage with a recorded reason (`no_match`,
   `filtered`, `below_threshold`, `budget_exhausted`, ...). Stage records are
   the answer to "why didn't you tell me".
5. **Task ledger and progress ledger per Assignment** (Magentic-One).
   The *task ledger* holds facts given, facts to look up, facts derived,
   the current plan and the acceptance criteria. The *progress ledger* is
   appended after every act step: `criteria_met`, `progress_made`,
   `loop_detected`, `blocked_on`, `next_step`. Two consecutive steps without
   progress, or a detected loop, trigger a replan; three replans without
   progress move the Assignment to `waiting_owner` with the ledger excerpt as
   evidence. The judge never marks criteria met without evidence references
   (ledger entries or artifacts).
6. **Lifecycle (all three kinds).**

   | From | Event | To |
   |---|---|---|
   | - | owner creates (or agent drafts) | draft |
   | draft | owner confirms | active |
   | active | budget or deadline reached | waiting_owner |
   | active | owner decision required (ADR-0109) | waiting_owner |
   | waiting_owner | owner extends, answers or edits | active |
   | active, waiting_owner | owner pauses | paused |
   | paused | owner resumes | active |
   | active | criteria met with evidence (Assignment) or end of schedule | completed |
   | active | unrecoverable error | failed |
   | any non-terminal | owner cancels or grant revoked | cancelled |

   Agent paths can create `draft` only; activation, widening and budget
   extension are owner actions (ADR-0082 D4 kept).
7. **Report protocol.** Act results reach the owner only as report records:
   `{source, severity (info|attention|urgent), summary, evidence_refs,
   proposed_actions, dedupe_key}`. A runtime cannot message the owner
   directly from proactive work; it emits a report.
8. **Attention inbox and budgets.** Reports land in a per-owner attention
   inbox. A deterministic policy decides `interrupt` (send now), `digest`
   (batch into the next digest) or `log` (visible on request only) from
   severity, owner rules, quiet hours and a daily interrupt budget per owner
   and per Assignment. `urgent` bypasses quiet hours but not the budget
   ceiling set by the owner; over-budget reports degrade to digest with a
   counter. Every decision is recorded with its reason.

## Boundary crossings

| Crossing | Allowed | Refused |
|---|---|---|
| source -> listener | signed push (hub-edge verifies), polls through egress-gateway under the owner's grants | unsigned push, polls with another owner's connection |
| event -> triage model | event as quarantined data, read-only tools | tool grants, destination choice, grant changes derived from content |
| proactive-engine -> task-engine | jobs as the owner's agent profile with `initiator=system:proactive-engine` | jobs as a system principal with user credentials |
| runtime -> owner (proactive) | report records only | direct channel sends bypassing attention |
| agent -> Assignment | draft creation, ledger append inside its run | activation, budget extension, grant widening |

## Alternatives

- **Keep routines in ToolHub and add listeners there.** Rejected: tool
  admission and proactive state have different owners and failure modes
  (ADR-0105 naming rule).
- **Model-only triage.** Rejected: deterministic filters first keep cost and
  false alarms bounded (ADR-0082 D6 evidence).
- **Free-form agent messages to the owner.** Rejected: no budget, no
  dedupe, no audit of why.

## Preserved v0 invariants

- Default-deny: an object's grants are a subset of its profile's.
- Fail-closed passports: a revoked or stale revision fences queued work
  (ADR-0082 D4).
- Uncertain outcomes are never retried; reports state uncertainty.
- Credentials never enter the model plane: listeners use connection
  references; injection happens at egress-gateway.
- Deterministic routines never wake a model.

## Migration

CHG-0344 converts existing routines, assignments, watches, goals and the
dreaming schedule into the new records losslessly (every field mapped or
explicitly retained as `legacy`), with a dry-run report and a refusal on
any unmapped record. No v0 record is deleted by the migration.
