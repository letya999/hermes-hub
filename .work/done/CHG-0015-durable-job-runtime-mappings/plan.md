# CHG-0015: Durable job, session, run and runtime mappings

## Goal

Make the accepted-job to context/runtime/Hermes session/run mapping survive process
and container restart while preventing duplicate execution and stale-generation writes.

## Delivery

1. Freeze SPEC-0014 and ADR-0015.
2. Persist communication job and conversation mappings in the existing spool.
3. Return and record Hermes session/run/status/generation metadata.
4. Persist supervisor state and fence leases by generation.
5. Add restart, idempotency, isolation, terminal-status and bad-state tests.
6. Run repository/security/Docker gates and record evidence.

## Non-goals

No database, new queue, multi-host coordinator, scheduler, provider integration or
Hermes fork.
