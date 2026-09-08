---
description: Keep only general-purpose Hermes and select environments through Docker.
last_verified: 2026-09-08
---
# ADR-0002: Separate the Hermes host from business applications

Accepted 2026-09-08. Supersedes ADR-0001's bundled career service and host-state layout.

## Context

The owner requested a general-purpose agent, not a packaged career application.
They explicitly clarified that user spaces are spaces/<user>; dev/prod belongs to
Docker and env configuration. Runtime identities must not share cookies or memories.

## Options

| Option | Benefit | Cost |
|---|---|---|
| Embedded optional career container | Convenient for one workflow | Couples the product to a business application |
| Generic external MCP | Any workflow, no embedded service | Owner operates external services separately |
| Shared dev/prod memory | Fewer files | Tests can corrupt production state |
| Distinct Docker volumes | Same user namespace, isolated runtime | Backups must include volumes |

## Decision

Use only standalone Hermes, the Go control plane and generic connectors. Keep one user
configuration space and separate dev/prod env files plus Docker volumes. Dev mounts only
an allowlist of source paths, never the root containing private user spaces. Use Jest as
CI orchestrator with native coverage gates; license original code AGPL-3.0-only.

## Consequences

CareerGo/JobFetch are absent from build, defaults and runtime dependencies. Extensions
and memory remain native Hermes features. Dev/prod source settings can share a checkout;
use tagged separate checkouts if production release isolation is required.

## Revisit when

Measured multi-tenant hosting needs stronger hostile-user isolation, or the owner asks
for a different agent runtime. Do not reintroduce business services as implicit defaults.

## Related records

SPEC-0002, docs/architecture.md, docs/operations.md, README.md.
