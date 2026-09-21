---
description: Current scoped runtime boundary and target scale-to-zero lifecycle.
last_verified: 2026-09-17
---
# Architecture

M5 stage-2 Calendar and Slack data calls use opt-in stateless `provider-api`
definitions behind the same ToolHub authorization/injection/audit gateway.
The protected `hubctl connector` host CLI verifies provider identity before
binding. Read/write definitions and scopes are independent; no bot/app credential
is a data grant. Official HTTPS requests reject redirects and bound response
sizes. Registry writes use an OS lock and loaded-content precondition so stale
snapshots fail rather than overwriting another process's revoke. Generated MCP
remains the default. See SPEC-0022 and ADR-0018. Personal Telegram has a pinned,
locally prepared read-only ToolHive workload; real account login is still pending.
Official Workspace MCP uses Google's remote endpoints, subject to Preview approval.
See ADR-0020 and [the owner setup guide](local-accounts-manager.md).

The current implementation follows [ADR-0009](adr/ADR-0009-scoped-homes-and-communication-hub.md):
organization and user homes are peers under `spaces/`, mutable Hermes data lives in
those homes, and `communication-hub` is a separate container. Existing deployments
retain their old layout until an explicit migration succeeds.

[ADR-0011](adr/ADR-0011-stable-identity-identifiers.md) separates the human principal
from verified channel identities, execution contexts, runtimes, conversations,
delivery audiences and provider connections. The private schema-1 envelope carries
the canonical IDs alongside compatibility fields: `user_id`/`actor_id` identify the
configured principal and `scope_id=user:<id>` references its personal context. Both
services receive the same stable runtime ID and content-derived policy version;
Telegram IDs are transport links, never filesystem or authorization keys.

Hermes owns reasoning, chat, tool selection, memory, skills and hooks. Go owns
space initialization, strict configuration, organization policy resolution, Docker
lifecycle, communication routing, durable routine schedules, bounded file/HH MCP and an optional native MCP bridge.
A small Go binary supervises container processes. There is no replacement agent loop,
CRM, crawler, database queue or vector database. Retrieval is search -> read -> reason
using files and tools.

The pinned Hermes artifact also contains an opt-in authenticated HTTP API server. Its
`/api/sessions` and `/v1/runs` contract is recorded in [SPEC-0011](../specs/active/SPEC-0011-hermes-api-contract.md)
and validated against the real image by the Docker gate. This is a future adapter
surface: the capability response explicitly reports `split_runtime=false`, so tools
would execute on the API-server host. The current Go `/v1/execute` runtime remains the
private identity and filesystem trust boundary until a split-runtime adapter is
implemented. When ToolHub writes a durable `toolhub-reconnect.request` projection
marker, the serve runtime consumes it and submits Hermes' native `/reload-mcp`
command through this API. That reconnects MCP transports and refreshes the cached
tool surface without restarting Hermes; the watcher is opt-out with
`HUB_TOOLHUB_RECONNECT=false`.

## Scope homes

Both organizations and users live at `spaces/<id>`. `scope.yaml` declares the kind and
stable ID; IDs are unique across the namespace. A user may reference one organization.
The effective user scope is the organization policy baseline plus the user's narrowed
settings. A direct conversation creates `user:<id>` state. Organization jobs require
the trusted `organization:<id>` scope and an approved organization action.

| Location | Data |
|---|---|
| `spaces/<id>/scope.yaml` | Scope kind, ID and organization membership/policy |
| `spaces/<user>/settings.yaml` | User features, model endpoint, MCP and hooks |
| `spaces/<id>/hermes/` | Hermes memories, skills, sessions, hooks, plugins and state |
| `spaces/<id>/connections/` | OAuth, browser, Telegram and self-service state |
| `spaces/<id>/workspace/` | User or organization work files |
| `spaces/<id>/archive/` | Read-only extracted archive |
| `spaces/<id>/generated/` | Ignored Compose, Hermes and filtered env files |
| `communication-hub-data` | Gateway queue and delivery ledger; outside scope homes |

Job and conversation lifecycle mappings are persisted beside the gateway queue. They
contain immutable routing, idempotency and Hermes run metadata, while supervisor state
contains runtime generations and leases; neither store receives provider credentials.

