# CHG-0158: M16 knowledge-lifecycle

Chain: ADR-0074 -> SPEC-0071 -> this plan -> state.yaml. GitHub: #249.

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
SPEC-0071 R8; issue #249. Real Docker/provider/channel acceptance remains unverified under the owner's explicit no-Docker instruction.

## Out of scope
Deployments, running stacks, external sending, publication, dependency additions and upstream Hermes patches.

## Implemented slices and regression evidence

- Read-only scoped snapshots and legacy files on both Hub and direct MCP; virtual nested names, line/hash/time provenance and explicit snapshot freshness.
- Refresh removes missing docs; stale/unavailable are explicit; absent removes derived snapshot bytes. Namespace and real symlink/path tests deny cross-context access.
- 32 MiB context/apply bounds, 9 MiB document bound and 128 documents per source; apply input preflight precedes writes.
- Raw-file and snapshot revisions deny new access at the next binding resolve; revoke is immediate. Previously delivered replies stay in the session history Hermes owns and are not rewritten. E2E-085 now points at the real lifecycle contract rather than the removed endpoint test.
- Regression: agenttools knowledge lifecycle/context/limits/transport suites; cmd/hubctl folder/apply preflight suites.

## Delivery boundary

Implementation is isolated in codex/m16-runtime-discipline. Focused Go contracts and the full `just check` gate passed on 2026-10-21, including race-enabled tests, 86.15% own Go statement coverage, package floors, lint, GoSec, documentation and configuration contracts. Local contract implementation: 100%. Docker, real Hermes/provider/channel acceptance and publication remain unverified and are not inferred from fixtures. CHG remains in progress pending live acceptance; no GitHub issue was closed.
