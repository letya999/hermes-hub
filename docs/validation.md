---
description: Measured delivery evidence and explicit unverified boundaries.
last_verified: 2026-09-23
---
# Delivery evidence — CHG-0009 and CHG-0012

## CHG-0037 ToolHub transport refresh

ToolHive and GitHub fallback name search now return source-only candidates from
their live public APIs; fixture tests cover Smithery Bearer-token search, prepared
priority, deduplication, bounded warnings and a rate-limited registry. Selection
still requires generic source review. A full unprepared registry install and
live Smithery search have not been proven.

The Go gateway now holds a stateful MCP server per authenticated owner token.
An HTTP wire regression confirms that an open client receives
`notifications/tools/list_changed` after disable and re-enable, retains its MCP
session ID, cannot execute a removed tool, and cannot use another owner's
session ID. The ToolHub process polls persisted projection changes; the old
serve-runtime Hermes restart watcher is removed. A separate pinned Hermes
0.21.0 Docker contract used a local read-only fixture and observed tool removal
and restoration, three successful ordinary runs plus an active run that completed
while projection changed, unchanged process PID and session, and a cold ToolHub
endpoint restart. This does not prove production Telegram delivery, every #74
failure/recovery case or a live provider account; #74 remains open.

## CHG-0026 / SPEC-0023 ToolHub control plane

M5.2 in-repo tests drive the shipped ToolHub MCP handler (`NewEndpointHandler`
/ `Gateway`) with two synthetic principals. They cover the eight control
operations, forged owner/locator/backend/policy denial, Alice/Bob isolation,
self-install grant enforcement, catalog assignment, promotion without converting
user bindings, loopback credential elicitation into the encrypted store, OAuth
PKCE/state/redirect/TTL/nonce, expired confirmation and replay, rotate/revoke
cutting an open session, idempotent enable, disable/remove dropping tools and
that binding's volumes, effect/budget escalation, concurrent lifecycle races,
and 95 unique plus 5 shared credential references. A live Hermes chat that
reconnects tools remains #74 / M5.3. Live GitHub import and Docker workload
start reuse M5.1 evidence when present; they are not required to freeze the
control-plane contract.

On 2026-09-17, `just check` passed, including race tests, formatting, vet,
staticcheck, documentation/workflow checks and 85.17% original Go statement
coverage. Production `NewEndpointHandler` wires a default source reviewer,
OAuth broker and `FormOrigin`; `required_credentials` URL elicitation points
at the same loopback `form_url` the status body returns. `go.mod` and `go.sum` were not changed, so `just security` was not
required. `just docker-check` built image `68424bee6191` and passed standalone
smoke plus the pinned Hermes 0.21.0 contract and five-minute gateway
lifecycle. The probe used fixtures and made no live Telegram or provider
call. faster-whisper `hub-stt` on that image was unavailable
(`exec /usr/local/bin/hub-stt: no such file or directory`); that is not
treated as ToolHub control-plane or provider integration success.

## CHG-0027 / SPEC-0024 M5.3 Hermes-mediated onboarding

The local M5.3 path now has immutable GitHub source resolution, restricted build
and preflight evidence, generic ToolHive admission, credential readiness before
projection, one-time credential completion and a durable reconnect request. A
projection revision is written to `toolhub-reconnect.request`. The earlier
`/reload-mcp` API acknowledgment was later found to be model output, not native
reload evidence. CHG-0036 replaces it with a controlled Hermes restart from the
same owner home and session store. `just check` passed for CHG-0027
with 85.01% Go statement coverage, race tests, vet, staticcheck, docs checks and
actionlint. The Docker gate also passes the production image, standalone smoke and
pinned Hermes 0.21.0 lifecycle/API contract.

This still does not claim live Telegram delivery, a real Notion login/token or a
read-only Notion data call. The shipped image lacks `/usr/local/bin/hub-stt`, so the
optional faster-whisper fixture remains unavailable. Those are external-account or
image-content boundaries, not evidence of a successful provider call.

