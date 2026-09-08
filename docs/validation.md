---
description: Measured 0.2.0 coverage and explicit runtime validation boundaries.
last_verified: 2026-09-08
---
# Delivery evidence — 0.2.0

`just check` passed on Windows: native Go race tests, CLI integration, native bridge
subprocess/HTTP MCP, file-boundary and HH contract tests, Go runtime-supervisor tests,
formatting, vet, staticcheck, documentation and GitHub workflow lint.

| Coverage scope | Measured | Required |
|---|---|---|
| All original Go statements, including runtime and packaging | 85.12% | >=85% |

Upstream Hermes/connector source and test code do not inflate the coverage denominator.
The Go coverage parser accepts valid zero-statement profile rows emitted on Windows.

`just security` passed: Go vulnerability check and browser npm audit reported no
known vulnerabilities. This does not scan every upstream Python or OS dependency.
Both all-feature dev/prod Compose configurations were parsed using Compose 2.40.3:
only agent is started, runtime volumes differ, dev source mounts exclude spaces/root
and prod has no source mounts. Hosted Actions pins resolve to official upstream refs.

Previously installed pinned Hermes/Google/Telegram virtualenvs and a real Hermes MCP 2
client verified basic compatibility with the Go stdio server. The 0.2.0 local suite
verifies generic MCP configuration and the native bridge without real account credentials.

`just build` and a Linux/amd64 cross-build passed. Docker Engine 29.4.3 was available,
but the cold prod image build stalled while pulling the pinned uv base image from GHCR;
it was interrupted after sustained zero network progress. Neither prod nor dev runtime
smoke is therefore claimed as passed. Re-run `just docker-check prod` and then dev; the
downloaded layers remain cached. Real Telegram/Google/HH/Slack/Meet/native
OS access requires owner authentication, account permissions and live verification.
No application or message was sent and no private account was read during testing.

No GitHub repository or release was published. Actions and a trusted self-hosted Runner
workflow are prepared for the owner's repository. Treat image and account checks as
required deployment acceptance, not as implicitly passed by the unit tests.