The opt-in persistent path can stream normalized NDJSON events through the private
runtime boundary. The supervisor records admission and run status; the communication
spool records event receipts and repairs outbox handoff on restart. SSE disconnects
fall back to the pinned durable run-status API because upstream SSE has no replay
cursor. Recovery observes an admitted run through `/v1/resume` and `/v1/observe`
instead of resubmitting its prompt. Approval response routing and periodic lifecycle
reconciliation remain tracked in CHG-0017 until their delivery evidence is complete.

Organization skills are configured through upstream Hermes `skills.external_dirs` and
are mounted read-only. Hub tools expose organization material with explicit provenance;
user writes remain in the user home. Duplicate organization/user secret keys are
rejected rather than overridden.

## Service boundary

`communication-hub` owns Telegram transport, external identity mapping, the bounded
file queue and reply delivery. It mounts only `communication-hub-data` and receives
the bot token. `hermes-runtime` owns the selected scope homes, provider credentials
and configuration. Rollback mode runs one Hermes process per job; target mode keeps
one pinned Gateway warm per active context. It is not published on a public host port.

The services use a small authenticated private HTTP contract: `POST /v1/jobs` carries
version 1, immutable identity fields, scope and a bounded prompt; `DELETE
/v1/jobs/<id>` requests cancellation. A Bearer runtime token is required. The runtime
is bound to one user and optional organization, rejects identity mismatches before
opening a path. The communication spool and host supervisor record idempotency
outcomes as succeeded, failed or uncertain; the runtime only executes the already-
authorized request. An uncertain result is never silently retried.

Gateway mode supervises `hub-communication`. Its channel adapters map an authenticated
channel sender (Telegram Bot API or Slack App Events API) to the configured user scope, persist jobs and replies in
`/state/gateway`, and run one bounded invocation at a time. Slack App credentials stay
in communication-hub; they do not create Slack data tools. Voice updates keep a bounded
media envelope, transcribe through the replaceable STT worker, and submit text to the
same Hermes session. Spoken replies are opt-in. Native Hermes memory, profile and
skills remain in the owning home; Honcho is an official opt-in config file. The initial deployment
has one configured user; the job envelope and per-user paths keep the expansion point for
multiple users, organizations and channels explicit without adding external IAM today.

## Target scale-to-zero lifecycle

[ADR-0014](adr/ADR-0014-scale-to-zero-context-runtimes.md) replaces the proposed
permanently resident per-user compute with one warm runtime per active execution
context. A host-side Go supervisor receives the trusted job envelope, deduplicates
starts by `(principal_id, context_id, runtime mode)`, launches the pinned runtime with
only that context's mounts and waits for authenticated Hermes readiness. The runtime
is reused while busy and for five minutes after its final lease by default, then its
compute is stopped while the context home remains intact.

The communication hub and supervisor remain small always-on control-plane services.
The communication hub has no Docker authority or scope mounts; the supervisor has no
channel credential or authority to derive identity from message content. Registered
users do not create resident runtimes. Reconciliation restores only contexts with
runnable work, due routines, pending approvals or explicit always-on leases.

Routine definitions and delivery targets are durable hub records. A due occurrence
becomes the same authenticated, idempotent job used for interactive work and wakes the
owning context. Hermes performs the agent run but does not remain alive merely to own
a clock. Existing native Hermes cron is migrated explicitly or pins that context on
until migration; dual scheduling is forbidden.

Hermes terminal can read credentials in its own user runtime: this is an isolated
organization/user deployment boundary, not a public hostile-tenant service. A user's
OAuth and Hermes state stay in that user's named volumes. Organization documents are
read-only. Organization actions are explicit strings in host policy and are passed to
hub-owned tools, not exposed as free `tenant`, `org` or `user` tool arguments.
The hub `env_update` tool is legacy-only and is disabled for connector credentials when
Communication Hub control is configured. `service_enable` returns a one-time protected
form; the authenticated runtime control endpoint writes only the user runtime's
`self-env.json`, and the supervisor loads it on startup. `service_catalog` reports
connector status, and `service_enable` writes only the user runtime's `self-services.json`;
the supervisor merges selected upstream MCP definitions into the next Hermes config.
The allowlist comes from connector references, and organization-owned keys plus
runtime-control keys are rejected. These tools cannot edit host env files, settings or
organization secrets.
Chromium uses --no-sandbox within the container boundary. A native companion runs with
its OS user's permissions and needs a private tunnel. Public authentication and request
routing are outside this repository; an ingress must authenticate a principal and map
it to exactly one user space before invoking `hubctl`.

## Connections

