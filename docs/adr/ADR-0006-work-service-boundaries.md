---
description: Use upstream provider boundaries for Atlassian, GitLab and Google.
last_verified: 2026-09-10
---
# ADR-0006: Work-service boundaries

Accepted 2026-09-10.

## Decision

Keep Atlassian on its official remote Rovo MCP with personal API-token authentication
(`email` + token, Basic auth) and rely on its organization-level Read, Write and Search
permissions in addition to the token's permissions. Use
the official `glab` CLI for GitLab instead of adding another MCP server. Run Google
Workspace MCP in its upstream read-only mode unless `google_write` is explicitly
enabled. All three execute inside the existing isolated user runtime.

## Consequences

The hub does not duplicate provider APIs or pretend to enforce controls it cannot
observe. `glab` reads `GITLAB_TOKEN` directly. Atlassian API-token authentication must
be enabled by an Atlassian admin; Google still needs one-time OAuth consent.
Live OAuth and provider behavior remain deployment acceptance checks.

## Related records

SPEC-0006, ADR-0002, ADR-0004, docs/integrations.md.
