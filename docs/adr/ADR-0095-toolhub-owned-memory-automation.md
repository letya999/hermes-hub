---
description: ToolHub owns per-user memory, routines, assignments and dreaming; communication-hub retains verified channel intake and delivery.
last_verified: 2026-11-04
---
# ADR-0095: ToolHub owns memory and automation

Status: accepted direction, implementation in progress. Decision: 2026-11-04.
Requirements: [SPEC-0094](../../specs/active/SPEC-0094-toolhub-memory-automation.md).
Amends ADR-0082, ADR-0086 and the ownership clauses of earlier routine ADRs.

## Context

The present gateway mounts one vault, captures turns only for its first enabled
user, and runs a single night clock. Runtime mounts the vault read-write.
`memory_card` authorizes against all live runs in a shared
`runtime/run-scope/current` file. Gateway read/modify/write of that file has
no concurrency fence. The `internal/routines` domain is a separate package,
but its engine borrows the communication spool's lock, storage, job queue and
delivery queue. Thus package separation did not move ownership.

## Decisions

### D1. Identity and storage unit

One configured user is one agent and one `spaces/<user>` home. Every user's
vault and automation records are separate. ToolHub resolves their paths from
trusted enrollment, never from model arguments, message text or an asserted
HTTP header. Shared ToolHub deployment must enforce principal, context and
policy pins on every call and fact. Per-user work has independent bounds and
serialization so one user cannot block another indefinitely.

### D2. ToolHub owns memory

ToolHub reuses `internal/memory`, `internal/vault` and
`internal/memory/night` behind its authenticated API and tool admission.
ToolHub is the only writer of the vault. Runtime has no vault mount.
OpenKnowledge reads and writes through the governed API. There is no new
`memoryd` service and no per-user daemon.

Communication-hub sends authenticated accepted-turn and completed-run facts.
It persists each fact before acknowledging the associated transport action
and retries until ToolHub records its idempotency key. ToolHub captures
provenance and applies private/group/sensitive policy. Communication-hub does
not read or write the vault, inject CORE, choose a memory owner or run a night
clock. It retains delivery identity, channel transport, outbox and receipts.

### D3. Exact run authorization

ToolHub binds memory calls to the current authenticated principal and run.
The run identity is out of model-controlled arguments; a missing, expired,
completed, mismatched or group run fails closed. A concurrent private owner
run cannot authorize another run. The shared `run-scope/current` file is
retired. Capture and reply events carry stable job IDs and are deduplicated.

### D4. ToolHub owns automation

The existing `internal/routines` engine and its durable schedules,
occurrences, assignments, watches and digests move together to ToolHub.
ToolHub owns the clock, admission and dispatch. It submits admitted work to
supervisor and delivery requests to communication-hub. The gateway verifies
transport destinations and sends, but does not schedule or execute routines.
Cutover stops the former owner before starting the new one; the old scheduler
is removed rather than retained as a fallback.

### D5. Dreaming is a protected system routine

Each enabled user's `config.yaml` declares the local nightly time. ToolHub
maintains one protected system routine per user, keyed by user and local day.
It reuses the routine clock and memory/night implementation, calls that user's
model relay, and checks current `memory.dreaming` plus memory enablement at
admission and before commit. User routine CRUD cannot edit it. A restart may
resume a failed day but may not apply it twice. Digests use the communication
delivery API; delivery downtime cannot stop the computation.

### D6. Governed edits

Memory tools execute after ToolHub's per-call decision. Changes to existing
memory require a proposal bound to exact user, diff, base revision and expiry;
only a verified human action confirms it. Confirmation is single use.
Proposed actions from nightly processing never become executable assignments
without owner authorization.

## Alternatives

An additional per-space memory service would duplicate lifecycle, enrollment
and scheduling. A ToolHub module uses the existing admission and audit point.
Keeping routines in communication-hub would leave the scheduler bound to
channel availability. Using a user-editable routine for dreaming would let
routine CRUD bypass the memory configuration and grant checks.

## Invariants

Hermes remains upstream. Provider mutations still require a concrete user
instruction. Secrets remain brokered. No logs or diagnostics expose content
or credential material. Unsupported or unavailable states fail closed with
bounded, safe status. Dev and prod storage remain separate.
