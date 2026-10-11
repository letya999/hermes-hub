# SPEC-0050: M10 stabilization amendment

Frozen: 2026-10-11. Owner authorized the remediation plan. This supplements
SPEC-0047, SPEC-0048 and SPEC-0049 without rewriting their frozen history.

1. Delivery preserves originals. Inbound receipt and isolated per-ID extraction
   make retries idempotent; incomplete pre-existing targets are never removed.
2. Archive limits apply to actual bytes and narrow filesystem grants apply to
   every input. Multiple images are compared in one request (maximum eight).
3. One writer owns a spool. Offline apply requires an exclusive lock, complete
   prevalidation, explicit routing and strict YAML. It is additive, not pruning.
4. Revision and owner checks fence work before execution and delivery. Deleted
   schedule IDs stay retired. Already sent effects cannot be recalled.
5. Proactive work is bounded (two workers, sixteen admissions, one per source).
   HTTP probes reject private connections, redirects and oversized responses.
   GitHub issue state is a verified public read contract; regex misses are errors.
6. Canonical result receipts recover partial queue handoff without refetching or
   rerunning a completed script. Digest cursors advance only reported sources.
7. Scripts run outside the control process with memory, CPU, wall-clock, PID,
   filesystem and network limits. No host in-process fallback is allowed.
8. Knowledge context derives from authenticated enrollment. Read-only local
   snapshots expose bounded search results with provenance; tokens and enabled
   owners are rechecked. New provider integrations need their own verified contract.
9. Usage reports distinguish measured deltas, first snapshots, legacy counters,
   resets and unavailable measurements. Background measurement never wakes a
   stopped runtime. Journal corruption must not silently discard complete records.
10. Mock attachments stay in owned local receipts. Deterministic commands retain
    Telegram topic identity. Idle TTL starts after the final activity lease ends.

Acceptance requires regression cases for isolation, authorization, concurrent
writers, cancellation, recovery, HTTP limits, cumulative counters and container
constraints; just check must pass at >=85% own statement coverage. Actual Docker
and live channel evidence are reported separately from mocks.
