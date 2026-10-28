# CHG-0214: Executable standing assignments (M23 #324/#332)

Chain: `docs/adr/ADR-0082-standing-assignments.md` ->
`specs/active/SPEC-0086-standing-assignment-mvp.md` -> this plan ->
`state.yaml`. Previous stage: CHG-0189.

## Goal

An owner can delegate a durable goal. Deterministic cursor reads and filters
discard irrelevant changes; only matching batches wake a specialist. The
specialist records findings and next steps, acts within granted tools, and
retires the assignment when its completion condition is met.

## Slices

1. Add a non-consuming, paged Telegram message read; use per-source cursor
   feedback, fail-closed filter verdicts and replay after sink overflow.
2. Bind assignment generations to jobs and deliveries, fence at claim/send,
   and persist bounded progress from a currently running trigger.
3. Bound global stream concurrency; fix daily reset, urgent behavior,
   sample safety and grant widening.
4. Ship agent catalog 5.3.2, prompt contract, tool/routine docs and tests.
5. Verify `just check`, Telegram MCP tests and local integration fixtures;
   then rebind/respawn managed dev space and run live acceptance when the
   connector pin and external-send authorization allow it.

## Acceptance criteria

- A 10-event page with sink cap 8 replays 2 without duplicate effects; the
  source cursor advances only after all 10 settle.
- Bad filter, missing/expired cursor and cancelled assignment fail closed.
- Irrelevant Telegram messages cost zero model wakes; one matching burst
  creates one job with text and source attribution.
- Progress can only come from the assignment's running job, survives restart,
  and completion retires all sources.
- A hundred watches remain bounded in concurrent connector calls; no
  zero-delay poll loop exists.
- Catalog, docs and prompt agree; local and Docker gates are reported
  separately, with no mock test promoted to live evidence.

## Multi-source recovery acceptance

The 2026-10-28 owner message is represented by draft
`asg-c9a417fa36ba34ccc46dad63f5abb91a`. After the 5.3.3 rebind and
service rebuild, apply one 14-input cursor watch at 60 seconds; record the
seven closed/unverified references in `plan.unavailable`. Verify the filter
with read-only sampling, cursor state and restart replay. Do not inject
Telegram messages or join private communities as a test. The broker's
lossless physical journal compaction is a separate reliability gate.
