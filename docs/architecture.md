---
description: System overview - the v1 service model (service table, core and optional services, profiles, layers and passports, zones, spawn flow, message path, launch backends, tool executors, extension points), the v0 to v1 extraction map, and where the v0 system that runs today is documented.
last_verified: 2026-11-20
---

# Architecture

Hermes Hub is a multi-tenant layer over the Hermes agent harness. It adds
what Hermes lacks for many users: isolation, secrets, rights, channels,
memory, jobs and proactivity. v1 (milestone M40) builds it from small Go
services with one narrow responsibility each, and keeps every existing tool
and feature.

The rules are in [engineering.md](engineering.md), the lifecycles in
[state-machines.md](state-machines.md), the decisions in
[ADR-0105](adr/ADR-0105-v1-topology.md)..[ADR-0112](adr/ADR-0112-v1-secrets.md),
the contract list and acceptance in
[SPEC-0100](../specs/active/SPEC-0100-v1-contracts.md). M40.2 ships the skeleton
plus minimal spawn-controller and harness-runtime binaries that carry the
process-mode message path; M40.3..M40.13 extract the full services and
contracts. What runs on `dev` today is the v0 system at the end of this page.

## Services

Each service is one binary with its own database, contract, state machines
and README ([ADR-0105](adr/ADR-0105-v1-topology.md)).

| Service | Zone | Owns | Does not own | Extension point | When disabled | Scale |
|---|---|---|---|---|---|---|
| spawn-controller | control | Space, PassportRevision per layer, Runtime, Worker, mounts, image pins, warm pool | people, jobs, call-time rights | launch backends; passport templates; extra services from `stack` | core, never disabled | 1 on SQLite, N on Postgres with a lease |
| harness-runtime | space | Hermes, session slots, workspace tool executor (tools-daemon) | everything else | Hermes native tools and plugins | core, never disabled | one per (space, agent, policy digest); scales to zero |
| identity-hub | control | Principal (human, agent, system), Org, roles, ChannelLink, signing keys, run tokens | tool rights, passports | identity providers (OIDC) | single owner from `stack` | 1 / N |
| task-engine | control | Job, Run, conversation queues, run events | execution, channels | none | Hermes queue | 1 / N |
| proactive-engine | control | Routine, Occurrence, Assignment, Goal, Watch | jobs (creates them through task-engine) | event sources (M41) | Hermes cron | 1 / N |
| tool-hub | agent | catalog, skills, connectors, bindings, call-time grant check, OwnerDecision, routing | memory, schedules, media, containers | connectors, skills, CLI recipes | Hermes native tools | horizontal |
| memory-hub | agent | memory items, vault index | workspace files | memory providers (OpenKnowledge first) | Hermes native memory | 1 / N |
| model-gateway | agent | model routes, limits, usage and cost ledger, image and video generation, STT, TTS | keys (refs to the broker) | model and media providers | runtime calls the provider directly | horizontal |
| channel-hub | edge | Conversation, inbound and outbound messages, delivery | people, jobs | channel adapters | Hermes native Telegram gateway | 1 / N |
| ingress-gateway | edge | the one public entry: TLS, webhooks, `?t=` links, site routes | business state | routes | no public entry (polling only) | horizontal |
| hosting-hub | edge | Site, Exposure, DNS record | - | DNS and tunnel providers | no site publishing | 1 / N |
| credential-broker | secret | secrets, OAuth state, credential leases | everything else | secret providers | env secrets (trusted only) | 1 / N |
| egress-gateway | egress | network exit, domain allowlist, boundary credential injection | - | corporate proxies | direct egress (trusted only) | horizontal |
| browser-runtime | space | one browser per space | - | - | Hermes native browser | per space |
| workers | space | disposable cells for MCP, CLI, terminal (cellinit, cred-proxy) | - | tool images | - | disposable |
| hubctl | - | nothing: operator CLI over the APIs, launcher | - | - | - | - |

## Core and optional

