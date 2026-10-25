# CHG-0155: M16 group-scopes

Chain: ADR-0074 -> SPEC-0071 -> this plan -> state.yaml. GitHub: #207.

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
SPEC-0071 R5; issue #207. Real Docker/provider/channel acceptance remains unverified under the owner's explicit no-Docker instruction.

## Out of scope
Deployments, running stacks, external sending, publication, dependency additions and upstream Hermes patches.

## Implemented slices and regression evidence

- Dedicated group init/scope/home, opt-in Telegram group/member mapping, bounded passive transcript and mention-only activation.
- Bounded source-event receipts, stable duplicate admission, no personal identities/runtime aliases, scoped forum session commands.
- Actor/audience-bound approval; current member/policy rechecked before dispatch, delivery and queued approval (revocation becomes cancellation).
- Regression: communication group suites; stack group scope; supervisor group binding/current-policy and runtime group identity tests. Threat model updated.

## Delivery boundary

Implementation is isolated in codex/m16-runtime-discipline. Focused Go contracts and the full `just check` gate passed on 2026-10-21, including race-enabled tests, 86.15% own Go statement coverage, package floors, lint, GoSec, documentation and configuration contracts. Local contract implementation: 100%. Docker, real Hermes/provider/channel acceptance and publication remain unverified and are not inferred from fixtures. CHG remains in progress pending live acceptance; no GitHub issue was closed.
