---
description: Use isolated Hermes deployments and MCP boundaries.
last_verified: 2026-09-07
---
# ADR-0001: Use one isolated Hermes deployment per person

Accepted 2026-09-07. Supersedes: none.

## Context

The owner needs a general agent connected to private messaging, Google, browsers and
career tools, locally and on a VPS. OAuth caches and browser sessions represent identities.
Hermes and Telegram MCP currently require incompatible Python MCP major versions.

## Options

| Option | Benefit | Cost |
|---|---|---|
| Shared agent with tenant IDs | Fewer processes | Complex identity and secret isolation |
| Separate deployment per person | Clear file/session boundary | More RAM and duplicate caches |
| New agent runtime in Go | Single language | Reimplements Hermes and its ecosystem |

## Decision

Use pinned upstream Hermes, isolated connector virtualenvs, and a small Go control CLI.
Each person owns a Compose project and private files. Career role profiles live within
their career service. Browser automation is container-local; native desktop access is an
explicit authenticated tools-only bridge. Retrieval starts with bounded text search.

## Consequences

No shared multi-tenant credentials, no arbitrary host mounts and no embedded vector DB.
Account bootstrap remains interactive. The agent container is sizable because it includes
browsers and speech libraries. Tool access and owner instructions govern external actions;
SOUL is guidance, not a security boundary against an agent with terminal access.

## Revisit when

Measured workload requires central tenant management, search quality needs semantic
retrieval, or a native MCP requires resources/sampling beyond the bridge's tools contract.

## Related records

Current behavior: docs/architecture.md, docs/integrations.md, specs/active/SPEC-0001-hub.md.
