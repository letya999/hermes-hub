---
description: "Frozen MVP requirements for M29: six space passports, catalogs, provisioning, enforcement, migration and live acceptance."
last_verified: "2026-11-15"
---

# SPEC-0091: Six-passport space contract MVP

Frozen: 2026-11-01. Design accepted; implementation not started.
Decision: [ADR-0092](../../docs/adr/ADR-0092-six-passport-space-contract.md).
Stages: CHG-0241–0253. Requirements R0–R12 correspond to M29 issues.

## Requirements

### R0. Baseline and migration map

- R0.1: Inventory every source, effective projection, consumer, owner and
  persistence class across Hub, Hermes, ToolHub, relay, Broker and channels.
- R0.2: Verify pinned Hermes context loading in a real managed run; document
  compatibility and fail-closed strategy. Cross-link M20/M27/M28 work.

### R1. Platform catalogs

- R1.1: Immutable versioned capabilities, toolsets and services catalogs
  derive from typed Go definitions; tool/operation/effect/via/scope parity
  against handlers and docs is checked in CI.
- R1.2: Unknown IDs, operations and incompatible catalog versions fail closed;
  full catalogs stay platform-owned, only admitted views reach user spaces.

### R2. Source compiler and templates

- R2.1: Resolve platform ceiling → pinned platform template → narrower org
  ceiling and versioned org template → permitted user choice, with field
  classes and deterministic provenance/diff.
- R2.2: Validate cross-file links before atomically publishing six read-only
  passports with common revision, timestamps, template and source/catalog
  versions; bad revisions preserve the previous snapshot.

### R3. Stack and config

- R3.1: `stack.yaml` owns only identity/service instances/modes/dependencies;
  `config.yaml` owns only channels, memory policy, supervision, ports and
  runtime settings. No secret bytes are serialized.
- R3.2: Compose, supervisor and service config render from the same effective
  revision; nonexistent proxy/service/channel dependencies reject the plan.

### R4. Workspace and mounts

- R4.1: Materialize the declared `/config`, `/workspace/{inbox,work,projects,
  artifacts,outbox}`, `/vault`, `/skills`, `/shared` layout; ASCII `tree` is
  derived. Preserve existing projects, Git repos, skills and files.
- R4.2: `writable`, `read_only`, mounts, safe attachment IDs and original
  metadata are enforced by mounts and file tools, including symlink/rename
  escapes and cross-user isolation. Vault memory is writable only via vault.

### R5. Grants

- R5.1: `grants.yaml` is an effective `allow: capability: [operations]`
  snapshot. Catalog supplies operation semantics, issuer and scope. Missing
  operation denies; templates never grant authority.
- R5.2: Platform/org ceilings, trusted grant ledger, revocation epoch and
  per-call admission govern files, models, tools, skills, connectors, agents,
  users, stack, config, channels and observability. Self-config cannot widen
  ceilings; cross-user actions require explicit operations.

### R6. Toolset disclosure and enforcement

- R6.1: `tools.yaml` groups whole toolsets in core/extended, core has ≤20
  selected tools, discovery stays core, overflow moves entire families in
  deterministic catalog order. Version-pinned `all` never silently grows.
- R6.2: `via`, include/exclude, access read, scope and per-tool rules are
  validated against executor contracts; direct and `tools_list`/`invoke`
  paths enforce identical live decisions. Unsupported native/raw MCP
  restrictions fail closed; tool-contract docs/version/rebind/respawn match.

### R7. Model credential delivery

- R7.1: Broker-managed named contracts accept provider API keys and bounded
  OAuth session material via approved provider backends, preserving owner,
  version, rotation, revoke and audit. No secret bytes/keys in passports,
  ledger DTOs, logs, argv, diagnostics or agent-visible mounts.
- R7.2: CLIProxy and relay obtain short-lived scoped leases; per-session
  pinning is proven or isolated per session. Broker remains a provider seam,
  not a container runner or universal canonical secret store.

### R8. Model routes, quotas and failover

