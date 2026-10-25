---
description: Delivery evidence extracted from docs/validation.md (moved by CHG-0136).
last_verified: 2026-10-20
---

## CHG-0022 credentials and connector platform

M3 stores secret values as AES-256-GCM ciphertext behind ToolHub locators. In-repo
tests drive encrypt/decrypt, cross-user/stale/removed/restart/restore/key-rotation
fail-closed behavior, inject after `AuthorizeProjected` through the shipped
`NewEndpointHandler` against a real ciphertext store, MCP workload-file inject
without vMCP HTTP leakage, open-session rotate/revoke from `hubctl secret` and
chat intercept before backend execution, workload stop, degraded rotate, group
rejection, a local OAuth PKCE and device-flow fixture with official
Google/Slack/Atlassian URL constants, and a privacy-preserving audit ledger that
records job/Hermes-run/tool-call correlation from the communication worker and
ToolHub request headers. Live Google/Slack/Jira login is not claimed. Live
ToolHive/VPS isolation remains #73. Hermes reconnect remains #74.

On 2026-09-13, `just check` passed for the shipped-path successor of CHG-0022,
including race tests, formatting, vet, staticcheck, documentation/workflow
checks and 85.07% original Go statement coverage. The successor tests drive
`NewEndpointHandler` against a real ciphertext store, MCP file inject without
HTTP leakage, communication/ToolHub ledger correlation, and open-session cut
from `hubctl secret` and chat intercept. `go.mod` and `go.sum` were not changed, so `just security` was not
required. `just docker-check prod` built image `a61ce4879a1d` and passed the
standalone runtime smoke with Hermes Agent 0.21.0, the pinned Hermes API contract,
and the five-minute gateway lifecycle. No live provider login or ToolHive/VPS
isolation is claimed.

On 2026-09-11, `just check` passed, including race tests, formatting, vet, staticcheck,
documentation/workflow checks and 85.02% original Go statement coverage. `just
security` found no Go or browser dependency vulnerability. `just docker-check prod`
built image `00437cced4d1` and passed the standalone runtime smoke with Hermes Agent
0.21.0. The smoke starts a separate real `hub-runtime serve` container and verifies
that a missing canonical identity envelope is rejected with HTTP 409 before Hermes;
no live provider call or external identity-provider integration is claimed.
Host-provisioned Telegram IDs remain the only implemented account-linking mechanism.

The local `go test ./...` suite passes on Windows. It covers peer scope initialization,
kind/ID/path checks, organization policy, persistent service mount boundaries, runtime
HTTP authentication and replay, cross-scope queued jobs, migration dry-run/apply,
symlink/unrelated-data/active-runtime refusal, and existing MCP/self-service behavior.

| Coverage scope | Measured | Required |
|---|---|---|
| All original Go statements, including runtime and packaging | 85.02% | >=85% |

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
The default hub image does not install that tree; `docker/mcp-atlassian.Dockerfile`
holds it. Docker smoke verifies that `glab` and Hermes sqlite are installed. These checks do not claim Google,
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
