---
description: System overview - the v1 target (trust zones, five hub processes, service extraction map, package owners) and the current v0 system it is extracted from; details live in services/, rbac.md, spaces.md and the ADRs.
last_verified: 2026-11-19
---

# Architecture

This page has two halves. **v1 target** is the direction fixed by
ADR-0105..ADR-0109 (milestone M40); each part lands with the CHG named in
[SPEC-0100](../specs/active/SPEC-0100-v1-contracts.md). **Current system**
describes what runs on `dev` today. When the halves disagree, the current
half is what is deployed.

## v1 target

### Trust zones and processes

| Zone | Process | Modules | Holds |
|---|---|---|---|
| control | `hub-control` (`hub serve --zone control`) | identity-hub, spawn-controller, task-engine, proactive-engine | principals and layers, signed policy bundles, the one job ledger, container authority, the proactive clock |
| agent | `hub-agent` (`hub serve --zone agent`) | tool-hub, memory-hub, model-gateway | tool admission and routing, memory, model routing; verifies run tokens |
| edge | `hub-edge` (`hub serve --zone edge`) | channel-hub, hosting-hub, ingress | channel credentials, public listeners, `?t=` links, hosted sites |
| secret | `credential-broker` | credential-broker | user and connector credentials, OAuth, leases |
| egress | `egress-gateway` | egress-gateway | outbound network, boundary credential injection |
| space (untrusted) | spawned per space | harness-runtime, browser-runtime, workers | Hermes, the space mounts, browser profile |

Minimal topology: one `hub` process (`--zone control`, optionally
`control,agent,edge`) plus one `harness-runtime` with native Hermes tools.
Zones co-hosted in one process still talk only through contracts.
Crossings and refusals: [boundaries.md](boundaries.md),
[ADR-0105](adr/ADR-0105-v1-topology.md).

### Contracts and the dependency gate

Modules talk through `contracts/<name>/v1` (DTOs, one `Client` interface,
in-process and HTTP transports, a conformance suite run against both).
`devcheck` checks the full `go list -deps` closure of every binary against a
shrink-only allowlist; a new forbidden edge fails `just check`
(ADR-0105 D5-D6, CHG-0330).

### Identity, tokens, policy

identity-hub owns principals (human, agent profile, system) and verified
channel identities; layers narrow `platform -> org -> user -> agent
profile`. identity-hub's token service signs a token per admitted run;
tool-hub, memory-hub, model-gateway and egress-gateway verify its signature,
audience and expiry locally, and check generation and revocation through
RunAuthority (`task/v1`) before any backend. Policy is a signed bundle
loaded by digest ([ADR-0106](adr/ADR-0106-identity-layers-run-tokens.md)).

### Execution and proactivity

spawn-controller is the only container authority and creates a space from
an identity plus six template passports by rendering only the runtime spec;
task-engine owns one job ledger with lanes and execution classes
(interactive, assignment, routine, system) and never retries `uncertain`
([ADR-0107](adr/ADR-0107-execution-spawn-task-runtime.md)). proactive-engine
owns listeners, the event inbox, Watch/Routine/Assignment, task and progress
ledgers and the attention inbox
([ADR-0108](adr/ADR-0108-proactive-engine.md)). Every run carries an action
mode (`read-only`, `plan`, `ask`, `ask-critical`, `full-access`) under the
grants ([ADR-0109](adr/ADR-0109-action-modes-owner-decisions.md)).

### Service extraction map

v1 services are built by moving existing code, never by rewriting beside it.

| v1 service | Built from (v0) | CHG |
|---|---|---|
| identity-hub | internal/identity, internal/iam, communication.users.yaml, TELEGRAM_ALLOWED_USERS | CHG-0331 |
| spawn-controller | internal/supervisor, passport publication in internal/stack and internal/passport, ToolHub workload controller (generic_*.go, cell_*.go) | CHG-0333 |
| task-engine | communication spool jobs, supervisor jobs and lanes, toolhub/automation dispatch/outbox/tasks, research jobs | CHG-0335 |
| proactive-engine | internal/routines, toolhub/automation, commodel routine/assignment, communication goal/watchers, runtime goal judge, agenttools routines/assignments/goals | CHG-0344 |
| harness-runtime | internal/runtime without relays and model relay | CHG-0336 |
| tool-hub | internal/toolhub admission and connector lifecycle, remainder of toolhub/api | CHG-0337 |
| memory-hub | internal/memory, internal/vault, ToolHub memory_*/night_*/knowledge_view, knowledge image (OpenKnowledge) | CHG-0342 |
| model-gateway | internal/runtime/model_relay*, cliproxy, internal/mediasvc generation, remote STT/TTS engines | CHG-0341 |
| credential-broker | services/credential-broker plus ToolHub OAuth, internal/credstore, internal/oauth, internal/secrets | CHG-0339 |
| egress-gateway | runtime egress-relay, cellinit cred-proxy, tcp_forwards, internal/netguard | CHG-0340 |
| channel-hub, hosting-hub, ingress | internal/communication remainder, internal/channel, internal/commops, internal/hosting, internal/exposure, internal/dns | CHG-0343 |
| browser-runtime | docker/browser and agenttools/browser.go | CHG-0351 |

