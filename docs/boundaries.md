---
description: Architectural boundaries — what each component may and may not do; non-goals.
last_verified: 2026-10-20
---

# Boundaries

The walls that keep the trust model intact. Each entry names the thing that
must never cross.

## Per-component hard boundaries

| Component | May | May never |
|---|---|---|
| `communication-hub` | Own channels, spool, `?t=` links, job admission | Mount scope homes, touch Docker, hold credential values (telegram_auth in-memory session is the single reviewed exception) |
| `supervisor` | Docker lifecycle for agent runtimes, control-call routing, `/v1/restart-request` relay | See channel credentials, derive identity from message content, own ToolHub authority |
| `toolhub` | Governance decisions, connector lifecycle, audit ledger, bounded cells | Hold credential values (refs only), write spawned-runtime state, admit a cell from model-supplied config |
| `credential-broker` | Store/deliver secrets under signed contracts and leases | Expose materialized values through MCP projections, logs, or audit ledgers |
| `hermes-runtime` | Spawn pinned Hermes, serve the private `/v1/*` contract, own its scope mounts | Receive the Docker socket, mount a parent containing other users' homes, be an authorization source |
| `workload-controller` | Admit cells against reviewed definitions | Trust HTTP reachability as enforcement evidence |
| Model / agent | Select and call projected tools | Mint grants or confirmations, choose egress destinations, see credential material, register MCP servers |

## Plane boundaries

- **Agent network vs control plane** — the managed agent network has no
  default route; Hermes reaches only ToolHub relay + model relay there.
  CLIProxy, broker, sibling runtimes never attach to it. The single
  exception path is supervisor `docker exec` for agent-tools — the only
  control path that never crosses the agent network.
- **Data vs authority** — channel IDs, session files, web content, tool
  output are transport/data. Identity, grants and policy come from
  host-owned records only.
- **User vs organization** — org mounts are read-only; org policy can only
  narrow the effective user scope; org-owned secret keys rejected from
  user self-env writes.
- **Dev vs prod** — separate env selection, separate volumes, separate
  broker state; never share a broker volume between environments.
- **Runtime vs workload state** — connector state lives per-connection in
  controller-owned volumes; runtime homes are never mounted into MCP
  workloads.

## Non-goals (system-wide)

- Public multi-tenant SaaS; ingress authentication and principal mapping
  are the deployer's job.
- A general office/document service, a GPU service, a database-backed job
  queue, a vector store.
- Replacement of upstream Hermes, ToolHive or provider APIs — pinned
  dependencies, reviewed contracts.
- Silent migration: existing local credential records are rejected once
  Broker mode is enabled rather than silently mixed; old layouts keep
  working until an explicit migration succeeds.

## Reading the boundary in code

Import direction between `internal/communication` and its leaf packages is
enforced by parser tests; the agent-visible capability surface is enforced
by ToolHub admission, not by convention. If a doc and code disagree about
a boundary, code wins and the doc is stale — fix the doc.
