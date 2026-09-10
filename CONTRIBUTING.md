# Contributing

Toolchain: Go 1.27.1, just 1.57+ and Docker Compose 2.30+. Node/npm exists only inside
the image build for the pinned upstream Playwright MCP package. Run `just check`; it
orchestrates Go race and integration tests, >=85% statement coverage for all original
Go code, formatting, vet/staticcheck, docs and actionlint. Upstream code is excluded.

`just security` runs Go vuln and npm audits. `just docker-check prod` and
`just docker-check dev` build and run credentials-free container smoke. Keep package
locks and source pins together; scan final images for upstream Python/OS vulnerabilities.
`just release` creates an allowlisted source archive and native CLI binaries.
Never include spaces, tokens, archives, personal messages or browser profiles in a release.

GitHub CI runs the same Just gate and both Docker targets. It does not publish. The
manual runner workflow targets labels self-hosted, linux,
x64, hermes-hub and only main. Register a dedicated runner through repository Settings
-> Actions -> Runners; install the same toolchain. Use a disposable runner for PR jobs;
this workflow does not run untrusted pull requests on your personal VPS.

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
