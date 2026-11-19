---
description: Frozen v1 proactive-engine, action modes and evaluation - listeners and event inbox, object model and lifecycle, pipeline and ledgers, attention budgets, modes and owner decisions, live Jira/Slack scenarios and proactivity eval thresholds.
last_verified: "2026-11-20"
---
# SPEC-0102: v1 proactive-engine, action modes and eval thresholds

Frozen: 2026-11-19. Implementation: M41 items 1-6 (event sources and
watches, assignments with progress, attention, modes and standing
approvals, user model, evaluation gate); M40.10 (#608) for R2.2-R2.3.

Scope note (2026-11-20, CHG-0334): the simplified v1 plan moves
listeners, the pipeline and ledgers, attention, action modes and the eval
gate to M41 ([ADR-0108](../../docs/adr/ADR-0108-proactive-engine.md) and
[ADR-0109](../../docs/adr/ADR-0109-action-modes-owner-decisions.md) as
aligned). In M40 only R2.2 (agent paths create drafts only) and R2.3
(migration with dry run, refusing unmapped records) apply, through
M40.10; owner decisions in M40 follow ADR-0109 D5 and SPEC-0100 A7.1. The
rest is a draft for M41 and is re-reviewed when M41 starts. The CHG
numbers this spec named before were a reserved table that no longer
exists.
Decisions: [ADR-0108](../../docs/adr/ADR-0108-proactive-engine.md),
[ADR-0109](../../docs/adr/ADR-0109-action-modes-owner-decisions.md).
Trace: [SPEC-0100](SPEC-0100-v1-contracts.md) exit criterion EC5;
latency P5 in [SPEC-0101](SPEC-0101-v1-execution-slos.md).
Prior art: `.work/done/CHG-0328-v1-architecture/research.md`.

## R1 Listeners and event inbox

- R1.1 Listener kinds `poll`, `push`, `stream`; push requires a verified
  signature at hub-edge; polls run under the owner's grants through
  egress-gateway.
- R1.2 Every event is persisted in the inbox before processing, keyed by
  `(listener, source_event_id)`; a duplicate key is a no-op.
- R1.3 Per-listener cursor survives restart; no event between two cursors is
  lost or processed twice (at-least-once ingest, exactly-once match by key).
- R1.4 Event content never changes grants, tools or destinations.

## R2 Object model and migration

- R2.1 Watch, Routine and Assignment share owner, agent profile, grants
  subset, mode, revision fence and the ADR-0108 D6 lifecycle.
- R2.2 Agent paths create `draft` only; activation, widening and budget
  extension are owner actions.
- R2.3 Migration from v0 routines, assignments, watches, goals and dreaming
  maps every field or keeps it as `legacy`, refuses on an unmapped record,
  deletes nothing, and has a dry-run report.

## R3 Pipeline and ledgers

- R3.1 Every unit passes the seven stages of ADR-0108 D4 and records the
  stage where it stopped and why.
- R3.2 Stages 1-4 make no model call; triage runs deterministic filters
  before any model call.
- R3.3 Each Assignment keeps a task ledger and an append-only progress
  ledger; two steps without progress or a detected loop trigger a replan;
  three replans without progress park it in `waiting_owner`.
- R3.4 Criteria are marked met only with evidence references.

## R4 Attention

- R4.1 Proactive output reaches the owner only as report records through the
  attention inbox.
- R4.2 The policy decides interrupt / digest / log from severity, owner
  rules, quiet hours and budgets; every decision records its reason.
- R4.3 Defaults: 5 interrupts per owner per day, 2 per Assignment per day,
  quiet hours 22:00-08:00 owner local time; the owner can change them.
- R4.4 Over-budget reports degrade to the digest; none is dropped silently.

## R5 Action modes and decisions

- R5.1 Modes `read-only`, `plan`, `ask`, `ask-critical`, `full-access` per the
  ADR-0109 D3 matrix, evaluated only after grants allow.
- R5.2 Every catalog tool member declares an effect class; an undeclared
  class denies.
- R5.3 The always-ask list applies in every mode and cannot be shortened per
  user.
- R5.4 Decisions follow the ADR-0109 D5 table, bind the exact call digest,
  cannot be minted on model paths, and resume the original job.
- R5.5 Remembered approvals expire in at most 30 days and never cover the
  always-ask list.

## R6 Live assignment scenarios

- R6.1 Jira: an Assignment watches a project for issues matching owner
  criteria, triages them, drafts a comment in `ask-critical` mode, asks
  before posting, and reports. Receipts: listener event, stage records,
  decision record, provider write receipt.
- R6.2 Slack: an Assignment watches channels for mentions of an owner topic,
  digests non-urgent ones and interrupts on an urgent one within P5.
- R6.3 Both scenarios run on the dev stack with real provider accounts the
  owner authorizes; receipts carry ids, never message content.

## R7 Proactivity eval gate

The suite follows ProactiveBench (offer or stay silent on an event stream)
and SentinelBench (monitoring with no-op cases) and runs in CI on fixtures
with a recorded model; a release also runs it against the live model.

- R7.1 Metrics per run: recall, precision, false-alarm rate
  (1 - precision), F1, premature-termination rate on no-op cases, median
  reaction time, cost per task, attention-budget violations, always-ask
  violations.
- R7.2 Absolute floors: always-ask violations = 0; attention-budget
  violations = 0; premature termination on no-op cases <= 5%; precision >=
  0.70; recall on must-notify events >= 0.90.
- R7.3 Regression bounds: F1 and precision may not drop more than 0.03
  below the baseline recorded by the M41 eval gate; cost per task may not rise more
  than 20%. A baseline update is an owner-confirmed commit.

## Acceptance

| ID | Check |
|---|---|
| A1.1-A1.4 | Listener tests: signature refusal, duplicate key no-op, restart cursor, content cannot change grants |
| A2.1-A2.3 | Lifecycle transition table test; agent-path activation refused; migration dry-run on a copy of the dev spool with zero unmapped records |
| A3.1-A3.4 | Stage records for every outcome; zero model calls in stages 1-4 (spy); replan and park tests; criteria-without-evidence refused |
| A4.1-A4.4 | Direct send refused; decision reasons recorded; default budgets; over-budget goes to digest |
| A5.1-A5.5 | Matrix table test; undeclared effect denies; always-ask under full-access; decision digest binding; remember expiry |
| A6.1-A6.3 | Live receipts in the M41 assignments CHG for Jira and Slack |
| A7.1-A7.3 | CI eval job output and baseline file in the M41 eval CHG |
