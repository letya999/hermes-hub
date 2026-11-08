---
description: Frozen issue-sized contracts for remaining M32 passport configuration work.
last_verified: 2026-11-07
---
# SPEC-0097: M32 configuration slices

Frozen: 2026-11-05. Implementation incomplete.
Decision: [ADR-0097](../../docs/adr/ADR-0097-m32-bounded-delivery.md).
Current worker contract for #508–#512; read only the assigned row plus invariants.
Trace: SPEC-0095 R1–R3/A1–A5. Historical text need not be loaded for routine work.

## Invariants

Exactly six authored inputs under `spaces/<user>/passports/`:
stack/config/models/workspace/tools/grants.yaml; templates use the same six.
No agent.yaml, self-settings, passport-src or other agent-config fallback,
conversion or synchronization. Protected issued-rights ledger, secrets,
operational connection records and generated outputs are distinct state.
Platform/build manifests are not agent configuration and are not deleted by suffix.

All consumers use a validated immutable published revision. Missing, malformed,
unknown fields or unpublished inputs fail closed; arbitrary draft/file writes do
not activate configuration. Each passport edit requires its own current authority.
Selection never grants rights; revocation never edits selection. Issued grants
must remain inside platform/org ceilings. tools.use and tools.configure are
independent; memory operations also require config.memory.enabled and their
separate grants. Capture/dreaming do not depend on model tool visibility.

## Atomic assignments

| Issue | Deliverable and code boundary | Acceptance / regression | Excluded |
|---|---|---|---|
| #509 C1 | Complete authored scopes through migration.selectedHubTools → CapabilitySelection → capability_store admission/executor. Reuse existing PathScopes/NarrowSelectionScopes. | Allowed path succeeds; sibling/traversal/symlink escapes, stale scope, malformed args and direct/indirect bypass fail. Set/member restrictions intersect grants. Unsupported host/URL or executor scope fails publication explicitly; never silently unscoped. TestPublishedToolScopeConstrainsCall plus authored-to-executor coverage. | Publisher transaction, connector selection, memory. |
| #510 C2 | tools.yaml is the only selector for connector/raw-MCP tools. Keep operational connection/credential state separate. | Removing selection blocks existing sessions without deleting credentials or changing grants. Reapply cannot resurrect selections from old profiles. | OAuth/provider changes; scope engine. |
| #511 C3 | Coherent activation in passport publisher, generated view, bound profile and consumer startup. One activation identity; immutable materialization before activation; readers reject mismatches. | Concurrent readers never admit mixed revisions. Failure before activation leaves old active state; consumers cannot use an uncommitted generation. | Recovery replay after crash (#512), settings transport (#508). |
| #512 C4 | Durable recovery of interrupted #511 activation and service effects. | Fault at each durable boundary, restart and repeated recovery converge or report blocked safely. Concurrent revokes survive rollback; unrelated profiles unchanged; incomplete service rollback is failure. | New config fields or grant issuance. |
| #508 C5 | After #511/#512: SettingsGet/SettingsSet use authenticated governed publisher, current passport edit right and expected base revision; remove overlay reader/writer in hermesconfig/supervisor/runtime callers. | Authorized edit survives restart; unauthorized or stale-base edit changes nothing; poisoned overlay/legacy file is inert. No runtime host mount/shell escape to publish. Settings read reports published/effective state honestly. | Rewriting publication/recovery, feature additions. |

C1 reuses existing code; it is not a request to rebuild scope enforcement.
C3 is a generation/coherence guarantee, not a promise of an atomic Docker operation.
External effect failure is visible and governed by C4 before readiness is claimed.

## Delivery gates

Caller/fixture/docs updates belong to the same issue. Tool contract changes
include descriptions, prompt_contract, docs/tools + docs/routines and an immutable
catalog bump; rebind/respawn the existing `artem_main` and `artem_two`
managed spaces when validating projection. Fault tests use isolated data.
User-facing text uses en/ru/kk/es locale keys; diagnostics allowlist safe fields.
Run scoped regressions, just check and applicable AGENTS.md gates.
#526 proves two-user live behavior; #527 verifies final absence of retired paths.
