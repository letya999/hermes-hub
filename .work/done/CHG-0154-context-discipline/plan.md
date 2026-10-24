# CHG-0154: M16 context-discipline

Chain: ADR-0074 -> SPEC-0071 -> this plan -> state.yaml. GitHub: #196.

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
SPEC-0071 R4; issue #196. Real Docker/provider/channel acceptance remains unverified under the owner's explicit no-Docker instruction.

## Out of scope
Deployments, running stacks, external sending, publication, dependency additions and upstream Hermes patches.

## Implemented slices and regression evidence

Revised 2026-10-24 per owner decision: dropped per-job fresh sessions and the
hub-held continuity/trace cache (duplicated state Hermes already owns, broke
in-loop compaction and native `/goal`, and replaced the explicit `/new`/`/use`
boundary).

- Stable conversation session across jobs and retry attempts; new session only via explicit `/new`/`/use`.
- Exact admission receipts retained for metadata-only observe/readmit recovery of the original request.
- Every relayed chat-completions request passes a runaway token ceiling (256k estimated, above the managed compaction trigger) refused pre-send with observable 413.
- Regression: runtime session-stability test, token-budget ceiling/ordering tests, admission recovery suites.

## Delivery boundary

Implementation is isolated in codex/m16-runtime-discipline. After the 2026-10-24 session-continuity revision the full `just check` gate passed (86.14% own Go statement coverage, package floors, lint, GoSec, docs, config contract) and `go test -race ./internal/runtime` is green. Docker, real Hermes/provider/channel acceptance and publication remain unverified and are not inferred from fixtures. CHG remains in progress pending live acceptance; no GitHub issue was closed.
