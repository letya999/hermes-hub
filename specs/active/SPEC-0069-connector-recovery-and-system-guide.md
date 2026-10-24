# SPEC-0069: Connector recovery and system guidance

Frozen: 2026-10-23. Implementation status: CHG-0149.
Decision: ADR-0072.

## Requirements

- R1: Diagnose connection references under their verified owner/context with
  TLS verification, bounded safe output and no secret/token/key disclosure.
  Establish current incident evidence before claiming deletion or restoring.
- R2: Preserve encrypted metadata/provider state across normal restart; retain
  strict corruption rejection. Detect replacement of initialized primary state.
- R3: Independently durable encrypted recovery includes consistent provider
  versions, contracts and revocations, with a separately protected recovery key.
  Acknowledged local credential/revocation changes must be recoverable.
- R4: Recovery is owner/environment/version-bound, idempotent, resumable and
  fenced against concurrent revoke/delete, stale backup and removed membership.
  Never restore leases; reacquire under current policy. Never expose plaintext
  through control APIs or place secret values in ledger DTOs.
- R5: Reconcile typed health causes, revisit recoverable degraded state, bound
  retries and isolate per-connection failures. Broker outage does not mean loss.
- R6: One caller-scoped inventory powers list/status/channel surfaces. Include
  transport, ownership, onboarding, connection, admission, workload, last verified
  provider evidence and permitted recovery action. History/catalog are distinct.
- R7: Ship an English system guide covering all agreed terms, philosophy,
  execution surfaces, source/runtime navigation, inventory/use/recovery workflows
  and evidence rules. MCP queries also inspect CLI/API/internal groups, label
  accurately, and honor explicit MCP-only filtering.
- R8: Deliver the same guide to all new/existing dev/prod runtimes, managed and
  unmanaged, Compose and supervisor. A native skill loader is not required.
  Keep core routing guidance always present and preserve user persona content.
- R9: Rotate disclosed service credentials through supported operational paths;
  contain diagnostic key copies without exposing them. External account login,
  provider mutation and sending remain separately authorized actions.
- R10: Document operation, recovery limits, key rotation/retention, status meanings
  and rollout. Run root and Broker quality gates, required coverage and isolated
  container failure/recovery acceptance before claiming completion.

## Acceptance

- A1: Current incident matrix distinguishes verified loss, owner mismatch, stale
  reference, wrong Broker instance and uncertain cases without secret output.
- A2: Real isolated persistence tests cover restart, kill, primary-volume loss,
  invalid key, corrupt backup, interrupted writes and encrypted orphan detection.
- A3: Cross-owner/environment/team restore, stale backup after revoke, concurrent
  restore/delete and removed-member access fail without resurrection or disclosure.
- A4: Recovery yields fresh authorized leases and successful safe provider probes;
  unrecoverable cases retain actionable state and protected reauthorization.
- A5: Inventory is consistent across all entrypoints for two users and a team,
  including historical, disabled, revoked, degraded and pending connections.
- A6: Every runtime variant can read the full guide; behavioral scenarios verify
  term classification, current-identity use, explicit filters and honest evidence.
- A7: Root/Broker tests and coverage, docs gates, container acceptance and any
  unavailable live gates are recorded individually. Mock success is not live proof.