The core is spawn-controller plus harness-runtime. It runs alone with
Hermes native tools: the trusted single-user mode. Every other service is
optional. A peer address comes from `HUB_<SVC>_URL`; empty means disabled,
and the feature falls back to the Hermes native implementation
(`via: native` in the `tools` passport) or switches off with a message. It
never breaks. Tenant isolation, secrets outside the model and grants need
identity-hub, tool-hub, credential-broker and egress-gateway.

## Installation profiles

| Profile | Services | Trust |
|---|---|---|
| `minimal` | spawn-controller, harness-runtime (native tools) | trusted |
| `family` | minimal + identity-hub, task-engine, channel-hub, tool-hub, model-gateway, credential-broker, egress-gateway | trusted or not |
| `enterprise` | all services, Postgres, gVisor or Firecracker | not trusted |

The installation flag `trusted: true|false` is the whole deployment mode.
With `false`, process mode and runc without gVisor are refused and
egress-gateway is mandatory.

## Layers and passports

Layers: `platform -> org -> user -> agent` (a special agent or a
subagent). Each layer carries all or part of the same six passports:
`stack`, `config`, `models`, `workspace`, `tools`, `grants`. A lower layer
only narrows. identity-hub knows membership and roles; spawn-controller
stores the layer passports and computes the effective spec with the
existing passport compiler, plus a digest. Editors: platform-admin,
org-admin, the user, and the user for their own agents. Workspaces and
extra services change through passports
([ADR-0106](adr/ADR-0106-identity-layers-run-tokens.md)).

Enforcement points: spawn-controller at spawn (mounts, services, enabled
tools, Hermes config); tool-hub at every call; model-gateway, channel-hub
and egress-gateway check their own slice by policy digest.

## Zones

Zones are network and secret rules, not processes. Edges are default-deny;
only edges declared in a service descriptor open, and each launch backend
implements them natively. Runtimes and workers are untrusted.

| Zone | Services | Reached from |
|---|---|---|
| edge | channel-hub, ingress-gateway, hosting-hub | internet (through ingress-gateway only) |
| control | spawn-controller, identity-hub, task-engine, proactive-engine | edge and agent services over contracts; never from a runtime |
| agent | tool-hub, memory-hub, model-gateway | runtimes with a run token |
| secret | credential-broker | agent and control services for leases |
| egress | egress-gateway | runtimes and workers (proxy settings) |
| space | harness-runtime, browser-runtime, workers | spawn-controller only; never another space |

Metadata and private address ranges are blocked from the space zone.
Crossing rules: [boundaries.md](boundaries.md).

## Spawn flow

1. A message reaches ingress-gateway, then channel-hub.
2. identity-hub resolves the human and the agent.
3. task-engine creates the Job (idempotency key = channel message id).
4. task-engine asks spawn-controller for a runtime for (space, agent).
   spawn-controller merges the layer passports into the effective spec and
   digest, claims a warm runtime for the image digest and binds tenant and
   policy digest, or starts one from the prebuilt pinned image (never
   builds on spawn). It mounts only that space's workspace (narrowed for a
   subagent) and its config slice.
5. identity-hub signs the run token: who, agent, space, run, generation,
   policy digest, origin, audiences.
6. The runtime runs the session in a free slot. Tools go to tool-hub (token
   plus grant check), which executes, routes to the owner service, asks the
   owner (OwnerDecision), or asks spawn-controller for a worker. Models go
   through model-gateway, network through egress-gateway. Runtimes hold
   only credential refs; values are injected at the boundary.
7. The result goes task-engine -> channel-hub -> user. An idle runtime
   stops; its volume stays (scale to zero).

## Message path

```text
user -> ingress-gateway -> channel-hub -> identity-hub (who)
                                       -> task-engine (Job) -> spawn-controller (runtime)
runtime -> tool-hub -> owner service | worker      (run token on every call)
        -> model-gateway -> provider               (key leased from credential-broker)
        -> egress-gateway -> internet              (domain allowlist, injection)
result  -> task-engine -> channel-hub -> user
```

Every conversation belongs to exactly one space. In a group chat the space
owner's rights apply; other participants are guests whose messages are
untrusted input and who cannot approve. Different users never share a
runtime. The same human, agent and policy digest share one runtime with
parallel sessions in slots; narrower rights get a separate runtime.

