---
description: Browser workloads as ordinary connectors — web egress class, per-connection isolation, owner-bound viewer links, staged toolsets and a bounded shared-memory budget.
last_verified: 2026-10-19
---
# ADR-0052: Browser workloads as connectors

Status: Accepted 2026-10-19. Implements the contract half of GitHub issue
#289 (M13 P8). Builds on [ADR-0048](ADR-0048-unified-connector-surface.md)
(the `connector_*` surface and operation journal),
[ADR-0049](ADR-0049-ownership-finalizers.md) (ownership labels and
finalizers), [ADR-0050](ADR-0050-ci-built-connector-images.md) (catalog
tiers) and the Credential Broker boundary.

## Context

A user should be able to say "install a browser for me" or "remove the
browser" and get the same lifecycle as any other connector. Browsers need
things no other connector needed before: arbitrary web egress, a durable
profile, a way for the owner to watch the session, and a larger
`/dev/shm`. The pre-existing hub-owned `browser` / `browser_guest` runtime
must keep its own behavior — it is not replaced by this connector path.

## Decision

- **D1**: `egress_class: web` is a container-MCP egress mode for arbitrary
  outbound HTTPS through the audited per-workload proxy — deny-first on
  private, loopback, link-local, CGNAT, cloud-metadata and hub-internal
  destinations, per-request DNS checks against rebinding, and bounded
  `egress_allow` / `egress_deny` connection-metadata refinements where
  deny always wins. Web entries carry reviewed extra deny rules in their
  `egress` list, not allowed hosts.
- **D2**: Each connection owns an isolated workload: a per-connection
  Chromium with its browser profile on the `connection-state` volume
  (`/state/browser-profile`, downloads inside it), so `remove` purges the
  profile through the ordinary finalizer and verified-zero receipt.
- **D3**: The viewer is never a published port. `connector_status` mints a
  one-time, short-TTL, owner-bound nonce delivered on the owner's channel;
  `GET /viewer/<connection>?t=<nonce>` serves it exactly once — 410 on
  reuse/expiry, 403 on a wrong nonce — and no link material reaches the
  model or the tool result.
- **D4**: `playwright-mcp` is the reviewed T0 entry: digest-pinned MCR
  image, pinned source commit, `--caps vision,pdf`. Default projection is
  read-only; `act`, `eval` and `network` are separate toolsets, `act`
  additionally gated by the human-confirmed `connector_allow_writes` step.
  All projection paths — including self-install list and dispatch —
  iterate `ProjectedToolSpecs(binding)` so toolset gating applies
  uniformly. The optional `browser-session` Broker contract imports a
  storage-state file and origin lists through the protected form.
- **D5**: `agent-browser` stays in `blocked.json` — no reviewed runnable
  image exists for its source, so it remains discoverable with reason,
  runbook and handoff but is never installable.
- **D6**: `ShmSizeMiB` is a bounded execution budget: rejected when
  negative, above the global cap, or above workload memory; it
  participates in execution equality and drift. The Docker fallback adds
  `--shm-size` to the workload container only; the ToolHive path forwards
  it only when its capability probe advertises the flag — otherwise it
  fails closed, or falls back when configured.

## Consequences

Browsers install, enable, disable, reauth and remove like every other
connector, with per-connection isolation and no new authority paths. Live
acceptance (two principals on one stack, noVNC/CDP rendering, real site
login) remains a deployment gate, not a contract claim.
