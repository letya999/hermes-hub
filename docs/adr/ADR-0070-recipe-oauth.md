---
description: Declarative OAuth for prepared and runtime-reviewed MCP installation.
last_verified: 2026-10-20
---
# ADR-0070: Recipe-driven OAuth

Status: Accepted (owner requested implementation on 2026-10-20).

## Context

Calendar embeds a single scope, client parser and token layout in Go. Workspace
needs another JSON layout and account filename. Existing remote MCP OAuth is a
separate protocol, not an adapter for local servers' provider sessions.

## Decisions

1. Extend existing connection recipes and immutable definitions with bounded
   OAuth contracts. Prepared, pinned source metadata and explicit reviewed
   install recipes use the same validator and engine.
2. Provider URLs, scopes, JSON paths/templates, state target and read probes are
   data included in review/credential digests. No executable expressions or
   repository-name dispatch. Upstream content is evidence, never authority.
3. Hub may project a canonical Broker state file to a percent-encoded account
   filename inside the same lease. Copy back only after the workload stops,
   before checkpoint; reject symlinks/escape/oversize. No wildcard snapshots,
   cross-lease state or secret argv. Broker retains its existing state contract
   and persistence ownership; no new module delivery method is introduced.
4. Keep owner/context/runtime/policy, PKCE, one-time state, TLS, Origin/CSRF,
   audit, read-only projection and opt-in boundaries. Existing remote OAuth stays.

## Alternatives

Another Workspace callback duplicates coupling. Arbitrary auth scripts expose
secrets. Upstream patches violate the dependency boundary. Neither is needed.