## Launch backends

The same set runs hub services (`hubctl run --mode`) and user runtimes and
workers (spawn-controller). The interface is `Start(spec)`, `Stop(id)`,
`Inspect(id)`, `List()`; the spec is fully resolved by spawn-controller.
One conformance suite runs against every backend.

| Backend | Isolation | Use | Milestone |
|---|---|---|---|
| process | none | dev, trusted single user only | M40.2 (services), M40.3 (runtimes) |
| docker / podman | container, seccomp, networks; `runsc` (gVisor) as option, default for multi-user | VPS, family, team | M40.3 |
| kubernetes | pods, NetworkPolicy, namespace per org, optional gVisor or Kata RuntimeClass | many users, horizontal scale | M40.15 |
| firecracker | microVM | untrusted users, enterprise | M40.15 |

## Tool executors

Every tool in the `hub-agent-tools` catalog keeps its name, schema, effect
and result; v1 changes only who executes it.

| Family | Executor today (v0) | Executor in v1 |
|---|---|---|
| files, documents, media inspect, context | runtime tools-daemon | harness-runtime |
| terminal | ToolHub cell | worker (through tool-hub) |
| ssh | runtime + broker lease | harness-runtime + broker lease + egress-gateway |
| exposure, sites | runtime -> hosting service | hosting-hub (through tool-hub) |
| web_search | ToolHub admission, runtime provider | tool-hub -> egress-gateway |
| browser | per-space Playwright sidecar | browser-runtime |
| routines, watches, assignments, goals | runtime -> communication -> ToolHub automation | proactive-engine (through tool-hub) |
| settings | ToolHub -> supervisor | spawn-controller (through tool-hub) |
| memory_* | ToolHub in-process | memory-hub (through tool-hub) |
| platform_*, tool_search, tool_call, connectors | ToolHub | tool-hub |
| sessions | ToolHub -> communication | task-engine (through tool-hub) |
| models | ToolHub -> model relay | model-gateway (through tool-hub) |
| media generation, media_speak, STT, TTS | hub-media, hub-stt, hub-tts, communication | model-gateway (+ channel-hub for delivery) |
| research | ToolHub flow | tool-hub flow (unchanged) |
| bounded CLI, prepared and remote connectors, raw user/org MCP | ToolHub cells, ToolHive, Hermes `mcp_servers` | tool-hub + workers; raw MCP stays native per passport |
| native Hermes tools | Hermes | Hermes, governed by the passport at spawn |

Features kept: dreaming and night runs, memory proposals, human
confirmation (now OwnerDecision), grant requests, onboarding, knowledge
view, OAuth, Telegram account MCP, hosting fleet budget, image pins and
dev-rebuild, backups, observability, CLI artifact build and release,
research jobs, context compaction.

## Extension points

Six, each one provider interface with an existing first provider (rule S1):

| Point | Owner | First provider |
|---|---|---|
| launch backends | spawn-controller | process, docker |
| connectors and skills | tool-hub | ToolHive and cells, prepared connectors |
| channel adapters | channel-hub | Telegram, Slack |
| model and media providers | model-gateway | current model relay providers, cliproxy, media engines |
| memory providers | memory-hub | OpenKnowledge vault |
| identity providers | identity-hub | OIDC (`oidc-provider`, bundled ZITADEL) |

Customization beyond these goes through passports and layers, and extra
services declared in `stack`.

## Extraction map (v0 -> v1)

Services are built by moving existing code, never by rewriting it beside
the original; the old code is deleted in the same change.

