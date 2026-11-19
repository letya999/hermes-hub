---
description: v1 topology - five trust zones, five hub processes plus on-demand space runtimes, the -hub/-engine/-controller/-gateway/-broker/-runtime/-worker naming rule, modules vs processes, versioned contracts and the v0 service extraction map.
last_verified: 2026-11-19
---
# ADR-0105: v1 topology - trust zones, five hub processes, contracts

Status: accepted direction (M40, owner issue #572). Date: 2026-11-19.
Delivery: CHG-0328 (records), CHG-0329 (fail-closed baseline), CHG-0330
(contracts and dependency gate), the service CHGs listed in SPEC-0100.
Supersedes: ADR-0046 (comm-plane zone map, replaced by hub-edge),
ADR-0095 (ToolHub as owner of memory and automation). Partly supersedes:
ADR-0026 (shared control plane render split), ADR-0094 (ToolHub as the
implementation owner of every tool family; the catalog r5 vocabulary stays).

## Context

v0 has service boundaries only at deployment time. Evidence on `dev`
(2026-11-19, `go list -deps`):

- `cmd/runtime` links 37 internal packages, `cmd/supervisor` 40 and
  `cmd/communication` 44; all three link `internal/toolhub`,
  `internal/toolhub/api`, `internal/stack`, `internal/hosting`,
  `internal/dns` and `internal/vault`.
- The edges come from `internal/agenttools` (opens the ToolHub store
  in-process: `loadToolHub`), `internal/stack` (opens the store during render:
  `migrateToolhubBindings`, and uses ToolHub controller types in hosting),
  `internal/runtime` and `internal/supervisor` (DTOs from `toolhub/api`).
- `internal/toolhub/api` is 24k lines in 104 files: DTOs mixed with
  Telegram, CLI release, OCI catalog, artifact build and recipe logic.
- Ownership is split per concern: four job ledgers (communication spool,
  supervisor jobs and lanes, ToolHub automation outbox/tasks, research jobs),
  three secret holders (broker, `internal/credstore`, ToolHub OAuth), and
  configuration fallbacks that continue instead of failing (the gateway runs
  Hermes itself when no runtime URL is set; an unmanaged capability mode
  duplicates the security model).

A proactive platform multiplies callers of every boundary (listeners,
assignments, watchdog). The boundaries must exist in code before more callers
are added, or every later extraction re-discovers hidden edges.

## Decisions

1. **Five trust zones.** Each zone has one reason to exist and one class of
   authority:

   | Zone | Holds | Reachable from |
   |---|---|---|
   | edge | channel credentials, public listeners, human-action links | internet (authenticated per channel), control zone |
   | control | identity registry, policy bundles, job ledger, container authority (Docker/Kubernetes/Firecracker), proactive clock | edge and agent zones over contracts; never from a runtime |
   | agent | tool admission, memory, model routing; verifies run tokens | space runtimes on their agent network only |
   | secret | user and connector credentials, OAuth state, leases | agent and control zones over signed contracts |
   | egress | outbound network, boundary credential injection | runtimes and workers through proxy settings only |

   Space runtimes and workers are not a zone: they are untrusted execution
   attached to one space.
2. **Five hub processes plus on-demand runtimes.** `hub-control`,
   `hub-agent`, `hub-edge` (one `hub` binary, `hub serve --zone
   control|agent|edge`), `credential-broker` (own Go module, ADR-0022) and
   `egress-gateway`. Space runtimes (`harness-runtime`, `browser-runtime`) and
   workers are spawned by `spawn-controller` on demand and scale to zero
   (ADR-0014). The minimal topology is `hub-control` plus one
   `harness-runtime` with native Hermes tools; the control process may then
   also serve the edge and agent zones (`--zone control,agent,edge`). Zones
   co-hosted in one process still talk only through contracts.
3. **Naming rule.** A service name ends with the suffix of what it owns:
   `-hub` a registry or surface many callers read (identity-hub, tool-hub,
   memory-hub, channel-hub, hosting-hub); `-engine` durable state that
   advances over time (task-engine, proactive-engine); `-controller` external
   resources reconciled to a desired state (spawn-controller); `-gateway` a
   policy-enforcing pass-through without own business state (model-gateway,
   egress-gateway); `-broker` secret custody and leases (credential-broker);
   `-runtime` per-space untrusted execution (harness-runtime,
   browser-runtime); `-worker` a disposable single-purpose cell. A name that
   needs two suffixes is two services.
4. **Modules vs processes.** A module is a Go package tree
   `internal/<module>` plus its contract `contracts/<module>/v1`. A process is
   a zone role that hosts modules. Deployment chooses co-location; code never
   assumes it. Moving a module between processes changes wiring, not callers.
5. **Contracts pattern.** `contracts/<name>/v1` holds the DTOs, one `Client`
   interface, an in-process transport, an HTTP transport and a conformance
   suite that runs the same cases against both transports. A contract
   imports only the standard library and other contracts. Breaking changes
   add `v2` beside `v1`; `v1` is deleted only after no caller links it.
   Contract DTOs carry no secret values (references only).
6. **Dependency gate.** `devcheck` holds a per-binary dependency allowlist
   computed with `go list -deps`. A module may import `contracts/*`, its own
   tree and shared platform packages (locale, audit, identity primitives,
   sqlitedb, netguard). The baseline is shrink-only: an existing violation may
   be removed from the baseline, a new one fails `just check`. Transitive
   edges count: the gate checks the full dependency closure of each binary,
   not direct imports.
7. **Single owners.** One job ledger (task-engine), one secret authority for
   user and connector credentials and OAuth (credential-broker), one registry
   owner per store (the module whose name is on it). Any other process reads
   that state only through the owner's contract; opening another module's
   store file is a gate violation (S2.3 removes the existing ones).
8. **Fail-closed configuration.** Missing or unreadable configuration never
   selects a weaker mode. A process without its required endpoint, passport,
   policy bundle or key material exits non-zero with a localized message.
   There is no unmanaged mode and no in-process Hermes fallback: authority
   comes from published passports only (ADR-0096).
9. **Service extraction map.** Each v1 service is built from existing code,
   never rewritten beside it:

   | v1 service (zone) | v0 sources |
   |---|---|
   | identity-hub (control) | internal/identity, internal/iam, communication.users.yaml, TELEGRAM_ALLOWED_USERS |
   | spawn-controller (control) | internal/supervisor, passport publication in internal/stack and internal/passport, ToolHub workload controller (generic_*.go, cell_*.go) |
   | task-engine (control) | communication spool jobs, supervisor jobs and lanes, toolhub/automation dispatch/outbox/tasks, research jobs |
   | proactive-engine (control) | internal/routines, toolhub/automation, commodel routine/assignment, communication goal/watchers, runtime goal judge, agenttools routines/assignments/goals |
   | tool-hub (agent) | internal/toolhub admission and connector lifecycle, remainder of toolhub/api |
   | memory-hub (agent) | internal/memory, internal/vault, ToolHub memory_*/night_*/knowledge_view, knowledge image (OpenKnowledge) |
   | model-gateway (agent) | internal/runtime/model_relay*, cliproxy, internal/mediasvc generation, remote STT/TTS engines |
   | credential-broker (secret) | services/credential-broker plus ToolHub OAuth, internal/credstore, internal/oauth, internal/secrets |
   | egress-gateway (egress) | runtime egress-relay, cellinit cred-proxy, tcp_forwards, internal/netguard |
   | channel-hub, hosting-hub, ingress (edge) | internal/communication remainder, internal/channel, internal/commops, internal/hosting, internal/exposure, internal/dns |
   | harness-runtime (space) | internal/runtime without relays and model relay |
   | browser-runtime (space) | docker/browser and agenttools/browser.go |

## Boundary crossings

Every arrow not listed is refused.

| From -> to | Allowed | Refused |
|---|---|---|
| internet -> edge | channel webhooks/polls with channel proof (Slack HMAC, Telegram allowlist), one-time `?t=` links, hosted sites | anonymous principal, any control or agent route |
| edge -> control | `identity`, `task` contracts with a service credential; edge submits jobs and reads outcomes | container, policy or ledger writes outside the task contract |
| control -> runtime | spawn, stop, restart, private `/v1/*` job contract | reading runtime memory or files beyond the mounted space |
| runtime -> agent | tool, memory and model calls carrying a run token | calls without a token, with a forged/aliased/stale-generation token, or to another space's audience |
| runtime -> egress | outbound HTTP(S) and declared TCP forwards per space policy | private, loopback and metadata destinations; destinations the model chooses outside policy |
| agent/control -> secret | credential lease requests bound to a verified run or operator confirmation | value export into tool results, logs, audit or DTOs |
| worker -> anything | only the endpoints its admission receipt names | hub networks, other workers, Docker socket |

## Alternatives

- **Microservice per v1 service (twelve processes).** Rejected: one VPS must
  hold the platform (SPEC-0101); extra processes buy no trust separation
  beyond zones.
- **Keep one binary per v0 service and split later.** Rejected: binaries
  already link everything; without a gate every extraction regresses.
- **Shared library of DTOs without transports.** Rejected: an import of
  DTOs is still an import of whatever the DTO package links (the 24k-line
  `toolhub/api` is that failure).
- **Separate repository per service.** Rejected: atomic contract changes
  and one CI gate matter more than repository isolation at this size.

## Preserved v0 invariants

- Default-deny admission: no tool is reachable without a grant (ADR-0031).
- Fail-closed passports: a missing or foreign revision denies (ADR-0096).
- Uncertain outcomes are never retried; they are reconciled by observation
  (ADR-0036).
- Credentials never enter the model plane: runtimes and models see
  references, never values (ADR-0016, ADR-0049).
- Scale-to-zero: idle spaces hold no compute (ADR-0014).
- Hermes is pinned upstream and never patched in place (ADR-0012).

## Consequences

- Each service issue adds its own contract when it extracts the service;
  this ADR fixes only the pattern and the first three contracts (identity,
  task, toolsurface) in CHG-0330.
- `toolhub/api` shrinks as DTOs move to contracts and logic moves to owning
  modules; the dependency baseline records what is left.
- A process that cannot reach a required peer fails health, it does not
  degrade into a local substitute.
