---
description: System overview — roles, component map, networks, lifecycle; details live in services/, rbac.md, spaces.md and the ADRs.
last_verified: 2026-10-21
---

# Architecture

## Split of responsibility

**Hermes** (upstream, pinned — ADR-0012) owns reasoning, chat, tool
selection, memory, skills and hooks. It is never patched in place; a
contract bump is a new ADR + spec.

**Go** owns everything around it: space initialization, strict config,
organization policy, Docker lifecycle, channel routing, durable routines,
bounded file/HH/document tools, ToolHub, the credential boundary and the
supervisor. There is no replacement agent loop, CRM, crawler, database
queue or vector database. Retrieval is search → read → reason over files
and tools.

## Component map

| Service | Role | Detail |
|---|---|---|
| `hermes-runtime` | Per-space agent container: spawns pinned Hermes, serves the private `/v1/*` contract, owns scope mounts | [runtime.md](services/runtime.md) |
| `communication-hub` | Channel gateway: adapters, job admission, spool, `?t=` human links | [communication.md](services/communication.md) |
| `supervisor` | Scale-to-zero spawner: warm runtimes, control-call routing, docker-exec agent-tools | [supervisor.md](services/supervisor.md) |
| `toolhub` | Capability gateway: governance evaluation, connector lifecycle, audit | [toolhub.md](services/toolhub.md) |
| `credential-broker` | Standalone credential authority (copied MIT module) | [credential-broker.md](services/credential-broker/index.md) |
| `hub-stt`/`hub-tts`/`hub-media` | Speech + media sidecars, bearer-auth internal API | [media.md](services/media.md) |
| `cellinit`/`cell-proxy`/`mcp-bridge` | Disposable workload cells + loopback relays | [cell-proxy.md](services/cell-proxy.md) |
| `model-relay` | Per-space model selection + provider ladder | [model-relay.md](services/model-relay.md) |
| `hubctl` | Operator CLI: render/up/down, secrets, migrations, connector host ops | [hubctl.md](services/hubctl.md) |

The agent-visible tool surface these produce is catalogued in
docs/tools.md; space layout and config in docs/spaces.md; the authorization
model in docs/rbac.md; runtime operation in docs/operations.md.

## Identity and scope

[ADR-0011](adr/ADR-0011-stable-identity-identifiers.md) separates the human
principal from verified channel identities, execution contexts, runtimes,
conversations and provider connections. The schema-1 envelope carries
canonical IDs — `user_id`/`actor_id` = configured principal,
`scope_id=user:<id>` = its personal context — plus a stable runtime ID and
content-derived policy version on both services. Channel IDs (Telegram,
Slack) are transport links, never filesystem or authorization keys.

Scopes live at `spaces/<id>` ([ADR-0009](adr/ADR-0009-scoped-homes-and-communication-hub.md)):
users and organizations are peers; an org job requires the trusted
`organization:<id>` scope plus an approved `org_actions` entry. The
effective user scope is org baseline *narrowed by* user settings — never
widened.

## Trust boundary

Default-deny capability posture (ADR-0031, ADR-0032 — accepted; T01–T22
acceptance rows evidenced in CHG-0065 and awaiting owner sign-off, so the
deployed stack is not yet certified against every guarantee — see
`.work/in-progress/CHG-0065-capability-enforcement/acceptance-matrix.md`
for the per-row enforced-vs-target delta). The rules:

- The model never sees credential values, host env, host paths or
  unrestricted egress; ToolHub projects only tools the principal's policy
  selects (docs/rbac.md).
- `hermes-runtime` is the private identity + filesystem trust boundary:
  `POST /v1/jobs` carries immutable identity + bounded prompt under Bearer;
  binding rejects user/org/actor/scope mismatch before opening a path.
  Uncertain results are never silently retried.
- Web/transcript/tool content is data, not authority. Provider mutations
  need a concrete user instruction; `Confirmation` digests bind human
  review to exact record bytes — model/agent paths cannot mint them.
- Secret values live only behind the Credential Broker or the encrypted
  store; ToolHub holds opaque locators. File deliveries need a shared
  tmpfs `credential_mount_root` or fail closed.
- Workloads (connectors, CLI cells) run as disposable containers admitted
  by `workload-controller` with a proven receipt; inspect verifies digest,
  limits, caps, seccomp, net/volume topology. HTTP reachability is not
  admission evidence.
- Human action links are one-time `?t=` tokens on loopback by default;
  public origin requires explicit `HUB_PUBLIC_*`.
- Supervisor has Docker authority but no channel credentials; the gateway
  has channels but no Docker authority or scope mounts; ToolHub has
  tool/cell authority but cannot write spawned-runtime state (restart is
  relayed, not written).
