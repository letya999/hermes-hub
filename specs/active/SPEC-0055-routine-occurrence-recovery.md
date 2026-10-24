# SPEC-0055 — Routine occurrence recovery

Status: active amendment to SPEC-0013, SPEC-0048 and SPEC-0053. Those frozen contracts remain unchanged.

## Requirements

1. A schedule revision with an unresolved pending, probing or scripting occurrence, or an agent occurrence whose job mapping is not terminal, does not create another occurrence on later due times. The scheduler advances its next due time as before. A terminal occurrence permits future due times.
2. A historical terminal job mapping cannot halt dispatch because its fingerprint differs from current derivation. A nonterminal mapping mismatch blocks additional work for that schedule but does not block independent schedules. Evidence remains available for diagnosis.
3. A malformed occurrence is quarantined with its ID, never dispatched, and never recreated through the same ID while quarantine evidence exists. Other valid occurrences remain processable.
4. Settled silent occurrences leave the hot directory during the hourly compaction pass. Full detail remains in an archive for at least 48 hours after its last write; minimal deduplication receipts live for the normal spool retention period (14 days by default). Pending, probing and scripting records remain in the hot directory. Dispatched records stay there while a job or delivery is active; after completion they may follow the same archive and receipt policy.
5. Routine failure logs include the stage and a bounded cause. Existing one-hour catch-up and 16-per-pass limits remain in force.

## Acceptance

Concurrent tick and dispatch calls do not create overlapping jobs for one schedule; replay after quarantine or compaction cannot create a second occurrence; unrelated due work proceeds past a corrupt record; active work and outbox evidence survive compaction.
