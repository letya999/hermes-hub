---
description: Architectural boundaries - v1 crossings between zones and services (who may cross, what is refused), current per-component walls, plane boundaries and system non-goals.
last_verified: 2026-11-20
---

# Boundaries

The walls that keep the trust model intact. Each entry names who may cross
and what must never cross. The v1 table is the target fixed by
[ADR-0105](adr/ADR-0105-v1-topology.md)..[ADR-0112](adr/ADR-0112-v1-secrets.md)
(zones and services: [architecture.md](architecture.md#zones)); the
per-component table describes the current v0 services.

## v1 crossings

Zones are network and secret rules; every edge not listed is refused. Each
refusal gets a test in the issue that builds the crossing
([SPEC-0100](../specs/active/SPEC-0100-v1-contracts.md)).

| From -> to | Allowed | Refused |
|---|---|---|
| internet -> edge | ingress-gateway: channel webhooks with channel proof, one-time `?t=` links, hosted sites | anonymous principal, any control or agent route |
| edge -> control | channel-hub to identity-hub and task-engine contracts with a service token | container, passport or ledger writes outside those contracts |
| control -> space | spawn-controller starts, stops and inspects runtimes and workers through a backend | any other service calling a backend; reading files beyond the space |
| space -> agent | tool, memory and model calls with a run token naming that service in `aud` | missing, forged, expired, foreign-space, run-aliased or stale-generation tokens; header identity |
| space -> egress | outbound HTTP(S) through egress-gateway per space policy | private, loopback and metadata destinations; destinations outside policy |
| agent or control -> secret | credential leases bound to a verified run or an operator action | value export into tool results, logs, rows or DTOs |
| space -> space | nothing | any cross-space path |
| worker -> anything | the endpoints its spec names | hub networks, other workers, the Docker socket |
| any service -> another service's database | the owner's contract | opening the owner's database |
| agent -> rights | the narrowed agent layer of its user's passports | widening, another user's credentials |
| system principal -> user work | jobs through a routine or assignment the user owns, `origin=unprompted` | acting with user credentials, appearing as the user |
| untrusted input -> authority | data for the run (email, web, webhooks, tool results, other agents, guests) | raising a mode, approving an OwnerDecision |
| unprompted run -> write-external or critical | an approved OwnerDecision | anything else |

## Current per-component walls (v0)

| Component | May | May never |
|---|---|---|
| `communication-hub` | Own channels, spool, `?t=` links, job admission | Mount scope homes, touch Docker, hold credential values (telegram_auth in-memory session is the single reviewed exception), run Hermes itself (it refuses to start without a runtime or supervisor endpoint) |
| `supervisor` | Docker lifecycle for agent runtimes, control-call routing, `/v1/restart-request` relay | See channel credentials, derive identity from message content, own ToolHub authority |
| `toolhub` | Governance decisions, connector lifecycle, audit ledger, bounded cells | Hold credential values (refs only), write spawned-runtime state, admit a cell from model-supplied config |
| `credential-broker` | Store/deliver secrets under signed contracts and leases | Expose materialized values through MCP projections, logs, or audit ledgers |
| `hermes-runtime` | Spawn pinned Hermes, serve the private `/v1/*` contract, own its scope mounts | Receive the Docker socket, mount a parent containing other users' homes, be an authorization source |
| `workload-controller` | Admit cells against reviewed definitions | Trust HTTP reachability as enforcement evidence |
| Model / agent | Select and call projected tools | Mint grants or confirmations, choose egress destinations, see credential material, register MCP servers |

## Plane boundaries

- **Agent network vs control plane** - the managed agent network has no
  default route; Hermes reaches only ToolHub relay + model relay there.
  CLIProxy, broker, sibling runtimes never attach to it. The single
  exception path is supervisor `docker exec` for agent-tools - the only
  control path that never crosses the agent network.
- **Data vs authority** - channel IDs, session files, web content, tool
  output are transport/data. Identity, grants and policy come from
  host-owned records only.
- **User vs organization** - org mounts are read-only; org policy can only
  narrow the effective user scope; org-owned secret keys rejected from
  user self-env writes.
- **Dev vs prod** - separate env selection, separate volumes, separate
  broker state; never share a broker volume between environments.
- **Runtime vs workload state** - connector state lives per-connection in
  controller-owned volumes; runtime homes are never mounted into MCP
  workloads.
- **Configuration** - there is no unmanaged mode: authority comes only from
  published passports, and a missing or unreadable input fails closed
  (ADR-0105 D11).

## Non-goals (system-wide)

- Public multi-tenant SaaS; ingress authentication and principal mapping
  are the deployer's job.
- A general office/document service, a GPU service, a database-backed job
  queue, a vector store.
- Replacement of upstream Hermes, ToolHive or provider APIs - pinned
  dependencies, reviewed contracts.
- Silent migration: existing local credential records are rejected once
  Broker mode is enabled rather than silently mixed; old layouts keep
  working until an explicit migration succeeds.

## Reading the boundary in code

Import direction between `internal/communication` and its leaf packages is
enforced by parser tests; the per-binary dependency closure is enforced by
`devcheck deps` until M40.2 replaces it with Go `internal` visibility
(ADR-0105 D1); the agent-visible capability surface is enforced by ToolHub
admission, not by convention. If a doc and code disagree about a
boundary, code wins and the doc is stale - fix the doc.