### Package owners

Target owner of every `internal/` package. Shared platform packages may be
imported by any module; everything else is reached through its owner's
contract once that owner's CHG lands.

| Owner | Packages |
|---|---|
| shared platform | audit, diagnostics, locale, migration, serviceauth, sqlitedb, storagebudget, identity (envelope primitives only) |
| identity-hub | iam, identity (registry part) |
| spawn-controller | supervisor (spawn part), stack (render), passport, passportview, reconcile, catalog (templates), firecracker, uno, contextlife |
| task-engine | supervisor (jobs, lanes), commodel (jobs), communication (spool jobs) |
| proactive-engine | routines, toolhub/automation, commodel (routine/assignment) |
| tool-hub | toolhub, toolhub/api (until moved to contracts), agenttools (hub side), sshcap, skillstore, skilleval, selfsettings |
| memory-hub | memory, vault, compaction (memory side) |
| model-gateway | media, mediasvc, runtime (model relay) |
| credential-broker | credentialbroker, credstore, oauth, secrets, envstore |
| egress-gateway | netguard |
| channel-hub, hosting-hub | communication (remainder), channel, commops, inboundfile, hosting, exposure, dns |
| harness-runtime | runtime (without relays), agenttools (executor side), companion, systemguide, compaction (runtime side) |
| tooling | devcheck, e2e |

## Current system (v0)

### Split of responsibility

**Hermes** (upstream, pinned — ADR-0012) owns reasoning, chat, tool
selection, memory, skills and hooks. It is never patched in place; a
contract bump is a new ADR + spec.

**Go** owns everything around it: space initialization, strict config,
organization policy, Docker lifecycle, channel routing, durable routines,
bounded file/HH/document tools, ToolHub, the credential boundary and the
supervisor. There is no replacement agent loop, CRM, crawler, database
queue or vector database. Retrieval is search → read → reason over files
and tools.

### Component map

| Service | Role | Detail |
|---|---|---|
| `hermes-runtime` | Per-space agent container: spawns pinned Hermes, serves the private `/v1/*` contract, owns scope mounts | [runtime.md](services/runtime.md) |
| `communication-hub` | Channel gateway: adapters, job admission, spool, `?t=` human links | [communication.md](services/communication.md) |
| `supervisor` | Scale-to-zero spawner: warm runtimes, control-call routing, docker-exec agent-tools | [supervisor.md](services/supervisor.md) |
| `toolhub` | Capability gateway: governance evaluation, connector lifecycle, audit | [toolhub.md](services/toolhub.md) |
| `credential-broker` | Standalone credential authority (copied MIT module) | [credential-broker.md](services/credential-broker/index.md) |
| `hub-stt`/`hub-tts`/`hub-media` | Speech + media sidecars, bearer-auth internal API | [media.md](services/media.md) |
| `browser-<user>` | Per-space browser sidecar: headed Chromium under Xvfb, authenticated noVNC, playwright-mcp endpoints, google_meet node; CDP stays on the space network | [runtime.md](services/runtime.md) |
| `cellinit`/`cell-proxy`/`mcp-bridge` | Disposable workload cells + loopback relays | [cell-proxy.md](services/cell-proxy.md) |
| `model-relay` | Per-space model selection + provider ladder | [model-relay.md](services/model-relay.md) |
| `hubctl` | Operator CLI: render/up/down, secrets, migrations, connector host ops | [hubctl.md](services/hubctl.md) |

The agent-visible tool surface these produce is catalogued in
docs/tools.md; space layout and config in docs/spaces.md; the authorization
model in docs/rbac.md; runtime operation in docs/operations.md.

### Identity and scope

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

### Trust boundary

Default-deny capability posture (ADR-0031, ADR-0032). The rules below are
enforced in code; certification of the deployed stack is pending owner
sign-off of T01-T22 (CHG-0329 S1.1 records the current receipts; rows that
need real-Docker canaries or the pinned probe are re-run at the M40 release
gate). Per-row evidence and open items:
[acceptance matrix](../.work/done/CHG-0065-capability-enforcement/acceptance-matrix.md).
The rules:

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

### Networks and lifecycle