- `communication-hub` never mounts scope homes. Its `telegram_auth` QR
  path (default-on for telegram stacks, `telegram_auth: false` opts out)
  keeps the MTProto session in memory only.

Threat model: docs/threat-model.md. Engineering rules: docs/engineering.md.

## Networks and lifecycle

Managed Compose gives each `spaces/<id>` an internal agent network; Hermes
reaches ToolHub and the per-space model relay there. CLIProxy, broker and
sibling runtimes are never attached to the agent network. Shared
control-plane services (toolhub, credential-broker, cliproxy,
workload-controller, communication-hub) are rendered once by the space
whose `agent.yaml` keeps `infra: true`; siblings join the external
`hermes-hub-runtime` network and enroll via `toolhub-tokens.json`.

Execution selection `http://supervisor:8876` runs the supervisor in the
main stack (ADR-0040): it receives the trusted envelope, dedupes by
`(principal_id, context_id, runtime mode)`, spawns the pinned runtime with
only that context's mounts, and stops compute ~30 min after the last lease
— the context home persists. Gateway and supervisor stay small always-on
control plane; registered users create no resident runtimes;
reconciliation restores only contexts with runnable work, due routines,
pending approvals or explicit always-on leases.

ToolHub removes a connector by cutting call authorization first, then
persisting a finalizer ladder for workloads, volumes, network, credentials,
workspace and a zero-resource check. The served endpoint scopes Docker
queries to the stack project and connection; purge rechecks both labels on
the object. Confirmed incomplete removals resume at ToolHub startup and
retry every 30 seconds. Recipe images remain shared and are excluded from
connection removal receipts. The same 30-second cadence runs the single
reconciler pass: scoped label scans purge orphans whose owner connection is
gone, dead credential ciphertext degrades the connection, and recipe,
credential-recipe and live-surface drift park it until a human /confirm
re-pins the observed digest (CHG-0147).

## Jobs, runs and events

Channel message → normalized `Envelope` → durable job → supervisor lease →
runtime spawn/warm hit → Hermes run. An admitted run streams NDJSON events
through the private boundary; SSE disconnect falls back to the durable
run-status API (upstream SSE has no replay cursor); recovery observes via
`/v1/resume` + `/v1/observe` instead of resubmitting. `/state/runstarts/
<runID>` markers keep artifact attribution across interruption
(SPEC-0037). Job phases, quarantine and `hubctl trace`: docs/operations.md.

Routines share the same job pipeline — clock, execution and delivery are
separate durable records. Deterministic kinds (reminder/watcher/check/
digest/script) never start a runtime; `brief` and `sink=agent` events are
the only kinds that may wake the agent, always flagged untrusted. Event
subscriptions poll connector read tools through `/v1/tool-call`;
`delivery=stream` holds one bounded wait-call loop. Detail:
docs/routines.md.

## Capabilities and egress

Each capability has exactly one surface — a native toolset, a hub-rendered
MCP entry, an owner `mcp_servers` entry, or a ToolHub projection; hub-owned
and native names are reserved against owner `mcp_servers` (SPEC-0040).
Feature flags (docs/spaces.md) render mounts, env and toolset wiring
declaratively — e.g. `browser` projects read-only Playwright tools,
`browser_act` separately adds mutations; `ssh`/`ssh_write`/`ssh_shell`/
`ssh_tunnel` stack onto a reviewed `connections/ssh/config.yaml`.

Workload egress is deny-by-default: `egress_class: web` permits audited
per-workload proxy HTTPS with deny-first private/loopback/metadata
destinations; credential-derived URLs/endpoints join the ACL at readiness;
`tcp_forwards` relay one credential-bound `host:port` each through the
same proxy — the model can never choose destinations.

## State

Per space: `connections/`, `workspace/`, `runtime/`, `hermes/`,
`generated/`, `diagnostics/` (layout: docs/spaces.md). Shared: broker state
on an external per-env volume, `communication-hub-data` spool, ToolHub
`/state` (store/audit/governance/credentials/artifacts). Durable records
are content-addressed or revision-fenced: policies and recipes by digest,
grants/bindings by revision, projection revisions monotonic, tombstones
retained for idempotent replay (removed 30 d, terminal operations 7 d).

## Non-goals

- No public multi-tenant service — this is an isolated owner deployment
  boundary; ingress auth + principal mapping are out of repo scope.
- No direct agent access to Docker socket, host shell, provider dashboards
  or credential material.
- No automatic installation of unpinned packages inside agent images.
- No dual scheduling: durable hub routines replace native cron; a
  pre-migration cron pins its context on until migrated.
- Not every guarantee is deployed yet — ADR-0031/32 acceptance is tracked
  in .work; describe the deployed stack, not the target, when answering.
