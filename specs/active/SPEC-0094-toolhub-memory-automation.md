---
description: Frozen M32 requirements for ToolHub-owned per-user memory, automation and night processing.
last_verified: 2026-11-07
---
# SPEC-0094: ToolHub memory and automation

Frozen: 2026-11-04. Implementation in progress.
Decision: [ADR-0095](../../docs/adr/ADR-0095-toolhub-owned-memory-automation.md).
Supersedes the ownership and run-scope mechanisms of SPEC-0080/0085. Their
memory data model, retention and routine behavior remain unless amended below.

## R1. Trusted per-user memory

- **R1.1** User, agent and space are one identity. ToolHub resolves each vault
  from authenticated enrollment; no model argument, URL path, header or
  message content may select another user. Storage, locks and work bounds are
  per user; dev and prod remain separate.
- **R1.2** Communication-hub persists accepted-turn and completed-run facts
  before acknowledging their transport step. Facts include canonical identity,
  job/run ID, audience, provenance, sensitivity, timestamp and bounded content.
  ToolHub authenticates the sender, validates identity, deduplicates event IDs
  and persists capture before acknowledgment. Failure remains durably pending.
- **R1.3** ToolHub is the only vault writer and handles recall/search.
  Runtime has no vault mount. OpenKnowledge mutations use the same governed
  writer. No other service mounts a user vault RW.
- **R1.4** Every memory call binds to the exact authenticated, live private
  owner run. A verified assignment run in the same owner's private audience
  may use the same currently selected and granted memory tools. Model arguments
  cannot supply that identity. A concurrent owner run
  cannot authorize another run. Expired/completed/mismatched runs fail closed.
  Shared `runtime/run-scope/current` is absent.
- **R1.5** Group and sensitive events cannot expose private CORE, recall or
  results. Existing search citation and retention contracts remain. Missing
  or disabled memory reports unavailable/disabled without opening a vault.
- **R1.6** Disabling memory stops capture, recall, search, write and dreaming,
  including an asynchronous write before commit.

## R2. Tools and confirmation

- **R2.1** ToolHub executes `memory_search`, `memory_card` and
  `memory_edit` after tool selection, `tools.use` and operation grant
  checks. A selected but ungranted tool is inert. Direct and indirect
  `tool_call` invocation receive identical admission.
- **R2.2** Editing or deleting existing memory creates a proposal bound to
  exact user, target, diff, base revision, expiry and single-use nonce. Only a
  verified human action confirms it. Apply rechecks rights and base revision.
  Model calls, proposal IDs and prompt text cannot confirm.
- **R2.3** Denials, proposals and mutations have allowlisted audit records
  with actor, operation, target ID, run and outcome; no body or secret content.

## R3. Automation ownership

- **R3.1** ToolHub owns durable routines, occurrences, assignments, watches,
  digests, clock, authorization and execution dispatch. `internal/routines`
  uses ToolHub-owned state and lock. Communication-hub owns channel intake,
  delivery outbox and receipts. There is one scheduler and writer after
  cutover, with no fallback scheduler.
- **R3.2** ToolHub submits admitted work directly to supervisor with canonical
  identity and idempotency key. It asks communication-hub to deliver results;
  the gateway verifies destinations. Channel downtime delays delivery but not
  execution. Completion and delivery are separately visible.
- **R3.3** Existing routine/assignment API behavior, pause/revoke fences,
  occurrence IDs and recovery remain. Stale work cannot bypass current rights.
  Per-user bounds prevent starvation.

## R4. Dreaming

- **R4.1** Each enabled user has one protected system routine from
  `config.memory` local time and timezone. It uses the ordinary routine due
  calculator, durable schedule/occurrence registration and recovery path;
  memory processing is its operation, not another scheduling tick. Ordinary
  routine CRUD cannot edit it. A `(user, local_date)` key prevents duplicate
  application and permits recovery of incomplete work.
- **R4.2** Dreaming uses that user's vault, model relay,
  `internal/memory/night` and its retention rules. Current memory enablement
  and `memory.dreaming` are required at admission and before commit.
- **R4.3** Night digests use an idempotent delivery request. Proposed future
  actions remain drafts until authenticated owner approval.

## Acceptance

- **A1:** Two managed users, parallel private/group turns and concurrent tool
  calls show no cross-user or cross-audience capture, recall, search or write.
  Kill/restart ToolHub between event send and ack: one capture survives.
- **A2:** A stale owner run, forged principal, replayed event and model-made
  confirmation fail closed; a confirmed exact diff applies once and a changed
  base revision refuses without partial write.
- **A3:** Create, pause, resume and remove a routine and assignment through
  public paths. While communication-hub is down, due work executes; delivery
  waits and arrives once after restart.
- **A4:** Two users get their own once-per-local-day night result. Restart
  mid-job and revoke `memory.dreaming` before commit: no duplicate or
  unauthorized write occurs. An isolated controlled-time acceptance path
  prepares three logical days with at least ten facts per day per owner;
  real-model smoke still runs for both owners without changing system clocks
  or editing personal vault data by hand.
- **A5:** Isolation, concurrency, authorization and HTTP contract regression
  tests, `just check`, required Docker gate and live dev evidence pass. Mock
  tests alone do not prove integration.
