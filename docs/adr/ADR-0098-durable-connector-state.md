---
description: Canonical connector state, versioned notification intent and incremental registry commits.
last_verified: 2026-11-12
---
# ADR-0098: Durable connector state and reconciliation

Status: accepted. Date: 2026-11-12. Refines ADR-0091; implemented by CHG-0298.

## Context

Credential acceptance, an install journal and channel notification could disagree.
Status reads performed provider work, while repeated catalog binding accumulated
profile history and charged a complete registry snapshot to a small mutation.

## Decisions

1. Onboarding is canonical. Install progress is derived from it. The store guards
   immutable ownership, legal phase transitions and monotonic revisions; removal
   fences late results before external cleanup starts.
2. Broker's existing event feed drives reconciliation with a durable owner-scoped
   cursor and gap repair. Provider admissions use per-onboarding leases, deadlines
   and bounded retries. Transient failure preserves the accepted credential.
3. Status reads saved state. Explicit resend owns link renewal. Persist notice
   intent with the transition, deduplicate replay and suppress older notices at
   delivery. Waiting for a human does not schedule a redundant model turn.
4. Confirmation action revisions exclude delivery/observation metadata. A spent
   nonce stays spent; verified consent may resume the unchanged failed action.
   A queued notice is distinct from a persisted provider delivery receipt.
5. Catalog enable is idempotent. Real changes append one target profile revision
   and retire only unreferenced digest-specific allows. Immutable history remains.
6. SQLite updates current rows by key and appends new history rows. Admission
   budgets changed rows and bounded page overhead. Integrity and stale-writer
   fences remain mandatory; the next-write limit remains 32 MiB.

## Consequences

No new queue, database or service is introduced. The registry digest records its
format as `rows-v1`; legacy snapshots are verified before conversion. Rollout
requires a native backup and matching service/CLI readers. An older reader needs
its matching backup for rollback. Protected owner approval and a real provider
read remain independent integration acceptance steps.
