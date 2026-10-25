# CHG-0153: M16 memory-boundary

Chain: ADR-0074 -> SPEC-0071 -> this plan -> state.yaml. GitHub: #184.

## Goal
Complete the issue acceptance on an isolated worktree, without Docker or account mutations.

## Slices
- Map all callers, schemas, catalog entries and configuration.
- Add a discriminating regression before implementation; self-review assertions before execution.
- Implement the smallest complete behavior and update current docs.
- Run focused tests, docs checks and the full local Go gate.

## Red-team checklist
Foreign owner/context, revoked grants, traversal/symlinks, concurrent changes, restart/replay, bounded input/output.

## Acceptance
SPEC-0071 R3; issue #184. Real Docker/provider/channel acceptance remains unverified under the owner's explicit no-Docker instruction.

## Out of scope
Deployments, running stacks, external sending, publication, dependency additions and upstream Hermes patches.

## Implemented slices and regression evidence

- Confined memory list/delete and all backup file reads; shared-spool export rejects foreign/unattributed records.
- Honcho owner namespaces and disable cleanup; native state stays separate and is not migrated/deleted on grant withdrawal.
- Regression: contextlife memory/backup isolation, stack session-state grant and Honcho tests, communication.TestAgentMemoryReplyNeverEntersHostContentLog; existing diagnostics owner/trace denial suites.

## Delivery boundary

Full-gate compatibility: the inherited OAuth changes already keep a live
Broker form until actual expiry, while an old test still expected early
replacement. `TestCredentialBrokerLiveRequestNearExpiryIsKept` now asserts
reuse and forbids a second pending request. No Broker production behavior
was changed by this milestone work; the original checkout is untouched.

Two baseline test fixtures were made deterministic during the full gate:
secret-file delivery now selects the named TLS credential instead of the
last map element, and read-only Docker version probes have a 30-second
shared deadline instead of five seconds. No secret-service or Docker
production behavior changed. The secret fixture passed ten race-enabled runs.

Implementation is isolated in codex/m16-runtime-discipline. Focused Go contracts and the full `just check` gate passed on 2026-10-21, including race-enabled tests, 86.15% own Go statement coverage, package floors, lint, GoSec, documentation and configuration contracts. Local contract implementation: 100%. Docker, real Hermes/provider/channel acceptance and publication remain unverified and are not inferred from fixtures. CHG remains in progress pending live acceptance; no GitHub issue was closed.