CHG-0011 freezes the identity and ownership contract and maps it to the existing space
layout without data migration. The Telegram gateway now creates a schema-1 canonical
identity envelope; the private HTTP runner preserves it and the runtime rejects
malformed IDs, principal/context/runtime mismatches and stale policy versions before
Hermes execution. Delivery records bind the normalized conversation to the numeric
Telegram audience, and spool restart tests preserve runtime and policy identity.
Effective configuration produces one policy digest shared by gateway and runtime.

## CHG-0023 personal assistant experience

M4 keeps Telegram Bot API and Slack App as communication-hub ingress/delivery.
In-repo tests drive shipped `handleUpdate` for private allowlisted DMs, foreign
sender isolation, groups disabled, commands, files, edits, duplicate updates,
empty-text voice envelopes, STT errors, opt-in TTS text fallback, official Slack
`v0` HMAC Events API fixtures, routine CRUD/ownership and hubctl
backup/restore/purge.

On 2026-09-14, `just check` passed, including race tests, formatting, vet,
staticcheck, documentation/workflow checks and 85.02% original Go statement
coverage. `go.mod` and `go.sum` were not changed, so `just security` was not
required. `just docker-check` could not rebuild: `docker build` failed to fetch
`github.com/korotovsky/slack-mcp-server` (`github.com:443` timeout). Evidence
used the existing pinned image `hermes-hub:0.3.0-runtime`
`sha256:c74d6046795a9b158bd89030ae25ba4f7f86a8cee07573bb1307120539eb884b`.
Standalone Docker runtime smoke passed with Hermes Agent 0.21.0; no live
provider calls. `hermes-contract` on that image passed: operator pin, idle
no-wake, due routine wake/final/handoff/sleep, host supervisor smoke, gateway
five-minute lifecycle, and the pinned Hermes 0.21.0 API contract.

faster-whisper on that image, driven with spoken fixture `hello.wav` (not a
sidecar mock), produced a non-empty `TRANSCRIPT=` line on two runs. A synthetic
440Hz tone is not treated as a transcript. Hugging Face Hub download warnings
are not treated as transcripts. Live Telegram voice send, live Slack workspace
events, and live Honcho are not claimed. Fixture Telegram updates and signed
Slack HTTP are the channel evidence. The pinned image still reports `audio_api`
and `realtime_voice` as false; TTS is an opt-in command worker with text as the
canonical delivery.

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

On 2026-09-12, communication rollout verification (CHG-0018) passed `just check`
with 85.06% own Go statement coverage and the final `just docker-check`
(image `7a3f67d90dba`, pinned Hermes 0.21.0). The complete production Telegram-adapter
gateway/worker/spool/HTTP supervisor path executed pre-rollout queued input once,
kept two verified users on separate exact homes/workspaces and sessions/runs,
reused a warm session/generation, settled terminal leases and automatically stopped
both contexts after five real minutes. Neither context stopped before its own idle
deadline. A later Alice task cold-started a replacement with the original session;
Bob stayed stopped. Telegram and model endpoints were local fixtures, while actual
upstream Hermes execution and Docker lifecycle were real. No live account or
external provider/send success is claimed.

The final combined gate also passed native approval/cancellation/restart, automatic
recovery, admission/reaper race, operator pin, idle no-wake, orphan ownership,
due routine wake/final/handoff/sleep and preservation checks. Native crashed turns
retained their original run while the upstream lease expired; input was not replayed
and upstream locks were not removed. Migration preflight uses the pinned native cron
SDK against a disposable snapshot and verifies the selected gateway's actual spool.
`just security` passed with no Go/browser vulnerabilities. Issues 16–18 are closed;
issue 19 retains its compatibility-release/legacy-deletion gate. Changes remain local.

## Published M1 compatibility acceptance

v0.2.1 was published on 2026-09-12 through PRs 66 and 67. Downloaded Linux
hubctl/runtime/communication assets and published SHA256SUMS match the binaries
in the real Docker acceptance image 7a3f67d90dba. See CHG-0018 acceptance audit
for hashes, actual five-minute deadlines and cold restoration evidence. Linux CI
container fixtures exposed missing host-gateway DNS mapping; production already
provided it, and the standalone fixture now does too. Final v0.3.0 retirement
verification passed in Linux CI 34715743182 for both dev/prod Docker targets; no real Telegram/model account send is claimed.