Built-ins are opt-in connection presets. settings.yaml's mcp_servers adds ordinary
HTTP or stdio servers without embedding their applications. CareerGo or any other
external tool is used only if configured. The agent works without any business service.
Remote hosted MCP APIs may change independently of this code.
GitLab uses the runtime's `glab` CLI rather than MCP; `GITLAB_TOKEN` and
`GITLAB_HOST` are supplied to the isolated runtime. Atlassian uses the pinned
`mcp-atlassian` stdio server from `docker/mcp-atlassian.Dockerfile` (ADR-0010,
ADR-0023), not a copy inside the hub image;
`JIRA_URL`, `JIRA_USERNAME` and `JIRA_API_TOKEN` are passed only to that process.

File writes use cross-process locks, hash preconditions and atomic rename. Search/read
are bounded UTF-8 operations. HeadHunter applications use an existing applicant resume,
explicit task authorization and actual HTTP receipts; unknown outcomes are not retried.

## M2 ToolHub foundation

The contract-only foundation is recorded in [SPEC-0017](../specs/active/SPEC-0017-toolhub-foundation.md)
and implemented in `internal/toolhub`. It separates immutable versioned definitions,
owner-scoped connections, opaque credential references, effective bindings and workload
instances. Exact `principal_id`, `context_id`, `runtime_id` and `policy_version`
checks reuse `internal/identity`; ToolHub does not create a second ownership registry.

Credential-free per-user bindings use their deterministic binding ID for workload
identity and a separate `per-binding/<principal>/<context>/<definition>/<binding>`
state directory. Different runtimes or definition versions cannot reuse that
identity/state; restart of the same binding retains its directory. Previously
created anonymous per-user workload IDs and `per-user/<context>/<definition>`
directories are not reused or automatically moved/deleted. The existing
credential-bearing Telegram/controller path remains connection-scoped. The generic
Docker fallback gives each binding its own workload ID, network, volumes and
`credentials.env`; shared stateful or credential-bearing workloads stay denied.
Live Docker 95-plus-5 used 100 sequential unique networks, volumes and
containers (95 distinct owner env-files + 5 of one shared file). One hundred
users means one hundred **registered** bindings and a bounded live set
(`max_active` + FIFO); it does not mean one hundred concurrent MCP+ToolHive
stacks. Same SHA reuses one image id with distinct processes, volumes and
credentials. The MCP container does not receive the forwarding token; a
process inside it cannot read that token from env, logs or `/proc`. A
same-UID sibling inside the ToolHive process namespace is a rejected
one-container layout, not the production fallback.

Definitions classify execution as shared, per-user or per-job and carry finite resource
limits, explicit egress, logical mounts and immutable source pins. The registry can
save/load a permissioned atomic JSON snapshot. Container definitions also require an
exact ToolHive version and digest-pinned sidecars; actual resource and egress enforcement
is admitted only by the private `HUB_TOOLHIVE_ADMISSION_ENDPOINT` controller contract
and fails closed when absent.

The M5.2 control plane is recorded in [SPEC-0023](../specs/active/SPEC-0023-toolhub-control-plane.md)
and [ADR-0021](adr/ADR-0021-toolhub-control-plane.md). The same authenticated
ToolHub MCP endpoint exposes `prepare_source`, `status`,
`required_credentials`, `confirm`, `enable`, `disable`, `revoke` and
`remove`. Identity is taken from the bearer mapping; model arguments cannot
change owner, locator, backend or policy. Operator grants select
catalog-default, assigned definition or self-install. Credentials enter a
loopback form (or MCP URL elicitation pointing at it) and stay in
ciphertext until inject-after-authorize. Promoting a user MCP to the
catalog does not convert existing user bindings into shared workloads.

M5.3 is tracked by [SPEC-0024](../specs/active/SPEC-0024-m53-hermes-onboarding.md).
For self-install, a canonical public GitHub repository URL is resolved once to
the default branch's exact commit SHA; review and build receive only that immutable
source. Control calls use standard MCP progress notifications over the existing
authenticated stateless streamable-HTTP endpoint.

