---
description: ToolHub control MCP, operator grants and loopback credential elicitation.
last_verified: 2026-09-17
---
# ADR-0021: Control MCP owns install, grants and elicitation

Accepted 2026-09-17.

## Decision

Expose ToolHub install and lifecycle as authenticated control MCP
operations on the existing gateway. The server derives principal, context
and runtime from the bearer mapping; the model cannot supply owner,
locator, backend or policy fields. Grants are operator records. Users
install either an already-approved catalog definition they are assigned,
or a GitHub source they are granted to self-install. Credentials enter
through a loopback form (MCP URL elicitation may point at it) into the
encrypted store and are injected only after `AuthorizeProjected`.

Hermes does not forward native elicitation today, so the loopback form is
the production secret input path. Chat `KEY=value` remains a communication
intercept for self-env, not the MCP credential path.

## Consequences

User bindings stay principal-owned when a user MCP is later promoted to
the catalog. Shared credential locators require an explicit admin policy.
Rotate/revoke cut the open session in Go. Hermes transport reconnect stays
#74.

Related: ADR-0013, ADR-0016, SPEC-0023, issues #81, #86, #101, #102.
