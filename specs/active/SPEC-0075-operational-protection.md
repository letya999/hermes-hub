# SPEC-0075: M17 operational protection and code closeout

Draft: 2026-10-25. Decision: ADR-0076. Delivery: CHG-0164.
Supplements SPEC-0076 without rewriting its frozen requirements.

- R1 Original surfaces satisfy #141/#195/#197/#198/#206: durable audit,
  measured runtime duration, pre-mutation validation, delivery-aware watch,
  immutable retained rollout images, isolated reproducible replay and useful diffs.
- R2 Owned resources (#326) have trusted scope, generation, consumers, reason,
  expiry and health; shared cancellation releases only its demand; bounded
  deterministic recovery preserves expected waits and unmanaged resources.
- R3 Budgets (#327) reserve atomically across lineage and scopes, settle once,
  survive restart, expose unknown costs, bound queues and preserve control
  capacity and background progress. Capacity claims require measured profiles.
- R4 Master containment (#329) persists holds, blocks new and stale work/effects,
  preserves unrelated consumers, requires administrative resume, and durably
  queues a safe owner notice independently of the stopped runtime.
- R5 Personal supervision (#328) wakes only for bounded actionable metadata,
  coalesces events, runs once per user, cannot widen authority/budgets, records
  expiring reasoned decisions, and escalates failure without disabling R2/R4.

## Acceptance boundary

Each requirement needs race/restart/isolation/authorization regression evidence
and the applicable project gates. Docker and real Telegram/provider/LLM evidence
are separate pending gates; no issue is called fully live-accepted without them.