v0.3.0 is published after PRs 68/69; downloaded runtime/communication assets match
SHA256SUMS and real Docker image c74d6046795a. Both Linux production-adapter gateway
scenarios passed actual five-minute automatic compute removal and original-session
cold restore. Linux own coverage was 85.06%; local `just check` passed at 85.03%.
Issue19 and milestone2 are closed (6/6). v0.2.1 remains the accepted rollback artifact.

The local retirement combined gate exposed a fixture reading terminal mapping
before CompleteJob settled it. The shared bounded mapping wait corrected that
assertion boundary; `just check` 36387 and full real gateway lifecycle 21011 then
passed, including actual five-minute automatic shutdown and cold session restore.
Production execution binaries were unchanged.

## CHG-0019 M2 ToolHub foundation

`internal/toolhub` now validates immutable versioned definitions for remote MCP,
stateful container MCP and bounded CLI workloads. It keeps connections and credential
references separate, derives deterministic binding/tool/workload names, and rechecks
exact identity, owner, current revision and status before admission. The registry
snapshot contains only metadata and opaque credential locators; no secret value is
returned or persisted by this contract.

Targeted `go test -race ./internal/toolhub` passed with 85.7% package coverage. The
tests are control-plane regressions for strict schema/budget/source/mount validation,
cross-owner and stale-policy denial, disabled/revoked bindings, concurrent admission
versus revocation, concurrent rotation, file round-trip and workload lifecycle
metadata. `just check` passed with 85.07% total Go statement coverage. With Docker
29.4.3, `just docker-check` passed on image `3a011b97f1c2`, including standalone
smoke and the pinned Hermes 0.21.0 contract/lifecycle probe; those fixtures made no
live Telegram or external-provider call. The safety scan passed secret scanning and
govulncheck but remains non-zero on 91 pre-existing gosec findings and unavailable
host npm/eslint; no dependency changed in CHG-0019, so `just security` was not
required. The tests and Docker probe do not claim ToolHub gateway, ToolHive/vMCP,
MCP backend or provider integration success.

## CHG-0020 M2 ToolHub stage 2

The stage-2 implementation adds an opt-in stateless streamable-HTTP ToolHub
gateway, a shared projected-tool resolver for list/call, private MCP HTTP
backend adapter, bounded direct-exec CLI runner and per-user/per-job workspace
lifecycle. Runtime wiring is opt-in: `HUB_TOOLHUB_ENDPOINT` injects the stable
authenticated endpoint, while `HUB_TOOLHUB_AUTOSTART=true` starts `hub-toolhub`
against `HUB_TOOLHUB_STORE`; unset variables preserve generated direct MCP.

Evidence completed locally:

- `go test -race ./internal/toolhub` and `go test -race ./internal/devcheck` passed;
  the final full run records the exact repository coverage below.
- `just check` passed with 85.03% total Go statement coverage, race/shuffle,
  format, vet, pinned staticcheck, docs and actionlint.
- `gosec ./internal/toolhub` passed with 0 issues and 6 narrow, justified
  `#nosec` annotations covering validated command/process and secret-boundary
  operations.
- `just docker-check` passed with Docker 29.4.3 and image
  `4257481689e2`: standalone smoke and pinned Hermes 0.21.0 contract/lifecycle
  passed, including actual five-minute idle removal and original-session cold
  restore. The probe used fixtures and made no live Telegram/provider call.
- `go run ./cmd/devcheck toolhub-contract` failed closed as expected because
  `TOOLHIVE_VMCP_ENDPOINT`, `TOOLHIVE_REMOTE_TOOL` and
  `TOOLHIVE_STATEFUL_TOOL` were not configured. The command therefore fails
  closed instead of treating a fixture or unset endpoint as integration proof.