| v1 service | Built from (v0) | Issue |
|---|---|---|
| shared/* | `contracts/internal/wire`, `internal/sqlitedb`, `internal/locale`, service auth | M40.2 #600 |
| spawn-controller | `internal/supervisor` (spawn, lease, reconcile, backends), `internal/stack` (render), `internal/passport`, `internal/passportview`, ToolHub workload controller, `hubctl space apply` logic | M40.3 #601 |
| harness-runtime | `internal/runtime` without relays, `internal/agenttools` executor side, `internal/compaction`, `internal/companion`, `internal/systemguide` | M40.4 #602 |
| identity-hub | `internal/identityhub`, `internal/iam`, communication identity code | M40.5 #603 |
| task-engine | supervisor jobs and lanes, communication spool jobs, ToolHub automation tasks, `sessions` backend | M40.6 #604 |
| tool-hub | `internal/toolhub` admission, projection, connectors, recipes, onboarding, lifecycle; remainder of `internal/toolhub/api`; `internal/catalog` as static YAML | M40.7 #605 |
| channel-hub, ingress-gateway | `internal/communication` (adapters, delivery), `internal/channel`, `internal/commops`, public listeners and `?t=` links | M40.8 #606 |
| memory-hub | `internal/memory`, `internal/vault`, ToolHub memory, night-run and knowledge view code | M40.9 #607 |
| proactive-engine | `internal/routines`, `internal/toolhub/automation`, communication goals and watchers, agent-tool routine/assignment/goal routes | M40.10 #608 |
| hosting-hub | `internal/hosting`, `internal/exposure`, `internal/dns` | M40.11 #609 |
| model-gateway | runtime model relay, cliproxy materialization, `cmd/hub-media`, `internal/mediasvc`, `cmd/hub-stt`, `cmd/hub-tts` | M40.12 #610 |
| credential-broker, egress-gateway | `services/credential-broker`, `internal/credstore`, `internal/oauth`, `internal/secrets`, runtime egress relay, cellinit cred-proxy, `internal/netguard` | M40.13 #611 |

Deleted without a successor: `cmd/hub` and the zone multiplexer, the
self-made dependency gate and baseline, `contracts/toolsurface` (M40.2);
the unused identity layer, profile, break-glass and admin APIs,
`communication.users.yaml`, `TELEGRAM_ALLOWED_USERS` (M40.5); every
service-state `.json`/`.jsonl` file (per service); signed policy bundles
(M40.3, M40.14).

## What runs today (v0)

Until each extraction lands, the v0 services below run on `dev`. Their
internals are documented per service; this page does not repeat them.

| v0 service | Role | Detail |
|---|---|---|
| `hermes-runtime` | per-space agent container: pinned Hermes, private `/v1/*` job contract, scope mounts, relays | [runtime.md](services/runtime.md) |
| `communication-hub` | channel gateway: adapters, job admission, spool, `?t=` links | [communication.md](services/communication.md) |
| `supervisor` | scale-to-zero spawner, job ledger and lanes, control routing, identity registry host | [supervisor.md](services/supervisor.md), [identity-hub.md](services/identity-hub.md) |
| `toolhub` | capability gateway: governance, connector lifecycle, automation, memory, audit | [toolhub.md](services/toolhub.md) |
| `credential-broker` | standalone credential authority (copied MIT module) | [credential-broker](services/credential-broker/index.md) |
| `hub-stt`, `hub-tts`, `hub-media` | speech and media sidecars | [media.md](services/media.md) |
| `browser-<user>` | per-space browser sidecar | [runtime.md](services/runtime.md) |
| `cellinit`, `cell-proxy`, `mcp-bridge` | disposable workload cells and loopback relays | [cell-proxy.md](services/cell-proxy.md) |
| `model-relay` | per-space model selection and provider ladder | [model-relay.md](services/model-relay.md) |
| `hubctl` | operator CLI | [hubctl.md](services/hubctl.md) |
| hosting tier (opt-in) | per-user static sites and stateless apps | [hosting.md](services/hosting.md) |

Space layout and passports: [spaces.md](spaces.md). Rights:
[rbac.md](rbac.md). Routines: [routines.md](routines.md). Operations:
[operations.md](operations.md). Threats: [threat-model.md](threat-model.md).

The v0 trust rules (default-deny admission, credentials never in the model
plane, private runtime job contract, one-time `?t=` links, deny-by-default
workload egress) are enforced in code. Certification of the deployed stack
is pending: the CHG-0065 T01-T22 receipts are re-run and signed off by the
owner at the v1 release (M40.16); until then the claims read "enforced in
code, certification pending", not "certified".