Managed Compose gives each `spaces/<id>` an internal agent network; Hermes
reaches ToolHub and the per-space model relay there. CLIProxy, broker and
sibling runtimes are never attached to the agent network. Shared
control-plane services (toolhub, credential-broker, cliproxy,
workload-controller, communication-hub) are rendered once by the space
whose `agent.yaml` keeps `infra: true`; siblings join the external
`hermes-hub-runtime` network and enroll via `toolhub-tokens.json`.
For a published infra owner, rendering also emits separate Compose plans:
`compose.shared.<env>.yaml` keeps shared services and their existing volumes
in the original project, while `compose.user.<env>.yaml` runs that owner's
`per-space` services in a second project. `hubctl up` and `space apply` use
these plans; the published `stack.yaml` decides placement. The owner's agent
network is an internal external network shared with supervisor-spawned runtimes.

For migrated spaces the published six-passport revision under
`generated/passports/<revision>/` (selected by `generated/passports/current`)
is the sole runtime authority: `stack.yaml` commands this split
imperatively — `stack.services.<instance>.mode` decides per-space (local
container), shared (infra-space render only) or supervised
(`hermes-runtime` as a spawn contract, not a compose service) — while
`config`/`models`/`workspace`/`tools`/`grants` govern behavior, model
routes, admission and rights. The retired `settings.yaml`,
`workspace.yaml` and per-space `models.yaml` are never opened by runtime
readers (`stack.ReadEffective` resolves only the revision plus the slim
`agent.yaml` extension file), and a managed claim without a loadable
revision fails closed rather than downgrading (SPEC-0092 R8.1). The
compose file renders exactly the declared set — a declared per-space
instance with no container fails the render, and dropping an entry removes
the container on apply (SPEC-0092 A3).
During apply, `current` is temporarily pending while the new immutable
bundle, mounted config view, generated files and bound ToolHub authority are
prepared. A single pointer update activates them; managed config reads and
ToolHub admission reject a pending or mismatched revision. Reconciliation of
external service effects follows that activation and can still fail.

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
re-pins the observed digest (CHG-0153).

### Jobs, runs and events

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

M16 session discipline (ADR-0074/SPEC-0071): a conversation's Hermes session
(`context + conversation + revision`) is stable across jobs and retry
attempts; a new session is an explicit owner action (`/new`, `/use`), never a
hidden per-job rotation. Hermes keeps native transcript continuity and
in-loop compaction; the hub does not assemble or inject replacement context.
Admission receipts retain the exact original payload for metadata-only
recovery. Long-lived conversations stay bounded through Hermes compression
and explicit `/new`, not hub-side context replacement.

M17 context control (ADR-0075, SPEC-0072/0073/0074) layers bounded hub
management on top: `/compact`/`context_compact` runs a dual-engine
compaction at turn boundaries (native Hermes compressor or the hub cascade:
prune tool outputs → summarize head → keep verbatim tail), an opt-in sweep
compacts between jobs at a declared threshold, and a journal records every
event and named refusal. `/goal`/`goal_*` persist a per-conversation
objective that continues across turns: each completed turn is judged
(aux/main model call or the self `GOAL_STATUS:` marker), a verified `done`
notifies with bounded evidence, and epoch-stamped continuation jobs stale
out on preemption so the loop can never spin unboundedly.

Every relayed chat-completions request is checked after hop/model rewriting.
The conservative ceiling is 256k estimated tokens — deliberately above the
managed compaction trigger (200k) so in-loop compression handles long
sessions first: serialized UTF-8 bytes at ~6 bytes/token worst case, plus at
least 8192 output tokens (or the larger requested output limit). An
over-ceiling request returns HTTP 413 `context_budget_exceeded` without
sending upstream or cooling down a healthy provider. This is a conservative
text bound, not a measured tokenizer result or a substitute for the selected
model's own limits.

### Capabilities and egress

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

### State

Per space: `connections/`, `workspace/`, `runtime/`, `hermes/`,
`generated/`, `diagnostics/` (layout: docs/spaces.md). Shared: broker state
on an external per-env volume, `communication-hub-data` spool, ToolHub
`/state` (store/audit/governance/credentials/artifacts). Durable records
are content-addressed or revision-fenced: policies and recipes by digest,
grants/bindings by revision, projection revisions monotonic, tombstones
retained for idempotent replay (removed 30 d, terminal operations 7 d).

### Non-goals

- No public multi-tenant service — this is an isolated owner deployment
  boundary; ingress auth + principal mapping are out of repo scope.
- No direct agent access to Docker socket, host shell, provider dashboards
  or credential material.
- No automatic installation of unpinned packages inside agent images.
- No dual scheduling: durable hub routines replace native cron; a
  pre-migration cron pins its context on until migrated.
- Not every guarantee is deployed yet — ADR-0031/32 acceptance is tracked
  in .work; describe the deployed stack, not the target, when answering.

### Site hosting

Opt-in `hosting` adds an owner/env Go web tier and a hosting-only workload controller. It consumes sish exposure and per-user wildcard DNS/TLS. Site snapshots have separate volumes; user code runs outside Hermes with no Hub network. See [hosting](services/hosting.md) for boundaries, fleet/zone coordination and deployment prerequisites.
