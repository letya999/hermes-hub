---
description: Measured delivery evidence and explicit unverified boundaries.
last_verified: 2026-09-12
---
# Delivery evidence — CHG-0009 and CHG-0012

CHG-0011 freezes the identity and ownership contract and maps it to the existing space
layout without data migration. The Telegram gateway now creates a schema-1 canonical
identity envelope; the private HTTP runner preserves it and the runtime rejects
malformed IDs, principal/context/runtime mismatches and stale policy versions before
Hermes execution. Delivery records bind the normalized conversation to the numeric
Telegram audience, and spool restart tests preserve runtime and policy identity.
Effective configuration produces one policy digest shared by gateway and runtime.

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

## CHG-0015 durable job/runtime mappings

The communication spool now persists job and conversation mappings with immutable
fingerprints, Hermes session/run metadata and explicit terminal/uncertain states. The
host supervisor persists runtime generations and leases and rejects stale lease
release after a generation change. Unit and race tests cover duplicate keys, legacy
mapping recovery, corrupt state, restart restoration, stale leases, terminal status
metadata and Hermes stop requests. These tests do not claim a second-host or live
provider integration; the Docker contract gate remains required for that evidence.

On 2026-09-12, `just check` passed with 85.06% Go statement coverage, including race
tests, formatting, vet, staticcheck, documentation and workflow checks. `just security`
passed the Go vulnerability scan and browser npm audit. `just docker-check prod` built
image `3248ea8a01bd`, passed the standalone runtime smoke and passed the real pinned
Hermes 0.21.0 sessions/idempotency/SSE/stop/approval/cron/restart contract plus the
host-supervisor warm/reap/cold-restore smoke. No live provider call or external message
was made.

## CHG-0012 pinned Hermes API contract

The pinned image contains Hermes Agent `0.21.0` at commit
`869228cab4a8276d3b4c78da9d9939670c47bd0f`. The real integration command is:

```text
go run -tags integration ./cmd/devcheck hermes-contract hermes-hub:test
```

It starts the upstream API server with an isolated named state volume and verifies the
capability document, durable run idempotency (same payload replay and different-payload
409), persistent session create/read/messages, run status and SSE, cancellation, the
approval boundary, `/api/jobs` availability alongside the gateway scheduler, and a
restart where an admitted non-terminal run is replayed as `interrupted`. The probe uses
safe dummy provider settings and makes no external provider call; it therefore proves
the HTTP contract and restart semantics, not model quality or account access.

The API server's `split_runtime=false` and server-side tool execution are recorded as a
trust-boundary limitation. TUI JSON-RPC session methods are not treated as an HTTP
contract, and unsupported admin/memory-write/audio/realtime endpoints are not claimed.
If the probe fails, Docker delivery is blocked; rollback is to the previous image and
code revision, with no data migration.

## CHG-0014 threat model

The personal and future-organization threat model records the authenticated channel,
Hermes, Go ToolHub, internal vMCP, ToolHive Runtime and provider boundaries. Every
required abuse case maps to a fail-closed control, non-secret audit evidence and an
implementation issue that owns the regression test. ADR-0013 makes Go authoritative
for identity, current binding/policy and credential ownership; no ToolHive or provider
integration success is claimed by this documentation change.

On 2026-09-11, `just check` passed with 85.03% original Go statement coverage,
including race tests, formatting, vet, staticcheck, documentation and workflow checks.
No dependencies or runtime behavior changed, so security and Docker gates were not
required for CHG-0014.

## CHG-0015 migration and deprecation map

The current-to-target map assigns the live execution, generated MCP, connector catalog,
self-service, credential, state and ToolHive paths one `keep`, `adapt`, `replace` or
`retire` decision. Each row records coexistence, rollback, an evidence-based deletion
gate and its downstream implementation issue. Rollout is per runtime/connector and
never performs startup-time data migration or dual tool execution.

On 2026-09-11, the documentation and workflow gate passed after the map was added. Two
subsequent full `just check` attempts did not complete because `go test -race` stalled
without output and were stopped; they are not claimed as passing. The immediately prior
CHG-0014 full gate passed in the same worktree with 85.03% coverage, and CHG-0015 changed
documentation only. No dependency/runtime change required security or Docker gates.

## CHG-0016 scale-to-zero runtime contracts

ADR-0014, SPEC-0012 and SPEC-0013 replace the proposed permanently resident
per-user compute with a warm scale-to-zero runtime per active context. The contracts
define host-side Docker authority, per-context leases, a five-minute default idle TTL,
safe reaping, durable hub-owned routines and explicit native-cron migration. Issues
#14-#19, #33, #46 and #55 plus the shared context in the remaining open implementation
issues were aligned to this decision.

On 2026-09-12, the initial implementation added the host-side Go supervisor, authenticated
context proxy, pinned Hermes Gateway startup/readiness contract, persistent `/v1/runs`
execution, typed leases, scoped mounts, resource limits and lease/TTL reaping. Unit and
race tests cover concurrent start deduplication, path/symlink isolation, authorization,
resource arguments, warm reuse and safe reaping.
`just check` passed with 85.06% Go statement coverage. `just security` passed with no Go
or browser dependency vulnerabilities. `just docker-check` passed the standalone runtime
smoke and the real pinned Hermes 0.21.0 sessions/idempotency/SSE/stop/approval/cron/restart
contract probe. The integration contract also runs a real host-supervisor Docker smoke
covering warm reuse, idle reap, cold restore and logical runtime identity. Durable hub
job/session mappings, external event/approval forwarding, routines and migration remain
tracked in #15-#19 and #33.

An additional real-image probe on 2026-09-12 started `hub-runtime serve` with
`HUB_PERSISTENT_HERMES=true`, waited for authenticated `/readyz` backed by the
upstream API, checked `/healthz`, and removed the container; it passed with no live
provider call.
