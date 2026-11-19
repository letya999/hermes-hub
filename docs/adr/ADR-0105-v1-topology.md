---
description: v1 service model - minimal isolated Go services with one responsibility each, a mandatory core (spawn-controller plus harness-runtime) and optional services with native fallback, zones as network and secret rules, installation profiles, launch backends, the trusted flag, contracts and single owners.
last_verified: 2026-11-20
---
# ADR-0105: v1 service model

Status: proposed. Rewritten 2026-11-20 for the simplified v1 plan (M40.1
#599, CHG-0334); the direction was agreed with the owner on 2026-11-20 and
the record text awaits owner review. The 2026-11-19 version (five hub
processes, `hub serve --zone`) was never merged to `dev` and is replaced,
not amended.
Delivery: M40.2 (skeleton) and the service issues M40.3..M40.13, traced in
[SPEC-0100](../../specs/active/SPEC-0100-v1-contracts.md).
Supersedes: ADR-0046 (comm-plane zone map), ADR-0095 (ToolHub as owner of
memory and automation). Partly supersedes: ADR-0026 (shared control plane
render split), ADR-0094 (ToolHub as implementation owner of every tool
family; the catalog r5 vocabulary stays).
Related: ADR-0106 (identity, tokens), ADR-0107 (execution), ADR-0110
(storage), ADR-0111 (state machines), ADR-0112 (secrets).

## Context

v0 grew into tightly coupled services. Evidence on `dev` (2026-11-19,
`go list -deps`):

- `cmd/runtime` links 37 internal packages, `cmd/supervisor` 40 and
  `cmd/communication` 44; all three linked `internal/toolhub`,
  `internal/stack`, `internal/hosting`, `internal/dns` and `internal/vault`
  before CHG-0330 cut the store edges.
- `internal/toolhub/api` is about 24k lines in 104 files: DTOs mixed with
  Telegram, CLI release, OCI catalog, artifact build and recipe logic.
- Three job ledgers (communication spool, supervisor jobs and lanes,
  ToolHub automation tasks), four effect vocabularies, five confirmation
  mechanisms, dozens of `.json`/`.jsonl` state files, and rules evaluated
  in several places.
- Configuration fallbacks continued instead of failing (removed by
  CHG-0329).

The first v1 plan (2026-11-19) grouped modules into five hub processes.
That kept the coupling inside each process and needed a zone multiplexer
(`cmd/hub`) and a self-made dependency gate to hold it apart.

## Decisions

1. **One service, one responsibility.** Each v1 service is a separate Go
   binary under `services/<svc>/` with its code in `internal`, its own
   database (ADR-0110), its own contract, its own state machines
   (ADR-0111), a README from the template and its own tests. A service
   imports only `shared/*` (infrastructure without domain rules) and
   `contracts/*`. Go's `internal` visibility enforces the walls; the
   self-made dependency gate and `cmd/hub` are deleted in M40.2.
2. **Core and optional.** The core is spawn-controller plus
   harness-runtime; it runs alone with Hermes native tools as the trusted
   single-user mode. Every other service is optional. When a service is
   disabled (`HUB_<SVC>_URL` empty) its feature falls back to the Hermes
   native implementation (`via: native` in the `tools` passport) or
   switches off with a message; it never breaks. Tenant isolation, secrets
   outside the model and grants need identity-hub, tool-hub,
   credential-broker and egress-gateway.
3. **The service set.** spawn-controller, harness-runtime, identity-hub,
   task-engine, proactive-engine, tool-hub, memory-hub, model-gateway,
   channel-hub, ingress-gateway, hosting-hub, credential-broker,
   egress-gateway; browser-runtime and workers are spawned by
   spawn-controller; `hubctl` is an API-only operator CLI. Owned records,
   extension points and fallbacks per service are in
   [architecture.md](../architecture.md).
4. **Naming rule (kept).** A name ends with the suffix of what it owns:
   `-hub` a registry or surface many callers read, `-engine` durable state
   that advances over time, `-controller` external resources reconciled to a
   desired state, `-gateway` a policy-enforcing pass-through, `-broker`
   secret custody and leases, `-runtime` per-space untrusted execution. A
   name that needs two suffixes is two services.
5. **Zones are network and secret rules, not processes.** Zones: control,
   agent, edge, secret, egress, space. Each service belongs to one zone.
   Edges are default-deny: only edges declared in the service descriptor
   open. Each launch backend implements them natively. Runtimes and workers
   (space zone) are untrusted and never get the Docker socket.
6. **Communication.** HTTP+JSON through `contracts/<svc>/v1` (DTOs, one
   client interface, in-process and HTTP transports, a fake that passes the
   same conformance suite). Token on every call, timeouts, idempotency key
   on writes. No shared files, no message broker. A breaking change adds
   `v2` beside `v1`. Contract DTOs carry no secret values.
7. **Single owners.** One job ledger (task-engine), one secret owner
   (credential-broker), one owner per database. Any other service reads that
   state only through the owner's contract.
8. **Installation profiles.** `minimal` = spawn-controller +
   harness-runtime (native tools), trusted. `family` = minimal +
   identity-hub, task-engine, channel-hub, tool-hub, model-gateway,
   credential-broker, egress-gateway. `enterprise` = all services, Postgres,
   gVisor or Firecracker.
9. **Launch backends.** The same backend set runs hub services
   (`hubctl run --mode`) and user runtimes and workers (spawn-controller):
   `process` (no isolation, trusted only), `docker`/`podman` (container,
   seccomp, networks; `runsc` as an option and the default for multi-user),
   `kubernetes` (pods, NetworkPolicy, namespace per org), `firecracker`
   (microVM). The backend interface is `Start(spec)`, `Stop(id)`,
   `Inspect(id)`, `List()`; the spec is fully resolved by spawn-controller
   and backends never decide policy. One conformance suite runs against
   every backend.
10. **The `trusted` installation flag.** `trusted: true|false` is the whole
    deployment mode. With `false`, spawn-controller refuses the process
    backend and runc without gVisor, and egress-gateway is mandatory.
11. **Fail-closed configuration (kept from v0, ADR-0096).** Missing or
    unreadable configuration never selects a weaker mode; the process exits
    non-zero with a localized message. An unknown `HUB_*` variable fails
    startup.
12. **Harness.** Hermes only in v1. Hermes-specific code lives in one
    package of harness-runtime; a generic harness contract is M42 work.

## Boundary crossings

Every edge not listed is refused.

| From -> to | Allowed | Refused |
|---|---|---|
| internet -> edge | ingress-gateway: channel webhooks with channel proof, one-time `?t=` links, hosted sites | anonymous principal, any control or agent route |
| edge -> control | channel-hub to identity-hub and task-engine contracts with a service token | container, passport or ledger writes outside those contracts |
| control -> space | spawn-controller starts, stops and inspects runtimes and workers through a backend | any other service calling a backend; reading runtime memory or files beyond the space |
| space -> agent | runtime calls to tool-hub, memory-hub, model-gateway with a run token naming that audience | calls without a token, or with a forged, aliased, stale or foreign-space token |
| space -> egress | outbound HTTP(S) through egress-gateway per space policy | private, loopback and metadata destinations; destinations outside policy |
| agent or control -> secret | credential leases bound to a verified run or an operator action | value export into tool results, logs, rows or DTOs |
| space -> space | nothing | any cross-space path |

## Alternatives

- **Five hub processes with zone multiplexing (2026-11-19 plan).**
  Rejected: it kept the coupling inside each process and needed `cmd/hub`
  plus a self-made dependency gate to hold modules apart.
- **Generic harness contract (ACP, adapters) now.** Deferred to M42: no
  second harness in v1 (simplicity rule S1).
- **Message broker between services.** Rejected: HTTP with idempotency
  keys and per-service state machines is enough and adds no dependency.
- **Separate repository per service.** Rejected: atomic contract changes
  and one CI matter more at this size.

## Preserved v0 invariants

- Default-deny admission: no tool is reachable without a grant (ADR-0031).
- Fail-closed passports: a missing or foreign revision denies (ADR-0096).
- Uncertain outcomes are never retried; they are resolved by observation
  (ADR-0036).
- Credentials never enter the model plane (ADR-0016, ADR-0049, ADR-0112).
- Scale-to-zero: idle runtimes hold no compute (ADR-0014).
- Hermes is pinned upstream and never patched in place (ADR-0012).
- Every existing agent tool keeps its name, schema, effect and result.

## Consequences

- M40.2 deletes `cmd/hub`, the zone multiplexer, the dependency gate and
  baseline, and `contracts/toolsurface`; `contracts/internal/wire` becomes
  `shared/httpapi`.
- Each service issue adds its contract, README and state machines when it
  extracts the service, and deletes the old code in the same change.
- A disabled optional service is a supported configuration and is tested.