Credential Broker now lives at `services/credential-broker` as a separate Go
module and process. Its reviewed-contract forms, approval pairing, provider
references, grants, leases and runtime materialization stay behind its own HTTP
and identity-audience boundary. The root image includes `credential-broker` and
`hub-credential-broker`; the root `go.work` and CI expose the module without
flattening it into Hermes. ToolHub remains the owner of source review, workload
admission, projection and reconnect. When the Broker audience/key environment
is configured, onboarding uses Broker requests and grants, Communication Hub
approves the pairing code, and the runtime materializer handles the lease. With
that environment unset, the existing encrypted store and loopback form remain
the compatibility path. Broker file deliveries use a dynamic read-only ToolHive
mount hand-off: the generic controller accepts only regular files below its
operator-configured `credential_mount_root`, starts a one-call workload, and
exposes `/release` to remove it before the Broker runtime lease is released.
A separate Broker container therefore needs an explicitly shared runtime
directory; with no shared root, file delivery remains fail closed.

The stage-2 opt-in surface is recorded in [SPEC-0018](../specs/active/SPEC-0018-toolhub-stage2.md).
When `HUB_TOOLHUB_STORE` is explicitly configured, `service_catalog`,
`service_enable` and `service_disable` use the manifest-backed catalog and exact
owner-scoped bindings. Catalog mutations are written back to that store; list and
call reload the snapshot so a tools process and `hub-toolhub` see the same current
bindings. Ambiguous owner connections fail closed. Organization scope may disable a
binding (narrow) but cannot enable one (expand). The legacy `stack`/`self-services.json`
path remains the compatibility fallback when the store is unset; no runtime route is
switched by this stage. Each projection has a persisted monotonic revision and an
observable, at-most-once reconnect hook that writes `toolhub-reconnect.request`.
Credential-bearing onboarding can use the generic MCP readiness hook: ToolHive
admission and its validated running receipt complete before a new binding is
persisted and its projection is published; failed admission restores any previous
credentials file.
The hook is transport-only: current binding state is rechecked under the registry
read fence before backend execution and Hermes session/home ownership stays outside
this package. Bounded CLI calls go to the CLI runner and fail closed without an
isolation check; MCP transports go to the private adapter.

Container admission requires a controller receipt proving the exact running workload,
digest-pinned images and requested execution policy. HTTP reachability alone is not
enforcement evidence; missing or mismatched receipts fail closed.

`internal/toolhub` also contains a stateless streamable-HTTP gateway, a private
ToolHive/vMCP MCP client adapter and a bounded direct-exec runner. The runtime
image enables the MCP SDK's legacy-session compatibility flag so Hermes clients
may send session headers while authorization remains per request. The backend
adapter disables standalone SSE because ToolHub calls are request/response only.
For upstream servers that implement the current legacy MCP wire version (for
example Serena 1.5.x), the backend hop pins `MCP-Protocol-Version: 2025-11-25`
so discovery and calls remain compatible with the SDK's newer default handshake.
Both list and call use the same current projection resolver. Setting `HUB_TOOLHUB_ENDPOINT` (or
`HUB_TOOLHUB_AUTOSTART=true` with an existing metadata store) injects one authenticated
ToolHub MCP entry into the copied Hermes config; unset variables leave the generated
runtime unchanged. Autostart runs the shipped `hub-toolhub` symlink to `toolhub`
against the existing store and never creates a second ownership registry. ToolHive v0.48.0 remains an
external conditional backend at the pin recorded in ADR-0013 and issue #11.

## M3 credentials and connectors

[SPEC-0020](../specs/active/SPEC-0020-credentials-and-connectors.md) and
[ADR-0016](adr/ADR-0016-encrypted-credentials-and-oauth.md) keep ToolHub as the
ownership registry and store secret values only as ciphertext behind opaque
locators. The encryption key is supplied by `HUB_CREDENTIAL_KEY` or
`HUB_CREDENTIAL_KEY_FILE` and is never written to the ToolHub snapshot, the
ciphertext file or a ciphertext backup. `AuthorizeProjected` is the only
decrypt/inject path; the shipped ToolHub endpoint decrypts inside that admit
and passes environment to CLI `CallEnv` and MCP workload files, never to vMCP
HTTP. Chat `KEY=value` is intercepted before Hermes and rotates matching
ToolHub connections. `hubctl secret` set/list/delete reads values from stdin or
`--from-file` and binds those mutations to the registry. Personal-terminal
exposure is an explicit reversible connection flag. Failed rotates become
`degraded`. One OAuth broker implements authorization-code+PKCE and device flow
against official Google, Slack and Atlassian URLs, with remote MCP discovery via
RFC 8414/9728. The audit ledger records who, runtime, connection, revision and
job/run/call correlation without prompts or tokens. Generated MCP remains the
default; ToolHub stays opt-in through `HUB_TOOLHUB_*`.
