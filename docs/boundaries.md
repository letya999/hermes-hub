---
description: Architectural boundaries - v1 zone crossings (who may cross, what is refused), current per-component walls, plane boundaries and system non-goals.
last_verified: 2026-11-19
---

# Boundaries

The walls that keep the trust model intact. Each entry names who may cross
and what must never cross. The v1 table is the target fixed by
[ADR-0105](adr/ADR-0105-v1-topology.md)..[ADR-0109](adr/ADR-0109-action-modes-owner-decisions.md);
the per-component table describes the current v0 services.

## v1 zone crossings

Every arrow not listed is refused. Each refusal gets a test in the CHG that
builds the crossing (SPEC-0100 A1.4).

| From -> to | Allowed | Refused |
|---|---|---|
| internet -> edge | channel webhooks/polls with channel proof, one-time `?t=` links, hosted sites | anonymous principal, any control or agent route |
| edge -> control | `identity` and `task` contracts with a service credential | container, policy or ledger writes outside the task contract |
| control -> space runtime | spawn, stop, restart, private `/v1/*` jobs with a signed run token | reading runtime memory or files beyond the mounted space |
| runtime -> agent zone | tool, memory and model calls with a valid run token for that audience | missing, forged, expired, cross-space, run-aliased or stale-generation tokens; header-only identity |
| runtime -> egress | outbound HTTP(S) and declared TCP forwards per space policy | private, loopback and metadata destinations; model-chosen destinations outside policy |
| agent/control -> secret | credential leases bound to a verified run or operator confirmation | value export into tool results, logs, audit or DTOs |
| runtime -> runtime | nothing | any cross-space path |
| worker -> anything | the endpoints its admission receipt names | hub networks, other workers, Docker socket |
| any module -> container backend | only spawn-controller | direct Docker/cluster calls from any other module |
| any module -> another module's store | the owner's contract | opening the owner's store file |
| agent profile -> grants | a subset of its user's grants | widening, another user's credentials |
| system principal -> user work | jobs through an Assignment the user owns, `initiator` recorded | acting with user credentials as itself, impersonating the user |
| event content -> authority | read as quarantined data in triage | changing grants, tools, destinations or modes |
| proactive run -> owner | report records through the attention inbox | direct channel sends |
| run -> action mode | the `mode` claim of its token | changing its own mode; skipping the always-ask list |

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
  (ADR-0105 D8).

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
`devcheck` (ADR-0105 D6); the agent-visible capability surface is enforced
by ToolHub admission, not by convention. If a doc and code disagree about a
boundary, code wins and the doc is stale - fix the doc.