- With the local vMCP inputs configured, the same probe reached initialize but
  failed at the external `404 Session terminated` continuity boundary described
  below; no ToolHive call success is claimed from that run.
- In an isolated local Docker run, the official ToolHive v0.48.0 source commit
  `c25e508592bc3bcfa10dea99af04daaf341db95a` was built outside the repository.
  `thv` created the remote and digest-pinned stateful fixtures and vMCP
  discovered their namespaced tools. The current vMCP process then returned
  `404 Session terminated` when the MCP session header was reused after
  initialize, so the live vMCP call and gateway contract are recorded as
  unavailable rather than claimed as passed.
- The stateful fixture used a bind-mounted state directory; its marker survived
  the ToolHive stop/start check. This proves only the local fixture persistence
  boundary. vMCP used anonymous auth and local fixtures, so no live provider or
  secret integration is claimed.
- Container MCP definitions now require an exact ToolHive version and
  digest-pinned sidecar references. The Go adapter fails closed unless the
  private `HUB_TOOLHIVE_ADMISSION_ENDPOINT` controller accepts the plan; this
  keeps CPU, memory, PID, filesystem and egress enforcement outside the Go
  gateway.
- `toolhub-hermes-contract` is an integration-only real-Hermes probe. It keeps
  the Hermes container/session volume alive while restarting only the Go
  ToolHub endpoint and requires explicit real vMCP inputs.

Unavailable acceptance evidence remains explicit: target Linux VPS resource and
egress measurements, production-pinned ToolHive sidecar/resource enforcement,
external remote-provider auth, the current vMCP session continuity gate, and
the real-Hermes reconnect probe (blocked by that external vMCP session failure).
`just security` was not required
because no repository dependency or pin changed.

## CHG-0021 M2 ToolHub stage 3

Stage 3 adds the manifest-backed opt-in catalog, exact-owner enable/disable,
persisted projection revisions, a bounded reconnect hook, controller enforcement
attestation and explicit dry-run/apply migration. The current generated MCP runtime
remains the default when `HUB_TOOLHUB_STORE` is unset.

Control-plane wiring now matches SPEC-0017/0018/0019:

- Enable/disable persist to `HUB_TOOLHUB_STORE`; list/call/catalog reload the file
  so a tools process and `hub-toolhub` share current bindings.
- `tools/call` holds `AuthorizeProjected` through backend admission.
- Ambiguous owner connections are denied. Organization scope may disable and cannot
  enable. Bounded CLI is dispatched to the CLI runner and fails closed without isolation.
- Autostart prefers `hub-toolhub` (image symlink to `toolhub`).
- Projection changes write secret-free `toolhub-reconnect.request`. Gateway audit
  records principal/context/runtime/binding/policy/backend/outcome without secrets.
- `hubctl migrate-toolhub` uses the user as context_id, reports generated MCP under
  the space `generated/` directory, and sets `rollback_on_failure` when apply restores
  the previous store.

Local evidence:

- Targeted ToolHub, migration, agent-tools and runtime tests pass after the wiring
  patch. `just check` passed: race/shuffle, Go statement coverage 85.03%, format,
  vet, staticcheck, docs and actionlint.
- `just docker-check` passed on image `dd669637042e` (Hermes Agent v0.21.0):
  standalone smoke and pinned Hermes contract/lifecycle, including the actual
  five-minute idle shutdown and original-session cold restore. No live Telegram
  or external-provider call. The image contains `/usr/local/bin/hub-toolhub` as a
  symlink to `toolhub`.
- Container admission now requires a JSON receipt proving `running`, enforcement,
  exact workload ID, digest-pinned images and CPU/memory/PID/mount/egress policy.
  Empty 200/204 responses fail closed, so controller reachability is not reported as
  enforcement success.
- `hubctl migrate-toolhub` defaults to dry-run, imports only explicit tool lists,
  preserves disabled state, writes symbolic credential references without values and
  keeps a rollback copy on apply. Generated/discovery-only services are reported as
  unmatched instead of guessed.
