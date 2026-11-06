# Contributing

Toolchain: Go 1.27.3, just 1.57+ and Docker Compose 2.30+ for container contracts.

Local feedback:

- `just check`: formatting, docs and `go vet`; no full suite, race instrumentation,
  coverage, security downloads or Docker builds. Target: under five minutes with
  a warm Go cache. Record actual timings; cold compilation is not a passing result.
- `just test ./internal/<changed-package> ...`: affected packages and callers.
  Go's test cache remains enabled. Run explicit race tests for concurrency changes.
- Run only one repository-wide check at a time on the shared machine.
- `just check-full`: exhaustive local fallback when intentionally needed.

CI keeps the four race/shuffle/coverage shards, >=85% own-code / >=75% internal-package
coverage, lint/security, broker and deployment contracts. `checks` and `containers`
remain mandatory acceptance results. Do not run all of CI again after a small local edit.

After an authorized push/PR, use `just ci-status <full-sha>` and
`just ci-watch <run-id>`. A published feature branch without an automatic run can
use `just ci-run <branch>`; this explicit action starts the full workflow and
publishes GHCR images. It cannot test uncommitted or unpublished code. Record the
actual run SHA, including the synthetic merge SHA for a PR, and run URL in the CHG.
A PR merge image is not automatically an image of the local branch HEAD.

`just docker-check [prod|dev]` consumes images for exact HEAD, with no stale `edge`
fallback. Container scenarios still take time; run them in CI for routine acceptance.
`just docker-check-build [prod|dev]` explicitly builds all seven local images first.
Never build or prune the shared development stack from parallel lanes.
`just security` runs Go vuln and npm audits for dependency changes. Node/npm is
needed for that audit but is not required by the fast local gate.

`just release` creates an allowlisted source archive and native CLI binaries.
Never include spaces, tokens, archives, personal messages or browser profiles in a release.
The manual self-hosted workflow is restricted to main and uses `check-full` plus
`docker-check-build`; do not point it at a personal agent VPS.

Branch flow: `dev` is the default integration branch. Do not commit directly to `dev`;
create a short-lived typed branch from `dev`, then open a PR back to `dev`.
`main` is the protected release branch: changes reach it only through a PR whose source
branch is `dev`. Keep both protected branches free of direct pushes.

Working branch names must use a Conventional Commit type: `feat/`, `fix/`, `chore/`,
`docs/`, `test/`, `refactor/`, `perf/`, `build/`, `ci/`, `revert/`, or `hotfix/`.
The repository hooks reject direct commits/pushes to `main` and `dev`, non-conventional
commit subjects, local Git identity overrides, and all `Co-authored-by` trailers. Enable
them with `git config core.hooksPath .githooks`.

Go modifications belong in cmd/internal; runtime process changes in internal/runtime.
Follow AGENTS.md and Memory Bank's rules/docs/ADR/spec/plans separation. Add behavior
regressions and preserve existing user assets on rendering. Accepted ADRs are immutable;
replace a decision with a successor. Source is AGPL-3.0-only; preserve upstream notices.
