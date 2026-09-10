---
description: Resolve organization policy before starting an isolated user runtime.
last_verified: 2026-09-10
---
# ADR-0004: Organization/user scope overlay

Accepted 2026-09-10.

## Decision

Keep Hermes as the per-user runtime and add a small host-side overlay. The overlay
validates organization membership, intersects organization features with user
settings, inherits only organization-approved read-only MCP definitions with explicit
Hermes tool allowlists, loads organization credentials separately, and mounts
organization documents read-only. User state and
OAuth tokens remain in the user's isolated runtime. Built-in HH, Telegram and Slack
mutations require explicit organization actions where the hub owns the tool boundary.

Do not add a memory database, graph store, ingestion queue, GitLab connector or
ClickHouse connector in this change. Hermes native memory remains user-scoped; an
organization knowledge layer can be evaluated after the scope boundary is exercised.

## Consequences

This is a deployment/control-plane boundary, not a public authentication gateway.
An ingress must authenticate and map a principal to one user space. Organization MCP
servers can be shared by policy but execute inside each user's runtime; their upstream
tool permissions still need separate review. The effective settings are deterministic
and testable without provider credentials.

## Related records

SPEC-0004, ADR-0002, docs/architecture.md, docs/integrations.md.