- R8.1: `models.yaml` describes default route and ordered model services;
  each service has one credential principal, ordered compatible models,
  reasoning, sharing and `{of,amount,per}` limits.
- R8.2: Model unavailable → next model in service; quota/service unavailable
  → next service; check request capabilities; no replay after stream start.
  Provider-quota percentages require reliable per-session usage/reset; shared
  and personal counters do not cross. Switch requires `models.switch`.

### R9. Prompt and Hermes context

- R9.1: Platform → org → user system-prompt sources and generated SOUL/USER/
  AGENTS are read-only, injected exactly once on every managed turn with a
  short current revision/core inventory and live discovery pointer.
- R9.2: Pinned upstream implicit SOUL/USER/MEMORY/project-rule/skill loading
  is empirically checked; no second long-term memory or privileged project
  AGENTS. Security uses per-call server enforcement, not prompt compliance.

### R10. Skills and connectors

- R10.1: Existing M24 effective skill store, ToolHub connector lifecycle and
  M27 packs are projected under the new toolsets and mounts without duplicate
  config or authority. Raw MCP is default-denied unless reviewed and granted.
- R10.2: Search/list remains lazy, draft/publish and connector write effects
  require cataloged operations; disabled/revoked entries disappear live.

### R11. Migration and documentation

- R11.1: `hubctl` dry-run/apply maps legacy files and grants to new sources,
  reports conflicts, preserves unknown/dynamic state, produces verified backup
  and rollout/rollback fence. Existing group/organization runtime scopes
  remain usable or are explicitly blocked with a migration plan; they are
  never silently retyped as users. No silent deletion or secret exposure.
- R11.2: Replace obsolete YAML/JSON authority paths only after consumers
  migrate. Update README, docs indexes, architecture, spaces, operations,
  RBAC, agent-home, tools, routines, model-relay and Broker docs, examples,
  prompt contract and localized user-visible output.

### R12. Acceptance and CI

- R12.1: Two-user and org tests prove effective inventory, protected mounts,
  model/session failover, grants/revokes, direct/discovered tools, prompt
  injection, migration/rollback and no cross-user access.
- R12.2: `just check` (≥85% own Go statement coverage), applicable `just
  security`, Broker quality gates (≥85% overall and per package), race/fuzz,
  prebuilt/full Docker gates, and real dev-stack E2E evidence are recorded
  separately. A real opt-in provider/CLIProxy session is required to claim
  provider compatibility; synthetic fixtures prove only Hub behavior. Mocks
  do not count as integration success.

## Acceptance

| ID | Observable condition |
|---|---|
| A0 | Baseline maps every legacy authority and demonstrates pinned Hermes loading behavior. |
| A1 | Generated platform catalogs match handlers, reject unknown entries and are version-pinned. |
| A2 | One invalid layer leaves all six previous passports active; metadata/provenance agrees. |
| A3 | Compose/services match stack+config and reject missing dependencies/port conflicts. |
| A4 | Two users see only their own projects/inbox/vault and admitted read-only mounts. |
| A5 | Grant revoke blocks the next direct and discovered call before prompt respawn. |
| A6 | Core contains ≤20 tools, never splits a family, and extended invoke is equally admitted. |
| A7 | Two separate CLIProxy sessions and one API key rotate/revoke without byte exposure. |
| A8 | Missing model, exhausted session and stream-start cases follow the specified route. |
| A9 | Every managed turn gets exactly one effective prompt and no writable authority files. |
| A10 | Skill/connector catalog and live projection have no stale or duplicate authority. |
| A11 | Dry-run, apply and rollback preserve data; retired inputs are neither read nor silently deleted. |
| A12 | Unit, race, security, Docker and live dev-stack gates have truthful receipts. |

## Boundary

M20 owns vault internals; M27 owns pack acquisition; M28 owns shared edge
tenancy. M29 integrates their contracts. Database substrate migration from
SPEC-0090 is independent. No guessed provider APIs or upstream Hermes patch.
