---
description: Measured CHG-0009 validation evidence and unverified boundaries.
last_verified: 2026-09-11
---
# Delivery evidence — CHG-0009

The local `go test ./...` suite passes on Windows. It covers peer scope initialization,
kind/ID/path checks, organization policy, persistent service mount boundaries, runtime
HTTP authentication and replay, cross-scope queued jobs, migration dry-run/apply,
symlink/unrelated-data/active-runtime refusal, and existing MCP/self-service behavior.

| Coverage scope | Measured | Required |
|---|---|---|
| All original Go statements, including runtime and packaging | 85.01% | >=85% |

Upstream Hermes/connector source and test code do not inflate the coverage denominator.
The Go coverage parser accepts valid zero-statement profile rows emitted on Windows.
The organization scope suite verifies member resolution, policy narrowing, separate
organization/user secrets, read-only `/org`, MCP tool allowlists and hub-owned action
gates. It does not claim a public authentication gateway or live provider access.
The self-env suite verifies atomic user-runtime updates, allowlist enforcement, protected
organization/runtime keys, startup loading and restart signaling. It does not claim a
live Telegram bot or personal Telegram account login.
The service catalog/enablement suite verifies secret-free status output, missing-key
guidance, dependency handling, organization blocking and isolated self-service state.
The communication-gateway suite verifies channel-neutral job routing, Telegram command
handling, durable job/reply spool recovery, bounded worker execution and that sensitive
job text is not written to disk. It uses a fake Bot API and runner; no live Telegram
polling, message deletion or model/provider integration is claimed by these tests.

`just security` passed: Go vulnerability check and browser npm audit reported no
known vulnerabilities. This does not scan every upstream Python or OS dependency.
Both dev/prod images passed the standalone Docker smoke. Rendered Compose separates
`communication-hub` from `hermes-runtime`; the gateway receives no user-space mounts or
provider credentials, while dev source mounts exclude spaces and prod has no source mounts.
Hosted Actions pins resolve to official upstream refs.

Previously installed pinned Hermes/Google/Telegram virtualenvs and a real Hermes MCP 2
client verified basic compatibility with the Go stdio server. The 0.2.0 local suite
verifies generic MCP configuration and the native bridge without real account credentials.
Google configuration tests verify the read-only default and explicit write opt-in;
runtime and stack tests verify that Atlassian is configured as the pinned local
`mcp-atlassian` stdio server with Jira credentials, without the legacy Rovo endpoint.
Docker smoke verifies that `glab` is installed. These checks do not claim Google,
Atlassian or GitLab account access; Google OAuth consent, Jira API-token validity,
GitLab PAT and a read operation require deployment acceptance, while any mutation
requires a separate explicit owner instruction.

`just build` and a Linux/amd64 cross-build passed. `just docker-check prod` and
`just docker-check dev` passed with standalone runtime smoke. A separately authorized local
CLIProxyAPI/Antigravity OAuth session was live-verified through Hermes with a marker
response; this does not claim access to other account integrations or external sending.
No application or message was sent and no private account was read during testing.

No GitHub repository or release was published. Actions and a trusted self-hosted Runner
workflow are prepared for the owner's repository. Treat image and account checks as
required deployment acceptance, not as implicitly passed by the unit tests.
