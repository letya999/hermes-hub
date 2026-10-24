---
description: Engineering rules — Go conventions, gates, pins, change process, boundaries on dependencies.
last_verified: 2026-10-20
---

# Engineering rules

## Language and layout

Go for hubctl, MCP tooling, runtime supervision and dev checks. Layout:
`cmd/<binary>` entrypoints, `internal/<domain>` packages, leaf packages
with parser-enforced import boundaries (`boundary_test.go`) — the import
direction is fixed by test, not convention. `services/credential-broker` is
a separate copied Go module (MIT) kept out of the flatten.

## Gates

- `just check` before delivery: fmt/vet/staticcheck, race+shuffle tests,
  docs checks (`devcheck docs` — numbering/index/verified/size),
  actionlint, coverage ≥85% on owned code.
- `just security` when dependencies change (govulncheck + npm audit).
- `just docker-check` / `docker-check-prebuilt` for image-side change.
- Report an unavailable gate as unavailable — never claim it by proxy.

## Pins

- Hermes `0.21.0` @ `869228cab` (ADR-0012). CLIProxy
  `ba7e55836dee959e93ec6d41395865d9ec535086`. ToolHive v0.48.0 / cellinit
  per ADR-0013. Full pin table: docs/integrations.md.
- Go toolchain pinned uniformly across host and image builds.
- Staticcheck pinned to upstream commit `452d5bb86b45`
  (v0.7.0-0.dev.0.20261009230814-452d5bb86b45): release v0.8.1 cannot
  decode this toolchain's export data — the compatible upstream tool is
  used without disabling checks.

## Change process

- Significant changes: `.work/in-progress/CHG-NNNN-name/{plan.md,
  state.yaml}`; milestones follow `.work/milestone-playbook.md`.
- Accepted ADRs are append-only — amend by a new ADR, never rewrite.
- SPECs are frozen requirements; the code must match them or the spec
  says what is intentionally deferred.
- Regression tests are mandatory for file-isolation, concurrency,
  authorization or HTTP-contract changes.
- Red→green where the probity hook applies: behavioral assertion failure
  first, then the implementation.

## Dependency and code hygiene

- No unsolicited framework/database/queue dependencies; stdlib first.
- Never commit deployments, tokens, browser profiles, transcripts or real
  personal data; secrets materialize only through the broker/secret store.
- Docker hygiene: build via `hubctl build`/`up` or `just docker-check`
  (BuildKit-forced paths); `just docker-clean` after other local builds,
  `--deep` only for an intentional cold reset; no broad system/volume
  prunes.
- Optional integrations stay out of the base image; features are opt-in
  env/feature flags.

## Verification of your own edits

Re-skim the file section you touched against the code it describes; bump
`last_verified` only when the content is actually confirmed against the
current tree — the devcheck docs gate rejects stale living docs.
