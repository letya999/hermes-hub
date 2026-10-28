# CHG-0182: Image budget gate & baseline (M26 stage 0)

Chain: `docs/adr/ADR-0083-lean-images-capability-packs.md` D1/D2 ->
`specs/active/SPEC-0082-image-role-budgets.md` R0 -> this plan -> `state.yaml`.
Evidence: `research.md`. GitHub issue: #371. Next stage: `CHG-0183`.

## Goal

The image stopped growing unnoticed only because nothing measures it. This
stage freezes the audit baseline and makes `devcheck image-budget` fail on
size or forbidden-binary violations, wired into the CI `image` job.

## Read before coding

1. `research.md` (this folder), `docker/Dockerfile`,
   `internal/devcheck/devcheck.go`, `justfile` (`docker-check*`, `image`),
   `.github/workflows/*` image job.
2. Open issues: #345 (lazy boot), #342 (RuntimeProvider).

## Preconditions and rules

- Never reset or commit other tasks' uncommitted work.
- Red test first, then fix. Go stdlib only unless an ADR allows a dependency.
- `just check` before handoff; Docker gates when image/runtime code changes.

## Slices

### S0.1 devcheck image-budget (R0.2)

Anchors: `internal/devcheck/devcheck.go` check table;
`cmd/devcheck` subcommand registration.

Change:
1. New check `image-budget <image> <role>`: reads the image, fails on
   size-over-budget or forbidden binary (chromium, node, python, uv, glab in
   core/control; any setuid file). Missing image = fail.
2. Budget map per role in one table (SPEC-0082 SLOs).

Tests (red first):
- `TestImageBudgetRejectsForbiddenBinary`: fixture report with `chromium`
  in a core image fails.
- `TestImageBudgetRejectsOversize`: over-budget size fails.
- `TestImageBudgetMissingImageFails`: no image = fail-closed.

### S0.2 CI wiring (R0.3)

Anchors: `.github/workflows/` image job; `justfile`.

Change:
1. Run `devcheck image-budget` for each built role image in the `image` job.

Tests (red first):
- `TestImageJobRunsBudgetGate` (workflow YAML assertion or devcheck list).

### S0.3 Baseline freeze (R0.1)

Change:
1. `research.md` already holds the audit table; extend with Trivy/Grype
   counts per image (run or document as pending if scanner unavailable).

## Red-team checklist

- Renaming `chromium` to `chrome` still fails (match on ELF paths/known set,
  not literal name only).
- Role images not yet built (pre-M26.3) get the documented "pending role"
  verdict, not a pass.

## Acceptance

- A0: `devcheck image-budget` exists, fails on violations, runs in CI.

## Out of scope

- Dockerfile splits, browser extraction, voice removal.

## Done checklist

- [ ] Tests red then green; `just check`.
- [ ] Red-team and live acceptance recorded in state.yaml.
- [ ] Issue #371 updated and closed; state done; folder moved to `.work/done/`.
