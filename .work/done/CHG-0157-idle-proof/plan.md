# CHG-0157: M16 idle-proof

Chain: ADR-0074 -> SPEC-0071 -> this plan -> state.yaml. GitHub: #214.

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
SPEC-0071 R7; issue #214. Real Docker/provider/channel acceptance remains unverified under the owner's explicit no-Docker instruction.

## Out of scope
Deployments, running stacks, external sending, publication, dependency additions and upstream Hermes patches.

## Implemented slices and regression evidence

- Existing typed execution/stream/approval leases cover long-running tool work; no speculative activity sampler added.
- Regression: supervisor.TestReaperNeverStopsLeasedRuntime, TestReaperKeepsIdleRuntimeWithLiveJob and supervisor.TestM10IdleDeadlineStartsAfterActivityFinishes, plus existing uncertain/approval lease tests.

## Delivery boundary

Implementation is isolated in codex/m16-runtime-discipline. Focused Go contracts and the full `just check` gate passed on 2026-10-21, including race-enabled tests, 86.15% own Go statement coverage, package floors, lint, GoSec, documentation and configuration contracts. Local contract implementation: 100%. Docker, real Hermes/provider/channel acceptance and publication remain unverified and are not inferred from fixtures. CHG remains in progress pending live acceptance; no GitHub issue was closed.
