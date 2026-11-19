---
description: v1 proactive-engine - the only owner of what wakes the agent (Routine, Occurrence, Assignment, Goal, Watch), deterministic occurrence ids, identity and grants resolved at fire time, jobs only through task-engine with origin unprompted; listeners, ledgers and attention are M41.
last_verified: 2026-11-20
---
# ADR-0108: Proactive engine

Status: proposed. Aligned 2026-11-20 with the simplified v1 plan (M40.1
#599, CHG-0334); the 2026-11-19 text was never merged to `dev`. Record text
awaits owner review. M40 delivers the extraction (D1-D5); the M41 section
records the direction for later work without adding its concepts to M40
code (rule S3, [engineering.md](../engineering.md)).
Delivery: M40.10 (#608). Later: M41 items 1-6.
Supersedes: ADR-0041 (proactive automation on the occurrence pipeline).
Partly supersedes: ADR-0082 (standing assignments: state machine and
materialization replaced; re-validation D4 and acquisition sharing D5
kept), ADR-0075 (goal mode moves to proactive-engine; the compaction half
stays), ADR-0095 (automation ownership moves from ToolHub).

## Context

v0 proactive behaviour is spread over routines and occurrences
(`internal/routines`), ToolHub automation (dispatch, outbox, event batches,
dreaming), communication goals and watchers, the runtime goal judge and
agent-tool routes. Each has its own clock, delivery path and notion of
"done". The 2026-11-19 audits found wedges in this code (dreaming
occurrence, event batch after a schedule edit, paused assignment apply,
goal lost update); M40.0 (CHG-0333) fixes them in place and M40.10 folds the
fixes into the new machines ([state-machines.md](../state-machines.md)).

## Decisions

1. **proactive-engine owns what wakes the agent.** Routine, Occurrence,
   Assignment, Goal and Watch live in `proactive.db`. Its clock is the only
   proactive clock. It submits all execution to task-engine and never
   spawns or calls runtimes itself.
2. **One state machine per kind,** each a table in the service README and
   in [state-machines.md](../state-machines.md), with a deadline and
   sweeper for every non-final state (ADR-0111). A failed occurrence
   settles `failed` after its attempt limit; it never blocks the next one.
3. **Deterministic occurrence id** from (tenant, routine, slot). It is the
   idempotency key of the job it creates, so a repeated wake collapses.
4. **Identity and grants resolve at fire time,** not at creation. A routine
   whose owner lost the right does not fire.
5. **Jobs only through task-engine,** with `origin=unprompted`. Unprompted
   runs are read-only by default; anything beyond write-local needs an
   OwnerDecision (ADR-0109). Agent paths may create drafts only;
   activation, widening and budget extension are owner actions (ADR-0082
   D4 kept).

## Later (M41)

Recorded so M40 code leaves room for them; none of these concepts enters
M40 code:

- Event sources and watches: poll first, push through ingress-gateway
  webhooks; a watch keeps a baseline and calls the model only on change;
  silent result by default. Heartbeat is a routine kind.
- Assignments with task and progress ledgers; stall detection, replan,
  `waiting_owner` after repeated stalls.
- Attention: notices with class `log | digest | interrupt`, a `why` built
  from data, quiet hours and an interrupt budget in the `config` passport.
- Modes and standing approvals over the M40 effect vocabulary (ADR-0109).
- Evaluation gate for proactivity and memory.

## Boundary crossings

| Crossing | Allowed | Refused |
|---|---|---|
| proactive-engine -> task-engine | jobs as the owner's agent with `origin=unprompted` and the occurrence id as key | jobs as a system principal with user credentials |
| proactive-engine -> runtime | nothing | direct runtime calls |
| agent -> routine or assignment | drafts through tool-hub | activation, budget extension, grant widening |
| event or tool content -> authority | data for the run | changing grants, modes or destinations |

## Alternatives

- **Keep automation inside ToolHub.** Rejected: tool admission and
  proactive state have different owners and failure modes (ADR-0105).
- **Build listeners, pipeline and attention in M40.** Moved to M41: M40
  keeps every existing proactive feature and adds no new one.

## Preserved v0 invariants

- Default-deny: a routine's grants are a subset of its owner's.
- Fail-closed passports: a revoked or stale revision fences queued work.
- Uncertain outcomes are never retried.
- Deterministic routines never wake a model.
- Credentials never enter the model plane.

## Migration

M40.10 imports per-user JSON stores, event state, occurrences, dreaming
days and communication goals into `proactive.db` with a dry-run report and
a refusal on any unmapped record. No v0 record is deleted by the import.