- The vMCP run remains unavailable: its log records `no backends returned
  capabilities` followed by `Manager.Terminate: session terminated`; the subsequent
  404 is therefore an upstream fixture/backend-health failure. No real remote or
  stateful MCP call, gateway integration, or reconnect success is claimed.

Remaining gates are follow-up issues, not mock-closed: [#73](https://github.com/letya999/hermes-hub/issues/73)
live ToolHive/vMCP on Linux VPS, and [#74](https://github.com/letya999/hermes-hub/issues/74) real-Hermes
transport reconnect while preserving session/home/memory/run state. `just security`
is not required because this change adds no dependency or pin. GitHub #20–#26 are
closed against this control-plane evidence; #47 stays open for M3 secret storage.

## CHG-0027 M5.3 onboarding (in progress)

- A generic control-MCP regression proves that an ordinary canonical GitHub
  repository URL is resolved to a full commit SHA before the reviewer sees it,
  and that only the repository plus SHA are persisted on onboarding.
- The streamable-HTTP control MCP now delivers standard progress notifications
  for source review/build, credential/OAuth requirement and workload readiness;
  its regression uses the real HTTP MCP transport.
- These repository tests do not prove Telegram delivery, provider login,
  Hermes dynamic discovery or a Notion data read. Those remain live acceptance
  gates and require explicit account-login instruction. Separate local Docker
  evidence below covers the immutable Notion artifact and ToolHive admission.
- The GitHub read guard resolved the public Notion repository to commit
  `1d38420769c8a1fe2d583ff1e7d2d108d4eb0b30`. The restricted Docker builder
  produced OCI manifest `sha256:6dbad51ce3168352093f49e796717079d5e173130339aaf9a4dfbc3b8508730d`
  with provenance and SBOM evidence, without credentials. This proves the
  immutable import/build slice, not ToolHive start or Notion account access.
- An isolated `--network none`, non-root, read-only-root MCP initialize and
  `tools/list` succeeded for that image. The live response exposed both explicit
  read-only and destructive annotations. A regression fixed preflight to trust
  read-only only when `readOnlyHint=true` and `destructiveHint` is not true;
  destructive or unannotated tools now fail closed to write policy.
- Stock ToolHive v0.49.0 admitted the built image through the explicit Docker
  fallback. The proxied endpoint completed MCP initialize and `tools/list`,
  exposed only the reviewed `API-get-self` tool, and repeated admission reused
  the same workload and endpoint; all disposable resources were removed after
  the probe. Credential-bearing onboarding now validates this receipt before
  publishing its binding/projection, with regression coverage for rollback and
  re-enable.
- `just docker-check` also passed the production image build, standalone
  Docker smoke, and the pinned Hermes 0.21.0 lifecycle/API contract. The
  faster-whisper fixture was unavailable on the shipped image because
  `/usr/local/bin/hub-stt` was absent; no external provider account was used.
- Live local ToolHub acceptance initially failed because the administrator-provisioned
  `spaces/local/runtime/artifacts` directory was missing. Runtime rendering now
  provisions it; after repair, the real Notion source resolved to commit
  `1d38420769c8a1fe2d583ff1e7d2d108d4eb0b30`, built to OCI, and passed MCP
  preflight with generic `NOTION_TOKEN` discovery. Preflight no longer duplicates
  discovered credentials into the ordinary environment contract, and wildcard
  listeners now emit protected-form links at `127.0.0.1`.
- The live onboarding is currently `awaiting-credentials`; no Notion token was
  entered, no workload was enabled, and no Notion data read or Telegram delivery
  is claimed. The loopback form is intentionally usable only from the same host.
- Retrying a self-install for the same repository commit now reuses the existing
  user-owned immutable definition instead of rebuilding it; expired credential
  forms rotate their nonce and expiry on resume.
- Final post-fix gates: `just check` passed with 85.06% Go statement coverage;
  `just docker-check` passed the prod image, standalone smoke and pinned Hermes
  contract/lifecycle. The pre-existing `/usr/local/bin/hub-stt` fixture warning
  remains unrelated.
