---
description: Ownership labels on every hub-spawned resource, a persisted finalizer ladder on connector_remove, one label-scoped reconciler, verified-zero removal receipts, and store compaction for terminal records.
last_verified: 2026-10-21
---
# ADR-0056: Ownership labels, finalizers, reconciler

Status: Accepted 2026-10-19. Implements GitHub issue #285 (M13 P4).
Builds on [ADR-0055](ADR-0055-unified-connector-surface.md) (operation
journal, typed codes) and [ADR-0054](ADR-0054-human-action-surface.md)
(one-time human links for remove/reauth).

## Context

Connector removal was a best-effort sequence: workloads were released, then
credentials, then workspace — with errors swallowed and no record of where a
cleanup stopped. A crash mid-remove left labelled and unlabelled residue with
no way to resume, and nothing distinguished hub-owned objects from foreign
Docker resources sharing a host.

## Decision

- **D1**: Every spawned resource carries ownership labels —
  `hermes-hub.owner`, `hermes-hub.connection`, `hermes-hub.binding`,
  `hermes-hub.operation`, `hermes-hub.recipe` — on containers, volumes,
  networks, bounded-cli cells and (as a marker file) workload workspaces.
- **D2**: `connector_remove` marks the connection `deleting`, cuts
  authorization first, then runs a persisted finalizer ladder —
  workloads → volumes → network → broker credential → ciphertext →
  workspace → image reference → verify-zero. Each step is idempotent and
  recorded on the onboarding; a retry resumes at the first incomplete step
  and `cleanup_incomplete` names the stuck finalizer.
- **D3**: Removal completes only after a label-scoped verify-zero sweep; the
  receipt (`removed: N container, M volume …; verified 0 remaining`) lives on
  the durable operation and surfaces through `connector_status`.
- **D4**: One reconciler pass (`Reconciler.ScanOnce`) purges orphans whose
  owner connection is absent, marks missing-credential or drifted
  (recipe digest, tool-surface digest) connections `degraded` with an owner
  notice — never silently re-minting secrets — inside a bounded scan rate.
- **D5**: A connection revoked while a readiness probe runs outside the
  store lock wins at commit: the reserved binding is dropped and the enable
  fails, so dead authority is never reactivated.
- **D6**: Terminal records are compacted on snapshot writes — removed
  onboardings after 30 days, done/failed operations after 7 days — keeping
  the file store bounded without a database.

## Consequences

Label-filtered queries can only ever return hub-owned objects, and the purge
path re-proves the ownership label before deleting. Removal is restart-safe
and observable. The reconciler heals drift by degrading and notifying, never
by recreating credentials. Runtime wiring of the label seams into the served
endpoint and periodic reconciler scheduling are deployment-side concerns
tracked in the change record.

Implementation note (2026-10-21): the served ToolHub now wires Docker label
query/purge with both project and connection checks. Confirmed removals resume
at startup and retry every 30 seconds, including a crash before the finalizer
list was recorded. The general orphan/credential/surface reconciler still
needs production scheduling and live acceptance (CHG-0147).
